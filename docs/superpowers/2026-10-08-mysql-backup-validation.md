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

## 2026-10-11 第三轮复验：钱包缓存竞态与结算接管

本轮起点为 Go `c2d47f261`、Python `51314a4`。重新通读设计与两份计划，对 Go 钱包/充值和 Python 结算器分别进行独立审查。使用指定备份和已有本机容器，仅验证 **MySQL 8.0.46**；没有连接线上数据库。以下结果均基于本轮最终修复后的工作树，不沿用前两轮结果。

### 本轮发现与修复

1. **P1：缓存重建和授信交错导致可消费额度重复增加。** 在真实 MySQL 与 Redis 中确定性插入“数据库提现提交后、Redis 同步前”的并发读取：原余额 100，提现 500000，数据库为 500100，缓存却变成 1000100；预扣 500101 被错误放行，数据库变成 -1。同样的公共授信同步路径影响管理员充值和 epay。另一个服务层回归证明，数据库只有 1、缓存虚高为 10000000 时，预扣 2 还能错误触发信任额度旁路。

   修复后，`GetUserQuota` 总是读取数据库，钱包预扣使用带余额条件的原子数据库更新；退款与差额结算也同步落库，钱包余额不再进入批量更新队列。Redis 中的额度只作快照，授信后刷新数据库快照，不再增量重复加款。令牌额度与用量统计的批处理保留。原有信任额度免预扣、实际用量超出预估后的差额扣款规则保持不变，因此不能把本修复描述为“任何请求都不允许钱包负数”。

   三种授信入口、批量开关与缓存虚高/缺失/不可用组合，以及服务层信任旁路均通过先 RED、后 GREEN 的回归。上线代价是增加同步数据库访问：每次钱包读取和变动都访问主库，启用 Redis 时刷新快照另需一次余额查询与 Lua 调用。

2. **MySQL 零金额结算边界。** 同步加款遇到 `quota + 0` 时，MySQL 默认返回零条变化行，原判断误报钱包上限错误。零金额现直接成功返回；同一实库表格的六个组合均先失败、修复后通过。管理员额度测试也改用真正的预扣，验证已落库预扣与后续调整的组合，不再假设只在缓存里存在的预扣。

3. **结算锁接管后旧进程仍可写状态或入账。** 原进程通过 Redis 检查后暂停，新进程接管时，旧进程恢复可把 `done` 覆盖为 `failed`；在邀请关系变化的合成场景下，还可在接管者已完成后按旧聚合继续发款。修复在日状态写入与每个邀请人事务最先锁住 `rebate_days` 行，再在数据库锁内确认 Redis 所有权；`done` 不再接受旧执行的修改。失锁执行返回失败，停止写库，日行可保留 `running` 供接管或重试。数据库行锁、唯一入账记录与水位共同保证已提交部分不会重复发放。

4. **故障恢复的审计丢失。** `rebound` 作废说明现与水位在同一事务提交，硬崩溃后重跑仍可追溯。部分提交后修改比例/开关，保持“本次执行配置”的原有语义；新增持久 `config_changed` 审计，记录前后配置和变更前已入账邀请人。未冻结旧比例，管理员关闭后未处理份额仍不发钱。无新增表或字段。

Go 与 Python 修改互相由未参与该实现的 agent 复审，最终未发现新的阻断问题。现有设计文档与 Python README 已同步失锁恢复和钱包语义。

### 指定备份的最终独立回放

`gzip -t` 通过。新建独占 `rebate_replay_round3`，从原始恢复库只读复制 57140 个用户、112172 笔在线订单、34967 个兑换码、1984878 条 10 月 1–6 日日志。原始完整库包含 18815557 条日志，其中 type=2 为 17706599 条。

- 历史充值独立 SQL 对账：6330 个受邀用户、26024 笔受邀在线订单、753815400000 quota。99433 笔成功在线订单中 99430 笔缺少有效完成时间；完成时间优先、创建时间兜底口径一致；4160 个软删除已用兑换码仍被计入。
- 最终六天返利仍为 **188318863 quota**。全部 **463 条入账记录**（含零值/作废）、**354 个累计水位**、**122 条管理日志**及各邀请人奖励余额/累计奖励与独立预期一致；钱包余额不被结算直接改变。
- 每日 dry-run 不写入，每日重复结算不改变资金或状态。10 月 6 日两位实际获付邀请人先提交 **17357342** 后模拟失锁，执行返回 failed、数据库保留 running；剩余 dry-run 为 **24890175**，恢复后的当日总额、水位、日志均完全匹配。
- 生产 CLI 再次执行六天范围退出 0，提示没有需要结算的日期。资金、日状态、入账记录、水位和日志均保持不变；Python 源码哈希在最终回放前后一致。
- 原始在线订单截至 7 月，留存消费日志始于 9 月，两者没有历史重叠窗口。六天返利回放主要验证兑换码；历史在线充值通过完整来源聚合对账与下述模拟支付全流程验证，不宣称还原了已经不存在的在线充值消费历史。

脚本：`/private/tmp/rebate-verify/scripts/clone_replay_round3.py`、`replay_round3.py`。结果：`/private/tmp/rebate-verify/replay-round3-report.json`、`replay-round3-cli.log`、`data-bounds-round3.json`。

### 最终 HTTP 全流程与检查

在 33306 的独立备份测试库运行本轮最终构建，Redis 为独立测试实例，**开启 `BATCH_UPDATE_ENABLED=true`**。真实上游均不参与，仅使用本机模拟模型及测试支付签名。

- `e2e_rounding.py`：**43 项通过**。注册邀请人 → 取得邀请码 → 受邀人注册 → 兑换码充值 → 在线下单/错误签名拒绝/有效签名及重复回调 → 管理员充值与赠送 → 六次真实 HTTP relay 消费 → Python CLI dry-run/结算/重跑 → 查看返利和日志 → 提现到钱包 → 立即消费。
- 本轮金额：钱包到账 20000000；实付基数 8665000（兑换码 3000000、在线实际支付 8.33 对应 4165000、管理员实付 1500000），消费 12000000，10% 返利 **866500**。加上邀请注册奖励 1，提现 **866501**；赠送的 10000000 不参与返利基数。
- `e2e_extra_claude.py`：**58 项通过**，覆盖充值封顶、多人汇总后取整、退款、封禁、排除名单、禁用期间不补发、溢出作废、管理员调整与提现边界。
- `transfer_concurrency_round3.py`：两笔并发提现各 600000，仅一笔成功；钱包 600100、推广余额 400000、累计奖励 1000000，总额守恒，重试被拒绝。
- 中间版与最终版重复启动后，`information_schema.columns` 与 `statistics` 全量快照 `cmp` 一致；没有非幂等的表结构或索引变动。

最终日志：`e2e-final-round3.log`、`e2e-extra-final-round3.log`、`transfer-concurrency-final-round3.log`，均在 `/private/tmp/rebate-verify/`。为模拟当天结算，沿用只在测试包装器中将 CLI 日期边界设为次日的方式，不改变生产资金代码或业务时间戳。

### 命令与结果

所有 MySQL DSN 均指向本机测试容器。`REBATE_MYSQL_TEST_DSN` 普通回归使用 `rebate_replay`，竞态回归使用 `rebate_cache_round3`；后者配独立 Redis DB14。控制器 MySQL 夹具使用 `TEST_MYSQL_DSN` / `TEST_MANAGE_USER_DIALECT=mysql`；服务信任用例使用独占 `rebate_service_round3` 与 Redis DB13。Python 全量回归独占 `rebate_python_round3` 与 Redis DB15。

- `go test -p 1 ./model ./controller ./service -count=1`：三个包全部通过。默认 Go 单元夹具包含内存 SQLite，不代表数据库兼容性矩阵。第一次执行 controller 时三个旧缓存预扣夹具失败；替换为真实预扣夹具后，完整重跑通过。
- `go test -p 1 ./model ./controller -run 'Rebate|TopUpPaid|ManageUser|AffCode' -count=1`，上述 MySQL 环境：两个包通过。
- `go test ./model -run '^TestRebateMySQL(TransferCacheMissCannotDoubleCredit|WalletMutationsStayAuthoritativeWithBatchAndRedisFailures)$' -count=1`，真实 MySQL＋Redis：通过。
- `go test ./service -run '^TestPreConsumeBillingRejectsInflatedCachedWallet$' -count=1`，真实 MySQL＋Redis：先 RED、后 GREEN。
- Python `python -m pytest -q -k 'not pg' -p no:cacheprovider`，MySQL＋Redis：**751 passed，175 deselected**。新增失锁检查使一个旧 CLI 调用次数夹具失效；改为真正替换 Redis 锁 token 后完整重跑通过。
- `go build ./...`、最终可执行文件构建、`go vet ./model ./controller ./service`、修改文件的 `gofmt -l`、两个工作树的 `git diff --check`：通过。
- `bun run test src/features/users/lib/__tests__/rebate-recharge.test.ts src/features/wallet src/features/users/components`：**10 文件、54 项通过**。
- `bun run build:check`：通过。

### 上线范围与切换要求

本轮修复了已复现的资金授权与结算接管问题；最终验证范围内未发现剩余阻断项。实库结论限定 MySQL 8.0.46，没有验证其它数据库/版本，也没有调用真实支付平台或真实模型供应商。

上线继续要求：消费日志在主库且持续开启、暂不启用订阅、活动起点/时区正确、00:05 结算任务和退出码告警配置正确。

**钱包版本切换必须统一完成：** 旧节点仍把 Redis 当成资金余额并可能持有内存批次，新节点只信任数据库，二者不能混跑。先停止接入新流量，等待在途请求/任务处理结束，确认旧节点余额批次已经落库，再停止全部旧节点并切换到修复版。旧版本退出代码没有强制刷新资金批次，不能把“收到 SIGTERM”当成批次落库的保证。应核对 `BATCH_UPDATE_INTERVAL`（缺省 5 秒）及实际批处理完成日志，确认完成后再恢复流量。修复版会增加主库访问，需按实际容量部署。

变更仍在 Go 与 Python 两个工作树，尚未提交、未部署。原始备份恢复库保持只读；本轮测试数据保留在隔离库中以便复核。本轮启动的测试网关和模拟上游已停止。

## 2026-10-11 全量邀请数据复验

按“对全量邀请数据测试一遍”的追加要求，在第三轮修复后的同一工作树执行。MySQL 版本为 **8.0.46**，使用本机 `23306` 容器内指定备份的原始恢复库 `new-api`，只读复制至独占库 `rebate_full_invites_20261011`。本轮没有新增业务代码修改。

### 全量范围与数据边界

| 项目 | 全量数量 |
|---|---:|
| 全库用户及最终资金核对 | 57,140 |
| 邀请人 | 2,471 |
| 受邀用户（`inviter_id <> 0`，含软删除） | 14,262 |
| 两种角色去重后的用户 | 16,069 |
| 受邀用户全部留存日志（全部日期、全部类型） | 6,350,463 |
| 纳入充值来源的成功在线订单 | 26,024 |
| 纳入充值来源的已用兑换码 | 7,364 |
| 消费日志（type=2） | 6,007,820 |
| 退款日志（type=6） | 2,494 |

完整复制 112,172 笔在线订单、34,967 个兑换码和全部用户/options；日志仅按全量邀请关系过滤，没有日期抽样。源表列与索引、充值表全表校验和、日志分类型数量/金额/时间边界均在复制后核验，确认当前读取所需兼容列存在；原始恢复库未执行迁移或写入。

其中 664 人同时具有两种角色；78 名受邀用户、4 名邀请人为软删除，后者关联 6 名受邀用户。已用兑换码中有 741 个已软删除，仍完整计入充值。26,024 笔相关在线订单均缺少有效完成时间，按设计回退创建时间；历史没有实付采集表记录，按设计回退到账额度。

逐日回放 **2025-09-19 至 2026-10-07，共 384 天**，时区 `Asia/Shanghai`，固定比例 5%，`QuotaPerUnit=500000`。历史活动起点只用于覆盖备份中的全部充值，不是上线活动起点配置。

**消费日志实际仅留存 2026-09-05 00:00:00 至 2026-10-07 00:21:02。** 早期 type=6 记录的 quota 全部为 0，不能据其日期声称存在更早的消费历史。10 月 7 日只回放备份中的部分日数据；本轮没有恢复或补造缺失的历史消费。

### 实际 CLI 与两套独立对账

直接调用生产 `rebate.__main__.main`，每次提供明确日期范围，使用独立 Redis DB12。包装器仅记录结果、执行只读预览并注入一次真实 Redis 所有权更换，不替换结算算法、不修改业务时钟。结算前后 12 个源码、locale 夹具和依赖描述文件的 SHA256 完全一致。

- 6,345 名受邀用户产生有效充值或消费活动，建立 6,345 条累计水位；其余 **7,917 名没有自身有效活动**，核验没有产生水位。无自身活动不代表不能以邀请人角色获利：其中 15 人合法取得下级返利，逐用户核对包含这种角色重叠。
- 1,703 名邀请人与有效活动关联，其余 768 名也纳入全量资金核对。1,241 名受邀用户贡献正返利基数，**504 名邀请人实际获付**。
- 16,860 条结算记录：12,632 条没有新基数、5 条向下取整为零、4,223 条实际入账；本备份没有正基数作废记录。合计 **4,527,018,658 quota**，对应 4,223 条管理日志。
- 第一套独立 oracle 不导入结算器代码，从原始只读库重算每天每名受邀用户的累计充值、净消费、高水位，再按邀请人汇总及整数取整。全部 **57,140 个用户余额、6,345 条水位、16,860 条 credits、384 个日状态、4,223 条日志逐项完全一致**。
- 第二套只读一致性快照核验所有用户的推广余额/累计收益增量、钱包及已用额度、邀请关系、日志唯一性、每日成员与汇总、末次活动日期。重复入账、重复日志、缺失日志、孤立日志、无活动用户水位、错误成员均为 0；所有用户钱包未被结算直接修改。
- 充值首日、无活动日、首个消费日以及故障后的剩余部分共四次 `settle_day(..., dry_run=True)`，前后状态哈希均相同。
- 最后一天两名邀请人提交 **1,225,566 quota** 后替换真实 Redis 锁 token。CLI 退出 1，日状态保留 `running`，未删除接管者 token；剩余 dry-run 为 **562,262 quota**。显式恢复退出 0，合计 **1,787,828 quota** 与尾日独立预期一致。
- 再跑完整 384 天退出 0，四张返利表、全部用户财务字段、相关配置及新增日志的哈希完全不变，证明未重复发款或重写结算状态。

### 全部邀请人转入钱包

两份独立对账 `PASS` 后，另建 `rebate_full_wallet_20261011`，复制全部用户和 credits。通过实际 Go `User.TransferAffQuotaToQuota` 对 **2,471 名邀请人**分别尝试转出全部推广余额，再用相同金额重复一次，共 **4,942 次真实方法调用**。启用批量开关，Redis 使用独立 DB11，覆盖 1,235 个热缓存、1,236 个冷缓存。

| 首次划转结果 | 人数 |
|---|---:|
| 成功 | 255 |
| 低于 500,000 quota 最低金额 | 1,840 |
| 没有正推广余额 | 372 |
| 软删除 | 4 |

2,471 次重复请求全部拒绝，资金不再变动。合计转入 **4,495,945,743 quota**；初始推广余额 **4,527,031,045 quota**（含原有余额），剩余 **31,085,302 quota**，三者守恒。逐全部 57,140 名用户核验预期钱包/推广余额，`aff_history` 不变，未操作用户的资金不变，错误计数为 0。全量结算库在钱包测试前后财务哈希相同。

首次临时 Go 验证程序在元数据扫描阶段因 `DATA_TYPE` 列名映射而退出，当时尚未开始任何划转。仅修复临时程序的查询别名，确认侧库与结算库财务哈希相同后恢复测试；这不是业务代码故障。侧库补入缺少的 `access_token_created_at`，保留四个额度列为 BIGINT；重复兼容检查新增列为 0，schema 哈希相同。此处不是一次完整启动迁移测试，启动迁移证据见前一轮。

### 执行证据

执行脚本均保存在 `/private/tmp`，所有数据库连接均指向本机测试容器。以下为实际命令（省略输出重定向）：

```text
/Users/hzy/PycharmProjects/ikun_rebate_v2/.venv/bin/python -u /private/tmp/rebate-verify/scripts/clone_full_invites_20261011.py
PYTHONDONTWRITEBYTECODE=1 /Users/hzy/PycharmProjects/ikun_rebate_v2/.venv/bin/python /private/tmp/rebate-full-invites-cli-20261011.py
/Users/hzy/PycharmProjects/ikun_rebate_v2/.venv/bin/python -B /private/tmp/rebate-full-invites/independent_oracle.py verify
go build -o /private/tmp/rebate-verify/full-invites-wallet-harness /private/tmp/rebate-verify/full_invites_wallet_harness.go
python3 /private/tmp/rebate-verify/run_full_invites_wallet.py --resume-prepared
/Users/hzy/PycharmProjects/ikun_rebate_v2/.venv/bin/python -u /private/tmp/rebate-verify/scripts/check_full_invites_source_final.py
```

克隆结果为 `CLONE_VERIFIED_READY_FOR_SETTLER`；CLI 完成 384 天、恢复与重跑退出 0；两份独立对账报告均为 `PASS`。故障注入阶段退出 1 为预期结果。

聚合报告：`/private/tmp/rebate-verify/full-invites-clone-20261011.json`、`/private/tmp/rebate-full-invites-20261011/cli-report.json`、`/private/tmp/rebate-full-invites/full-independent-comparison.json`、`/private/tmp/rebate-verify/full-invites-independent-reconciliation.json`。逐用户财务夹具仅保存在本机受限临时目录，未写入仓库。

钱包结果：`/private/tmp/rebate-verify/full-invites-wallet-reconciliation.json`、`full-invites-wallet-clone.json`、`full-invites-wallet-run.log`。最终源库核验：`/private/tmp/rebate-verify/full-invites-source-final-check.json`，57,140 名用户五项财务字段与事前快照 SHA256 一致，29 个其它字段零差异，订单/兑换码校验和、原有 129 项配置、五表结构/索引一致。源日志总数、最大 ID、type=2/6 数量/金额/时间聚合与基线一致；未预存全源日志逐行内容哈希，不作每个日志字段逐字节不变的证明。

**结论范围：以上为 MySQL 备份中留存数据的全量验证。** 用户随后确认完整消费日志另存于 ClickHouse，并要求补全后再测试；该后续验证必须单独记录，不能以本节结果代替缺失历史日志的验证。变更尚未提交、未部署，前一轮的钱包切换要求仍适用。

## 2026-10-11 ClickHouse 历史日志补全复验

### 数据来源与快照一致性

读取局域网 `192.168.0.101` 上实际位于 `/Users/hzy/PycharmProjects/clickhouse-analysis` 的项目。ClickHouse **25.8.33.6** 的 `archive.logs` 是普通 MergeTree 原始日志表，没有日志 TTL 或引擎级 ID 去重；分析用的 `fact_usage`/汇总表不含退款，因此本轮直接读取原始表。

`ops.ingest_runs` 最新成功批次正是指定的 `ik_______2026-10-07_00-21-00.sql.gz`：文件大小 **1,977,732,975 bytes**，SHA256 **ac2f0b396961492d1b4c3b2a3c9c2972155fa3f46e58025dec5a62bab7f94548** 与本机原文件完全一致，原始日志高水位 **232893219**。同批用户、在线订单、兑换码快照数量分别为 57,140、112,172、34,967，与原 MySQL 一致。没有使用较新的用户或充值状态拼接旧备份。

冻结原 MySQL 的全部 **2,471 名邀请人、14,262 名受邀用户**及充值来源，只从 ClickHouse 补日志。读取所有归档来源，包括不在常规导入批次表中的早期手工补档。原始表全库有 231,681,785 条日志；本轮业务范围内的数据如下：

| 项目 | 数量 |
|---|---:|
| 消费日志（type=2） | 63,915,959 |
| 退款日志（type=6） | 2,494 |
| 合计原始记录 | 63,918,453 |
| 相比 MySQL 留存数据补入的记录 | 57,908,139 |
| 有日志的受邀用户 | 5,795 |
| 日期／用户／类型汇总分组 | 151,130 |
| 有日志的自然月份／日期 | 14／373 |

受邀日志范围为 **2025-09-19 01:22:03 至 2026-10-07 00:21:02（Asia/Shanghai）**；与充值合并后的测试日历仍为 384 天。最后一天仍是备份中的部分日，不代表 10 月 7 日全天。消费额度合计 **1,159,088,769,397 quota**。实际退款 quota 全为 0，负额度和订阅排除记录均为 0，这些真实数据不能替代前述专项合成边界测试。

### 原始记录装载与独立来源验收

新建本机 MySQL **8.0.46** 隔离库 `rebate_ch_full_20261011`，从原始 `new-api` 克隆用户、订单、兑换码和配置，原有表校验和一致。另建空日志表及三张空的返利输入表；没有复用上轮已结算的水位或用户推广余额。

- 按月导出每条原始记录的 `id/user_id/created_at/type/quota/other/request_id/upstream_request_id`。三个字符串以 HEX 传输，MySQL 用 UNHEX 还原，保留原始 `other` 字节及其订阅标记语义。未用日汇总记录替代真实消费行。
- 每月 `count=uniqExact(id)`，各月 ID 范围严格不重叠，证明本轮全量子集无重复 ID。14 份 gzip 合计约 4.51 GiB，各自记录 SHA256；解压时逐行检查八列、整数范围、用户集合、月份、HEX/UTF-8、gzip CRC，并计算未压缩文件 SHA256。
- 每月事务内执行 LOAD DATA，再核对影响行数、原始八字段 CRC32 的 XOR/SUM、字段字节数、ID/额度及类型聚合；任何警告或差异都回滚当月。**14 个月全部提交，63,918,453 行，警告全部为 0，所有校验一致**，进程退出 0。
- 日志表保留原始列，导入测试使用主键及日期／类型索引。临时开启的 `local_infile` 已恢复 OFF；仅本机测试实例曾临时增大 buffer pool 和 redo，结束后已实际恢复 **1 GiB／100 MiB**，resize 状态确认完成。持久性配置未降低，展开 TSV 已清理。
- 独立只读 SQL 逐 ID 核对原 MySQL 的 **6,010,314** 条相关消费／退款日志，七项返利字段没有缺失或差异，额外核对 `upstream_request_id` 也没有差异。本批重叠数据没有数值 NULL 或字符串 NULL 转换。
- 独立核对全部 **63,918,453** 条记录的唯一性、用户归属和类型；与直接从 ClickHouse 另行导出的 **151,130** 个日期／用户／类型分组逐项比较，条数、额度、ID/时间边界及订阅排除结果完全一致。自然日期为 373，日／类型组合为 381；自然月份为 14，月／类型组合为 16，四者不混用。
- 原用户 31 列、订单 10 列、兑换码 11 列与原始快照一致；原始恢复库的全部用户财务 SHA256、129 项配置及上一轮保留基线也一致。来源验收报告 **PASS**，数据库全程只读。

本轮辅助脚本及聚合证据位于 `/private/tmp/rebate-clickhouse-20261011/`：`export_months.py`、`export-manifest.json`、`load_mysql_logs.py`、`load-report.json`、`mysql-runtime-tuning.json`、`source-cohort-crosscheck.json`、`readonly-merge-reconciliation.json`。来源审计完成后已关闭本次 SSH 连接；后续结算与钱包测试使用本机隔离数据库。

### 完整历史结算、失锁恢复与独立对账

在新建的 `rebate_ch_full_20261011` 上调用生产 Python CLI，Redis 使用独立 DB9。回放 **2025-09-19 至 2026-10-07，共 384 天**，时区 `Asia/Shanghai`、固定返利率 5%、`QuotaPerUnit=500000`；历史活动起点与比例均为测试参数，不作为上线配置。没有复用 MySQL 留存日志测试产生的余额或水位。

- 全部 14,262 名受邀用户纳入计算：6,384 人有有效活动并建立水位，其余 7,878 人没有自身活动且未建水位；5,736 人贡献正返利基数。
- 全部 2,471 名邀请人纳入资金核对：1,722 人关联有效活动，749 人无有效活动；**1,597 人实际获付**。
- 实际返利合计 **34,368,568,162 quota**，产生 **62,934 条 credits**：56,848 条正金额入账、5,912 条没有新基数、49 条取整为零、125 条因邀请人软删除作废。软删除作废涉及 3 名邀请人，作废基数合计 402,555,071 quota。正金额入账对应 56,848 条管理日志。
- 四次只读预演均保持资金和状态哈希不变：2025-09-19 为 15,414,244 quota，2025-09-21 为 0，2026-09-05 为 82,083,860，末日中断后的剩余部分为 539,047。
- 10 月 7 日两名邀请人已提交 **343,102 quota** 后，包装器替换真实 Redis 锁所有者。首次 CLI 按预期退出 1，日状态保留 `running`，未删除接管者 token；剩余 dry-run 为 **539,047 quota**。显式恢复退出 0，当日总额 **882,149 quota** 与独立预期相同。
- 全部 384 天再次执行 CLI 退出 0，全部资金、返利表、相关配置和新增日志的哈希不变。结算没有改变任何用户钱包或已用额度，推广余额与累计收益增量逐用户匹配 credits。12 个 Python 源码／语言／依赖文件及全部冻结输入报告哈希在执行前后一致。回放与故障恢复流程耗时 **827.48 秒**，不含前置导入与预检。

独立 oracle 不导入生产结算器，从原 MySQL 用户／充值快照以及**另行直接从 ClickHouse 导出的逐日聚合**重算预期，没有使用导入 MySQL 的消费日志作为其输入。最终读取实际库逐项比较 **57,140 个用户余额、6,384 条水位、62,934 条 credits、384 个日状态、56,848 条新增日志，全部一致**；7,878 名无自身活动受邀用户未产生水位。日志按原始最大 ID 之后的全部记录验收，不先按预期类型过滤。报告 `oracle/full-independent-comparison.json` 为 **PASS**。

### 全量钱包测试发现的历史字段副作用

独立对账通过且结算进程退出后，首次复制全部用户与 credits 至 `rebate_ch_wallet_20261011`。从当前 Go 工作树重新构建验证程序，开启批量开关，使用独立 Redis DB8，对全部 2,471 名邀请人执行首次划转及同金额重试，实际调用 `User.TransferAffQuotaToQuota` **4,942 次**。

首次测试资金守恒通过，但额外的非钱包字段逐列核对失败，不能据资金通过将整轮标记成功。只读定位发现以下历史 NULL 被重写，且全部发生在成功划转的用户行中：

| 非钱包字段 | 受影响行数 | 错误变化 |
|---|---:|---|
| `remark` | 9 | NULL → 空字符串 |
| `stripe_customer` | 22 | NULL → 空字符串 |
| `discord_id` | 91 | NULL → 空字符串 |
| `created_at` | 834 | NULL → 0 |

这些集合可能重叠，不相加为用户数。原有 31 列 schema 未变化，未成功划转的用户没有字段差异；根因是锁定读取把 NULL 装入 Go 零值后，`tx.Save(user)` 将整行回写。侧库兼容检查仅新增 `access_token_created_at`，不是上述差异的来源。57,140 个用户资金、`used_quota` 和 `aff_history` 仍完全符合预期。首次 runner 退出 1，失败库及报告保留在 `wallet/first-attempt-frozen/` 与 `wallet/first-attempt-column-differences.json`，后续修复验证使用新的隔离侧库。

修复将整行 `Save` 改为仅包含 `quota`、`aff_quota` 的 map 更新，完整保留行锁、锁内重读、额度校验、事务及提交后的缓存同步。map 更新保留明确的零余额。既有 `model/rebate_mysql_test.go` 新增 `TestRebateMySQLTransferPreservesHistoricalNullFields`，在独占 MySQL 8.0.46 库 `rebate_transfer_null_20261011` 中设置四个历史 NULL，逐列核对其余整行不变，并检查钱包、推广余额归零及累计收益；测试清理恢复四列原始值。

实际 RED 精确失败于四个字段，GREEN 通过。携带真实 MySQL／Redis 配置的完整 `go test -count=1 ./model` 通过；`go test -count=1 ./controller ./service` 也通过。后者首次在沙箱中因本地测试监听限制失败，允许本机测试后完整重跑通过。独立审查确认无阻断发现。该增量修复只涉及 Go 钱包写入，没有改动已通过完整历史回放的 Python 结算器。

### 修复后的全部邀请人钱包复验

从已通过独立对账的完整历史结算库重新复制 57,140 名用户及 62,934 条 credits，创建新侧库 **`rebate_ch_wallet_fixed_20261011`**。重新编译最终工作树，构建前后核对 287 个实际源码／嵌入输入，确认调用当前实现；原失败侧库保留。启用批量开关，Redis DB8 启动前为空，覆盖 1,235 个热缓存和 1,236 个冷缓存。

再次对全部 **2,471 名邀请人**执行首次划转与同金额重试，共 **4,942 次真实方法调用**，结果如下：

| 首次划转结果 | 人数 |
|---|---:|
| 成功 | 1,126 |
| 低于 500,000 quota 最低金额 | 1,168 |
| 没有正推广余额 | 173 |
| 软删除 | 4 |

- 2,471 次重复请求全部拒绝，未产生额外资金变动。实际转入 **34,289,445,256 quota**；初始推广余额 **34,368,580,549 quota**（包含备份原有余额），剩余 **79,135,293 quota**，完全守恒。
- 全部 57,140 名用户的预期钱包／推广余额、`used_quota`、`aff_history` 逐项一致；原有全部非钱包字段零差异，四类历史 NULL 完整保留。最终 `violations` 为空。
- 原始 `new-api`、上一轮 MySQL 留存数据结算库、本轮完整历史结算库及首次失败钱包库，前后财务哈希完全相同。钱包测试只写新的侧库。
- 测试侧库仅补入当前模型需要的 `access_token_created_at`；第二次兼容检查新增列为 0，schema 哈希相同，四个额度字段保持 BIGINT。这不是完整启动迁移测试；此前的启动迁移证据仍见第三轮报告。
- Go 验证程序及外围验收 runner 均退出 **0**；`wallet/wallet-reconciliation.json`、`wallet/wallet-clone.json` 均为 **PASS**，独立 Redis DB8 结束时为空。

### 最终证据、命令与结论边界

原始恢复库最终只读核验：57,140 名用户五项财务字段 SHA256 与基线相同，29 个其它字段无差异；112,172 笔在线订单、34,967 个兑换码校验和、129 项配置及五表 schema/index 一致。装载时临时调整的 MySQL 参数已恢复，旧证据及冻结报告哈希均未改变。该最后检查不重扫日志内容，非财务字段与 schema/index 的历史基线采用此前已验收的保留副本，因此报告明确标为 `PASS_WITH_EXPLICIT_SCOPE_LIMITS`；原始日志逐 ID／字节及独立聚合验收证据仍由本节前面的来源审计提供。

以下命令实际执行，脚本与聚合报告位于 `/private/tmp/rebate-clickhouse-20261011/`：

```text
PYTHONDONTWRITEBYTECODE=1 /Users/hzy/PycharmProjects/ikun_rebate_v2/.venv/bin/python /private/tmp/rebate-clickhouse-20261011/cli/replay_cli.py --execute
/Users/hzy/PycharmProjects/ikun_rebate_v2/.venv/bin/python -B /private/tmp/rebate-clickhouse-20261011/oracle/independent_ch_oracle.py verify
go test -count=1 ./model -run '^TestRebateMySQLTransferPreservesHistoricalNullFields$' -v
go test -count=1 ./model
go test -count=1 ./controller ./service
go build -o /private/tmp/rebate-clickhouse-20261011/wallet/wallet-harness /private/tmp/rebate-clickhouse-20261011/wallet/wallet_harness.go
/Users/hzy/PycharmProjects/ikun_rebate_v2/.venv/bin/python /private/tmp/rebate-clickhouse-20261011/wallet/run_wallet.py --settler-stopped
/Users/hzy/PycharmProjects/ikun_rebate_v2/.venv/bin/python -u /private/tmp/rebate-clickhouse-20261011/check_final_source.py
python3 /private/tmp/rebate-clickhouse-20261011/check_final_acceptance.py
```

MySQL 回归由 `wallet-fixed/run_mysql_tests.py` 注入独占测试库及 Redis 配置，凭据未写入本文；controller/service 使用自身测试夹具。故障注入 CLI 与修复前 RED 的退出 1 是预期结果；首次钱包外围验收退出 1 是真实发现，已修复并在新侧库重新通过，未删除或改写首次失败证据。

最终聚合验收 `final-acceptance.json` 为 **PASS**，同时复核源文件、最终构建、回归日志与全部关键报告 SHA256。详细证据包括 `cli/cli-report.json`、`oracle/full-independent-comparison.json`、`wallet/build-provenance.json`、`wallet/wallet-reconciliation.json`、`wallet/wallet-clone.json`、`wallet-fixed/fix-verification.json` 和 `final-source-check.json`。逐用户财务与原始日志仍仅保存在本机受限临时目录及隔离测试库中。

最终修改文件 `gofmt -l` 无输出，Go／Python 两个工作树的 `git diff --check` 均通过。`wallet/final-execution-summary.json` 记录结算与钱包写入已经停止；额外只读进程检查 `final-process-check.json` 为 PASS，本次导入、结算、钱包与回归执行进程均已退出。测试数据库保留以便复核。

**本轮完成完整历史日志补全后的全量邀请测试，修复了提现整行回写历史字段的副作用；最终验证范围内未发现剩余阻断问题。** 按用户要求，数据库结论限于 **MySQL 8.0.46**，不宣称其它引擎或版本已验证。10 月 7 日仅覆盖备份截至 00:21:02 的部分日。ClickHouse 在本轮作为只读历史来源，日志导入隔离 MySQL 后由现有生产结算器回放；没有新增线上直接查询 ClickHouse 的结算功能。

上线仍须遵守第三轮的配置与切换要求：主库持续记录消费日志、暂不纳入订阅、明确真实活动起点／时区／返利率；确认旧节点钱包批次落库并结束在途任务后统一切换，新旧钱包节点不能混跑。所有修改尚未提交、未部署。

## 2026-10-11 ID 1247 邀请人黑名单专项复验

按追加要求，将 `user_id=1247, role=inviter` 作为测试排除项。该真实账户状态正常、未软删除，没有上级邀请人，直接受邀用户共 **2,489 人**。验证其自身不获得邀请返利，名下用户结算水位继续推进，移出名单后不补发被作废份额；已取得收益不追回。本次配置变化限于新建测试库，原始库及前轮完整历史结算、钱包库不改动。

### 完整历史范围与独立预期

1247 名下所有直接受邀用户共有 **28,356,613 条**原始消费／退款日志；另选一个完整正常邀请组作为对照，包含 1 名受邀用户及 47,022 条日志。合计 **2,490 名受邀用户、28,403,635 条原始日志、35,381 个日期／用户／类型汇总**，没有按日期或用户抽取 1247 邀请组的数据。

新建 `rebate_blacklist_1247_a_20261011` 与 `rebate_blacklist_1247_b_20261011`，均保留原始 57,140 名用户的关系与初始财务字段，仅复制这两个邀请组对应的全部充值来源和原始日志。相关在线订单保留全部 6,026 行（不只复制成功行），相关兑换码保留 1,678 行，其中 322 行已软删除；来源过滤仍由生产结算器执行。全部原有 129 项配置复制后，仅在测试库设置活动参数。日志来自已验收的 ClickHouse 补全库，不用汇总记录替代原始行；按月复制，并逐 ID 核对全部原始列及独立 ClickHouse 日／用户／类型汇总。临时复制不改变 MySQL 全局参数。

两种场景均使用生产 CLI 回放 **2025-09-19 至 2026-10-07，共 384 天**，`Asia/Shanghai`、5% 测试返利率、`QuotaPerUnit=500000`。独立预期直接复核原始充值与 ClickHouse 日汇总，并与前轮已验收的完整历史 oracle 交叉核对，不导入生产结算算法。

- **A：全期保持邀请人黑名单。** 正常情况下 1247 有 353 条 credits，其中 347 天正返利合计 **11,816,169,032 quota**；本场景这些正返利应全部以 `excluded` 作废，新增收益为 0。另 6 个无新基数的 credit 日不强制要求 `excluded`。
- **B：正常 → 加黑 → 移除。** 10 月 20 日先正常取得 **216,125 quota**；10 月 21 日加入名单，应作废 **343,483 quota**且保留既得收益；移除名单后重跑已完成日期不能补发，10 月 22 日仅取得当日新增的 **1,426,053 quota**。最终 1247 应得到 **11,815,825,549 quota**，与无黑名单对照仅差被排除一天的 343,483。
- 正常对照邀请组两场景均应获 **10,124,927 quota**，credits 与日志保持原预期。两个场景最终均有 1,127 条水位（目标组 1,126，对照组 1）和 590 条 credits；全部 57,140 用户余额纳入核对。1247 实际没有上级，因此不把这一数据集描述为验证了其自身兼任受邀人的情形。

### 黑名单管理接口

使用本机 MySQL 8.0.46，实际执行 `go test -count=1 ./controller -run '^TestRebateExclusion' -v`，四个测试函数及四个备注长度子用例全部通过。覆盖新增／列表／删除、同用户同角色重复添加拒绝、两种角色分别保存、非法 ID／角色／JSON、中文及 ASCII 备注长度、操作审计。每个测试由既有夹具创建独立 `newapi_audit_*` 库，未连接线上。

证据目录为 `/private/tmp/rebate-blacklist-1247-20261011/`，包括 `controller-checks.json`、`controller-checks.log`、冻结 `private-scope.json`、`independent-expectations.json`、`checkpoint-private.json` 与 `oracle-gate.json`。个人明细仅留在本机受限文件中。

### 实际结算与最终独立核验

A/B 两库原始输入全部验收 **PASS**，每库 14 个月、28,403,635 条日志的完整 20 列逐 ID 比较一致，35,381 个独立日汇总桶一致，警告全部为 0。全部用户原始财务字段与关系一致，结算前四张 Python 表均不存在。两库依次使用生产 CLI 和独立 Redis DB7；没有修改生产源码、替换计算算法、修改业务时钟或复用前轮已发放结果。

| 实际结果 | A：全期黑名单 | B：加入后移出 |
|---|---:|---:|
| 完成结算日 | 384 | 384 |
| 1247 新增返利（quota） | **0** | **11,815,825,549** |
| 1247 的 `excluded` 记录 | **347** | **1** |
| 1247 全部 credits | 353 | 353 |
| 正常对照组返利（quota） | 10,124,927 | 10,124,927 |
| 两组全部 credits／水位 | 590／1,127 | 590／1,127 |
| 新增付款日志 | 149 | 495 |

B 的实际边界与独立预期一致：10 月 20 日正常获得 216,125 quota；10 月 21 日加黑后累计仍为 216,125，当天的 343,483 被作废；移出后重跑已完成前缀，资金与状态哈希不变；10 月 22 日只发新增 1,426,053，没有补发作废金额。新增或移除排除项本身也没有改变既得收益或已有结算状态。

全部 **21 次 CLI 调用退出 0**。其中 6 次单日 dry-run 均未写入，7 次已完成前缀／完整区间重跑均保持状态哈希不变。A/B 回放分别耗时 255.57／260.14 秒，不包含原始数据复制。两库全部 57,140 用户资金增量、非推广额度字段、原始充值表、日志聚合及 12 个生产源码／语言／依赖文件检查通过；结束后 DB7 为空。

执行进程退出并确认停止写入后，另一个独立只读进程重新读取数据库，逐行核对 **每库 57,140 个用户余额、1,127 条水位、590 条 credits、384 个日状态，以及原始最大 ID 之后的全部新增日志**，全部一致。两场景的水位内容及正常对照 credits 完全相同，证明黑名单只作废该邀请人的返利、继续推进消费水位，并且恢复后不补发。`independent-final-comparison.json` 为 **PASS**。

原始 `new-api`、前轮 MySQL 留存数据结算库、完整 ClickHouse 历史结算库及首次失败钱包库的财务 SHA256 均与此前冻结值一致。前轮最终修复钱包库的 57,140 行也与独立重建预期一致，转入总额与成功人数符合旧报告；该库此前未保存单独的最终财务 SHA，因此不将此项描述为与旧哈希比较。所有验证均为只读，未改动线上配置。

实际执行命令（虚拟环境 Python 位于 `/Users/hzy/PycharmProjects/ikun_rebate_v2/.venv/bin/python`）：

```text
python clone_blacklist_inputs.py --scenario a
python clone_blacklist_inputs.py --scenario b
PYTHONDONTWRITEBYTECODE=1 python replay_blacklist_cli.py --execute
python verify_final_readonly.py
```

上述脚本均位于 `/private/tmp/rebate-blacklist-1247-20261011/`。最终证据为 `clone-a-report.json`、`clone-b-report.json`、`a/cli-report.json`、`b/cli-report.json`、`independent-final-comparison.json`。最终独立报告 SHA256：**5fcd12938e96aeef712130b75bf55e63f2b3ec43e34ca7e1a9dc42a0766f919c**。

**结论：ID 1247 配置为 `role=inviter` 黑名单后不获得新增邀请返利，水位正常推进，移出后不补发，既得收益保留，正常对照不受影响。** 本轮未发现新业务 bug，未新增业务代码修改。结论限于上述 MySQL 8.0.46 与备份历史范围；10 月 7 日仍只覆盖备份中的部分日。黑名单仅在测试库配置，线上及原始恢复库配置保持原样。

## 2026-10-21 ClickHouse 全量同步 MySQL 后的实网复验

本轮在 ClickHouse 所在本机执行。Python 结算器 `ikun_rebate_v2` 不在本机，本轮未运行结算器（2026-10-24 补跑，见下一节）；"结算"由两套与其无关的独立推演按规范计算：Python 逐日回放（输入取自 MySQL）与 ClickHouse 闭式 SQL。数据库仅 **MySQL 8.2.0**（与生产 dump 头的 Server version 一致），未验证 PostgreSQL/SQLite 兼容性矩阵。

### 环境与数据同步

- 业务表来自 `ik_______2026-10-17_00-21-00.sql.gz`，恢复到独立容器（仅绑定 127.0.0.1）的 `newapi_src`：用户 57,706、在线订单 112,172、兑换码 38,786、配置 129 项，与 ClickHouse 同批快照行数一致。
- 日志从 ClickHouse 25.8.33.6 `archive.logs` 逐日同步：全部 14,343 名受邀用户的 type=2 共 66,230,935 条、type=6 共 2,494 条（2025-09-19 01:22:03 至 2026-10-17 00:21:02），另含全站 type=1/3/4/7 共 294,493 条。字符串以 HEX 传输；每天核对条数、`sum(quota)`、`sum(length(other))`、`sum(length(content))` 后才继续，全部一致。二级索引按生产 DDL 重建后逐项比对一致。
- 网关跑在容器中，只发布到 127.0.0.1；真实渠道全部禁用，SMTP、OAuth、Turnstile、Worker 凭据清空，上游与 OAuth 身份源均为本机模拟。生产库表结构上首次启动迁移与二次启动的列/索引快照完全相同。除修复版新增的 `retired_aff_codes` 表外，修复版与基线版已有表的列/索引快照相同；该新表在两次启动下的快照也一致。

### 历史充值与邀请核查

- 99,433 笔成功在线订单与 99,433 条充值日志逐笔对上，到账额度与支付金额 100% 一致。其中 3,188 笔有折扣（合计 70,857 单位）；历史订单没有实付快照，结算侧会按到账额度计。另有 43 笔按 `create_time` 落日会比实际支付早一天。
- 34,847 个已用兑换码与 34,847 条兑换日志在用户、额度、时间（≤5 秒）、日期上全部一致，含 4,160 个已软删除的已用码。
- 56 期用户快照中，没有任何用户的 `inviter_id` 发生过变化，也没有硬删除用户。
- **发现：OAuth 注册不写 `inviter_id`。** 邀请奖励照发、`aff_count` 照加，但受邀人不入返利。2026-05-27 起可按"邀请用户赠送"日志与其前 3 秒至后 1 秒内创建、唯一未绑定的 LinuxDO 账号精确匹配出 163 人；按 10% 计，至 10-17 共少发 289,196,223 quota。按 `aff_count` 与实际绑定数的差额估算，全史至少 594 人、涉及 185 名邀请人。数据修补 SQL 只生成、未执行；2026-10-24 重新生成，并在副本上用结算器复验，见下一节。

### 逐日推演（2025-09-19 至 2026-10-17，共 394 天，10%，QuotaPerUnit=500000）

- MySQL 与 ClickHouse 两侧的逐日输入完全一致：充值 26,966 组、R 769,390,400,000；消费 154,351 组、C 1,200,505,148,508；订阅计费日志 0 条。
- Python 回放与 ClickHouse 闭式结果逐行一致，共 64,494 条入账记录：返利合计 70,315,562,230 quota，其中正额 58,244 条、取整为 0 的 15 条、无新增基数的 6,108 条；3 名软删除邀请人作废 127 条（基数 402,555,071）。水位 6,411 条，获付邀请人 1,607 名。若历史订单按实付金额计，合计为 69,563,557,819。
- 把回放金额加到隔离副本 `aff_quota`/`aff_history` 后，2,481 名邀请人全部通过 HTTP 登录，每人对全额发起 3 个并发划转并重试一次：
  - 1,256 人恰好成功一次，1,049 人低于最低额被拒，172 人无余额，4 名软删除邀请人无法登录。
  - 57,707 行用户的钱包、推广余额、累计收益增量全部符合预期，其它列零差异。
  - 总额守恒：划转 70,249,296,124，剩余 66,278,654。

### 实网 HTTP 用例与修复

用例覆盖：
- 密码注册与 OAuth 注册带邀请码；
- 兑换码兑换（含并发）；
- 易支付下单、实付快照、伪造签名、重复与并发回调、折扣档位；
- 管理员充值标记及其非法值；
- 排除名单权限与校验；
- 返利开关与比例写入；
- 模拟上游的真实消费与失败退款；
- 按规范结算后划转（最低额、余额不足、钱包上限、并发、历史 NULL 字段、用户 1247）；
- Creem 补单。

结果：基线版 66 通过、16 失败。最终版扩展到 118 项，全部通过；在最终构建、全新的生产副本上重跑过。扩展项包括：
- 低于订单金额的易支付回调；
- 划转日志与错误信息；
- 硬删除后邀请码退役；
- 签到钱包上限；
- 禁用邀请人；
- 渠道测试日志记 0；
- 管理员补单的实收金额（按 实收 ÷ 标价 下调快照、写入充值日志、已完成订单重复补单不再写日志）；
- 实时语音（本机 WebSocket 模拟上游，两轮其中一轮缺 `total_tokens`）：倍率计价、按次计价、分层表达式计价的会话各写一条消费日志，钱包与令牌按日志金额只扣一次，用户挂断后上游连接被关闭；分层表达式计价的会话在钱包付不起某一轮时，在该轮 `response.done` 处中止会话，该轮已转发的内容由最终结算计一次（钱包可能透支一轮，属于“交付后扣费”）；本用例的模拟上游只发 `response.done`、不发 delta。

之后用 10 轮多智能体对抗审查（每轮最多 3 次查找，每条发现三票核实）复核修复，确认问题一并修复。第 9 轮仍找出并修复了实时会话结算后读协程继续转发缓冲轮次的问题；第 10 轮在实时计费、资金路径、邀请、前端、测试与文档五个维度上连续两次查找均无任何发现，审查收敛。本轮修复：
1. 注册：
   - `InsertWithTx`/`Insert` 写入 `InviterId`，OAuth/OIDC/自定义 OAuth 注册的受邀人进入返利；微信注册也会带上邀请码。
   - OIDC 身份在账号软删除后仍算已占用（与其它登录方式一致），不能重新注册换邀请人。
2. 实付快照：
   - Stripe 改为 `Amount × QuotaPerUnit`。
   - Stripe 按 `amount_total/amount_subtotal` 扣除优惠码折扣，金额从类型化的 Checkout Session 读取（≥100 万最小单位也正确）。
   - Creem 按 `(amount_paid − tax_amount)/sub_total` 扣除折扣码折扣。
   - 沙箱/测试环境支付快照写 0。
   - 管理员补单可带 `paid_money`（网关实收金额，币种与订单标价一致；Stripe 的标价是 数量 × Stripe 单价，Creem 填不含税金额），服务端按 实收 ÷ 标价 只下调快照，实收写入充值日志；Stripe/Creem/Pancake 不填时快照写 0。前端不再按显示汇率换算。
3. 支付回调：
   - 管理员补单按渠道入账（Creem 不再乘以 QuotaPerUnit）。
   - 易支付回调在行锁事务内校验金额，低于下单时发给网关的金额即拒绝。
   - Stripe 回调要求会话带本站写入的订单元数据，同账户其它 Payment Link 不能冒充订单完成、过期或异步支付失败。
4. 返利设置：`IkunRebateRate` 只接受 `0.xxxx`，`IkunRebateEnabled` 只接受 `true`/`false`，写库失败不再报成功；`rebate_info` 使用同一解析，`LogConsumeEnabled` 关闭时报 `enabled=false`，未开启时不公开比例。
5. 排除名单：拒绝不存在的用户 ID；并发重复与删除未知 ID 返回已翻译的错误；令牌作用域改为 `option:read/write`。
6. 推广余额：
   - 划转写系统日志，错误信息不再暴露内部细节。
   - `GetAffCode` 只写 `aff_code`。
   - 硬删除用户的邀请码记入 `retired_aff_codes`，`aff_code` 为 NULL 的老账号同样能硬删除。
   - 禁用邀请人不再累计邀请奖励。
7. 钱包与结算：
   - 图片重新预留后，退款退回全部预扣。
   - 结算写库失败时，消费日志只记录实际扣到的额度，差额进 `admin_info.unsettled_quota`；没有计费会话的旧路径同样处理。
   - 已退款的会话不再结算；用户不存在时扣款不再静默成功。
   - 关机时等待异步退款完成（原子计数）。
   - 签到受钱包上限约束。
   - 订阅完成不会把同号的钱包订单改成成功。
8. 实时音频（WSS）与音频：
   - 按轮扣费后结束时不再整段重复结算（原先多扣一倍）。
   - 某轮扣费失败时，该轮不再被重复计入。
   - 结算前先关闭上游、等待两个读取协程退出；结算后仍在运行的读协程（例如卡在向不读取的客户端转发）读到缓冲中的后续轮次时，既不扣费也不转发，直接退出；用户挂断时上游连接随之关闭。
   - 钱包已扣、后续步骤失败的那一轮也计入已扣额度。
   - 低余额提醒按整段会话计算。
   - 按次计价的模型不按轮扣费，由最终结算计一次（原先按次计价的实时与音频请求结算为 0）。
   - 分层表达式计价的模型每轮扣“会话累计用量的表达式费用 − 已扣额”并照常检查余额（原先按兜底倍率 37.5 逐轮先扣）；音频、图片、缓存 token 进入表达式的 `ai`/`ao`/`img`/`cr`，不再按文本价计；表达式同时引用 `cr` 与 `ai` 时，已缓存的音频只按 `cr` 计一次（实时上游的 `audio_tokens` 包含缓存音频）；会话累计时保留图片与分模态缓存明细。
   - 订阅资金的会话不再因钱包余额不足被中断。
   - `response.done` 缺 `total_tokens` 时按输入加输出计；缺 `response` 对象时不再触发空指针。
9. 渠道测试：消费日志 quota 记 0（不扣任何人），估算写入 `admin_info.channel_test_quota`，管理员日志详情中显示估算与未扣金额。
10. 前端：
    - 额度弹窗防回车重复提交（会重复记充值）。
    - 排除名单增删失败时提示，添加与移除都防重复点击；列表加载失败显示服务端原因与重试，不再显示为空；改用项目的 Select，输入控件有可访问名称。
    - 比例范围校验提示已翻译。
    - 额度弹窗的实付金额与补单弹窗的实收金额改为文本输入：浏览器无法解析的内容（如 `$50`）不再被当作空值按全额提交，而是显示错误并拒绝；全角数字与中文句号按半角读取；逗号后至多两位数字时视为小数点（部分小数键盘只有逗号），`1,000` 这类可能是千分位的写法拒绝；正在输入的 `40.`、`40,` 不报错。
    - 补单确认改用共享的 ConfirmDialog；日志详情中结算差额为负时显示为“未退还金额”。
    - 补齐 7 种语言文案（含俄语“未扣金额”的更正），新增服务端日志行的中文。

### 命令与结果

- `go test ./model ./controller ./service ./middleware ./router ./relay/... ./common ./i18n -count=1`（SQLite 默认夹具）：全部通过。`relay/channel/openai` 的 `TestImageExpressionUsesCompletedCountAndProtectsAbortedStreams` 耗时 30 秒，在未修改的 HEAD 上同样如此。
- `go test -race ./relay/channel/openai -run TestRealtime -count=3` 与 `go test -race ./service -run 'TestRealtime|TestRefunded|TestWalletRefund|TestConsumeLog|TestPerRequestPricedAudio' -count=2`：通过。
- MySQL 8.2.0（测试容器）：`REBATE_MYSQL_TEST_DSN=…/rebate_gotest TEST_MYSQL_DSN=…/mysql TEST_MANAGE_USER_DIALECT=mysql TEST_SECURITY_DIALECT=mysql TEST_FIXED_MYSQL_DSN=…/rebate_fixed REBATE_TEST_REDIS_URL=redis://…/13 REBATE_REDIS_TEST_URL=redis://…/14 go test -p 1 ./model ./controller ./service -count=1`：三个包通过。`rebate_gotest` 由最终版网关在全新生产副本上迁移生成；首次运行时 `TestRebateMySQLControllerContracts` 失败，原因是新副本没有结算器初始化写入的 `IkunRebateEnabled`/`IkunRebateRate` 两行，按结算器初始化补上后通过。最终代码在同一环境重跑三个包仍全部通过（`TestWeChatSignUpBindsInviter`、`TestChannelTestLogsZeroQuotaAndKeepsTheEstimate` 也在 MySQL 夹具上运行）。两次运行夹具产生的 `newapi_audit_*` 临时库（1,176 与 591 个）已删除。
- 新增回归在修复前失败、修复后通过（实时、补单、音频、微信等用例均用回退修复的变异版本复核过）：
  - 注册与邀请码：`TestInsertStoresInviterForEveryRegistrationPath`、`TestWeChatSignUpBindsInviter`、`TestInvitationBonusSkipsDisabledInviters`、`TestEnsureAffCodeWritesOnlyTheCode`（封禁落在读写之间、并发请求已先写入邀请码）、`TestHardDeletedAffCodeIsNeverReissued`（含 NULL 邀请码）、`TestDeletedAccountKeepsItsOidcIdentity`
  - 充值与快照：`TestManualCompleteTopUpCreditsLikeGatewayCallback`（各渠道到账、按实收比例下调、无标价拒绝、充值日志）、`TestGatewayChargeAdjustsPaidSnapshot`、`TestSubscriptionCompletionLeavesWalletTopUpAlone`、`TestIkunRebateOptionsRejectValuesTheSettlerRefuses`、`TestTopUpPaidSnapshotMatchesGatewayAmount` 的沙箱行
  - 支付回调：`TestEpayNotifyRejectsUnderpayment`、`TestStripeGatewayChargeReadsLargeAmounts`、`TestStripeFulfillmentRequiresTheSessionCreatedForTheOrder`（含异步支付失败；优惠码与测试模式经完成事件进入实付快照）、`TestAdminCompleteTopUpValidatesPaidMoney`
  - 钱包与结算：`TestWalletRefundReturnsEveryImageReservation`、`TestConsumeLogRecordsOnlyTheCommittedCharge`（文本、音频、实时三条路径；钱包已扣、仅令牌更新失败时记全额，计费会话与无会话的旧路径各一行）、`TestRefundedSessionIsNotSettledAndShutdownWaitsForRefund`、`TestPerRequestPricedAudioIsBilledItsPrice`、`TestCheckinRespectsTheWalletCap` 与 `TestRebateMySQLCheckinRespectsTheWalletCap`（MySQL 上零奖励不报错）、`TestTransferAffQuotaUpdatesCachedWalletQuota` 中的划转日志断言
  - 实时音频：`TestRealtimeSessionIsBilledOnce`（倍率、按次、分层表达式、含音频输入、跨轮长上下文、缓存音频与音频输出、不含 `cr` 时缓存音频按音频计、图片输入、余额不足、用量只在明细中（会话总量为 0 仍保留按轮已扣）、订阅、令牌扣减失败十二行）、`TestRealtimeSessionUsageCountsEveryTurnOnce`（按轮扣费失败、缺 `total_tokens`、先 delta 后带用量的 `response.done`、跨轮保留图片与缓存明细、未收到 `response.done` 的已交付输出、不带用量的 `response.done`）、`TestRealtimeUserHangUpClosesUpstream`、`TestRealtimeHandlerWaitsForAnInFlightTurnCharge`（读协程等待期内与超出等待上限两种情况）、`TestRealtimeSettledSessionNeitherChargesNorDeliversLaterTurns`
  - 其它：`TestChannelTestLogsZeroQuotaAndKeepsTheEstimate`、`TestUpdateOptionReportsAFailedWrite`、`TestRebateExclusion*` 与 `TestGetRebateInfo` 的新增行
  - 前端：`user-quota-dialog` 重复提交与无法解析的实付金额、`rebate-recharge.test.ts`、`rebate-exclusions.test.tsx`（含加载失败与重试）、`rebate-rate.test.tsx`、`billing-history-complete.test.tsx`、`settlement-shortfall.test.tsx`、`wechat-login.test.ts`
- 通过的检查：
  - `go vet`
  - `gofmt -l`（仅本次未改动的 `service/privacy/privacy.go` 有输出）
  - `bun run typecheck`、修改文件的 `oxlint` 与 `oxfmt --check`、`bun run build`
  - `bunx vitest run src/features/wallet src/features/system-settings src/features/usage-logs src/features/users src/features/auth src/lib`（84 个文件、922 项通过）
- 实网 HTTP 用例 118 项在最终构建、全新的生产副本上全部通过；该副本上首次与二次启动的列/索引快照一致，与上一版构建的快照也一致。
- 全体邀请人 HTTP 划转在此前的修复版上复跑通过，并核对每次成功划转恰好一条划转日志。

未覆盖与遗留：
- Python 结算器 `ikun_rebate_v2` 本轮不在本机，其实现本身未运行（2026-10-24 已补跑，见下一节）。本机 `/Users/hzy/PycharmProjects/ikun_rebate` 是已被取代的 v3.2 设计（直接加 `users.quota`、本地 SQLite 台账、不读实付快照与管理员充值），与本规范口径不同，没有拿来代替。
- 未调用真实支付、Stripe 或上游供应商；实时语音只用本机模拟上游验证。
- 实时音频与 Midjourney 仍是"已交付后扣费"，并发会话最多透支一次请求的费用；重复结算问题已修复。
- 部署前已创建、部署后才支付的 Stripe 会话不带订单元数据，不会自动入账，需要管理员补单。
- 补单的 Stripe 标价使用补单时的 Stripe 单价；单价在下单与补单之间被修改时，按实收比例算出的快照会有偏差。
- 本轮修改已提交（`0c57521ea`），未部署。新增表 `retired_aff_codes` 的迁移只在 MySQL 8.2.0 上验证（新库、生产库升级、二次启动幂等），未覆盖 SQLite/PostgreSQL 矩阵。

## 2026-10-24 结算器 v2 全量复验与 OAuth 修补验证

本轮补上 2026-10-21 未覆盖的结算器本身：在同一本机、同一 MySQL 8.2.0 测试容器中运行 Python 结算器 `ikun_rebate_v2`（`9a7b4af`，从仓库克隆后未改动）。同时重新生成 OAuth 受邀关系修补 SQL，并在副本上验证。只验证 MySQL，未运行 PostgreSQL。

### 结算器环境与自带测试

- 依赖按 `uv.lock` 的版本安装（SQLAlchemy 2.1.3、PyMySQL 1.2.3、redis 8.1.0、pytest 9.1.1）。只有 `cryptography` 例外：锁定的 50.0.2 没有 macOS x86_64 预编译包，改用 48.0.1，它在这里只用于 MySQL 认证。
- `pytest`：不连库时 571 通过、371 跳过；连 MySQL 8.2.0 与 Redis 时 760 通过、182 跳过（跳过的都是 PostgreSQL 用例）。

### 输入数据

- 业务表 `users`、`top_ups`、`redemptions`、`options`、`subscription_plans` 复制自 `newapi_src`（2026-10-17 dump）。网关的三张返利表按最终版结构建成空表：历史订单没有实付快照，也没有管理员充值记录，排除名单为空。
- `logs` 只装结算器读取的 type=2/6：
  - 在 2026-10-21 已同步的 14,343 名受邀用户之外，从 ClickHouse 补同步了 163 名待修补受邀人的日志 120,458 条（quota 合计 2,950,109,319）。
  - 装载后 type=2 共 66,351,393 条（5,849 名用户，quota 合计 1,203,455,257,827），type=6 共 2,494 条，与 ClickHouse 逐项一致；带订阅标记的日志 0 条。
  - 为缩短装载时间，`other` 只在含订阅标记时保留，二级索引只建 `idx_created_at_type`。结算器只用 `other` 判断订阅标记，这两点不影响结果。
- 配置：`init --activity-start 2025-09-19 --timezone Asia/Shanghai`，`IkunRebateEnabled=true`、`IkunRebateRate=0.1`，`QuotaPerUnit=500000`，`REBATE_LOGS_IN_MAIN_DB=true`。

### 两次全量结算

同一份输入复制成两个库：`rebate_s_base`（dump 原样）和 `rebate_s_repaired`（执行修补 SQL 后）。两个库各运行 `python -m rebate settle --from 2025-09-19 --to 2026-10-17`：

- 两个库都是 394/394 天 `done`，退出码 0，无 WARN。
- 与 ClickHouse 闭式推演对账：
  - 口径同 2026-10-21，输入取自 ClickHouse；修补后的库按修补后的邀请关系推演。
  - 按（日，邀请人）逐行比对基数增量、返利额，以及基数增量为正的行的作废原因。
  - 两个库都没有缺行、多行或不一致。

| | 入账记录 | 基数增量合计 | 返利合计 | 正额记录 | 水位 |
|---|---|---|---|---|---|
| 修补前 | 64,494 | 703,558,435,511 | 70,315,562,230 | 58,244 | 6,411 |
| 修补后 | 64,691 | 706,450,399,295 | 70,604,758,507 | 58,438 | 6,443 |

- 修补前的结果与 2026-10-21 的 Python 回放一致（64,494 条、70,315,562,230、正额 58,244、水位 6,411）。
- 钱包与日志：
  - 每名邀请人 `aff_quota`、`aff_history` 的增量都等于其入账记录合计，其他用户这两列不变。
  - 所有用户的 `quota`、`used_quota` 与 dump 相同。
  - 返利日志（type=3）条数、`request_id` 去重数与正额记录数相等，金额合计等于返利合计。

### OAuth 受邀关系修补

- **匹配结果：** 2026-05-27 起共 3,214 条"邀请用户赠送"日志，按日志前 3 秒至后 1 秒的窗口分为：
  - 3,049 条：窗口内已有绑定的受邀人；
  - 163 条：窗口内没有已绑定的受邀人，且恰有 1 个未绑定的 OAuth 账号，纳入修补；
  - 2 条：两种账号都有，无法区分，未修补；
  - 0 条：窗口内没有账号。
- **dump 核对：**
  - 163 对涉及 49 名邀请人，全部为 LinuxDO 注册，无重复，无自邀。
  - 157 个账号与日志同秒创建，6 个早 1 秒。
  - 受邀人在 dump 中均为 `inviter_id=0`；邀请人均存在、启用、未删除，且 ID 小于受邀人。
  - 受邀人 47632 已软删除，仍在修补范围内，会使邀请人 18100 多得 80,940。
- **副本执行：**
  - 首次更新 163 行，再次执行 0 行。
  - 只有这 163 行的 `inviter_id` 变化，受邀用户由 14,343 增至 14,506。
  - 执行回滚 SQL 后，`CHECKSUM TABLE users` 与修补前一致。
- **影响：**
  - 返利合计增加 289,196,277 quota（约 578.39 单位）。只有 17 名邀请人增加、无人减少，且都是这 163 人的邀请人；163 人中有 32 人在结算期内有可返利的消费。
  - 增加最多的是 1247（涉及 43 人，+161,215,894）和 20857（涉及 1 人，+98,780,504）。
  - 上一节的 289,196,223 是只对这 163 人取整得出的，这里按邀请人当日合并后取整，两者相差 54。
- **执行时机：** 结算器在结算当日读取 `users.inviter_id`，新绑定受邀人的水位从 0 起算。
  - 在首次生产结算前执行：163 人从活动起点计入，结果即上表"修补后"一行。
  - 已结算若干天后再执行：只从下一个结算日起计入，已结算日不补发。
  - 2026-10-17 的 dump 中没有 `rebate_*` 表，说明截至该日返利尚未上线。
- **执行注意：** 不需要重启网关，也不改 `aff_count`、`aff_quota`。生产执行前应先确认这 163 行仍为 `inviter_id=0`；SQL 的条件也只会更新仍未绑定的行。
- **文件：** 修补 SQL、回滚 SQL 与匹配明细保存在仓库外的本机备份目录，未在生产执行：
  - `oauth-lost-invitees-repair.sql`，sha256 `839807e8a843bb18ed570c853902704379395990a8663a47945e6da68b2aae45`
  - `oauth-lost-invitees-rollback.sql`，sha256 `9bd757c2aff5429b38a2147bb7b5d02d7056ca80bea5ca0a65aa62c84639f175`
  - `oauth-lost-invitees-candidates.tsv`

未覆盖与遗留：
- 只验证 MySQL 8.2.0。结算器的 PostgreSQL 用例与网关的 PostgreSQL/SQLite 矩阵未运行。
- 2026-05-27 之前丢失的受邀关系（按 `aff_count` 差额至少 594 人）无法匹配，未修补。
- 测试容器与 `rebate_s_base`、`rebate_s_repaired` 等隔离库仍在本机。`/private/tmp` 下的推演脚本与逐行比对明细已被系统清理，复核时需从隔离库与 ClickHouse 重新生成。
