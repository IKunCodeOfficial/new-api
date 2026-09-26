# 缓存丢失补偿(Cache Rebuild Relief)技术方案

> 状态:设计稿(未实现)
> 适用分支:ikv(fork 特性,遵循"新文件隔离、少改上游文件"原则)

## 1. 背景与问题

new-api 聚合多个上游。客户使用 Claude Code / Codex 时,即使渠道亲和性(channel affinity)保证了同一会话始终命中同一渠道,上游侧仍可能发生 prompt 缓存丢失(上游内部负载均衡、缓存过期、渠道商换池等)。表现为:

- **Claude Code**:本应命中 `cache_read_input_tokens` 的回合,变成大额 `cache_creation_input_tokens`(按 1.25x/2x 计费),客户为同一段上下文反复付"写缓存"的钱。
- **Codex**:本应 `cached_tokens` 占大头(0.1x~0.25x 价格),变成全额 `input_tokens`(1.0x 价格)。

客户对此有意见:缓存丢失不是客户的错,却由客户买单。

**产品目标**:当识别到"同一会话连续 2 次及以上大额缓存创建"(且上下文 > 50k tokens)时,从第 2 次开始,该请求费用按原价 **50%** 结算,并在使用日志中明确展示补偿信息,让客户感知到确实省钱了。

## 2. 目标与非目标

**目标**

1. 仅对 Claude Code / Codex 客户端会话生效(可配置)。
2. 精确定义并检测"连续大额缓存创建"(见 §4,这是本方案的核心)。
3. 从连续第 2 次开始,整单费用 ×50%(比例可配置)。
4. 使用日志(消费日志 `other` 字段 + 日志内容文案 + 前端详情弹窗)可见补偿明细:第几次连续重建、原价、折后价、节省金额。
5. 遵守项目计费安全不变量(`AGENTS.md`):不产生负额、饱和转换走 `common/quota_math.go`、异常可审计。
6. 多实例部署(Redis)下状态一致。

**非目标**

- 不试图阻止上游缓存丢失本身(那是亲和性/渠道治理的职责)。
- 不修改预扣费(pre-consume)逻辑:折扣在结算(settle)时生效,多扣部分由现有 `SettleBilling` 差额退还机制自动返还。
- 不对非 Claude Code / Codex 流量生效(普通 API 调用方通常自行管理缓存策略)。

## 3. 现有代码基础(方案依赖的事实)

| 能力 | 位置 | 说明 |
|---|---|---|
| 会话识别 | `service/channel_affinity.go` `GetChannelAffinityStatsContext(c)` | 亲和性规则命中后,settle 阶段可拿到 `{RuleName, UsingGroup, KeyFingerprint, TTLSeconds}`。内置规则:`claude cli trace`(键源 `metadata.user_id`)、`codex cli trace`(键源 `prompt_cache_key`),见 `setting/operation_setting/channel_affinity_setting.go` |
| 统一结算入口 | `service/text_quota.go` `PostTextConsumeQuota` | Claude `/v1/messages`(`relay/claude_handler.go`)与 Codex `/v1/responses`(`relay/responses_handler.go`)最终都调用它;此处 usage 已是上游真实值 |
| usage 语义 | `calculateTextQuotaSummary` | anthropic 语义下 `PromptTokens` 不含缓存部分;openai 语义下 `PromptTokens` 含 `cached_tokens`。缓存字段:`CacheTokens`(读)、`CacheCreationTokens/5m/1h`(写) |
| 分布式状态 | `pkg/cachex` `HybridCache`(内存 + Redis) | 亲和性缓存与 usage 统计已用同一套(`channelAffinityUsageCacheStatsCache` 模式,含分片锁) |
| 饱和转换 | `common/quota_math.go` | `QuotaFromDecimalChecked` + `noteQuotaClamp` + `attachQuotaSaturation` 审计链 |
| 日志注入 | `service/log_info_generate.go` | `other` 顶层字段用户可见;`other.admin_info` 仅管理员可见(`formatUserLogs` 会剥离) |
| tiered 计费 | `service/tiered_settle.go` | **绕过 `PriceData.OtherRatios`**,quota 由表达式直接算出 —— 因此折扣不能用 `AddOtherRatio` 实现,必须作用于最终 quota |

## 4. 核心问题:如何判定"连续大额缓存创建"

### 4.1 为什么不能简单看"连续两次出现 cache_creation"

Claude Code 的**正常**工作方式就是每回合增量写缓存:第 N 回合命中前 N-1 回合的缓存(大额 `cache_read`),同时把新增的一小段写入缓存(小额 `cache_creation`)。因此"连续多次出现 cache_creation"是常态,不是故障。

**缓存丢失的真实签名**是:在一个已建立的会话中,某个回合 `cache_read ≈ 0`(或远小于上下文),而 `cache_creation`(Claude)/未命中输入(Codex)接近整个上下文 —— 即客户端把整段历史重新写了一遍缓存。

### 4.2 请求事件分类(结算时,基于真实 usage)

先定义各格式的规范化量(全部来自 `textQuotaSummary`,即 `effectiveBillingUsage` 重映射后的计费口径):

| 量 | anthropic 语义(Claude Code) | openai 语义(Codex) |
|---|---|---|
| `contextTokens` 总上下文 | `PromptTokens + CacheTokens + CacheCreationTokens`(anthropic 的 input_tokens 不含缓存) | `PromptTokens`(已含 cached) |
| `readTokens` 缓存读 | `CacheTokens`(= cache_read_input_tokens) | `CacheTokens`(= prompt_tokens_details.cached_tokens) |
| `writeTokens` 缓存写/未命中 | `cacheWriteTokensTotal(summary)`(5m/1h 合并) | `PromptTokens - CacheTokens`(OpenAI 写缓存免费,"重建"体现为大额未命中输入) |

每个请求被分类为以下事件之一(阈值全部可配置,默认值见 §7):

```
REBUILD(大额缓存重建)——同时满足:
  contextTokens >= MinContextTokens          // 必要条件,默认 50_000
  writeTokens   >= MinRebuildTokens          // 大额,默认 30_000
  writeTokens   >  readTokens * MissDominanceFactor
                                             // 未命中占主导,默认 factor=1(写 > 读)
                                             // 排除"正常增量写 + 大额读命中"的健康回合

HIT(健康命中):
  readTokens >= contextTokens * HitFloorRate // 默认 0.5,读命中占上下文一半以上

SMALL(小请求):
  contextTokens < MinContextTokens           // 例如 Claude Code 的 haiku 子请求、
                                             // topic detection、Codex 的辅助小调用

OTHER:其余(中等规模、读写都不显著)
```

分类只在 usage 非空且计费成功的请求上进行;`usage == nil`(上游超时)不参与,不改变状态。

### 4.3 会话状态机("连续"的精确定义)

**会话键** = 亲和性统计键:`RuleName + UsingGroup + KeyFingerprint`(Claude Code 按 `metadata.user_id`,Codex 按 `prompt_cache_key`)。不落明文,沿用现有 SHA1 前 8 位指纹。

每个会话键维护状态:

```go
type cacheRebuildState struct {
    Consecutive     int   // 连续 REBUILD 计数
    LastEventAt     int64 // 上一次 REBUILD 的 unix 秒
    LastContext     int64 // 上一次 REBUILD 的 contextTokens(留作后续启发式)
    WindowStartAt   int64 // 防滥用窗口起点
    WindowDiscounts int   // 窗口内已折扣次数
    WindowSavedQuota int64 // 窗口内累计节省 quota(展示 + 防滥用)
}
```

**状态转移(在 settle 时执行,原子地读-改-写):**

| 事件 | 转移 | 说明 |
|---|---|---|
| `REBUILD` 且 `now - LastEventAt > MaxGapSeconds` | `Consecutive = 1`,不折扣 | 间隔超窗:上游缓存(Claude 5m / OpenAI ~5-10min)本来就会自然过期,重建是**合理付费**,序列重新开始 |
| `REBUILD` 且在窗口内 | `Consecutive++`;若 `Consecutive >= ConsecutiveThreshold`(默认 2)→ **本次请求折扣** | 第 1 次重建全价(首次建缓存天然要付钱),第 2 次起打折 |
| `HIT` | `Consecutive = 0` | 缓存恢复正常,序列终止 |
| `SMALL` | 状态不变 | 小请求(子 agent、标题生成)穿插在会话中,不应打断"连续"判定 —— 这是与朴素"相邻两次请求"定义的关键差异 |
| `OTHER` | 状态不变 | 保守:不加计数也不清零 |

状态 TTL = `MaxGapSeconds * 4`(自然过期即视为序列结束)。

**时序示例**(Claude Code,context 120k):

```
T0  REBUILD(首次建缓存)         → Consecutive=1,全价
T1  HIT(正常回合)               → 0
T2  REBUILD(上游缓存丢了)       → 1,全价(单次丢失不补偿:可能只是 5m TTL 自然过期)
T3  REBUILD(又丢,连续第 2 次)   → 2,★ 本单 50%
T3' SMALL(haiku 子请求)         → 2(不打断)
T4  REBUILD(还在丢)             → 3,★ 50%
T5  HIT(渠道缓存恢复)           → 0,恢复全价
```

这正回答了"连续大额缓存如何判断":**连续 = 同一会话键下,两次 REBUILD 之间没有 HIT、且时间间隔不超过 MaxGapSeconds;SMALL/OTHER/无 usage 的请求不打断序列。**

### 4.4 客户端识别(必要条件:Claude Code 或 Codex)

判定链(全部满足才进入分类器):

1. 功能开关 `Enabled == true`。
2. `GetChannelAffinityStatsContext(c)` 存在,且 `RuleName ∈ AffinityRuleNames`(默认 `["claude cli trace", "codex cli trace"]`)。亲和性规则本身已按 model/path/键源过滤,复用它避免重复造识别逻辑,也保证"补偿"与"粘渠道"的会话口径完全一致。
3. (可选,默认开启)`User-Agent` 包含 `UserAgentInclude` 关键词之一:`claude-cli`(Claude Code)/`codex`(Codex CLI)。用于排除自建脚本恰好带 `metadata.user_id` / `prompt_cache_key` 的情况;设为空数组则跳过 UA 校验。

若亲和性总开关关闭,本功能自然失效(文档化为前置依赖,不做独立的会话追踪兜底,保持实现单一)。

## 5. 计费实现

### 5.1 注入点与折扣方式

**唯一插入点:`PostTextConsumeQuota`(`service/text_quota.go`)**,在 tiered 组合完成之后、`UpdateUserUsedQuotaAndRequestCount`/`SettleBilling` 之前,对 `summary.Quota` 做最终缩放:

```go
// service/text_quota.go — PostTextConsumeQuota 内,tiered 分支之后插入(约 3 行):
relief := ApplyCacheRebuildRelief(ctx, relayInfo, &summary, billingUsage)
if relief != nil {
    extraContent = append(extraContent, relief.LogContent())
}
```

`ApplyCacheRebuildRelief`(新文件 `service/cache_rebuild_relief.go`)内部:

```go
func ApplyCacheRebuildRelief(ctx *gin.Context, relayInfo *relaycommon.RelayInfo,
    summary *textQuotaSummary, usage *dto.Usage) *CacheRebuildReliefResult {

    // 1. 前置过滤:开关 / usage 非空 / summary.Quota > 0 / 客户端识别(§4.4)
    // 2. 事件分类(§4.2)+ 状态机转移(§4.3,分片锁内读-改-写 HybridCache)
    // 3. 未触发折扣 → 返回 nil(状态已更新)
    // 4. 触发折扣:
    original := summary.Quota
    dq := decimal.NewFromInt(int64(original)).
        Mul(decimal.NewFromFloat(setting.DiscountRatio)) // 默认 0.5
    discounted, clamp := common.QuotaFromDecimalChecked(dq)
    noteQuotaClamp(relayInfo, clamp)
    if discounted < 1 { discounted = 1 }   // 保持"有消耗至少记 1"的既有语义
    if discounted >= original { return nil } // 防御:比例配置异常时不生效
    summary.Quota = discounted
    // 5. 组装结果(consecutive、original、discounted、saved)供日志展示
}
```

**关键决策:为什么缩放最终 quota,而不是 `PriceData.AddOtherRatio("cache_rebuild_relief", 0.5)`**

- tiered_expr 计费路径(`TryTieredSettle`)不经过 `ApplyOtherRatiosToDecimal`,`OtherRatios` 方案会在 tiered 用户身上失效;
- 检测必须发生在 settle(依赖真实 usage),而 `OtherRatios` 语义上属于定价期参数;
- 最终缩放对 普通按量 / 按次(UsePrice)/ tiered 三条路径行为一致,且天然把工具附加费(web_search 等)一并纳入 5 折 —— 与产品口径"费用降低为原来的 50%"(整单)一致。

**折扣范围决策**:整单 ×0.5(而非仅折扣 cache_creation 部分)。理由:客户诉求是"感知省钱",整单口径最直观、文案最好写;若后续认为过于慷慨,可加配置 `Scope: "total" | "cache_write_only"`,本期只做 `total`。

### 5.2 与既有计费机制的交互

| 机制 | 交互 | 结论 |
|---|---|---|
| 预扣费 / 差额退还 | 折扣只改 settle 的 `actualQuota`,`SettleBilling` 自动退差 | 无需改动 |
| tiered_expr | 缩放发生在 `composeTieredTextQuota` 之后 | 覆盖 |
| 按次计费(UsePrice) | 同样被缩放 | 覆盖(该场景 REBUILD 几乎不会触发,因为按次模型没有缓存语义,分类器会给 OTHER) |
| 免费模型 / `Quota == 0` | 前置过滤直接跳过,状态照常更新 | 不产生"负折扣" |
| 断流计费(fork 的 interrupt billing:client_gone 后 drain 出真实 usage) | usage 真实,正常参与分类与折扣 | 兼容 |
| 重试换渠道 | 一次请求只 settle 一次,状态记一次 | 兼容 |
| admin reject | `adminRejectReason` 非空时跳过折扣(仍更新状态) | 避免争议账目 |
| 饱和审计 | `QuotaFromDecimalChecked` + `noteQuotaClamp` → `attachQuotaSaturation` 既有链路 | 满足 AGENTS.md 不变量 |

`DiscountRatio` 配置加载时校验 `0 < ratio < 1`,非法值回落 0.5 并 `common.SysError`。折扣结果恒有 `1 <= discounted < original`,不可能为负、不可能反向加价。

### 5.3 状态存储与并发

- 新 `HybridCache[cacheRebuildState]`,namespace `new-api:cache_rebuild_relief:v1`,完全复制 `channelAffinityUsageCacheStatsCache` 的构造模式(内存 LRU + Redis JSONCodec + janitor)。
- 进程内用 64 路分片 `sync.Mutex`(同 `channelAffinityUsageCacheStatsLocks` 模式)保证单实例读-改-写原子。
- 多实例:Redis 层是 get/set 非原子,极端并发下同一会话两个实例可能各自 +1。后果上界:多给或少给一次折扣,金额有 §6 防滥用上限兜底,可接受;不引入 Lua 脚本的复杂度。Redis 不可用时退化为各实例本地内存状态(判定独立、不会翻倍折扣,只是连续性统计各算各的)。

## 6. 防滥用(必须与折扣同时上线)

风险:恶意用户故意每次请求变换 prompt 前缀、强制全量重写缓存,把所有大上下文流量刷成 5 折(Claude 写缓存 1.25x×0.5=0.625x < 正常输入 1.0x;Codex 1.0x×0.5=0.5x)。

缓解(全部落在 `cacheRebuildState` 的窗口字段上,默认值可配置):

1. **每会话滑动窗口折扣次数上限** `MaxDiscountsPerWindow`(默认 10 次 / `WindowSeconds` 默认 3600s):超限后仍记录状态与日志标记(`relief_capped: true`,进 `admin_info`),但不再打折。正常客户一小时内连续 10 次缓存丢失早该换渠道了,该上限对真实受害者几乎无感。
2. **每会话窗口节省额度上限** `MaxSavedQuotaPerWindow`(默认 0 = 不限;运营可按站点定价设置)。
3. 折扣事实进入 `other.admin_info.cache_rebuild_relief_detail`(检测数据:read/write/context、counter、会话指纹),管理员可离线审计高频受益 token/用户。
4. 兜底:功能全局开关 + 按规则名单收窄,出现刷量可即时关停。

## 7. 配置

新文件 `setting/operation_setting/cache_rebuild_relief_setting.go`,注册 `config.GlobalConfig.Register("cache_rebuild_relief_setting", ...)`(与亲和性配置同机制,管理端运营设置可改,无需重启):

```go
type CacheRebuildReliefSetting struct {
    Enabled                bool     `json:"enabled"`                  // 默认 false,灰度开启
    AffinityRuleNames      []string `json:"affinity_rule_names"`      // ["claude cli trace","codex cli trace"]
    UserAgentInclude       []string `json:"user_agent_include"`       // ["claude-cli","codex"];空=不校验 UA
    MinContextTokens       int      `json:"min_context_tokens"`       // 50000(产品必要条件)
    MinRebuildTokens       int      `json:"min_rebuild_tokens"`       // 30000(大额写阈值)
    MissDominanceFactor    float64  `json:"miss_dominance_factor"`    // 1.0(write > read*f)
    HitFloorRate           float64  `json:"hit_floor_rate"`           // 0.5(HIT 判定)
    ConsecutiveThreshold   int      `json:"consecutive_threshold"`    // 2(第 N 次起折扣)
    MaxGapSeconds          int      `json:"max_gap_seconds"`          // 600(超窗视为自然过期)
    DiscountRatio          float64  `json:"discount_ratio"`           // 0.5
    WindowSeconds          int      `json:"window_seconds"`           // 3600(防滥用窗口)
    MaxDiscountsPerWindow  int      `json:"max_discounts_per_window"` // 10
    MaxSavedQuotaPerWindow int64    `json:"max_saved_quota_per_window"` // 0=不限
}
```

## 8. 日志与前端展示(让客户"看得见省钱")

### 8.1 消费日志 `other`(用户可见,不放 admin_info)

```json
{
  "cache_rebuild_relief": {
    "applied": true,
    "consecutive": 2,          // 连续第几次大额重建
    "discount_ratio": 0.5,
    "original_quota": 250000,  // 折前
    "quota": 125000,           // 折后(与日志主 quota 字段一致)
    "saved_quota": 125000
  }
}
```

同时在 `other.admin_info.cache_rebuild_relief_detail` 写入检测明细(event、read/write/context tokens、会话指纹、是否 capped),仅管理员可见。

### 8.2 日志内容文案(`extraContent`,出现在日志"详情"文本里)

```
缓存丢失补偿:连续第 2 次大额缓存重建,本次费用按 50% 计,节省 $0.6250
```

金额用现有 `logger.LogQuota` 格式化,保持与其他文案(工具调用花费等)一致。

### 8.3 前端(`web/src/features/usage-logs/`)

1. `types.ts`:`other` 类型补充 `cache_rebuild_relief` 字段。
2. 列表列 / 移动端卡片:`applied === true` 时在费用旁渲染绿色徽标 `省 50%`(复用现有 Tag 风格)。
3. `details-dialog.tsx`:新增"缓存补偿"区块,展示 连续次数 / 原价 / 折后价 / 节省金额(格式化复用 `lib/format.ts`)。
4. i18n:`web/src/i18n/locales/{en,zh,zh-TW,fr,ru,ja,vi}.json` 按"英文原文为 key"约定新增:`Cache loss compensation`、`Saved {{amount}}`、`Consecutive large cache rebuilds: {{count}}` 等;运行 `bun run i18n:sync`。

## 9. 实现清单(文件级)

| 文件 | 类型 | 内容 |
|---|---|---|
| `setting/operation_setting/cache_rebuild_relief_setting.go` | 新增 | §7 配置结构 + 注册 + 取值校验 |
| `service/cache_rebuild_relief.go` | 新增 | 事件分类器、状态机、HybridCache 存取、`ApplyCacheRebuildRelief`、日志字段组装 |
| `service/cache_rebuild_relief_test.go` | 新增 | §10 测试 |
| `service/text_quota.go` | 修改(~5 行) | `PostTextConsumeQuota` 内单一调用点 + `other["cache_rebuild_relief"]` 注入 |
| `pkg/cachex/namespace.go` | 修改(1 行) | 若命名空间需集中登记则追加 |
| `web/src/features/usage-logs/*` | 修改 | §8.3 展示 |
| `web/src/i18n/locales/*.json` | 修改 | 新文案 |

后端改动集中在两个新文件 + `text_quota.go` 一个调用点,符合本 fork "新文件隔离、减少 merge 冲突"的约定;`text_quota.go` 本就是历次合并的热点文件,插入点选在 tiered 组合之后的稳定位置并保持单行调用,降低冲突面。

## 10. 测试计划(testify,表驱动)

1. **分类器表测试**:anthropic / openai 两种语义 × {REBUILD, HIT, SMALL, OTHER} 边界值(恰好 50k、write=read、5m/1h 拆分写、OpenAI cached>prompt 异常值),精确断言事件类型。
2. **状态机表测试**:§4.3 的时序示例逐步断言 `Consecutive` 与是否折扣;覆盖 gap 超窗重置、SMALL 不打断、HIT 清零、阈值=3 的自定义配置。
3. **折扣安全**:`Quota=1` 折后仍 ≥1;`DiscountRatio` 非法值不生效;`math.MaxInt32` 附近 quota 折扣不溢出(`QuotaFromDecimalChecked` 路径断言 clamp 审计)。
4. **防滥用**:窗口内第 11 次 REBUILD 不折扣且状态标记 capped。
5. **settle 集成**:构造 gin context + 亲和性 meta + usage,走 `PostTextConsumeQuota`,断言消费日志 `other.cache_rebuild_relief` 与最终 quota;显式初始化测试内 DB/settings 状态(项目测试规范)。

## 11. 上线与灰度

1. 默认 `Enabled=false` 合入;
2. 灰度站点打开,观察 1~2 天 `admin_info.cache_rebuild_relief_detail` 的触发分布(误报率:HIT 会话被误判 REBUILD 的比例应≈0;真实缓存丢失渠道应集中在少数 channel);
3. 根据分布微调 `MinRebuildTokens` / `MissDominanceFactor` / `MaxGapSeconds`;
4. 全量开启,公告客户"缓存丢失自动补偿"作为服务卖点。

## 12. 开放问题(实现前需拍板)

1. **MaxGapSeconds 与 Claude 5m 缓存的关系**:Claude Code 默认 5m 缓存,严格说间隔 >5min 的重建是合理付费;默认 600s 略慷慨(把 5~10min 的边界让利给客户)。 -》下调为 330s。
2. **Codex 首轮长上下文**:用户粘贴超大 prompt 开新会话,第 1、2 轮都可能是大额未命中 —— 第 2 轮会被折扣(readTokens 小)。这偏向客户;若要更严,可加启发式"本次 context 与上次 REBUILD 的 context 相似(±20%)才计连续"(状态里已预留 `LastContext` 字段)。=》默认从第三轮
3. **是否叠加渠道惩罚**:连续 REBUILD 本质是渠道质量信号,后续可把该计数回馈给亲和性(如自动 `ClearCurrentChannelAffinityCache` 换渠道),本期不做,只补偿。-》不做
