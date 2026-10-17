# 模型状态页（/status）设计

日期：2026-10-17 · 分支：`ikun-new`（new-api fork）

## 1. 意图

普通用户打开 `/status`，想知道一件事：**我要用的模型现在能不能用**。页面按分组 × 模型，展示基于全站真实请求（不是主动探测）的可用率和时延。

所以优先级是：
1. **"现在"必须准确、及时**：顶部横幅、每行的"当前正常/降级/异常"、5 分钟和 1 小时时间轴。
2. 长时间段（24 小时 / 7 天 / 30 天）只用来看"平时稳不稳"，数据粗一点没关系。

视觉与交互以高保真原型为准：`docs/design_handoff_model_status/README.md` 和 `Model Status.dc.html`。本文只写**和原型不同的地方**，以及后端怎么实现。

## 2. 目标 / 非目标 / 反目标

**目标**
- 新页面 `/status`，按原型实现列表视图、矩阵视图、横幅、工具栏、展开行和图例。
- 新接口 `GET /api/model_status?range=5m|1h|24h|7d|30d`。
- 顶部导航新增"模型状态"入口，后台导航设置里可以开关。

**非目标**
- 主动探测或拨测。
- 长时间段（24h/7d/30d）的分位数和失败原因。
- 任何场景下展示请求数，管理员也不展示。
- 原型里已删除的统计卡片行。

**反目标（出现了就算失败）**
- 修改或新增数据库表。**本设计不改任何表，也不新增表。**
- 扫描 `logs` 表。
- 请求热路径上多一次网络往返，或者出现明显的 CPU / 锁开销。
- 并发访问页面的人越多，后端计算量越大。也就是说，计算次数必须与访问量无关。
- 泄露用户不可用分组的数据。

## 3. 约束

- 本仓库是 new-api 的 fork，需要能轻松同步上游。**新代码放在新文件里**，上游文件只加一两行挂载点。预计要动的上游文件只有：`pkg/perf_metrics/metrics.go`（调用新采集）、`router/api-router.go`（注册路由）、前端导航 hook、后台导航设置、路由树和 i18n 文件。
- 遵守 `AGENTS.md`：JSON 必须走 `common.*`；三种数据库都要兼容（本设计不碰 schema，但新增的查询仍要在 SQLite / MySQL / PostgreSQL 上验证）；前端优先复用已有组件，文案走 i18n，数字格式化走 `@/lib/format`。
- **README 里的前端路径已经过时**：真实源码在 `web/src/`，`web/default/` 只有构建产物。另外 `uptime-panel.tsx` 的实际路径是 `web/src/features/dashboard/components/overview/uptime-panel.tsx`。
- 仓库里还没有 `SegmentedControl`。动手前先在 `web/src/components/ui` 里找 toggle-group / tabs 一类的组件；确实没有合适的，再按原型规格新建一个共享组件 `web/src/components/segmented-control.tsx`。

## 4. 数据来源

### 4.1 长时间段：复用上游 `perf_metrics`（零改动）

上游已有完整的采集链路，本设计直接复用，不改它：
- `perfmetrics.RecordRelayResult` 和 `RecordTaskResult` 在每个请求或任务结束时调用一次 `Record(Sample)`。
- 样本先累加到进程内的 `hotBuckets`，如果开了 Redis 再 HINCRBY 到 Redis；`flushLoop` 定时把已完成的桶写进 `perf_metrics` 表，主键是（model, group, bucket_ts），默认 1 小时一个桶。
- 注意：现有的 `Query` / `QuerySummaryAll` 只读两部分数据：已落库的行和**本进程**的 hotBuckets。`mergeRedisActiveBuckets` 写好了，但没有任何地方调用。所以多实例部署时，当前小时只包含本实例的流量。

24h / 7d 和 30d 的读取方式（**我的决定**）：
- **已完成的小时**只读 `perf_metrics` 表，不合并本进程的 hotBuckets，避免各实例看到的数字不一样。代价是刚结束的那个小时最多要等一个 flush 间隔（默认 5 分钟）才完整，对"粗看稳定性"的用途可以接受。
- **当前这个还没结束的小时**改从 §4.2 的短时间段存储里取：它覆盖最近约 65 分钟，开了 Redis 时还是全实例共享的。把整点到现在的 1 分钟桶加起来，就得到最新那根柱子的数据。

按 `bucket_ts` 范围查出本区间、可见分组的行，再在 Go 里重新分桶：

| range | 柱数 | 每柱 | 和原型的差异 |
|---|---|---|---|
| 24h | 24 | 1 小时 | 原型是 48 根 30 分钟柱。现有数据只到小时级，所以改成 24 根（用户已确认） |
| 7d | 42 | 4 小时 | 与原型一致 |
| 30d | 60 | 12 小时 | 与原型一致 |

查询返回行数约等于"区间小时数 × (模型, 分组)对数"，30 天大约 720 × 对数。如果管理员把 `perf_metrics_setting.bucket_time` 调成 `minute`，行数会多 60 倍。处理办法由实现者选：在 SQL 层按（model, group, bucket_ts）SUM 聚合，或者在 bucket_time 小于 1 小时的时候限制查询规模。不管选哪种，都要保证在缓存（§5.3）的前提下单次计算可以接受。

长时间段的展开行**只显示吞吐**（tok/s，来自 `output_tokens / generation_ms`），不显示分位数和失败原因面板。

### 4.2 短时间段：新增细粒度采集（只放内存 / Redis，不落库）

新包放在 `pkg/model_status/`（名字可调整）。在 `perfmetrics.Record` 里、`Enabled` 判断之后加一行调用，让两份采集共享同一个开关和同样的样本过滤规则。

**每个样本要记的东西**
- `req`、`ok`、`lat_sum`、`ttft_sum`、`ttft_n`、`out`、`gen_ms`，含义和现有计数器一致。
- 时延直方图：首字时延和总耗时各一组，大约 20 个固定的对数区间（比如从 50ms 到 120s 按 1.5 倍左右递增，最后一个区间是"以上"）。每个样本只给对应区间 +1。分位数取区间的上界或中点作为近似值，误差在区间宽度以内，用户已接受。
- 失败分类：只对失败样本记，分成 `5xx` / `429` / `timeout` / `other` 四类。
  - `Sample` 要加一个失败分类字段，在 `RecordRelayResult` 里根据 `ClassifyRelayOutcome` 用到的同一批信息来填：流式结果状态和 `apiErr` 的根错误。规则：状态码 429 或错误码属于 rate-limit / quota 类 → `429`；超时类（`StreamEndReasonTimeout`、`DeadlineExceeded`、504/524）→ `timeout`；其余 ≥500 → `5xx`；剩下的 → `other`。分类优先级和边界情况由实现者决定，但必须写成表驱动测试。
  - `RecordTaskResult` 的失败统一记为 `other`。

**时间粒度**
- 5m 档：30 个 10 秒桶。1h 档：60 个 1 分钟桶。
- 可以只存 10 秒桶，1 分钟由 6 个 10 秒桶合成；也可以两种粒度都存。由实现者根据下面的读取成本约束来选。

**存储**
- **单实例或没开 Redis**：每个（model, group）在内存里维护一个固定长度的环形数组，覆盖最近约 65 分钟。槽位里是原子计数器；写入时只做原子加，不加全局锁。超过 1 小时没有流量的 key 要定期清掉，防止模型或分组名无限增长导致内存泄漏。
- **开了 Redis（多实例）**：每个时间桶一个 Hash，key 类似 `model_status:{粒度}:{bucket_ts}`，字段是 `model\x00group\x00指标`，TTL 略大于覆盖时长（约 70 分钟）。这些 HINCRBY 要**追加进现有 `recordRedis` 的同一个 pipeline**，不能新开一次往返（这一条是硬约束）。读取时，单次计算访问的 Redis key 数应该在百级以内，例如 5m 档读 30 个，1h 档读 60 个。生产环境有两种部署：一是多台 new-api 共用一个普通 Redis，二是单机 new-api 配单个 Redis。两种都**不是** Redis Cluster，`common.RDB` 是单节点 `*redis.Client`（go-redis v8），所以现有的 `TxPipeline`（MULTI/EXEC）里放多个 key 没有问题，不需要处理 hash slot，也不需要改成 ClusterClient。
- 开了 Redis 的时候以 Redis 为准，不再读本进程内存。这和现有 perf_metrics 的做法一致。

## 5. 接口 `GET /api/model_status`

### 5.1 访问控制
- 新增导航模块 `status`，配置为 `{ enabled, requireAuth }`。**不另加** `ModelStatusEnabled` 选项。
- **我的决定**：关闭就是真的关闭。路由挂 `middleware.HeaderNavModuleAuth("status")`，和 `/api/rankings` 一样：模块关闭时返回 403；`requireAuth` 为真时要求登录。不用 `HeaderNavModulePublicOrUserAuth`，因为它在模块关闭时只是改成要求登录，并不会拦住已登录用户。前端 `/status` 路由在模块关闭时显示和 rankings 同样的禁用或 404 状态。模块关闭时，§4.2 的细粒度采集也不再进行。
- **分组可见性（用户强调的硬性要求）**：每个用户只能看到**自己用户分组权限范围内**的分组；管理员能看到所有分组。
  - 直接复用 `controller/perf_metrics.go` 里的 `getUsablePerfMetricGroups(c)`，不要复制一份。它内部调用 `service.GetUserUsableGroupsForRole(userGroup, role)`：普通用户和游客按各自的用户分组（游客的分组为空）取可用分组；管理员会补全 `GroupRatio` 里的所有分组。
  - 不可见分组的数据**在后端就去掉**，前端拿不到。分组列表、模型行、矩阵列、横幅里的异常芯片、分组可用率都只基于可见分组计算。
  - `auto` 分组不作为一个独立分组展示，与原型里的 `EXCLUDED_GROUPS` 保持一致。
- **分组倍率按用户分组显示**：和 `GetPricing` 的写法一样，先取 `ratio_setting.GetGroupRatioCopy()`，再对每个分组用 `ratio_setting.GetGroupGroupRatio(userGroup, g)` 覆盖。分组描述的来源与 `usable_group` 一致。也就是说，同一个分组，不同用户看到的倍率可能不同，所以倍率要在每次请求时计算，**不能放进 §5.3 的共享缓存**。
- 每个分组下列哪些模型，以定价数据为准（`model.GetPricing()` 的 `EnableGroup`，过滤方式同 `filterPricingByUsableGroups`）。所以"有售卖但这段时间没有请求"的模型也会出现，状态显示"无请求"。只有统计数据、已不再售卖的模型不显示。
- 测试必须覆盖三种身份：普通用户看不到其他分组、倍率按用户分组覆盖、管理员能看到全部分组。

### 5.2 响应结构（建议，字段名可以调整）

```jsonc
{
  "success": true,
  "data": {
    "range": "1h",
    "bucket_seconds": 60,
    "window_start": 1760000000,   // 第一根柱的起点
    "updated_at": 1760003600,
    "has_detail": true,           // 只有 5m/1h 为 true：有分位数和失败原因
    "groups": [{
      "key": "default", "desc": "…", "ratio": 1,
      "availability": 0.995,      // 分组内按请求数加权；无请求时为 null
      "models": [{                // 已按本区间请求数降序排好
        "name": "gpt-4o", "vendor_icon": "OpenAI",
        "availability": 0.998,    // 整个区间
        "current": 1.0,           // 最近 max(3, ceil(N*8%)) 根柱；无请求时为 null
        "ttft_avg_ms": 820, "latency_avg_ms": 5400, "tps": 61.2,
        "buckets": [{ "rate": 1.0, "ttft_ms": 800 } /* 无请求时为 null */],
        "detail": {               // 仅 has_detail=true 时出现
          "ttft_p50": 0, "ttft_p95": 0, "ttft_p99": 0,
          "latency_p50": 0, "latency_p95": 0, "latency_p99": 0,
          "fail_share": { "5xx": 0.6, "429": 0.3, "timeout": 0.1, "other": 0 }
        }
      }]
    }]
  }
}
```

- **响应里不出现任何请求数**，包括管理员。凡是需要按请求数加权的值（分组可用率、当前可用率、排序）都在后端算好。失败原因只给占比。
- 柱子是位置数组，第 i 根的起点是 `window_start + i * bucket_seconds`，前端据此生成悬浮提示里的时间。
- 状态阈值（≥99% 正常、95%–99% 降级、<95% 异常、无请求）由前端按原型的 `status()` 计算。后端只给比率。

### 5.3 缓存（确保计算量与访问量无关）
- 每个 range 在进程内缓存一份**不按用户过滤的完整聚合结果**，TTL 与前端刷新间隔一致：5m 10 秒，1h 30 秒，24h 5 分钟，7d 30 分钟，30d 60 分钟。用 singleflight 或等价机制，保证同一时刻同一 range 最多只有一次计算。
- 按用户可见分组的过滤，在每次请求时对缓存副本做。这一步只是遍历内存，很便宜。**不要修改缓存中共享的切片**。
- 定价数据本来就有缓存（`model.GetPricing()`），直接用。

## 6. 前端

- 路由 `web/src/routes/status/index.tsx`，参照 `routes/pricing/index.tsx`：用 zod `validateSearch` 管理 `range`、`group`、`view`、`q`，默认值是 `1h` / `all` / `list` / `''`。
- 功能目录 `web/src/features/model-status/`，包含 `api.ts`、`types.ts`、`components/*`、`index.tsx`。
- 布局复用 `PublicLayout` / `PublicHeader`。Hero 和背景光晕参照 `features/pricing/index.tsx`；状态点参照 `components/status-badge.tsx`；分组头参照 `uptime-panel.tsx`；厂商图标用 `getLobeIcon`。
- 数据请求：`useQuery(['model-status', range])`，`refetchInterval` 和 §5.3 的 TTL 一致。切换 range 时收起已展开的行。
- 排序：先按当前严重程度（异常 > 降级 > 正常 > 无请求），同级时保持后端给的请求量顺序（稳定排序）。
- 展开行：`has_detail` 为 true 时显示三块面板（时延分位、失败原因、吞吐）；为 false 时只显示吞吐面板，布局由实现者调整，保持美观即可。
- 悬浮提示：柱子数量多（最多 60 列 × 很多行），按原型做一个共享的浮动提示即可，不必给每根柱子挂一个 shadcn Tooltip。
- 导航：`hooks/use-top-nav-links.ts` 新增 `status` → `t('Model Status')` → `/status`。后台 `system-settings/maintenance` 的导航设置里加上这个模块，默认值和 `rankings` 保持一致。
- 所有文案走 `t('English key')`，并同步 7 个语言文件（`bun run i18n:sync`）。要遵守 `prefers-reduced-motion`，暗色模式靠主题 token 实现。

## 7. 测试与验证

- 后端：按 `AGENTS.md` 的要求，**最多新增一个测试文件**，用 testify 写表驱动测试，覆盖：失败分类规则；10 秒到 1 分钟的合并和窗口边界；直方图分位数；当前可用率的桶数公式；响应里不含请求数；不可用分组的数据被过滤掉；缓存不会被按用户过滤修改。
- 数据库：长时间段的读取在 SQLite / MySQL / PostgreSQL 上各跑一次，记录版本和结果。不涉及 schema 变更，所以不需要做迁移验证。
- 前端：`bun run build`，加上 lint / typecheck。用本地实例（见 local-run 流程）打一些真实请求，再到页面上目测 5m / 1h / 24h 三档。

## 8. 已决定事项（用户确认）
- 页面面向普通用户，最重要的是"现在能不能用"。
- 分位数和失败原因只在 5m / 1h 显示。
- 不扫描 `logs`，不改表也不新增表。长时间段复用 `perf_metrics`，24h 用 24 根小时柱。
- 页面和接口都不展示请求数。
- 开关和登录要求走 `status` 导航模块。
- 新代码尽量放在新文件里。

## 9. 留给实现者的决定
- 直方图的具体区间划分和分位数取值方式（上界还是中点）。
- 只存 10 秒桶还是两种粒度都存；Redis key 和字段的具体命名。
- 失败分类的完整映射表和优先级。
- bucket_time 小于 1 小时时长时间段的查询规模怎么控制。
- `SegmentedControl` 复用已有组件还是新建。
- 长时间段展开行的具体布局。
