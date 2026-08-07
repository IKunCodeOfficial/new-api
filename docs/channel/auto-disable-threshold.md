# 渠道自动禁用「连续命中阈值」设计方案

## 1. 背景与目标

当前渠道自动禁用逻辑：上游返回错误后，`service.ShouldDisableChannel`（`service/channel.go:45`）依次判断

1. 显式渠道错误（`types.IsChannelError`）；
2. 状态码命中 `AutomaticDisableStatusCodes`（默认 `401`）；
3. 错误文本命中 `AutomaticDisableKeywords`（AC 自动机，`service/str.go:132`）。

任一命中 **一次** 即异步调用 `service.DisableChannel` 禁用渠道（多 key 渠道禁用对应单 key）。上游偶发性返回一条含关键字的错误（例如上游自身故障时转发的错误文案）就会误禁渠道。

**目标**：新增可配置阈值 N（默认 1，保持现状）——同一渠道（多 key 渠道为同一渠道+同一 key）**连续命中 N 次**自动禁用规则才真正禁用。要求高并发下无明显开销、多机部署下计数一致、并发触发不重复禁用/通知。

## 2. 现有链路盘点（阈值逻辑的收敛点）

所有「因请求错误触发的自动禁用」都收敛在 `processChannelError`（`controller/relay.go:373`）：

| 调用点 | 路径 |
|---|---|
| `controller/relay.go:245` | 普通 relay 重试循环（含 Claude/Gemini/Realtime/Embedding 等所有 RelayFormat） |
| `controller/relay.go:573` | 任务类 relay（RelayTaskSubmit） |
| `controller/channel-test.go:960` | 渠道测试（手动 + 定时监控） |

不走该收敛点、**保持立即禁用**的路径：

- `controller/channel-billing.go:476`：余额查询确认余额不足，直接 `DisableChannel`（确定性事实，无需阈值）。

禁用执行 `model.UpdateChannelStatus`（`model/channel.go:712`）本身幂等：状态已相同则返回 `false`，不重复更新、不重复通知——这是并发安全设计的基石之一。

## 3. 语义定义

- **命中**：`ShouldDisableChannel(err) == true` 的一次错误（关键字/状态码/显式渠道错误统一计数，不区分来源；relay 流量与渠道测试共享同一计数器）。
- **连续**：两次命中之间没有该渠道(+key)的成功请求；任一成功请求将计数清零。
- **观察窗口**：距最后一次命中超过 30 分钟（代码常量 `autoDisableCounterWindow`），计数自动归零。作用：① 防止低流量渠道数天内零散错误累积误禁；② 兼作 Redis 键 TTL 回收。窗口每次命中刷新（滑动窗口）。
- **计数粒度**：普通渠道按 `channelId`；多 key 渠道按 `channelId + fnv64(usingKey)`（与 `UpdateChannelStatus` 按 key 禁用的粒度对齐；hash 避免明文 key 进入 Redis）。
- **阈值 N=1**：完全等价于现有行为（短路，不产生任何计数开销）。

## 4. 配置项

新增全局选项 `AutomaticDisableConsecutiveThreshold`（int，≥1，默认 1）。

- 存储：沿用 option 表机制，惰性写入，无 DB 迁移。
- 进程内：`atomic.Int32`，读路径无锁（每次失败读一次，成功路径在 N=1 时零开销短路）。
- 命名归入 `AutomaticDisable*` 家族；注意与既有 `ChannelDisableThreshold`（渠道测试响应时间秒数阈值）语义不同，前端文案需明确区分。
- 观察窗口暂不做成配置项（减少旋钮），如有需求后续可平滑升级为 `AutomaticDisableCounterWindowMinutes`。

## 5. 后端实现

### 5.1 新文件 `setting/operation_setting/auto_disable_threshold.go`

```go
var automaticDisableConsecutiveThreshold atomic.Int32 // 初始化为 1

func GetAutomaticDisableConsecutiveThreshold() int      // 读，clamp 到 >=1
func AutomaticDisableConsecutiveThresholdToString() string
func AutomaticDisableConsecutiveThresholdFromString(s string) // 解析失败/越界时回退 1 并 SysError
```

### 5.2 新文件 `service/channel_auto_disable_counter.go`（核心逻辑全部隔离于此）

```go
const (
    autoDisableCounterWindow  = 30 * time.Minute
    autoDisableResetThrottle  = time.Second
)

// 计数 key："auto_disable_hits:{channelId}"，多 key 渠道追加 ":{fnv64(usingKey)}"
func autoDisableCounterKey(channelId int, isMultiKey bool, usingKey string) string

// 命中 +1，返回 (是否达到阈值, 当前计数)
func recordAutoDisableHit(key string, threshold int) (bool, int64)

// 成功/启用后清零（内部节流）
func ResetChannelAutoDisableCounter(channelId int, isMultiKey bool, usingKey string)

// processChannelError 的新入口：判定 → 计数 → 达阈值才禁用
func ProcessChannelDisableHit(channelError types.ChannelError, reason string)
```

`ProcessChannelDisableHit` 逻辑：

```go
threshold := operation_setting.GetAutomaticDisableConsecutiveThreshold()
if threshold <= 1 {
    DisableChannel(channelError, reason)   // 与现状完全一致
    return
}
key := autoDisableCounterKey(...)
reached, hits := recordAutoDisableHit(key, threshold)
if !reached {
    common.SysLog(fmt.Sprintf("渠道 #%d 命中自动禁用规则 %d/%d 次：%s", ...))  // 可观测性
    return
}
resetCounter(key) // 清零，避免渠道重新启用后残留计数导致一次失败即再禁
DisableChannel(channelError, fmt.Sprintf("连续命中自动禁用规则 %d 次，最后错误：%s", hits, reason))
```

### 5.3 双模式计数器

**Redis 模式**（`common.RedisEnabled`，多机部署下全局一致）：

```go
pipe := common.RDB.TxPipeline()
incr := pipe.Incr(ctx, key)
pipe.Expire(ctx, key, autoDisableCounterWindow)
_, err := pipe.Exec(ctx)
// incr.Val() >= threshold → 达到阈值
```

- 每次**失败** 2 个 O(1) 命令，且失败路径本就在慢路径（错误处理/日志/重试），开销可忽略；成功路径见下面的节流重置。
- 注意：不复用 `common.RedisIncr`（`common/redis.go:242`，它是配额缓存专用语义——key 无 TTL 时不写入），直接在新文件内用 `RDB` pipeline。
- **Redis 故障降级**：`Exec` 出错时记录 `SysError` 并回退到本节点内存计数（阈值语义退化为单节点，绝不因 Redis 故障出现「立即禁用」或「永不禁用」两个极端）。

**内存模式**（未启用 Redis 的单机部署）：

```go
var autoDisableCounters sync.Map // key -> *counterEntry
type counterEntry struct {
    hits    atomic.Int64
    lastHit atomic.Int64 // unix 秒，读时惰性判断窗口过期则重置
}
```

- 命中：`LoadOrStore` + `hits.Add(1)`，无锁。
- 惰性过期：`Add` 前若 `now-lastHit > window` 则先 `Store(0)`；另由每 10 分钟的清理 goroutine（复用 `gopool`）删除过期条目。条目数上限 = 渠道数 ×（多 key 渠道的 key 数），天然有界。

### 5.4 成功路径重置（高并发关键点）

重置点及方式：

| 触发点 | 位置 | 说明 |
|---|---|---|
| relay 成功 | `controller/relay.go:237`（`newAPIError == nil` 出口） | `gopool.Go` 异步调用 Reset，零阻塞 |
| 任务 relay 成功 | `controller/relay.go:591`（`taskErr == nil`） | 同上 |
| 渠道测试成功 | `controller/channel-test.go` 成功分支 | 同上 |
| 渠道被自动/手动启用 | `service.EnableChannel`（`service/channel.go:36`） | 直接 Reset |
| 达阈值触发禁用后 | `ProcessChannelDisableHit` 内部 | 见 5.2 |

**成功路径不能每请求打一次 Redis DEL**（成功是绝对多数流量）。方案：**节流重置**——

```go
var resetThrottle sync.Map // key -> *atomic.Int64（上次重置 unix 秒）
// CAS 抢占：每渠道(+key)每秒至多 1 次真正的 DEL / map 清零，其余调用直接返回
```

- 阈值为 1 时函数第一行短路返回，**特性关闭 = 零开销**。
- 内存模式额外优化：先 `Load`，条目不存在（绝大多数情况）直接返回，`sync.Map` 读是 lock-free 的。
- 正确性影响：重置最多延迟 1 秒。要出现误禁，需要同一渠道在 1 秒内涌入 ≥N 次命中且期间的成功都被节流吞掉——而 1 秒内 N 次关键字错误本身就是应当禁用的强信号，语义可接受。
- 不采用「本地记录有命中才 DEL」的方案：多机下 A 节点计数、B 节点成功时 B 无本地记录，会漏重置，破坏「连续」语义。

### 5.5 触点改造：`processChannelError`

```go
// 现状（controller/relay.go:377）
if service.ShouldDisableChannel(err) && channelError.AutoBan {
    gopool.Go(func() { service.DisableChannel(channelError, err.ErrorWithStatusCode()) })
}

// 改为：同步求值判定与 reason，异步执行计数+禁用
if service.ShouldDisableChannel(err) && channelError.AutoBan {
    reason := err.ErrorWithStatusCode() // 同步取字符串，顺带消除现有的与 defer 中 SetMessage 的数据竞争
    gopool.Go(func() { service.ProcessChannelDisableHit(channelError, reason) })
}
```

三个调用点（relay / task / channel-test）共用此函数，一处改动全覆盖。

## 6. 并发正确性论证

1. **计数原子性**：Redis `INCR` / `atomic.Int64`，无锁竞争，无丢失更新。
2. **恰好一次禁用**：以 `hits >= N` 判定（而非 `== N`，避免「INCR 后、禁用前进程崩溃」导致计数越过阈值后永不触发）。并发下可能多个 goroutine 同时满足 `>= N` 重复调用 `DisableChannel`，但 `UpdateChannelStatus` 幂等（状态相同直接返回 false），通知也只在真正更新成功时发出且按 `channelId+status` 类型化（`formatNotifyType`）——最终恰好一次实际禁用、一次通知。
3. **重置/命中竞态**：同一瞬间成功与失败并发到达时二者顺序本无定义，两种交错结果语义上都成立，无需加锁裁决。
4. **成功路径开销**：N=1 时零指令短路；N>1 时内存模式为一次 lock-free `Load`，Redis 模式为节流后每渠道每秒 ≤1 次 DEL。
5. **失败路径开销**：2 个 O(1) Redis 命令且在 `gopool` 异步 goroutine 内执行，不阻塞请求返回。
6. **资源有界**：内存条目数受渠道/key 数约束；Redis 键带 TTL；节流 map 条目同样有界；无新增常驻 goroutine（清理任务复用现有 gopool 模式）。

## 7. 前端改动

设置位置：系统设置 → Routing Reliability → 「Auto-disable rules」组（`Disable on failure` 开关旁）。

| 文件 | 改动 |
|---|---|
| `web/src/features/system-settings/types.ts` | +1 字段 `AutomaticDisableConsecutiveThreshold` |
| `web/src/features/system-settings/models/section-registry.tsx` | defaultValues 透传 +1 行 |
| `web/src/features/system-settings/models/routing-reliability-section.tsx` | schema：`z.coerce.number().int().min(1)`；defaults/normalize 各 +1；新增数字输入框（仿 `RetryTimes`，`safeNumberFieldProps`） |
| `web/src/i18n/locales/*.json` | 新增文案（`bun run i18n:sync`） |

文案（英文为 key）：

- Label: `Consecutive failures to disable`
- Description: `Disable a channel (or key) only after it hits the auto-disable rules this many times in a row. Any successful request resets the counter; the counter also expires 30 minutes after the last hit. Set 1 to disable immediately (default).`

## 8. 选项系统接线（上游合并热点，改动最小化）

| 文件 | 改动 |
|---|---|
| `model/option.go` `InitOptionMap`（约 :176 附近） | +1 行注册默认值 |
| `model/option.go` `updateOptionMap`（约 :595 附近） | +1 个 `case "AutomaticDisableConsecutiveThreshold"` |

其余逻辑全部在两个新文件中，与上游合并冲突面仅为上述 2 行 + `processChannelError` 内 1 处替换 + 前端 4 处小改（`routing-reliability-section.tsx` 为上游活跃文件，合并时留意）。

## 9. 边界与已知取舍

- **余额不足禁用**（channel-billing）不计数、保持立即禁用：属确定性事实而非瞬时错误。
- **渠道测试路径**：测试失败与真实流量共享计数器（语义一致）。副作用：`performChannelTests` 的 `summary.Disabled` 语义从「已禁用数」变为「触发禁用流程数」（阈值未达时实际未禁）。如需精确可后续让 `ProcessChannelDisableHit` 返回结果供测试路径同步统计，非本期必须。
- **管理员手动启用渠道**（不走 `service.EnableChannel` 的 admin 直改路径）可能残留计数——由 30 分钟窗口兜底自动归零，且触发禁用时已主动清零，影响可忽略。
- **多机无 Redis 部署**：退化为每节点独立计数（每节点各需 N 次）。该部署形态本身不被推荐（渠道缓存也无法同步），文档注明即可。
- **`specific_channel_id` 调试路径**：`getChannel` 构造的临时 Channel 无 `ChannelInfo`，多 key 信息缺失，计数按普通渠道粒度处理，可接受。
- **不选的替代方案**：
  - 时间窗口计数（T 秒内 N 次，无需成功重置）——实现更简单，但不是「连续」语义，不符合需求；
  - 每渠道独立阈值（channel 表加列）——需迁移三种数据库，本期用全局阈值，留作扩展；
  - 计数落 channel 表 `OtherInfo`——每次失败一次 DB 写，高并发下行不通，排除。

## 10. 测试计划（`service/channel_auto_disable_counter_test.go`）

testify（`require`/`assert`），deterministic 表驱动：

1. 阈值语义：N-1 次命中不触发、第 N 次触发、触发后计数清零重新累计。
2. 成功重置：命中若干次 → Reset → 需重新累计满 N 次。
3. 窗口过期：构造 `lastHit` 超窗后计数归零（内存模式直接操纵 entry，避免 sleep）。
4. 并发：M 个 goroutine 并发命中，`reached==true` 的返回总次数 ≥1 且禁用幂等下最终效果恰好一次（配合对 `UpdateChannelStatus` 幂等语义的既有约定）。
5. Redis 分支：`miniredis`（go.mod 已有 v2.38.0）验证 INCR/EXPIRE/DEL、阈值触发、TTL 刷新。
6. N=1 短路：不产生任何计数键/条目（回归保护「默认行为不变」契约）。

## 11. 发布与回滚

- 默认值 1，升级后行为与现状完全一致，无迁移、无破坏性变更。
- 运行中修改阈值即时生效（option 热更新链路），从大改小时已有计数立即按新阈值判定；从小改大无副作用。
- 回滚：将阈值改回 1 即恢复旧行为，无需回滚代码。
