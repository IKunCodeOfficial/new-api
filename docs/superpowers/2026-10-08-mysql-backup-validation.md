# 邀请返利：真实 MySQL 备份验证

验证日期：2026-10-08。已阅读 `specs/2026-10-01-invite-rebate-design.md` 与 `plans/` 下的 fork、settler 两份计划，核对 Go 充值/兑换/推广余额划转、后台接口、前端配置及独立 Python 结算器实现。

## 环境与数据

- 备份：用户指定的 `ik_______2026-10-07_00-21-00.sql.gz`，完整恢复到本机独立 MySQL 8.0 容器；未连接或修改线上数据库。
- MySQL 容器：`codex-rebate-mysql-20261008`，仅绑定 `127.0.0.1:23306`；Redis：`codex-rebate-redis-20261008`，仅绑定 `127.0.0.1:26379`。
- `new-api`：完整原始恢复库；`rebate_replay`：业务回放库；`rebate_unit`：合成数据回归库，避免自动清表影响备份。
- 真实数据：57,140 个用户、112,172 笔在线订单、34,967 个兑换码；完整备份有 17,706,599 条消费日志。
- 回放库保留真实用户、配置、在线订单、兑换码及 10 月 1–6 日的原始日志。该窗口共 1,984,878 条原始日志，其中 1,947,881 条消费日志。三张 Go 返利表由当前项目 GORM 模型创建，四张 Python 表由结算器初始化。
- 测试活动起点为 `2026-10-01`，时区 `Asia/Shanghai`，比例为 `0.05`。这是隔离库测试设置，不代表线上活动已经启用。

## 复现并修复的问题

### 1. 不同 MySQL 排序规则导致所有日期结算失败

旧 `top_ups.trade_no` 使用 `utf8mb4_0900_ai_ci`；数据库默认值是 `utf8mb4_unicode_ci`，GORM 新建的 `rebate_topup_paid.trade_no` 继承后者。原跨表关联报错 `1267 Illegal mix of collations`，无在线订单的日期也会失败。

结算器改为先取当天订单，再按订单号分批查询实付记录，以原样订单号关联；保留无记录时回退到账额度、实付金额封顶规则。不修改原有表结构、字符集或排序规则。回归测试先复现 1267，再验证修复。

### 2. 正常历史在线订单缺少完成时间而被漏算

99,433 笔成功订单中，99,430 笔 `complete_time` 为 NULL 或 0，所有成功订单都有有效创建时间。用户确认采用：有效完成时间优先，否则用创建时间归日。

已同步代码、设计文档和结算器 README。测试覆盖 NULL、0、已有完成时间优先、跨日边界和未支付订单排除。历史全量充值诊断与独立 SQL 按用户汇总完全一致，覆盖 26,024 笔受邀用户的在线订单，以及已使用且软删除的兑换码。合计 6,330 个受邀充值用户、753,815,400,000 quota。

创建时间兜底的跨午夜付款可能归入下单日；活动起点之前的充值仍不参与活动。历史全量聚合仅用于验证充值来源，不将历史充值灌入 10 月活动水位。

### 3. 推广奖励转钱包可突破钱包安全上限

真实 MySQL 表结构下，将钱包设置为 `MaxWalletQuota - 1`、推广余额设置为 500,000 后，旧实现允许划转 500,000，突破 JavaScript 安全整数上限。

现先验证划转金额，并在已有用户行锁内校验钱包容量。超限返回错误，两边余额及累计推广收益均不变；恰好达到上限的划转仍成功。MySQL 回归用例已验证修复前失败、修复后通过。

## 六天真实业务回放

独立 SQL 按日统计充值和消费，再单独计算累计充值、累计净消费、高水位及每个邀请人的向下取整返利；与实际结算输出逐人逐日比对。

| 日期 | 实发 quota | 发放邀请人数 | 有返利的受邀用户数 |
| --- | ---: | ---: | ---: |
| 2026-10-01 | 32,700,966 | 13 | 24 |
| 2026-10-02 | 26,351,054 | 17 | 26 |
| 2026-10-03 | 26,248,818 | 17 | 30 |
| 2026-10-04 | 30,672,948 | 20 | 33 |
| 2026-10-05 | 30,097,560 | 24 | 47 |
| 2026-10-06 | 42,247,517 | 31 | 56 |
| 合计 | **188,318,863** | **122 人次** | **216 人次** |

验证结果：

- 354 个受邀用户的累计充值、消费、高水位、最后结算日全部一致。
- 每日 `rebate_credits`、`users.aff_quota`、`users.aff_history`、122 条管理日志与独立预期一致；所有用户的 `users.quota` 保持不变。
- 每日 dry-run 与随后真实结算金额一致，dry-run 不修改余额。
- 每日重跑均跳过且不重复入账；CLI 再次执行该范围返回“没有需要结算的日期”。
- 10 月 6 日在两个邀请人事务后模拟锁丢失，先失败、再恢复；已提交部分不重复入账，当日汇总仍与完整独立计算一致。
- 备份中的 4,160 个已使用且软删除兑换码仍纳入历史充值汇总。

真实在线订单结束于 7 月，备份保留的消费日志始于 9 月，因此六天真实返利回放主要验证兑换码充值。在线充值以完整历史订单聚合对账，加当前 Go 下单/支付回调和 Python 回归用例验证；未伪造已被清理的历史消费日志。

## 其他验证

- Python：`pytest -q -k 'not pg'`，配置独立 MySQL 与 Redis：**741 passed，165 deselected**。未测试 PostgreSQL。
- Go：`go test ./model -run '^TestRebateMySQL' -count=1`：通过。覆盖实付落库、重复支付回调幂等、管理员充值、非法实付拒绝、真实未使用兑换码兑换与重试、划转容量边界。
- Go：`go test ./controller -run '^TestRebateMySQLControllerContracts$' -count=1`：通过。覆盖 Python 初始化后的真实表结构读取、开启/关闭返利信息、管理员实付非法值和缺省值的 HTTP 契约。
- `go build ./...`、`go vet ./model ./controller`、两个仓库的 `git diff --check`：通过。
- 独立只读代码审查：未发现需要修改的问题。

Go 实库测试通过 `REBATE_MYSQL_TEST_DSN` 显式启用，要求库名以 `rebate_` 开头。其余情况下跳过；测试目标应为已恢复并初始化返利表的专用回放库。充值/管理接口测试回滚事务，划转测试恢复原有金额。

变更位于两个仓库：`new-api-ikun` 与 `/Users/hzy/PycharmProjects/ikun_rebate_v2`。本次未提交、未部署。

## 2026-10-11 当前版本复验

基于 Go 仓库 `80416fe73` 与当前 Python 结算器重新阅读设计、两份实现计划及相关实现，进行独立代码审查和实库验证。本次数据库范围遵循用户要求：**仅 MySQL 8.0.46**，未验证 PostgreSQL/SQLite 的数据库兼容性。Go 默认回归中存在 SQLite 单元测试，不能视为数据库兼容性矩阵。

### 本次新增修复

三个在线支付入口（epay、Waffo、Waffo Pancake）发送给网关的是按币种舍入的金额，但 `TopUp.Money` 原先保存舍入前价格，导致 `rebate_topup_paid` 的实付快照可多算或少算。例如单价 1、数量 1、倍率 0.3333：支付 0.33，却按 0.3333 记录 166650 quota；正确值是 165000。

现统一使用**各渠道实际发送的金额字符串**，解析后存入订单并由原有事务钩子计算实付快照。保留 epay/Waffo 与 Pancake 各自的半分舍入规则；Waffo 的 JPY 等零小数币种舍入为 0 时，建单前拒绝。只修正新订单，不追溯修改备份历史订单或已结算记录。

回归集中在现有 `controller/topup_quota_limit_test.go` 的 `TestTopUpPaidSnapshotMatchesGatewayAmount`，11 个子用例在 MySQL 上先失败、修复后通过，覆盖向上/向下、半分、零小数币种和零收费拒绝。Waffo/Pancake 使用无效测试密钥停止外部调用，验证真实 handler → 本地订单 → GORM 实付钩子；epay 同时校验返回的支付金额参数。未调用真实支付平台扣款。

### 新的真实数据回放

`gzip -t` 验证指定备份完整性通过。复用此前从该备份完整恢复的本机 `codex-rebate-mysql-20261008` 原始库，另建 `rebate_replay_20261011`，复制 57140 用户、112172 在线订单、34967 兑换码，以及 10 月 1–6 日的 1984878 条日志。原始恢复库不修改；回放库只补结算需要的兼容字段和返利表。

重新逐日运行独立 SQL 对账脚本，结果与上文六天表格完全一致：

- 历史充值来源：6330 个受邀用户、26024 笔受邀用户在线订单、753815400000 quota，包含 4160 个已使用且软删除的兑换码。
- 六天实际返利合计 **188318863 quota**，354 个受邀用户的累计充值/消费/高水位/最后结算日逐项一致。
- 邀请者余额、累计返利、122 条管理日志与独立预期一致，钱包余额不被结算直接修改。
- 每日 dry-run 不写入且与实发一致；每日重跑不重复发放；最后一天模拟部分提交后丢锁，恢复后汇总和余额仍一致。
- 本次保留同样的数据边界：历史在线订单和备份消费日志无重叠窗口，不将不存在的历史消费当成已验证数据。

结果：`/private/tmp/rebate-verify/replay-report-20261011.json`；脚本：`/private/tmp/rebate-verify/scripts/replay_20261011.py`。

### 当前服务全流程

在 `rebate-verify-mysql-20261009`（127.0.0.1:33306）与独立 Redis 上运行本次构建的网关；备份中真实上游渠道均已禁用，只使用本机模拟模型接口。通过 HTTP 完成：邀请人注册 → 取得邀请码 → 受邀人注册并绑定 → 兑换码充值 → 在线下单/伪造回调拒绝/正确签名回调/重复回调 → 管理员充值与赠送 → API token → 六次模型请求消费 → Python CLI dry-run/结算/重跑 → 用户读取返利及日志 → 转入钱包 → 立即消费。

两次修复版流程均通过：

1. 常规案例：到账 20000000 quota，其中真实实付基数 8500000，消费 12000000，10% 返利 **850000 quota**；转入钱包后 Redis 同步，可立即消费。
2. 舍入案例：在线价格 8.3333、网关金额 8.33、在线实付快照 4165000；加上兑换码 3000000 和管理员实付 1500000，总基数 8665000，消费 12000000，返利 **866500 quota**；转入钱包后可立即消费。赠送 10000000 不增加返利基数。

模拟结算当天活动时，只在测试包装脚本中将 CLI 的“今天”设为次日，真实业务时间戳和资金逻辑不修改。测试邀请本身有 1 quota 注册奖励，所以两例实际划转分别为 850001 / 866501；`aff_history` 在划转时保持不变。

日志：`/private/tmp/rebate-verify/e2e-fixed-20261011.log`、`e2e-rounding-20261011.log`。这些是隔离模拟，不是线上支付或真实供应商调用验收。

### 命令与结果

实库环境变量指向本机测试容器：`TEST_MYSQL_DSN` 用专用 `rebate_unit`（夹具另建临时库），`REBATE_MYSQL_TEST_DSN` 用 `rebate_replay`，Python 的 `REBATE_TEST_MYSQL_DSN` 用 `rebate_unit`、`REBATE_TEST_REDIS_URL` 用独立测试 Redis DB 15。

- `go test ./controller -run '^TestTopUpPaidSnapshotMatchesGatewayAmount$' -count=1`，`TEST_MANAGE_USER_DIALECT=mysql`：11 个子用例通过。
- `go test -p 1 ./model ./controller -run '^TestRebateMySQL' -count=1`：两个包通过。旧回放库首次运行因缺少当前模型的 `access_token_created_at` 字段失败；补跑当前 Go 模型迁移两次后通过。完整服务的测试库也成功启动了旧版与修复版当前构建。
- `python -m pytest -q -k 'not pg' -o cache_dir=/private/tmp/codex-rebate-pytest-cache`：**741 passed，165 deselected**。
- `go test ./model ./controller -count=1`：model 通过；controller 首次遇到默认 SQLite 的 `TestSecurityAccountDeletionConcurrentRequestsHaveOneWinner` 单次失败。随后 `go test ./controller -count=1` 全部通过；该并发用例另用 `TEST_SECURITY_DIALECT=mysql` 在 MySQL 单独复核也通过。记录此次波动，不将首次执行描述成全绿；未改动认证代码。
- `go build ./...`、`go vet ./model ./controller`、`git diff --check`：通过。
- `bun run test src/features/users/lib/__tests__/rebate-recharge.test.ts src/features/wallet`：**4 个文件、18 项通过**。
- `bun run build:check`：类型检查和生产构建通过。
- 独立只读审查发现上述金额问题，修复后二次审查未发现新的阻断问题。

上线结论限定于设计既定范围：消费日志保存在主库且持续开启，尚不启用订阅，活动起点/时区正确初始化，结算任务按 00:05 运行。订阅任务日志标记缺口仍是已有上线限制；本次不宣称全站无缺陷，也不将 MySQL 8.0 验证扩展为其它数据库或版本的保证。代码改动尚未提交、尚未部署。

## 2026-10-11 第二轮复验（含注册邀请码冲突修复）

在上一节的工作树基础上（金额舍入修复尚未提交）重新通读设计与两份实现计划，独立审查 Go 与 Python 实现，再次只在 **MySQL 8.0.46** 上验证；未覆盖 PostgreSQL/SQLite。

### 新增修复：注册时邀请码重复导致注册失败

真实备份有 57207 个用户，`users.aff_code` 为 4 位字母数字（1470 万种组合）。原实现 `common.GetRandomString(4)` 直接写入，每次注册约 0.4% 概率撞上已有用户的邀请码，唯一索引把它变成直接返回给用户的 `Error 1062 ... users.idx_users_aff_code`。本轮全流程脚本连续注册约 20 个账号就命中一次（`il7p`）。这是邀请返利的入口路径，故一并修复。

- 新文件 `model/aff_code.go`：`NewAffCode(tx)` 最多尝试 16 次随机码，`Unscoped` 计数排除已被任何用户（含软删除）占用的码；仍以唯一索引作为并发兜底。邀请码长度保持 4 位，不影响已经发出的邀请链接。
- 挂载点：`model/user.go` 的 `Insert` / `InsertWithTx`、`controller/user.go` 的 `GetAffCode`（三处上游文件，合并时注意）。
- 回归：`model/aff_code_test.go` 用确定性序列注入随机源，断言跳过在用码与软删除用户持有的码、耗尽后返回 `ErrAffCodeExhausted`、注册路径分配到空闲码。修复前该用例编译失败（RED），修复后通过。

### 独立对账与补充场景

- `scripts/recompute_claude_20261011.py`：不复用结算器代码，直接用 SQL 重算 2026-06-01 至 2026-10-11 每个受邀用户每日 r/c，再按规范推水位，并按 `rebate_days.rate` 的逐日快照（0.05 → 0.1）比对。结果：`rebate_credits` 9838 行、`rebate_watermarks` 2857 行、430 个邀请人的返利日志全部一致，总额 3852626547 quota。
- `scripts/e2e_rounding.py` 在本轮构建上重跑：43 项全部通过（邀请 → 兑换码/在线下单与签名回调/管理员充值 → 消费 → 结算 → 日志 → 转入钱包 → 立即消费）。
- 新增 `scripts/e2e_extra_claude.py`，全部通过：C>R 按 R 封顶；同一邀请人两个受邀用户先汇总再向下取整（2000007 × 0.1 → 200000）；type=6 退款冲减 C；受邀用户排除名单作废且水位照推、移除后不补发；邀请人封禁作废、解封后不补发；`aff_history` 接近 2^53−1 时按 overflow 作废、当日仍 done、WARN 与 `status` 可见；开关关闭写 disabled 日、rate 为 NULL，重新打开不补发；管理员 subtract/override 与未勾选充值的赠送不进基数、`paid_quota` 缺省等于额度、`paid_quota=0` 或 subtract 带充值标记被拒；非 root 无法访问排除名单接口；划转超出推广余额、低于 1 单位、超出钱包上限均被拒且余额不变，重置后划转成功、Redis 缓存同步、可立即消费。
- 注意：rc.42 起管理员禁用/启用用户需要二次安全验证（`SECURITY_PROOF_REQUIRED`），脚本改为直接修改 `users.status` 模拟封禁，结算器只读该列。

### 命令与结果

- `go test ./model ./controller -count=1`（SQLite 默认）：通过。
- `TEST_MANAGE_USER_DIALECT=mysql ... go test -p 1 ./model ./controller -run 'Rebate|TopUpPaid|ManageUser|Register|AffCode' -count=1`（`rebate_unit` / `rebate_replay` 库）：通过。
- `go build ./...`、`go vet ./model ./controller`、`gofmt -l`、`git diff --check`：通过。
- Python：`pytest -q -k 'not pg'`（MySQL + Redis DB 15）：**741 passed，165 deselected**。
- 前端：`bun run test` 覆盖 `rebate-recharge`、`wallet`、`users/components`：54 项通过。
- 注册压测：修复后在 57207 用户的备份库上连续注册 400 次（`scripts/register_load_claude.py`），358 次成功、42 次被接口限流（HTTP 429）拒绝，**0 次邀请码重复**，服务日志无 `Duplicate entry`；用户数 57207 → 57565，`aff_code` 仍全部唯一。

数据观察（未改代码）：226 个真实用户的 `setting.language` 为 `"zh"`，按前端 `normalizeInterfaceLanguage` 归一化为 `en`，其返利日志为英文；这是规范规定的回退，是否把 `zh` 视作简体中文属产品决策。改动仍未提交、未部署。
