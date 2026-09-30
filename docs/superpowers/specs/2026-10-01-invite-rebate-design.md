# 邀请返利（重写版）设计

> 日期：2026-10-01
> 取代：`/Users/hzy/PycharmProjects/ikun_rebate`（v3.5，从未上线、未发过钱，整体废弃）
> 范围：一个独立 Python 项目（结算）+ fork 内少量改动（充值口径采集、后台配置、钱包展示）

## 1. 目标与约束

受邀用户用**真实充值**得到的额度被消费后，按比例把返利加到邀请人余额，并给邀请人写一条管理类型日志。

用户给定的约束：

1. 管理员可修改返利百分比（后台页面）。
2. 在线充值、兑换码充值算充值；管理员加额度时勾选"充值"才算，并可填实付金额（默认等于加的额度），返利按实付算。在线充值同样按实付算（含档位折扣、分组倍率）。
3. 每天 00:05 结算前一天。
4. 结算是独立 Python 项目，不在 new-api 进程内。
5. 结算基数是受邀用户实际消费；漏结或异常时可手动按日期范围补结。
6. 只能新增表，不能改已有表结构；MySQL 与 PostgreSQL 都要支持。不使用 SQLite。
7. 合理利用 Redis。
8. 入账后给邀请人写管理日志："返利结算：共 x 个受邀用户消费，返利 xx"，金额单位与钱包余额展示一致。

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
| 在线充值 | `top_ups` 中 `status='success'`，按 `complete_time` 落日 | `min(到账额度, paid_quota)`；`rebate_topup_paid` 无该 `trade_no` 时取到账额度 |
| 兑换码 | `redemptions` 中 `status=3`，按 `redeemed_time` 落日，归属 `used_user_id` | `quota` 全额 |
| 管理员充值 | `rebate_admin_recharges`，按 `created_at` 落日 | `paid_quota` |

在线充值到账额度（与 `model/topup.go` 入账一致；渠道按 `payment_provider` 判定，为空的旧行按 `payment_method` 推断：`stripe`/`creem`/`waffo`/`waffo_pancake` 同名，其余视为 `epay`）：

| 渠道 | 到账额度 |
|---|---|
| epay / waffo / waffo_pancake | `amount × QuotaPerUnit` |
| stripe | `money × QuotaPerUnit`（Decimal，半远离零取整） |
| creem | `amount`（本身已是 quota） |

`payment_provider='balance'` 或其他未知渠道的成功订单：当天结算失败（见 §6），不静默跳过。

### 2.2 实付 `paid_quota`

- **在线充值**：fork 在 `TopUp` 的 GORM `AfterCreate` 钩子里，于下单时刻按当时单价换算：

  | 渠道 | `paid_quota` |
  |---|---|
  | epay | `Money ÷ operation_setting.Price × QuotaPerUnit` |
  | waffo | `Money ÷ setting.WaffoUnitPrice × QuotaPerUnit` |
  | waffo_pancake | `Money ÷ setting.WaffoPancakeUnitPrice × QuotaPerUnit` |
  | stripe | `Money × QuotaPerUnit`（stripe 的 Money 已是分组倍率换算后的美元数量，无折扣可体现） |
  | creem | `Amount`（固定商品，无原价可比） |

  单价 ≤ 0 或结果非正时不写行（结算侧回落为到账额度）。换算使用 `common.QuotaFromDecimal` 等 `common/quota_math.go` 助手，不做裸 `int` 转换。
  分组倍率 > 1 时 `paid_quota` 会超过到账额度，由结算侧 `min` 封顶。
- **管理员充值**：管理员在弹窗填写，约束 `0 < paid_quota ≤ value`，违反返回 400。

### 2.3 排除与失效

以下情况对应部分**不发钱，但水位照常推进**，之后恢复也不补发：

- 邀请人被封禁（`status ≠ 1`）或软删除；
- 邀请人在排除名单（role=inviter）；受邀用户在排除名单（role=invitee，只作废该受邀用户的份额）；
- 开关关闭（`IkunRebateEnabled ≠ true`）：当天照常结算推水位，全部不发，`rebate_days.disabled = true`。

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
| rate `VARCHAR(32)` | 当日比例快照（Decimal 字符串） |
| disabled | 开关关闭时为 true |
| inviter_count / invitee_count | 实际发放的邀请人数 / delta>0 的受邀用户数 |
| total_rebate `BIGINT` | 当日实发总额 |
| error `TEXT` | 失败原因 |
| started_at / finished_at | Unix 秒 |

**`rebate_watermarks`**：`invitee_id PK`, `cum_recharge`, `cum_consume`, `settled_base`（均 BIGINT），`last_day CHAR(10)`, `updated_at`。

**`rebate_credits`**：`(day, inviter_id)` 唯一；`invitee_count`, `delta_sum`, `rebate`（实发，作废为 0）, `forfeit_reason`（空/`banned`/`deleted`/`excluded`/`disabled`）, `log_request_id`, `created_at`。
这是防重复发放的最终保证：入账事务里先插这一行，唯一冲突即说明已结算过。

### 3.3 配置（`options` 表，已有键值表，只新增行）

| key | 值 | 写入方 |
|---|---|---|
| `IkunRebateEnabled` | `true`/`false` | 后台设置块（root 的 `PUT /api/option/`） |
| `IkunRebateRate` | 小数字符串，`0 < rate < 1`，如 `0.05` | 同上 |

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
2. 聚合当日活动：按受邀用户求当日 `r`（§2.1 三个来源）与 `c`（type=2 的 `quota` 之和减 type=6 的 `quota` 之和），只保留 `inviter_id ≠ 0` 且 `r ≠ 0 或 c ≠ 0` 的受邀用户，按当前 `inviter_id` 分组。
3. 每个邀请人一个数据库事务：
   1. `SELECT ... FOR UPDATE` 锁邀请人 `users` 行（SQLAlchemy `with_for_update()`，两种库都生成 `FOR UPDATE`）。
   2. 插入 `rebate_credits(D, I)`；唯一冲突 → 回滚并跳过该邀请人（已结算过）。
   3. 读取并锁定其名下受邀用户的 `rebate_watermarks` 行（不存在则视为全 0），按 §2 更新 `cum_recharge`、`cum_consume`、`settled_base`、`last_day`。
   4. 判定作废（§2.3）；作废的份额不进入 `rebate`。
   5. `rebate > 0` 时：校验 `users.quota + rebate ≤ 2147483647`（`users.quota` 是 32 位 `int` 列），超限则抛错使当日失败；否则 `UPDATE users SET quota = quota + rebate`，并插入一条日志（§5）。
   6. 回填 `rebate_credits` 的统计字段，提交。
   7. 提交后 `DEL user:{I}`；失败只记警告（缓存最多 `SYNC_FREQUENCY` 秒后自愈）。
4. 全部邀请人完成后，`rebate_days[D]` 置 `done` 并写汇总；任何异常置 `failed` 并写 `error`，然后停止。

崩溃在任意点：已提交的邀请人有 `rebate_credits` 行，重跑跳过；未提交的整体回滚。无需 pending 状态或入账标记核对。

### 4.3 水位与跳过的一致性

同一邀请人事务内同时写 `rebate_credits` 与其受邀用户的 `rebate_watermarks`，因此"跳过已结算邀请人"与"水位已推进"总是一致。受邀用户在同一天只属于一个邀请人分组，不会被两个事务同时推进。

## 5. 入账日志

插入 `logs`：`user_id=I`、`username`=邀请人用户名、`type=3`、`quota=rebate`、`created_at=now`、`request_id=rebate-YYYYMMDD-{I}`（仅用于追溯）、`content` 如下，其余列取表默认值。

文案按邀请人 `users.setting` JSON 的 `language`：`en` 用英文，其余（含缺省）用中文：

- 中文：`返利结算（MM-DD）：共 {x} 个受邀用户消费，返利 {amount}`
- 英文：`Rebate settlement (MM-DD): {x} invitees consumed, rebate {amount}`

`x` = 该邀请人当日 delta>0 且未被作废的受邀用户数。`amount` 按结算时刻的钱包展示设置格式化，与前端 `web/src/lib/format` 的 `formatQuota` 结果一致（实现时逐项对照其源码）：

| `general_setting.quota_display_type` | 格式 |
|---|---|
| `USD` | `$` + `rebate / QuotaPerUnit` |
| `CNY` | `¥` + `rebate / QuotaPerUnit × USDExchangeRate` |
| `CUSTOM` | `custom_currency_symbol` + `rebate / QuotaPerUnit × custom_currency_exchange_rate` |
| `TOKENS` | `rebate` 原始数值 |

## 6. 前置检查与故障处理

`settle` 开始时依次检查，任一不通过即退出码非 0、不写任何数据：

1. 获取 Redis 锁 `rebate:settle:lock`（`SET NX PX` + 随机 token，后台续期；释放用 token 比对的 Lua 脚本）。拿不到 → 已有结算在跑。
2. 环境变量 `REBATE_LOGS_IN_MAIN_DB=true`（运维声明 new-api 未配置 `LOG_SQL_DSN`；否则消费日志不在主库，会把消费当 0 永久少发）。
3. `options.LogConsumeEnabled` 不是 `false`。
4. `rebate_meta` 存在且 `activity_start`/`timezone` 合法；`IkunRebateRate` 合法（缺失或越界即拒绝，不回落默认值）。

运行中：

- 当日存在未知渠道的成功充值订单、邀请人事务数据库错误、余额超上限 → 当日 `failed`，停止。
- 锁续期失败 → 不再开启新的邀请人事务，当日 `failed`（原因写明锁丢失），退出。
- 删除 Redis 用户缓存失败 → 仅警告。

告警：暂以退出码 + 容器日志为准。

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
  display.py      # 金额格式化 + 中英文文案
  lock.py         # Redis 锁与缓存失效
```

命令：

- `init --activity-start YYYY-MM-DD --timezone Asia/Shanghai`：建表并写 meta；已存在的 meta 不覆盖。
- `settle [--from D1 --to D2] [--dry-run]`：`--dry-run` 只读：打印各邀请人计划金额与作废原因，不拿锁、不写库。
- `status [--days 14]`：最近各日状态、总额、失败原因。

环境变量：`REBATE_DB_DSN`（`mysql+pymysql://...` 或 `postgresql+psycopg://...`）、`REBATE_REDIS_URL`、`REBATE_LOGS_IN_MAIN_DB`、`TZ`。

数据库账号权限：读 `users`/`top_ups`/`redemptions`/`logs`/`options` 与 Go 的 3 张返利表；`UPDATE users(quota)`；`INSERT logs`；Python 4 张表全权限；`init` 时需要建表权限。

### 7.1 部署

镜像基于 `python:3.12-slim`，uv 安装依赖，supercronic 执行 crontab `5 0 * * * python -m rebate settle`。容器 `TZ` 决定 00:05 属于哪个时区，结算窗口只取决于 `rebate_meta.timezone`。手动补结：`docker compose run --rm rebate python -m rebate settle --from ... --to ...`。

## 8. fork 改动

原则：新代码放新文件，上游文件只做挂载级改动。

### 8.1 后端

| 文件 | 类型 | 内容 |
|---|---|---|
| `model/rebate.go` | 新 | 3 个 GORM 模型；`TopUp.AfterCreate` 钩子（§2.2，与订单插入同一事务，钩子返回错误即下单失败）；`IncreaseUserQuotaAsRecharge(userId, quota, paidQuota, operatorId)`：同一事务内带钱包上限加额度 + 插入 `rebate_admin_recharges`，提交后更新用户缓存 |
| `model/main.go` | 上游 | `migrateDB` 与 `migrateDBFast` 各注册 3 个模型 |
| `controller/user.go` | 上游 | `ManageRequest` 加 `Recharge bool`、`PaidQuota *int`；`add_quota` 的 `add` 分支在 `Recharge` 时校验 `PaidQuota`（缺省等于 `Value`，`0 < paid ≤ value`）并改调 `IncreaseUserQuotaAsRecharge`；审计 params 带 `recharge`、`paid_quota` |
| `controller/rebate_admin.go` | 新 | `GET/POST/DELETE /api/rebate/exclusions`，RootAuth |
| `router/api-router.go` | 上游 | 挂排除名单路由 |
| `controller/rebate_info.go` | fork 已有，重写 | 读 `IkunRebateEnabled`/`IkunRebateRate`（直读 DB + 60 秒缓存）与 `rebate_meta`；任一缺失/非法或 `rebate_meta` 表不存在 → `{"enabled":false}`；否则 `{"enabled":true,"rate":..,"activity_start":..,"timezone":..}` |

`AfterCreate` 注意：钩子在 `model` 包，读取 `setting`/`operation_setting` 的单价（`model` 已依赖这些包，无循环）。只对 5 个已知 `PaymentProvider` 写行。

### 8.2 前端

| 文件 | 类型 | 内容 |
|---|---|---|
| `web/src/features/users/components/user-quota-dialog.tsx` | 上游 | `add` 模式下"充值"勾选框；勾选后显示"实付金额"输入（单位同额度输入，默认跟随额度，可改小），提交 `recharge`、`paid_quota` |
| `web/src/features/users/api.ts` / `types.ts` | 上游 | 请求类型加两个可选字段 |
| `web/src/features/system-settings/.../rebate-settings-section.tsx` | 新 | 开关、百分比（界面百分比，存小数）、排除名单表格（增删，分邀请人/受邀用户，带备注）、只读显示活动起点与时区 |
| 系统设置某 tab 的 `section-registry.tsx` | 上游 | 注册上面的设置块 |
| `web/src/features/wallet/components/affiliate-rewards-card.tsx` | fork 已有 | 适配新 `rebate_info` 结构 |
| `web/src/i18n/locales/*.json`（7 个） | 上游 | 新增文案 |

## 9. 测试

**Python（pytest）**：同一套用例分别跑 MySQL 与 PostgreSQL（docker compose 起库），覆盖：

- 水位：退款使 C 回落再回升不重复返利；R 封顶；多日累计。
- 实付：`paid_quota` 小于到账额度时按实付；无 `rebate_topup_paid` 行时回落到账额度；分组倍率 >1 时封顶到账额度。
- 选日：cron 自动补齐漏跑日；手动范围跳过 `done`；前面有缺口时拒绝；拒绝今天/未来/早于起点。
- 崩溃恢复：在第 k 个邀请人事务后中断，重跑不重复发放、水位一致。
- 作废：封禁、软删、两类排除、开关关闭——不发钱但水位推进，恢复后不补发。
- 前置检查：未声明 `REBATE_LOGS_IN_MAIN_DB`、`LogConsumeEnabled=false`、比例非法、未知渠道订单。
- 日志：4 种展示单位的金额格式；中英文文案；`quota`/`type`/`request_id`。

**Go（testify require/assert）**：

- `rebate_info` 返回契约（开启/关闭/非法值/表不存在）。
- `TopUp.AfterCreate` 各渠道 `paid_quota` 换算、单价为 0 不写行。
- `add_quota`：`recharge` 时 `paid_quota` 越界返回错误且额度不变；成功时额度与充值记录同时存在；插入充值记录失败时额度回滚。

## 10. 已定取舍

- 返利基数 `min(R, C)` 高水位；按天、按顺序结算；不支持对已结算日重算或追回。
- 比例修改从下一次结算起生效；补结历史日期用执行时的比例，不保留比例历史。
- 开关关闭与排除名单同语义：期间不发、之后不补。
- 兑换码按面额全额算充值（无实付信息）。
- 管理员"减少"与"覆盖"模式不涉及充值。
- 日志必须在主库（`LOG_SQL_DSN` 未配置），由运维显式声明。
- 受邀用户改绑后返利归当前邀请人。
