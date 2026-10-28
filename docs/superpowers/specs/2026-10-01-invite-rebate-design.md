# 邀请返利（重写版）设计

> 日期：2026-10-01
> 取代：`/Users/hzy/PycharmProjects/ikun_rebate`（v3.5，从未上线、未发过钱，整体废弃）
> 范围：一个独立 Python 项目（结算）+ fork 内少量改动（充值口径采集、后台配置、钱包展示）

## 1. 目标与约束

受邀用户用**真实充值**得到的额度被消费后，按比例把返利加到邀请人的**推广奖励余额**（`users.aff_quota`，钱包页 "Referral Program" 卡片显示为 Pending），由邀请人自行用已有的 "Transfer to Balance" 提取到钱包余额；同时给邀请人写一条管理类型日志。

用户给定的约束：

1. 管理员可修改返利百分比（后台页面）。
2. 在线充值、兑换码充值算充值；管理员加额度时勾选"充值"才算，并可填实付金额（默认等于加的额度），返利按实付算。在线充值同样按实付算（含档位折扣、分组倍率）。
3. 每天 00:05 结算前一天。
4. 结算是独立 Python 项目，不在 new-api 进程内。
5. 结算基数是受邀用户实际消费；漏结或异常时可手动按日期范围补结。
6. 只能新增表，不能改已有表结构；MySQL 与 PostgreSQL 都要支持。不使用 SQLite。
7. 合理利用 Redis。
8. 入账后给邀请人写管理日志："返利结算：共 x 个受邀用户消费，返利 xx"，金额单位与钱包余额展示一致，文案覆盖前端全部 7 种界面语言。

部署全部为 docker。活动从首次部署当天开始，无历史数据迁移。

## 2. 返利口径

对每个受邀用户 `u`（`users.inviter_id ≠ 0`）维护累计值，活动起点 `T0` 之前的数据一律不计：

```
R = 累计有效充值（quota）
C = 累计净消费（quota）= Σ type=2 消费 − Σ type=6 退款
base = min(R, C)
delta = max(0, base − settled_base)
settled_base += delta
```

邀请人 `I` 在日期 D 的返利：`rebate = floor(Σ_{u∈I 名下} delta_u × rate)`，用 Decimal 计算，按邀请人汇总后再取整。

含义：只有"用充值得来的额度"被消费才返利；赠送额度、邀请奖励、未勾选"充值"的管理员加额度，消费了都不返。
退款会让 C 回落，回落期间 delta 为 0，C 回升到原水位之前不会重复返利（高水位）。

### 2.1 有效充值 R 的三个来源

| 来源 | 取数 | 计入 R 的值 |
|---|---|---|
| 在线充值 | `top_ups` 中 `status='success'` **且 `amount > 0`**，`complete_time > 0` 时按完成时间落日，否则按 `create_time` 落日 | `min(到账额度, paid_quota)`；`rebate_topup_paid` 无该 `trade_no` 时取到账额度 |
| 兑换码 | `redemptions` 中 `status=3`，按 `redeemed_time` 落日，归属 `used_user_id`。**必须忽略 `deleted_at`**：管理员"清理无效兑换码"（`model.DeleteInvalidRedemptions`）会对 `status=3` 的行软删除，直连 SQL 若不过滤就会静默少算 R | `quota` 全额 |
| 管理员充值 | `rebate_admin_recharges`，按 `created_at` 落日 | `paid_quota` |

历史在线订单正常情况下可能没有 `complete_time`（NULL 或 0）。2026-10-08 经业务备份验证并确认，采用完成时间优先、创建时间兜底；有完成时间的订单不得按创建时间提前计入。兜底订单跨午夜支付时可能归入下单日，仍受活动起点与成功状态约束，不回填或修改原始订单。MySQL 历史 `top_ups` 与新建 `rebate_topup_paid` 可能使用不同排序规则，读取实付记录时按订单号分批参数查询，避免跨表字符串 JOIN 的排序规则冲突。

在线充值到账额度（与 `model/topup.go` 入账一致；渠道按 `payment_provider` 判定，为空的旧行按 `payment_method` 推断：`stripe`/`creem`/`waffo`/`waffo_pancake` 同名，其余视为 `epay`）：

| 渠道 | 到账额度 |
|---|---|
| epay / waffo / waffo_pancake | `amount × QuotaPerUnit` |
| stripe | `money × QuotaPerUnit`（Decimal，半远离零取整） |
| creem | `amount`（本身已是 quota） |

已知但**不该出现在 `top_ups`** 的渠道：`payment_provider='balance'` 只在"用余额购买订阅"时使用，该路径只写 `subscription_orders`，用余额扣 `users.quota`，不写 `top_ups`（`model/subscription.go:799-816`），所以它出现在成功订单里说明库被人工改过。与其他未知渠道同样处理：当天结算失败（见 §6），不静默跳过。

但**在线支付**购买订阅会写下一行 `top_ups`：`model.CompleteSubscriptionOrder` 调用 `upsertSubscriptionTopUpTx`（`model/subscription.go:649-667`），写入 `status='success'`、`amount=0`、`money=套餐价`、`payment_provider` 为空（渠道靠 `payment_method` 推断，stripe/creem/epay 等都可能）的行。这行的 `amount=0`，所以 `TopUp.AfterCreate` 钩子按下一节的规则不为它写 `rebate_topup_paid`；但结算侧若照上表按 `status='success'` 全取，就会把 `amount ≤ 0` 的 stripe 订阅行当成 `money × QuotaPerUnit` 的充值计入 R。因此 **R 的在线充值来源必须排除 `amount <= 0` 的 `top_ups` 行**（等价写法：`amount > 0`），订阅资金链就与返利口径完全隔开。

### 2.1.1 消费侧与订阅

消费口径就是 `logs` 的 `type=2` 之和（减 `type=6`），**但不含订阅计费的日志**。**本期不考虑订阅**：启用订阅后必须重新评估口径，不要沿用下面的结论。

原因是订阅在本系统里走一条完全独立的资金链：买订阅写的是 `subscription_orders`，用余额买订阅是直接扣 `users.quota` 且不产生消费日志（`model/subscription.go:799-816`）；在线支付买订阅虽然会由 `upsertSubscriptionTopUpTx` 顺带写一行 `top_ups`（`amount=0`，见 §2.1），但该行不写 `rebate_topup_paid`，且结算侧按 §2.1 的规则跳过 `amount <= 0` 的行，所以订阅既不进 R；同时订阅计费的消费日志（`billing_source="subscription"`，`logs.other` 里带 `subscription_id`）里的 `quota` 计的是**订阅额度**，不是钱包额度，也进不了 C。两套口径互不相干。目前站内未启用订阅；一旦启用订阅，需要决定订阅消费是否、以及按什么基数参与返利。

在此之前结算侧对订阅消费**不计入 C**，而不是让当天失败（失败会让任何一个受邀用户用过订阅的日子永久卡住）：`type` 为 2 或 6、且 `logs.other` 含标记 `"billing_source":"subscription"` 的日志不计入当日 `c`。标记由 `service/log_info_generate.go` 的 `appendBillingInfo`（`other["billing_source"] = relayInfo.BillingSource`）写入，`other` 经 `common.MapToJsonStr`（`encoding/json`，紧凑输出、无空格）序列化，所以按子串 `"billing_source":"subscription"` 匹配：用方言无关的 `LIKE '%…%' ESCAPE '/'`，`_` 被转义，不当通配符；嵌在其它 JSON 字符串值里的同样文字会被序列化成 `\"billing_source\":\"subscription\"`，匹配不上。`other` 为 NULL 或空串的日志照常计入。每天统计受邀用户被排除的条数，大于 0 时打一行 WARN（`--dry-run` 同样打印）。这个方向只会少发：用订阅消费的受邀用户不给邀请人带来返利。

已知缺口：只有文本 / 音频 / 实时请求的消费日志带这个标记。按次的异步任务（`service/task_billing.go`：提交时的消费日志、差额结算的消费 / 退款日志、失败退款日志）不带 `billing_source`，订阅支付的任务仍会作为钱包消费计入 C（提交扣费与差额补扣会多付，退款会少付）。因此上线时不应有启用中的订阅套餐；要支持订阅，需要先让任务计费日志也写出这个标记。

### 2.2 实付 `paid_quota`

- **在线充值**：fork 在 `TopUp` 的 GORM `AfterCreate` 钩子里，于下单时刻按当时单价换算。epay、Waffo、Waffo Pancake 的 `TopUp.Money` 必须先按各网关实际发送的金额字符串固化，不能保留币种舍入前的价格；发送和落库复用同一金额。保留各渠道已有的舍入规则（epay/Waffo 的浮点格式化、Pancake 的 Decimal 格式化；Waffo 零小数币种按整数）。舍入后为零的订单在创建前拒绝：

  | 渠道 | `paid_quota` |
  |---|---|
  | epay | `Money ÷ operation_setting.Price × QuotaPerUnit` |
  | waffo | `Money ÷ setting.WaffoUnitPrice × QuotaPerUnit` |
  | waffo_pancake | `Money ÷ setting.WaffoPancakeUnitPrice × QuotaPerUnit` |
  | stripe | `Amount × QuotaPerUnit`（Checkout 按 `Quantity=Amount` × 固定 Stripe 价格收款，分组倍率只放大到账额度 `Money = Amount × 倍率`，所以实付按购买数量计；优惠码折扣在支付完成时按 `amount_total / amount_subtotal` 下调快照） |
  | creem | `Amount`（下单时按商品额度；折扣码在支付完成时按 `(amount_paid − tax_amount) / sub_total` 下调） |

  单价 ≤ 0 或结果非正时不写行（结算侧回落为到账额度）。换算使用 `common.QuotaFromDecimal` 等 `common/quota_math.go` 助手，不做裸 `int` 转换。
  支付完成时网关报告的折扣只会下调快照，不会上调：Stripe 按 `amount_total / amount_subtotal`、Creem 按 `(amount_paid − tax_amount) / sub_total` 向下取整；沙箱/测试环境的支付（Waffo 沙箱、Creem 测试模式下单或测试事件，Stripe `livemode=false`、Waffo Pancake `test` webhook）照常到账，但快照写为 `paid_quota = 0`，结算侧 `min` 后不计入 R。
  管理员补单（`POST /api/user/topup/complete`）可带 `paid_money`：网关实际收款金额，币种与订单标价一致。服务端按 `实收 ÷ 标价` 下调快照（只降不升，同 webhook 折扣规则），标价为订单 `money`，Stripe 为 `amount × StripeUnitPrice`（`money` 是到账口径，不是收款额，实收填 Stripe 价格币种的 Checkout 合计）；Creem 按 webhook 规则填不含税实收；标价不为正时拒绝补单。实收金额写进补单的充值日志。Stripe、Creem、Waffo Pancake 的实收只有网关完成事件才知道，未填写时快照写 0，不计入 R；epay、Waffo 保留下单时快照。
  分组倍率造成的偏差由结算侧 `min` 封顶：epay、Waffo、Waffo Pancake 在倍率 > 1 时 `paid_quota` 超过到账额度；Stripe 的快照按购买数量计，倍率 < 1 时 `paid_quota` 超过到账额度。
- **管理员充值**：管理员在弹窗填写，约束 `0 < paid_quota ≤ value`，违反时按仓库约定返回参数错误（HTTP 200 + `success:false`），额度不变。

### 2.3 排除与失效

以下情况对应部分**不发钱，但水位照常推进**，之后恢复也不补发：

- 邀请人的 `users` 行不存在（被 `DELETE /api/user/:id` 物理删除，`model.HardDeleteUserById`）：整组按 `deleted` 作废；
- 邀请人被封禁（`status ≠ 1`）或软删除；
- 邀请人在排除名单（role=inviter）；受邀用户在排除名单（role=invitee，只作废该受邀用户的份额）；
- 开关关闭（`IkunRebateEnabled ≠ true`）：当天照常结算推水位，全部不发，`rebate_days.disabled = true`；
- 发放会使 `aff_quota` 或 `aff_history` 超过按列宽确定的上限（`aff_history` 只增不减，会先到顶）。上限取结算时反射到的两列类型中较窄的那个：`BIGINT` → 2^53−1（new-api 的 `common.MaxWalletQuota`，钱包页把 `aff_quota` 当 JS number 渲染）；32 位整数 → 2147483647；其它类型当天 `failed`。fork 的 `model/main.go` `ensureUserQuotaColumns` 在启动时要求 `users.quota/used_quota/aff_quota/aff_history` 都是 64 位，所以生产库是 `BIGINT`；只有设了 `SKIP_64BIT_QUOTA_SCHEMA_CHECK` 跑在旧库上，才会是 32 位。超限时：仅该邀请人按 `overflow` 作废，**不使当天失败**，否则 cron 会从第一次溢出起永久卡住。代价是这个状态不会自愈——同一邀请人之后每天都作废，而退出码仍是 0，只靠告警通道抓不到，因此必须额外打一条 WARN（见 §6），并让 `status` 子命令显示 overflow 次数。

允许自己邀请自己。受邀用户被改绑邀请人后，水位随受邀用户走，返利发给结算时的当前邀请人。

## 3. 数据表

所有新表只新增、不改动已有表。Go 负责的表由 GORM `AutoMigrate` 创建；Python 负责的表由 `rebate init` 用 SQLAlchemy `metadata.create_all` 创建。日期列统一用 `CHAR(10)` 存 `YYYY-MM-DD`（避免 DATE 在两种库的时区/驱动差异），金额与额度列用 `BIGINT`，时间戳用 `BIGINT` Unix 秒。

### 3.1 Go 负责（fork）

**`rebate_admin_recharges`**

| 列 | 类型 | 说明 |
|---|---|---|
| id | 主键 | GORM 生成 |
| user_id | int, index | 被加额度的用户 |
| quota | bigint | 实际加的额度 |
| paid_quota | bigint | 实付对应额度 |
| operator_id | int | 操作管理员 |
| created_at | bigint, index | Unix 秒 |

**`rebate_topup_paid`**

| 列 | 类型 | 说明 |
|---|---|---|
| id | 主键 | |
| trade_no | varchar(255), unique | 对应 `top_ups.trade_no` |
| user_id | int | |
| paid_quota | bigint | |
| created_at | bigint | |

**`rebate_exclusions`**

| 列 | 类型 | 说明 |
|---|---|---|
| id | 主键 | |
| user_id | int | |
| role | varchar(16) | `inviter` / `invitee` |
| note | varchar(255) | |
| created_at | bigint | |

唯一索引 `(user_id, role)`。

### 3.2 Python 负责

**`rebate_meta`**：`meta_key varchar(64) PK`, `meta_value varchar(255)`。`init` 写入 `activity_start`（`YYYY-MM-DD`）与 `timezone`（IANA 名），此后不可修改（`init` 重复执行不覆盖）。

**`rebate_days`**

| 列 | 说明 |
|---|---|
| day `CHAR(10)` PK | 结算日 |
| status | `running` / `done` / `failed` |
| rate `VARCHAR(32)` NULL | 当日比例快照（Decimal 字符串）；开关关闭时为 NULL |
| disabled | 开关关闭时为 true |
| inviter_count / invitee_count | 实际发放的邀请人数 / delta>0 的受邀用户数 |
| total_rebate `BIGINT` | 当日实发总额 |
| error `TEXT` | 失败原因 |
| started_at / finished_at | Unix 秒 |

**`rebate_watermarks`**：`invitee_id PK`, `cum_recharge`, `cum_consume`, `settled_base`（均 BIGINT），`last_day CHAR(10)`, `updated_at`。

**`rebate_credits`**：`(day, inviter_id)` 唯一；`invitee_count`, `delta_sum`, `rebate`（实发，作废为 0）, `forfeit_reason`（空/`banned`/`deleted`/`excluded`/`disabled`/`overflow`）, `log_request_id`, `created_at`。
这是防重复发放的最终保证：入账事务里先插这一行，唯一冲突即说明已结算过。

### 3.3 配置（`options` 表，已有键值表，只新增行）

| key | 值 | 写入方 |
|---|---|---|
| `IkunRebateEnabled` | `true`/`false` | 后台设置块（root 的 `PUT /api/option/`） |
| `IkunRebateRate` | 小数字符串，`0.` 后接 1–4 位数字且 `> 0`（界面为两位小数的百分比），如 `0.05`；保存时校验，`IkunRebateEnabled` 只接受 `true`/`false` | 同上 |

旧的 `IkunRebateConfig` 不再读取。

## 4. 结算流程（Python）

### 4.1 `settle` 选日

- 无参数（cron）：从"最后一个 `done` 日的次日"（没有则为 `activity_start`）到"配置时区的昨天"，逐日结算。
- `--from D1 --to D2`（手动）：只结算范围内非 `done` 的日期；若 `D1` 之前存在非 `done` 日期，拒绝执行并提示先补更早的日期。
- 任何日期 ≥ 今天或 < `activity_start` 都拒绝。
- 严格按日期升序；某日失败即停止，不处理其后的日期。`failed` 日在下次运行时自动重试（入账事务原子，重试安全）。

### 4.2 单日 D

窗口 `[D 00:00, D+1 00:00)`，按 `rebate_meta.timezone` 换算为 Unix 秒。

1. upsert `rebate_days[D]`：`status=running`，写入 `rate` 与 `disabled` 快照。
2. 聚合当日活动：按受邀用户求当日 `r`（§2.1 三个来源）与 `c`（type=2 的 `quota` 之和减 type=6 的 `quota` 之和，不含订阅计费的日志，见 §2.1.1），只保留 `inviter_id ≠ 0` 且 `r ≠ 0 或 c ≠ 0` 的受邀用户，按当前 `inviter_id` 分组。
3. 每个邀请人一个数据库事务：
   1. 先锁当日 `rebate_days` 行，在锁内再次确认 Redis 锁仍属于本进程；已为 `done` 时停止写入。再 `SELECT ... FOR UPDATE` 锁邀请人 `users` 行（SQLAlchemy `with_for_update()`，两种库都生成 `FOR UPDATE`）。日状态写入也遵守相同的锁内检查，防止旧进程暂停后恢复并覆盖接管者。
   2. 插入 `rebate_credits(D, I)`；唯一冲突 → 回滚并跳过该邀请人（已结算过）。
   3. 读取并锁定其名下受邀用户的 `rebate_watermarks` 行（不存在则视为全 0），**跳过 `last_day >= D` 的受邀用户**（见 §4.3），其余按 §2 更新 `cum_recharge`、`cum_consume`、`settled_base`、`last_day`。
   4. 判定作废（§2.3）；作废的份额不进入 `rebate`。
   5. `rebate > 0` 时：校验 `aff_quota + rebate ≤ 上限` **且** `aff_history + rebate ≤ 上限`（上限按列宽：`BIGINT` 为 2^53−1，32 位列为 2147483647，见 §2.3）。超限按 `overflow` 作废（§2.3），不写余额、不写日志、当日照常继续；否则 `UPDATE users SET aff_quota = aff_quota + rebate, aff_history = aff_history + rebate`，并插入一条日志（§5）。
   6. 回填 `rebate_credits` 的统计字段，提交。
4. 全部邀请人完成后，`rebate_days[D]` 置 `done` 并写汇总；仍持锁时遇到异常置 `failed` 并写 `error`。失锁进程只返回失败并停止，不再写日状态；未完成的 `running` 日由接管者或下次运行恢复。

返利只写 `aff_quota`/`aff_history`，不动 `users.quota`。`aff_quota` 不在 Redis 用户缓存里（缓存哈希只含 `quota` 等基础字段，且 `GetUserById` 直读数据库），**无需失效任何缓存**。用户提取到钱包余额走已有接口 `POST /api/user/aff_transfer`（`model.TransferAffQuotaToQuota`，最小 1 单位，`FOR UPDATE` 锁定用户行）。

钱包的额度读取、预扣、退款及差额结算同步读写数据库；Redis 的 `quota` 仅是可滞后的快照，不决定能否消费。钱包余额不再随批量统计延迟落库，避免提现/充值提交与缓存重建交错时重复授信。原有信任额度免预扣、实际用量超过预估后的差额扣款规则不变。

崩溃在任意点：已提交的邀请人有 `rebate_credits` 行，重跑跳过；未提交的整体回滚。无需 pending 状态或入账标记核对。

### 4.3 水位与跳过的一致性

同一邀请人事务内同时写 `rebate_credits` 与其受邀用户的 `rebate_watermarks`，因此"跳过已结算邀请人"与"水位已推进"总是一致。

但"同一受邀用户同一天只属于一个分组"**只在单次运行内成立**。改绑场景：D 日结算到邀请人 A 已提交、后面某邀请人失败；此时把受邀用户 u 从 A 改绑到 C；重跑 D 日时 u 落在 C 组，`rebate_credits(D, C)` 不存在，u 的 `cum_recharge`/`cum_consume` 会再加一遍 D 日的 r 和 c，C 被多付、u 的水位永久虚高。因此受邀用户必须按 `rebate_watermarks.last_day >= D` 跳过——既保证重跑不重复累计，也让水位成为"该受邀用户已结算到哪一天"的唯一事实来源。

### 4.4 快照语义（按什么时刻的口径算）

- **开关与比例**：按执行结算那一刻的配置判定，不按 D 日当天。开关关闭期间 cron 仍逐日写出 `disabled` 日并推进水位（因此不会事后补发）；补结历史日期时用的是补结时刻的开关与比例，不按充值或消费发生时的历史比例回溯。
- **部分入账后的配置变更**：重跑继续按本次执行配置处理未完成的邀请人，已提交入账不重算。若配置与上次不同，在 `rebate_days.error` 保留 `config_changed` 审计，记录前后比例/开关及变更前已提交的邀请人，以便还原混合比例日；不能只用最终 `rebate_days.rate` 重算整日。
- **开关关闭时 `rate` 允许缺失**：此时不校验、按 0 发放，`rebate_days.rate` 写 NULL；只有开关打开时才要求比例合法（§6）。
- **金额展示单位**：日志金额按执行结算那一刻的展示设置格式化，补结旧日期也用当前设置。
- **支付单价**：在线充值的实付额度在下单时刻固化进 `rebate_topup_paid`，之后改价不影响历史订单；若某笔订单没有该行（部署前下单、部署后支付完成），结算时以到账额度为上界，不会多算。

## 5. 入账日志

插入 `logs`：`user_id=I`、`username`=邀请人用户名、`type=3`、`quota=rebate`、`created_at=now`、`request_id=rebate-YYYYMMDD-{I}`（仅用于追溯）、`content` 如下，其余列取表默认值。

文案按邀请人 `users.setting` JSON 的 `language`。写入该字段的接口不做校验（`controller/user.go:825-839` 直接存字符串），且历史上存过 `zh-CN` 一类旧值，所以**必须复刻前端 `normalizeInterfaceLanguage`（`web/src/i18n/languages.ts`）而不是逐字匹配**：`trim` → `_`→`-` → 转小写；`zh-TW`/`zh-HK`/`zh-MO`/`zhTW` → `zhTW`；`zh-CN`/`zh-Hans`/`zhCN` → `zhCN`；其余不在 `zhCN`/`en`/`fr`/`ru`/`ja`/`vi`/`zhTW` 内的（含缺失）→ `en`：

| language | 文案 |
|---|---|
| `zhCN` | `返利结算（MM-DD）：共 {x} 个受邀用户消费，返利 {amount}` |
| `zhTW` | `返利結算（MM-DD）：共 {x} 位受邀用戶消費，返利 {amount}` |
| `en` | `Rebate settlement (MM-DD): {x} invitees consumed, rebate {amount}` |
| `fr` | `Règlement des parrainages (MM-DD) : {x} filleuls ont consommé, remise {amount}` |
| `ru` | `Расчёт вознаграждений (MM-DD): потребили {x} приглашённых, вознаграждение {amount}` |
| `ja` | `紹介報酬の精算（MM-DD）：招待ユーザー {x} 名が消費、報酬 {amount}` |
| `vi` | `Quyết toán hoa hồng (MM-DD): {x} người được mời đã tiêu dùng, hoa hồng {amount}` |

`x` = 该邀请人当日 delta>0 且未被作废的受邀用户数。`amount` 按结算时刻的钱包展示设置格式化，与前端 `web/src/lib/format` 的 `formatQuota` 结果一致（实现时逐项对照其源码）：

实现方式：**复刻** `formatQuota`（`web/src/lib/format.ts:73`，参数 `digitsLarge: 2, digitsSmall: 4, abbreviate: true`）→ `formatQuotaWithCurrency`（`web/src/lib/currency.ts:516`）→ `formatCurrencyFromUSD` → `formatCurrencyValue`，不要按"两位小数"简化。

**基准不是浏览器，而是 Node 生成的夹具。** 前端 `DEFAULT_FORMAT_OPTIONS.locale` 是 `undefined`（`web/src/lib/currency.ts:140`），`Intl.NumberFormat(undefined)` 跟随访问者的浏览器语言，同一个金额在不同浏览器会渲染成 `$1,234.56` / `1 234,56 $` / `1.234,56 $`；连字节都不稳定——`fr-FR` 的千分位在 CLDR 34 从 U+00A0 换成了 U+202F，新旧浏览器输出不同。所以：

- 日志一律按**邀请人语言**取一个固定 locale（与 §5 的文案语言一致）；
- 目标是"与夹具一致"，不是"与某个用户的浏览器一致"。夹具由 Node 跑 `Intl` 生成（本次生成环境：Node v24.15.0 / ICU 78.2 / CLDR 48.0），随代码入库并作为测试期望值；locale 数据升级导致夹具变化时，重跑生成脚本并一并更新测试。

locale 映射：

常量取自数据库（不能写死，`QuotaPerUnit` 与汇率都可被管理后台覆盖）：

| 值 | 来源 | 缺省 |
|---|---|---|
| locale | 由 §5 归一化后的语言映射：`zhCN`→`zh-CN`、`zhTW`→`zh-TW`、`en`→`en-US`、`fr`→`fr-FR`、`ru`→`ru-RU`、`ja`→`ja-JP`、`vi`→`vi-VN` | 同左 |
| 分组符 / 小数符 / 货币符号 | **由 Node 生成并入库的夹具**（见下表） | 夹具 |
| `quotaPerUnit` | options `QuotaPerUnit`（`model/option.go:596`） | 500000 |
| `displayType` | options `general_setting.quota_display_type` | 见下方回退 |
| `usdExchangeRate` | options `USDExchangeRate`（`model/option.go:439`） | 7.3 |
| `customSymbol` | options `general_setting.custom_currency_symbol` | `¤` |
| `customRate` | options `general_setting.custom_currency_exchange_rate` | 1.0 |

`displayType` 的回退链（直读表时这些行可能缺失或曾是内存态）：`general_setting.quota_display_type`（合法值 `USD`/`CNY`/`TOKENS`/`CUSTOM`）→ 旧开关 `DisplayInCurrencyEnabled`（`true`→`USD`，`false`→`TOKENS`；该开关只在内存生效，`model/option.go:345-353` 不写库）→ `USD`。

**Python 侧没有 `Intl`，所以符号与分组符必须来自夹具，不能指望 Python 库自己推断。**（`babel` 只有标准符号、没有 `narrowSymbol`：`fr-FR` 会给 `$US`，而前端实际输出 `$`。）夹具内容（Node 生成，随代码入库）：

| locale | USD | CNY | 分组符 | 小数符 |
|---|---|---|---|---|
| `zh-CN` / `zh-TW` / `en-US` | 前缀 `$` | 前缀 `¥` U+00A5 | `,` U+002C | `.` U+002E |
| `ja-JP` | 前缀 `$` | 前缀 `￥` U+FFE5（全角） | `,` U+002C | `.` U+002E |
| `fr-FR` | 后缀 ` $` | 后缀 ` ¥` | U+202F 窄不换行空格 | `,` |
| `ru-RU` | 后缀 ` $` | 后缀 ` ¥` | U+00A0 不换行空格 | `,` |
| `vi-VN` | 后缀 ` $` | 后缀 ` ¥` | `.` U+002E | `,` U+002C |

`fr-FR`/`ru-RU`/`vi-VN` 的符号是**后缀**（`1 234,56 $`）而不是前缀。逗号、句点这类 ASCII 字符也照夹具的码位抄，别按"看起来一样"处理。

算法（`usd` 为 double）：

```
usd = rebate / quotaPerUnit
USD:    currency(usd, digits=2/4)
CNY:    currency(usd * usdExchangeRate, digits=2/4)
CUSTOM: f"{customSymbol} " + decimal(usd * customRate)      # 符号不受 locale 影响，数字部分受
TOKENS: shorten((rebate / quotaPerUnit) * quotaPerUnit)     # 必须做这一次浮点往返

currency(value, digits):   # Intl.NumberFormat, currencyDisplay=narrowSymbol, locale 见上
  digits = |value| >= 1 ? 2 : 4
  value = adjustForMinimum(value, digits, minimumNonZero=0)  # 0<|v|<10^-digits 时钳到 ±10^-digits
  number = 按 locale 分组 + 最多 digits 位小数 + 去尾零
```

**TOKENS 的浮点往返不能省。** 前端格式化的不是 `rebate` 本身，而是 `amountUSD * quotaPerUnit`（`web/src/lib/currency.ts:417-431` 的 tokens 分支），对整数 `rebate` 来说多了一次 `/qpu` 再 `*qpu` 的往返。在 1..3,000,000 内逐一比对，省掉往返有 **468 个值与前端不同**：

| rebate | 前端实际 | 省掉往返 |
|---|---|---|
| 8150 | `8.1k` | `8.2k` |
| 16150 | `16.2k` | `16.1k` |
| 32050 | `32.1k` | `32k` |

原因是 `8150/500000×500000` 在 double 里是 `8149.999999999999`。§5 的样例表六个值不受影响。

`shorten(v)` = `formatNumberWithSuffix(v, digitsLarge=0, digitsSmall=4, abbreviate=True)`：

- `|v| >= 1000`：`(v/1000).toFixed(1)` 去尾零 + `k`（**只有 k 一级，没有 M/B**）；
- 否则：**`digitsLarge = 0`**（`formatCurrencyFromUSD` 的 tokens 分支传的是 `0` 与 `digitsSmall`，不是 `formatQuota` 的 2/4），即 `|v| >= 1` 用 `toFixed(0)`、`|v| < 1` 用 `toFixed(4)`，再去尾零。

CUSTOM 的数字部分同样按 locale 分组和取小数符（`${meta.symbol} ${decimal}` 里的 `decimal` 由 `Intl.NumberFormat(locale)` 产出），`fr-FR` 下形如 `¤ 1 234,56`。

**两条分支的舍入规则不同，不能混用**：

- USD/CNY/CUSTOM 走 `Intl.NumberFormat`，它对 double 的最短十进制表示做 halfExpand：`1.005 → 1.01`。Python 复刻方式：`Decimal(repr(x)).quantize(Decimal('0.01'), ROUND_HALF_UP)`。
- TOKENS 走 `formatNumberWithSuffix`，用的是 `Number.prototype.toFixed`，按精确二进制值处理：`(1.005).toFixed(2) → "1.00"`。Python 复刻方式：`Decimal(x)`（精确二进制展开）再按 half-up（平局取较大值）。

已验证的样例（`QuotaPerUnit=500000`、`USDExchangeRate=7.3`，`en-US`）：

| rebate | USD | CNY | TOKENS |
|---|---|---|---|
| 1 | `$0.0001` | `¥0.0001` | `1` |
| 12500 | `$0.025` | `¥0.1825` | `12.5k` |
| 250000 | `$0.5` | `¥3.65` | `250k` |
| 500000 | `$1` | `¥7.3` | `500k` |
| 5368709 | `$10.74` | `¥78.38` | `5368.7k` |
| 500000000 | `$1,000` | `¥7,300` | `500000k` |

两个容易写错的地方：

- TOKENS 只有 `k` **一级，没有 M/B**：5,000,000 会渲染成 `5000k`（实测确认），不要自作主张加 M 档。且 `k` 分支也用 `toFixed(1)` 再去尾零。
- 货币符号随 locale 变化：`ja-JP` 下 CNY 的 narrowSymbol 是 `￥`（全角），`en-US` 下是 `¥`。钉死 locale 后符号随之确定，实现时按 locale 直接取 `Intl` 的输出，不要自己拼符号表。

CUSTOM 分支例外：它是自己拼字符串（`${meta.symbol} ${decimal}`），符号和数字之间有一个空格，符号始终是 `custom_currency_symbol` 原值，不受 locale 影响。

### 5.1 日志内容中的金额

`amount` 用 §5 格式化结果。中/日/俄/越 的"共 x 个"和"返利"之间按上表文案；`x` 为该邀请人当日 delta>0 且未被作废的受邀用户数。

## 6. 前置检查与故障处理

`settle` 开始时依次检查，任一不通过即退出码非 0、不写任何数据：

1. 获取 Redis 锁 `rebate:settle:lock`（`SET NX PX` + 随机 token，后台续期；释放用 token 比对的 Lua 脚本）。拿不到 → 已有结算在跑。
2. 环境变量 `REBATE_LOGS_IN_MAIN_DB=true`（运维声明 new-api 未配置 `LOG_SQL_DSN`；否则消费日志不在主库，会把消费当 0 永久少发）。
3. `options.LogConsumeEnabled` 视为开启：这一行不存在，或值**恰好**是 `"true"`（与 Go 的 `== "true"` 一致；`false`、`False`、空串等任何其它值都按关闭处理并拒绝）。
4. `rebate_meta` 存在且 `activity_start`/`timezone` 合法。
5. **开关打开时** `IkunRebateRate` 必须合法（`0 < rate < 1`；Go 侧保存时只接受 `0.` 后 1–4 位数字的写法）；开关关闭时不校验，按 0 发放并在 `rebate_days` 标 `disabled`（否则首次部署时 enabled=false 且未填比例会让每天都拒绝，之后一旦填上比例 + 打开开关，cron 会从 `activity_start` 一路补结并全部发钱）。

不需要、也无法判断"开关曾经打开过"：`options` 只存当前值，没有变更历史。因此口径统一为 §4.4 的"执行时刻配置"——当前打开则比例必须合法，当前关闭则按 0 推水位且不补发。

运行中：

- 当日存在未知渠道的成功充值订单、邀请人事务数据库错误 → 当日 `failed`，停止。（余额溢出**不在此列**：按 `overflow` 作废该邀请人、当日照常 `done`，见 §2.3。）
- 锁续期失败 → 当前执行返回失败（原因写明锁丢失）并退出，不再写任何日状态或开启新的邀请人事务。数据库可保留 `running`，也可已被接管者推进；下次运行将未完成日期按幂等规则恢复。已在日行锁内确认持锁的在途事务可以完成，接管者需等待该事务释放数据库锁。
- 不写任何缓存失效动作：`aff_quota`/`aff_history` 不进 Redis 用户缓存，`GetUserById` 直读数据库，因此没有可失败的缓存步骤。

告警：暂以退出码 + 容器日志为准。由于 `overflow` 作废不改变退出码，这类永久性异常只能靠日志发现：

- 任一日出现 `overflow` 作废时，立即打 WARN，格式 `overflow: inviter=123 aff_quota=... aff_history=... rebate=... limit=... day=...`，并把该日 `rebate_days.error` 写为 `overflow: inviter=123,456`（当日状态仍是 `done`）。
- `status` 子命令在最近 N 天里统计并显示 `overflow` 次数与涉及的邀请人 ID，便于人工发现"某个邀请人的 `aff_history` 已经到顶"。
- 当日有受邀用户的订阅计费日志被排除时（§2.1.1），打 WARN `subscription: N 条订阅计费的消费/退款日志未计入净消费 day=...`，不改变状态与退出码。

## 7. Python 项目

新仓库（位置在实现计划中确定），`uv` 管理，依赖：SQLAlchemy 2（Core，不用 ORM）、pymysql、psycopg、redis。

```
rebate/
  __main__.py     # CLI：init / settle / status
  config.py       # 环境变量、options、rebate_meta 读取与校验
  db.py           # 引擎、表定义（Python 负责的 4 张 + 读取用的已有表/Go 表）
  sources.py      # 单日 r / c 聚合（三类充值 + 消费/退款）
  engine.py       # 纯函数：水位推进、返利计算、作废判定
  settle.py       # 选日、单日流程、邀请人事务
  display.py      # 金额格式化 + 7 种语言文案
  lock.py         # Redis 结算锁
```

命令：

- `init --activity-start YYYY-MM-DD --timezone Asia/Shanghai`：建表并写 meta；已存在的 meta 不覆盖。
- `settle [--from D1 --to D2] [--dry-run]`：`--dry-run` 只读：打印各邀请人计划金额与作废原因，不拿锁、不写库。
- `status [--days 14]`：最近各日状态、总额、失败原因，以及各作废原因（`banned`/`deleted`/`excluded`/`disabled`/`overflow`）的次数和涉及的邀请人 ID。

环境变量：`REBATE_DB_DSN`（`mysql+pymysql://...` 或 `postgresql+psycopg://...`）、`REBATE_REDIS_URL`、`REBATE_LOGS_IN_MAIN_DB`、`TZ`。Redis 只用于结算锁（new-api 自身用单节点 `redis.Client`，不依赖集群特性，脚本同样只用单 key 的 `SET NX`/`EVAL`）。

账单口径：`rebate_credits.rebate` 记录实际发放的额度，与 `aff_quota` 的增量一致；`transfer` 之后额度进入钱包余额是用户行为，不再与本项目相关。

数据库账号权限：读 `users`/`top_ups`/`redemptions`/`logs`/`options` 与 Go 的 3 张返利表；`UPDATE users(aff_quota, aff_history)`；`INSERT logs`；Python 4 张表全权限；`init` 时需要建表权限。不需要 `users.quota` 的写权限。读 `redemptions` 时必须绕过 `deleted_at` 过滤（见 §2.1）。

### 7.1 部署

镜像基于 `python:3.12-slim`，uv 安装依赖，supercronic 执行 crontab `5 0 * * * python -m rebate settle`。容器 `TZ` 决定 00:05 属于哪个时区，结算窗口只取决于 `rebate_meta.timezone`。手动补结：`docker compose run --rm rebate python -m rebate settle --from ... --to ...`。

## 8. fork 改动

原则：返利专用代码放新文件；上游文件只做挂载级改动，以及修正会影响返利口径（R、C）的支付与计费缺陷。改动范围以下表为准：代码中只有部分位置带 `fork (ikun)` 注释，合并上游时须按本表逐个文件核对，不能只搜索标记。

### 8.1 后端

| 文件 | 类型 | 内容 |
|---|---|---|
| `model/rebate.go` | 新 | 3 个 GORM 模型；`ParseIkunRebateRate` 与选项校验（§3.3）；`paidQuotaFromCharge`；`TopUp.AfterCreate` 快照钩子（§2.2，与订单插入同一事务，钩子返回错误即下单失败；Waffo 沙箱、Creem 测试模式写 0）；`GatewayCharge` 与 `applyGatewayCharge`（按网关报告的折扣或测试标记只下调快照） |
| `model/aff_code.go` | 新 | `retired_aff_codes`：硬删除用户的邀请码入表，`NewAffCode` 永不重发；`EnsureAffCode` 只写 `aff_code` 一列 |
| `model/main.go` | 上游 | `migrateDB` 注册上面的 4 个模型（含 `RetiredAffCode`） |
| `model/user_quota_adjustment.go` | 上游 | `AdjustUserQuota(..., recharge *UserQuotaRecharge)`：`add` 且带 `recharge` 时，同一事务内加额度并插入 `rebate_admin_recharges`（`0 < paid ≤ value`） |
| `model/user.go` | 上游 | 所有注册路径（含 OAuth、微信）把 `inviter_id` 写进新用户行；邀请奖励只发给启用状态的邀请人，入账成功才记日志；`TransferAffQuotaToQuota` 要求划转额为正、受钱包上限约束、只更新 `quota`/`aff_quota` 两列，提交后刷新钱包缓存并写系统日志；`GetUserQuota`/`IncreaseUserQuota`/`DecreaseUserQuota` 同步读写数据库（040f8d4a4），`DecreaseUserQuota` 未命中行时报错；`IsOidcIdAlreadyTaken` 含软删用户；`HardDelete` 先回收邀请码 |
| `model/option.go` | 上游 | `IkunRebateEnabled`/`IkunRebateRate` 写入前校验；`UpdateOption` 不再吞掉写库错误 |
| `model/topup.go` | 上游 | `RechargeEpay` 在锁内比对通知金额，低于订单金额不到账（`ErrPaymentAmountTooLow`）；Stripe、Creem、Waffo Pancake 回调传入 `GatewayCharge`；`ManualCompleteTopUp` 的到账与回调一致，并按 `paid_money` 下调快照（§2.2） |
| `model/subscription.go` | 上游 | 订阅订单写 `top_ups` 时，不覆盖已有的钱包充值行 |
| `model/quota_reserve.go`、`model/user_cache.go`（及 `model/user.go`、`model/checkin.go`、`model/subscription.go`、`model/user_quota_adjustment.go` 的相应部分） | 上游 | 钱包余额以数据库为准（040f8d4a4）：预扣在数据库内原子校验并提交，Redis 与批量更新不能授权扣费；缓存只是快照，钱包变动后刷新，资料更新不覆盖它 |
| `model/checkin.go` | 上游 | 签到奖励为 0 时跳过，入账受钱包上限约束 |
| `controller/user.go`、`controller/user_quota.go` | 上游 | `ManageRequest` 加 `Recharge`、`PaidQuota`（缺省等于 `Value`），审计 params 带 `recharge`、`paid_quota`；划转接口只回固定文案；`GetAffCode` 改调 `EnsureAffCode` |
| `controller/wechat.go` | 上游 | 微信登录读取 `aff`，新用户绑定邀请人 |
| `controller/rebate_admin.go` | 新 | `GET/POST/DELETE /api/rebate/exclusions`，RootAuth；用户不存在、重复、未知 id 返回对应文案 |
| `controller/audit.go` | 上游 | 排除名单的审计模板 `rebate.exclusion_add` / `rebate.exclusion_delete` |
| `controller/misc.go` | 上游 | `/api/status` 返回 `topup_link`（钱包与侧栏的充值入口按配置读取） |
| `router/api-router.go`、`middleware/access_token_routes.go` | 上游 | 挂排除名单路由；个人访问令牌按 `option:read`/`option:write` 授权 |
| `controller/rebate_info.go` | fork 已有，重写 | 读 `IkunRebateEnabled`/`IkunRebateRate`/`LogConsumeEnabled`（直读 DB + 60 秒缓存）与 `rebate_meta`。`rebate_meta` 表不存在或活动窗口非法 → `{"enabled":false}`；否则返回 `activity_start`/`timezone`，`enabled` = 比例合法且开关为 `"true"` 且 `LogConsumeEnabled` 缺省或为 `"true"`，仅在 `enabled` 时返回 `rate` |
| `controller/topup.go` | 上游 | `RequestEpay` 下单时把发给网关的舍入后金额写入 `Money`（实付快照由它换算）；易支付回调把通知金额传给 `RechargeEpay`；补单接口加 `paid_money`（≥ 0） |
| `controller/topup_waffo.go` | 上游 | `RequestWaffoPay` 按币种舍入后的金额同时作为 `OrderAmount` 发给网关并写入 `Money`，舍入为 0 的订单不创建 |
| `controller/topup_stripe.go`、`controller/subscription_payment_stripe.go` | 上游 | Checkout Session 写 `metadata.new_api_trade_no`；完成、过期、异步失败事件只接受为该订单创建的会话；按 `amount_subtotal`/`amount_total`/`livemode` 构造 `GatewayCharge` |
| `controller/topup_creem.go`、`controller/topup_waffo_pancake.go` | 上游 | 按网关报告的折扣与测试标记构造 `GatewayCharge`；Waffo Pancake 下单时把舍入后的金额写入 `Money` |
| `controller/channel-test.go` | 上游 | 渠道测试日志 `quota` 记 0（不扣任何人），估算费用放 `admin_info.channel_test_quota` |
| `service/billing.go`、`service/billing_session.go`、`service/funding_source.go`、`service/text_quota.go`、`service/quota.go`、`service/log_info_generate.go`、`main.go` | 上游 | 消费日志 `quota` 只记实际提交的扣费（结算失败时记预扣额，差额放 `admin_info.unsettled_quota`）；图片追加预扣后，失败退款累计退还全部预扣额（`WalletFunding.PreConsume` 用 `+=`，不只退最后一次）；已退款的会话不再结算；关闭时等待未完成的退款；实时与音频按次计价模型带上 `ModelPrice`；实时会话按轮扣费与最终结算合计只扣一次，订阅会话不查钱包余额；分层表达式计价的实时会话每轮扣“会话累计用量的表达式费用 − 已扣额”，并照常检查余额；音频、图片与缓存 token 进入 `ai`/`ao`/`img`/`cr`，表达式引用 `cr` 时缓存音频不再重复计入 `ai` |
| `relay/channel/openai/relay_realtime.go`、`relay/common/relay_info.go` | 上游 | 实时会话：最终结算前关闭上游并等待两个读协程退出（超时后仍由结算锁等待在途的按轮扣费完成），结算后读协程不再扣费也不再转发；按轮扣费时传入含本轮的会话累计用量；累计时保留图片与分模态缓存明细；`response.done` 缺 `total_tokens` 时按输入加输出计；按轮已扣额记在 `WssChargedQuota`，结算失败的差额记在 `UnsettledQuota` |
| `common/log_text.zh-CN.json` | 上游 | 新增服务端日志行的中文 |

`AfterCreate` 注意：钩子在 `model` 包，读取 `setting`/`operation_setting` 的单价（`model` 已依赖这些包，无循环）。只对 5 个已知 `PaymentProvider` 写行。

### 8.2 前端

| 文件 | 类型 | 内容 |
|---|---|---|
| `web/src/features/users/components/user-quota-dialog.tsx`、`web/src/features/users/lib/rebate-recharge.ts` | 上游 / 新 | `add` 模式下"充值"勾选框；勾选后显示"实付金额"输入（单位同额度输入，默认跟随额度，可改小），提交 `recharge`、`paid_quota`；提交中不重复发送 |
| `web/src/features/users/types.ts` | 上游 | 额度调整请求类型加 `recharge`、`paid_quota` 两个可选字段 |
| `web/src/features/system-settings/general/rebate-settings-section.tsx` | 新 | 开关、百分比（界面百分比，存小数）、排除名单（增删，分邀请人/受邀用户，带备注；加载失败显示错误与重试）、只读显示活动起点与时区 |
| `web/src/features/system-settings/billing/section-registry.tsx`、`web/src/features/system-settings/billing/index.tsx`、`web/src/features/system-settings/types.ts` | 上游 | 注册上面的设置块；`IkunRebateEnabled`/`IkunRebateRate` 的类型与默认值 |
| `web/src/features/system-settings/hooks/use-update-option.ts` | 上游 | 保存 `TopUpLink` 后刷新状态缓存 |
| `web/src/features/usage-logs/lib/format.ts` | 上游 | 排除名单审计动作的显示文案 |
| `web/src/features/auth/types.ts`、`web/src/stores/system-config-store.ts`、`web/src/lib/status-query.ts`、`web/src/hooks/use-sidebar-data.ts`、`web/src/features/shop/index.tsx`、`web/src/features/wallet/index.tsx`、`web/src/features/wallet/components/recharge-form-card.tsx` | 上游 | 充值入口与兑换码链接按 `topup_link` 动态读取 |
| `web/src/features/wallet/components/affiliate-rewards-card.tsx` | fork 已有 | 适配新 `rebate_info` 结构 |
| `web/src/features/wallet/components/dialogs/billing-history-dialog.tsx`、`web/src/features/wallet/hooks/use-billing-history.ts`、`web/src/features/wallet/types.ts` | 上游 | 补单确认框加"实收金额"（网关收款币种，不做汇率换算），提交 `paid_money` |
| `web/src/features/usage-logs/components/dialogs/details-dialog.tsx`、`web/src/features/usage-logs/types.ts` | 上游 | 管理员可见：结算未提交的金额（未扣或未退）、渠道测试的估算费用 |
| `web/src/features/auth/api.ts` | 上游 | 微信登录带上 `aff` |
| `web/src/lib/format.ts` | 上游 | 新增 `parseDecimalInput`：解析补单实收与管理员实付输入（全角数字与句号按 ASCII 读取，逗号后至多两位数字视为小数点，`1,000` 类写法拒绝），上面两个弹窗依赖它 |
| `web/src/i18n/locales/*.json`（7 个） | 上游 | 新增文案 |

## 9. 测试

**Python（pytest）**：同一套用例分别跑 MySQL 与 PostgreSQL（docker compose 起库），覆盖：

- 水位：退款使 C 回落再回升不重复返利；R 封顶；多日累计。
- 实付：`paid_quota` 小于到账额度时按实付；无 `rebate_topup_paid` 行时回落到账额度；分组倍率 >1 时封顶到账额度。
- 选日：cron 自动补齐漏跑日；手动范围跳过 `done`；前面有缺口时拒绝；拒绝今天/未来/早于起点。
- 崩溃恢复：在第 k 个邀请人事务后中断，重跑不重复发放、水位一致。
- 作废：封禁、软删、两类排除、开关关闭——不发钱但水位推进，恢复后不补发。
- 前置检查：未声明 `REBATE_LOGS_IN_MAIN_DB`、`LogConsumeEnabled=false`、比例非法、未知渠道订单。
- 日志：4 种展示单位的金额格式；7 种语言的文案与 `language` 兜底；`quota`/`type`/`request_id`。
- 入账目标：返利只进 `aff_quota`/`aff_history`，`users.quota` 不变；两列任一越界时按 `overflow` 作废该邀请人，当日仍置 `done`，且后续日期照常结算（不卡死 cron）。上限按列宽：测试库 `users` 额度列与生产一致为 `BIGINT`，在 2^53−1 边界上断言；另把两列改成 32 位再断言 2147483647。
- 订阅计费：带 `"billing_source":"subscription"` 的消费与退款日志都不计入 C，普通日志与 `other` 为 NULL/空串的照常计入，转义后的同样文字与 `billingXsource` 不被误判；当日排除条数打 WARN。
- 改绑重跑：D 日部分提交后把受邀用户改绑，重跑 D 日不重复累计、不多付（`last_day >= D` 跳过）。
- 开关关闭 + 比例缺失：每天正常写出 `disabled` 日并推水位，之后打开开关不补发。
- 兑换码软删除：`status=3` 且 `deleted_at` 非空的行仍计入 R。
- 语言：`zh-CN`/`zh-Hans`/`zh-TW` 旧值分别归到 `zhCN`/`zhCN`/`zhTW`；未知值回落 `en`。
- 金额格式：按 §5 的样例表与 Node 夹具逐条断言（含 <1 的小额、≥1000 的 k 缩写、`5368709 → $10.74` 这类需要正确舍入的值），覆盖 7 个 locale 的符号位置（`fr-FR`/`ru-RU`/`vi-VN` 为后缀）与分组符码位（U+202F / U+00A0）、`ja-JP` 下 CNY 的全角 `￥`，以及 CUSTOM 数字部分的 locale 分组。
- TOKENS 浮点往返：对 1..3,000,000 全量重算并与夹具比对（至少覆盖 §5 表中 `8150`/`16150`/`32050` 三个反例），确保没有省掉 `/qpu × qpu` 的往返。
- 舍入分歧：两条路径各有断言。TOKENS 走 `toFixed`、按精确二进制值舍入：`rebate=1150` → `1.15` 的 double 略小于 1.15，`(1.15).toFixed(1)` 得 `"1.1"`，渲染为 `1.1k`（按十进制 half-up 会错成 `1.2k`）。USD 走 `Intl.NumberFormat`、按最短十进制表示舍入：`rebate=502500`（QuotaPerUnit 500000）→ `1.005` → `$1.01`（按精确二进制值会错成 `$1.00`）。
- 溢出静默性：连续多日结算中某邀请人持续 `overflow` 时，当日仍 `done`、退出码为 0，但 WARN 与 `status` 计数都能反映。

**Go（testify require/assert）**：

- `rebate_info` 返回契约（开启/关闭/非法值/表不存在）。
- `TopUp.AfterCreate` 各渠道 `paid_quota` 换算、单价为 0 不写行。
- `add_quota`：`recharge` 时 `paid_quota` 越界返回错误且额度不变；成功时额度与充值记录同时存在；插入充值记录失败时额度回滚。
- 补单：各渠道到账与回调一致；`paid_money` 按 `实收 ÷ 标价` 下调快照，标价不为正时拒绝且订单保持待支付；实收写入充值日志。
- 网关：Stripe、Creem、Waffo Pancake 的折扣与测试标记只下调快照；Stripe 只接受带本单 metadata 的会话；易支付通知金额低于订单金额不到账。
- 计费：文本、音频、实时的消费日志都只记实际提交的扣费；实时会话合计只扣一次（倍率计价、按次计价、分层表达式含音频、图片、缓存音频与跨轮长上下文、订阅、令牌扣减失败）；分层表达式会话余额不足时按轮扣费被拒；按轮扣费失败的轮次只计一次，缺 `total_tokens`、不带用量、未收到 `response.done` 的轮次照常计，带用量的轮次不再叠加本地估算；用户挂断时关闭上游；按次计价的音频按价格计费。
- 邀请：所有注册路径写入邀请人；停用的邀请人不发奖励；硬删除用户的邀请码不再发放。

## 10. 已定取舍

- 返利基数 `min(R, C)` 高水位；按天、按顺序结算；不支持对已结算日重算或追回。
- 比例修改从下一次结算起生效；补结历史日期用执行时的比例，不保留比例历史。
- 开关关闭与排除名单同语义：期间不发、之后不补。
- 兑换码按面额全额算充值（无实付信息）。
- 管理员"减少"与"覆盖"模式不涉及充值。
- 日志必须在主库（`LOG_SQL_DSN` 未配置），由运维显式声明。
- 受邀用户改绑后返利归当前邀请人。
- 返利进 `aff_quota`（推广奖励余额），不进钱包余额；提取由用户自行执行。
- `aff_history`（Total Earned）与 `aff_quota` 同步增加；封禁、软删、排除名单、开关关闭时两者都不增加。
