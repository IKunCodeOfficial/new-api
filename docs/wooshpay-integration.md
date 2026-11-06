# WooshPay 充值接入开发文档

> 适用分支：`ikun-new`
> 范围：钱包在线充值（一次性支付），只收人民币（CNY）。订阅支付不在第一期范围内，见 §11。
> 参考实现：WooshPay 的**接口形态**（Checkout Session、PaymentIntent、Webhook 签名）与 Stripe 高度一致，HTTP 调用和回调链路参照 `controller/topup_stripe.go`；**定价和入账语义**则与易支付、Waffo Pancake 一致（本站定价，分组倍率只放大应付金额，入账 = 购买数量 × `QuotaPerUnit`，见 §5.2）。

---

## 1. 方案结论

采用 **WooshPay Checkout（托管收银台）+ Webhook 异步通知 + 入账前 API 回查**。

| 方式 | 选择 | 原因 |
|---|---|---|
| Checkout 托管页 | ✅ | 官方推荐；卡数据不经过我方，无 PCI 合规负担；改动最小 |
| JS SDK 内嵌 | ❌ | 前端集成成本更高，收益有限 |
| Direct API | ❌ | 直接接触卡数据，需要 PCI DSS 合规 |

三条可靠性原则：

1. **只信服务端事实**：`success_url` 跳转只用于展示，入账只由 Webhook 触发。
2. **验签 + 回查双保险**：Webhook 验签通过后，再用我方密钥调 `GET /v1/payment_intents/{id}` 回查，校验状态、订单号、金额全部一致且币种为 CNY 才入账。
3. **幂等入账**：复用 `LockOrder` 进程锁和 `lockForUpdate` 行锁；已经 `success` 的订单不再入账，重复通知直接返回 200。回查确认已支付的订单即使已被置为 `expired`/`failed` 也照常入账（钱确实到了）。

> **单节点与多节点**：`LockOrder` 是进程内的锁，只在同一个进程里互斥；多节点部署时，不同节点上的 Webhook、关单和管理员补单不会被它串行化。设置项（密钥、API Base、开关、单价、`ServerAddress` 等）由各节点每 `SYNC_FREQUENCY` 秒（默认 60 秒）从数据库重新加载一次，保存后其他节点最多要等这么久才生效，期间各节点可能按新旧不同的设置下单、回查。跨节点真正生效的只有入账时的数据库行锁：`RechargeWooshpay` 在事务里用 `lockForUpdate` 锁住订单行，再检查状态、金额并入账，所以同一订单在多个节点上也只入账一次（SQLite 不支持 `FOR UPDATE`，靠它的单写者锁）。

> **币种（必须）**：本站只按人民币收款，币种写死为 `CNY`，没有币种配置项。WooshPay 商户账号必须按报价单 / 入网（onboarding）结果开通 CNY 收款，否则创建收银台会失败。

> **测试模式会充入真实额度（必须知道）**：`sk_test_` 密钥下的付款不产生真实资金，但订单入账时照常给用户加**真实额度**（只是返佣快照记为 0）。所以测试密钥下只有管理员（`role >= common.RoleAdminUser`）能报价、下单，普通用户在充值页看不到 WooshPay，调用 `/amount`、`/pay` 会得到 “WooshPay is in test mode; only administrators can place test orders”。`/amount`、`/pay` 在请求开始时只读一次账号（密钥和 API Base），管理员判断和建收银台用同一份快照，不会在两者之间换成测试密钥。入账侧也有兜底：Webhook 收到测试模式的付款（`livemode=false`），而订单的用户不是管理员时（例如订单创建后才换成测试密钥）不入账，返回 200，记 Warn 日志（带 `alert=wooshpay_test_payment_refused` 和 trade_no、payment_intent、user_id、client_ip；没有真实资金，所以不带 `alert=wooshpay_paid_not_credited`，见 §10）。管理员凭 PaymentIntent 补单同样受这条限制：测试模式的 PaymentIntent 只能给管理员的订单补单，非管理员的订单会被拒绝，提示 “This is a WooshPay test payment and cannot credit a non-administrator account; adjust the user's quota directly if needed”，不入账（确需补偿时直接调整该用户额度）。后台 WooshPay 页在已保存的密钥是测试密钥时显示醒目警告（见 §8.1）。这条“测试模式只允许管理员”的规则是有意比 Stripe、Creem、Waffo 更严格：那些渠道在测试/沙箱模式下不限制谁能下单。

> **部署顺序（必须）**：返佣结算服务 `ikun_rebate_v2` 必须先升级到支持 `wooshpay` 渠道的版本（分支 `feat/wooshpay-provider`，提交 `cdd10a1`），之后才能产生任何 WooshPay 订单，包括 `sk_test_` 测试订单。旧版结算服务不认识 `payment_provider = wooshpay` 的订单：只要某一天有 WooshPay 订单，那一天的结算就会失败，直到结算服务升级为止。上线前先确认结算服务版本，再在后台打开 WooshPay 开关。结算服务这个版本在 MySQL / PostgreSQL 上的测试还没有跑（待补）。

---

## 2. WooshPay 接口要点（已对照官方文档核实）

| 项目 | 内容 |
|---|---|
| 测试域名 | `https://apitest.wooshpay.com`，收银台 `checkouttest.wooshpay.com` |
| 生产域名 | `https://api.wooshpay.com` |
| 鉴权 | HTTP Basic：用户名 = `sk_test_xxx` / `sk_live_xxx`，密码为空 → `Authorization: Basic base64("sk_xxx:")` |
| 请求体 | `application/json`（已与 WooshPay 确认） |
| 幂等 | 支持 `Idempotency-Key` 请求头（错误类型 `idempotency_error`、HTTP 409） |
| 币种与金额单位 | 固定 `CNY`；`unit_amount` 是**分**（CNY 小数位为 2） |
| 会话有效期 | 默认 24h。WooshPay 的时间戳是毫秒级，本站**不传** `expires_at`，直接使用默认的 24h |
| 错误体 | `{"error":{"type","code","message","param"}}` |

### 2.1 使用到的接口

| 用途 | 接口 |
|---|---|
| 创建收银台会话 | `POST /v1/checkout/sessions` |
| 回查支付结果 | `GET /v1/payment_intents/{id}` |
| 回查会话（可选） | `GET /v1/checkout/sessions/{id}` |
| 主动关闭会话（可选） | `POST /v1/checkout/sessions/{id}/expire` |
| 注册 Webhook（一次性运维操作） | `POST /v1/webhook_endpoints`（[Create a Webhook](https://docs.wooshpay.com/42648310e0)），也可以在控制台 Developer → Webhook 里创建 |

### 2.2 创建会话请求示例

```json
{
  "mode": "payment",
  "success_url": "https://example.com/wallet?show_history=true",
  "cancel_url": "https://example.com/wallet",
  "client_reference_id": "wsp_9f2c...",
  "customer_email": "user@example.com",
  "line_items": [
    {
      "price_data": {
        "currency": "CNY",
        "unit_amount": 7300,
        "product_data": { "name": "Account top-up", "description": "1 x top-up" }
      },
      "quantity": 1
    }
  ],
  "payment_intent_data": {
    "merchant_order_id": "wsp_9f2c...",
    "merchant_user_id": "123",
    "metadata": {
      "new_api_trade_no": "wsp_9f2c...",
      "new_api_amount_listed": "7300",
      "new_api_amount_charged": "7300"
    }
  },
  "metadata": {
    "new_api_trade_no": "wsp_9f2c...",
    "new_api_amount_listed": "7300",
    "new_api_amount_charged": "7300"
  }
}
```

Session 的 `metadata` 与 `payment_intent_data.metadata` 是同一份内容（订单号、标价、扣款金额）。`success_url` / `cancel_url` 由 `ServerAddress` 拼成绝对地址（见 §6.3），示例里的 `https://example.com` 就是 `ServerAddress`。

响应里会用到这些字段：`id`（`cs_...`）、`url`（跳转地址，必须是 `https://` 且主机为 `wooshpay.com` 或 `*.wooshpay.com`，否则不建订单，见 §6.3）、`payment_intent`（`pi_...`）、`status`（`open`）。

`new_api_amount_listed`（未折扣的标价，单位分）只用于对账：超出 8 位金额上限时不写这一项（见 §6.1）。币种固定为 CNY，回查时直接与常量比较，所以 metadata 里不写币种。

> 订单号必须写进 `payment_intent_data.metadata` 和 `merchant_order_id`。成功事件 `payment_intent.succeeded` 的 `data.object` 是 PaymentIntent，不是 Session，只有写在 PaymentIntent 上的字段才能在回调里拿到。

### 2.3 事件

官方事件列表里**没有** `checkout.session.completed`，支付成功只能以 `payment_intent.succeeded` 为准。

| 事件 | data.object | 处理 |
|---|---|---|
| `payment_intent.succeeded` | PaymentIntent | 回查后入账（订单为 `pending`、`expired` 或 `failed` 都入账） |
| `checkout.session.expired` | Checkout Session | 订单置 `expired`，这是唯一会关单的事件 |
| `payment_intent.payment_failed` | PaymentIntent | **只记日志，不改状态**：单次扣款失败后用户还可以在同一收银台换卡重试 |
| `payment_intent.canceled` | PaymentIntent | **只记日志，不改状态**：取消的 PaymentIntent 不能证明会话不会再付款，关单只以会话过期为准 |
| 其他 | — | 忽略，返回 200 |

### 2.4 Webhook 签名

以下几点已与 WooshPay 确认，并经测试环境联调和官方 “Signature Check” 文档的示例向量（`TestVerifyWooshpaySignatureMatchesWooshpayExample`）验证：

- 请求头：`Wooshpay-Signature: t=<毫秒时间戳>,v1=<hex>`。WooshPay 部分文档把它写成 `Signature`，为兼容起见，请求里没有 `Wooshpay-Signature` 时再读 `Signature`（`wooshpaySignatureHeader`）。
- 算法：`hex(HMAC_SHA256(key = webhook.secret, msg = t + "." + rawBody))`。
- 密钥：形如 `whsec_yDjRWBxxxx`，取自创建 Webhook 时返回的 `secret` 字段。**整串作为 key 的字节使用**，包括 `whsec_` 前缀，不做解码；只去掉首尾空白。
- `t`：单位是毫秒，只作为签名的盐值参与计算，**不做时间窗口校验**。计算时用 header 里的原始字符串，不要转成数字再格式化回去。
- 比较：`hmac.Equal` 常量时间比较。
- 防重放：不靠时间戳，而是靠两点。第一，入账前必须调 API 回查，且已 `success` 的订单不会再入账，所以重放成功事件不会重复入账。第二，关单事件只会影响同一订单号的 `pending` 订单，订单号唯一，所以重放也无害。
- 重试：没收到 2xx 时，72 小时内最多重投 18 次（指数退避）。

---

## 3. 整体流程

```
用户            前端(web/src)              new-api 后端                    WooshPay
 │ 选择 WooshPay │                          │                               │
 │──────────────>│ POST /api/user/wooshpay/amount ──> 计算应付金额            │
 │               │ POST /api/user/wooshpay/pay ─────> 校验/建 TopUp(pending)  │
 │               │                          │── POST /v1/checkout/sessions ─>│
 │               │<──────── pay_link ───────│<──────── {id,url,pi} ─────────│
 │── 打开 pay_link 付款 ───────────────────────────────────────────────────────>│
 │<─ 跳回 success_url（仅展示）──────────────────────────────────────────────────│
 │               │                          │<── POST /api/wooshpay/webhook ──│
 │               │                          │   1 验签  2 解析事件            │
 │               │                          │── GET /v1/payment_intents/{id}─>│
 │               │                          │   3 回查校验  4 加锁幂等入账    │
 │               │                          │── 200 ────────────────────────>│
```

---

## 4. 配置项

### 4.1 `setting/payment_wooshpay.go`（新增）

```go
package setting

var WooshpayEnabled = false         // 后台开关（选项值 "true"/"false"），默认关闭
var WooshpaySecretKey = ""          // sk_test_xxx / sk_live_xxx
var WooshpayWebhookSecret = ""      // webhook_endpoint.secret
var WooshpayApiBase = "https://api.wooshpay.com" // 测试环境填 https://apitest.wooshpay.com
var WooshpayUnitPrice = 7.3         // 每 1 单位充值额对应的人民币价格（与易支付 Price 默认值相同）
var WooshpayMinTopUp = 1            // 最低充值数量
```

测试模式由密钥前缀推导（`strings.HasPrefix(WooshpaySecretKey, "sk_test_")`），不再单独设开关，避免“生产密钥 + 测试开关”这种配置不一致。密钥与 API Base 必须对应：`sk_test_` 只能配 `apitest.wooshpay.com`，`sk_live_` 只能配 `api.wooshpay.com`，否则视为未启用。`WooshpayApiBase` 保存时去掉首尾空白和末尾的 `/`；`WooshpayWebhookSecret` 保存时和参与 HMAC 计算时都去掉首尾空白。

保存时交叉校验（`model.validateWooshpayEnvironment`，由 `model.UpdateOption` / `model.UpdateOptionsBulk` 调用）：只在这次保存包含 `WooshpaySecretKey`、`WooshpayApiBase`、`WooshpayWebhookSecret`、`WooshpayEnabled` 中的某一项时运行；没有保存的项取当前值，按合并后的组合校验。组合是已知的错配（`sk_test_` 配非 `apitest` 主机，`sk_live_` 配非 `api` 主机）就拒绝保存；前缀不是这两种的密钥不算已知错配，只是保持未启用。`WooshpayEnabled` 为 `true` 时还要求密钥、Webhook Secret 都已填写，且密钥与 API Base 属于同一环境（`setting.ValidateWooshpayEnabled`）；并且系统设置里当前的服务器地址（`ServerAddress`）必须是顾客能访问到的地址（`setting.ValidateWooshpayServerAddress`），否则拒绝打开（WooshPay 已打开时，保存上面这几项也按同样规则检查）：

| 规则 | 拒绝时的提示 |
|---|---|
| 带主机的绝对 `http(s)` 地址 | “WooshPay can only be enabled once the server address in system settings is an absolute http(s) URL” |
| 主机不是本机：`localhost`、`*.localhost`、`localhost.localdomain`、回环地址（`127.0.0.0/8`、`::1`、`::ffff:127.0.0.1` 等）、未指定地址（`0.0.0.0`、`::`），按 `net.ParseIP` 判断。判断前转小写、去掉末尾的 `.` 和 IPv6 zone（`%en0`） | “WooshPay cannot return customers to a localhost server address; set the public address of this site in system settings” |
| 密钥是 `sk_live_` 时必须是 `https://` | “WooshPay live secret keys (sk_live_) need an https:// server address in system settings” |

**单独保存 `ServerAddress` 不检查 WooshPay 规则**：WooshPay 已打开时把它改成上表不允许的地址，保存照常成功，但新充值随即停止（`isWooshpayTopUpEnabled` 为 false，充值页不再显示 WooshPay），后台 WooshPay 页显示 `invalid_server_address` 警告（见下文和 §8.1）；已打开的收银台付款仍照常入账。

**关闭 WooshPay 不受密钥校验阻挡**：保存把 `WooshpayEnabled` 设为 `false`、且不改密钥和 API Base 时，跳过密钥与 API Base 的交叉校验，库里已存在的错配组合不会挡住关闭。同一次保存改了密钥或 API Base 时照常校验。

`GET /api/option/` 返回两个派生的只读项，都不能通过任何保存接口写入（返回 “WooshPay status is derived from its settings and is read-only”），库里即使有同名行也不回显：

- `WooshpayTestMode`：已保存的密钥是不是测试密钥。
- `WooshpayUnavailableReason`：`WooshpayEnabled` 为 `true` 但 WooshPay 不可用时给出第一个不满足的条件，可用或开关关闭时为空。取值是稳定的机器码，按以下顺序判断：`compliance_not_confirmed`（未确认支付合规）、`missing_secret`、`missing_webhook_secret`、`key_base_mismatch`（密钥与 API Base 不属于同一环境）、`invalid_unit_price`、`invalid_server_address`（`ServerAddress` 不满足上表）。后台 WooshPay 页按机器码显示对应的翻译警告（见 §8.1）。

后台保存只走一个请求：`PUT /api/option/wooshpay/environment`，请求体是部分字段 `{"enabled","secret_key","webhook_secret","api_base","unit_price","min_topup"}`，只带有改动的项；空的密钥字段表示保留原值。服务端规范化（API Base 去空白和末尾 `/`）后用 `UpdateOptionsBulk` 校验整个组合并在同一事务里写入，被拒绝时一项都不写。审计日志用 `option.update` 模板，`key` 参数是改动项名称的逗号列表，不记录值。通用的 `PUT /api/option/` 单项保存走同样的校验；后台页面不用它保存 `Wooshpay*` 项。

### 4.2 `model/option.go`

- `InitOptionMap`：把上面 6 个配置项全部注册进去。其中 `WooshpaySecretKey` 和 `WooshpayWebhookSecret` 照 Stripe 密钥的方式处理，不回显给前端。
- `updateOptionMap` 的 switch：加对应的 case，`WooshpayUnitPrice` 用 `strconv.ParseFloat`，`WooshpayMinTopUp` 用 `strconv.Atoi`。
- 保存时校验：`WooshpayUnitPrice` 必须大于 0 且是有限值；`WooshpayMinTopUp` 必须是 1 到 10000（单笔购买数量上限）之间的整数；`WooshpayApiBase` 必须是只有协议和主机的 `https://` 地址（不能带路径、查询串、片段或用户信息）；`WooshpayEnabled` 只能是 `true` / `false`；组合校验见 §4.1。
- 加载和保存时 `WooshpayApiBase` 去掉末尾 `/`，OptionMap 里存规范化后的值。

### 4.3 可用性判断：`controller/payment_webhook_availability.go`

```go
// 新充值：开关打开，且 wooshpayUnavailableReason 的条件全部满足
// （合规确认、账号配置完整、单价、ServerAddress 是顾客能访问的返回地址，见 §4.1 的表）
func isWooshpayTopUpEnabled() bool {
	return setting.WooshpayEnabled && wooshpayUnavailableReason() == ""
}

// 按用户判断：在上面的基础上，测试密钥只允许管理员（GetTopUpInfo、/amount、/pay 共用）。
// account 是调用方读一次的账号快照，后续建收银台也用它
func wooshpayTopUpUnavailable(account wooshpayAccount, role int) error

// 后台警告用：开关打开时第一个不满足的条件（机器码），都满足时为空
func wooshpayUnavailableReason() string

// 收款：不看开关和 ServerAddress，关掉开关后已经打开的收银台付款仍会入账
func isWooshpayWebhookEnabled() bool {
	if !isPaymentComplianceConfirmed() {
		return false
	}
	return strings.TrimSpace(setting.WooshpaySecretKey) != "" &&
		strings.TrimSpace(setting.WooshpayWebhookSecret) != "" &&
		setting.WooshpayUnitPrice > 0 &&
		setting.WooshpayKeyMatchesApiBase(setting.WooshpaySecretKey, setting.WooshpayApiBase) // sk_test_ ⇔ apitest，sk_live_ ⇔ api
}
```

---

## 5. 数据模型：`model/topup.go`

### 5.1 常量

```go
PaymentMethodWooshpay   = "wooshpay"
PaymentProviderWooshpay = "wooshpay"
```

### 5.2 订单字段语义（与易支付、Waffo Pancake 一致）

WooshPay 和易支付、Waffo、Waffo Pancake 一样由本站定价，所以沿用它们的语义，而不是 Stripe 的：**充值分组倍率只放大应付金额，不放大入账额度**。

| 字段 | 值 |
|---|---|
| `Amount` | 购买数量（整数个货币单位）。Tokens 展示模式下用户输入的是 token 数，必须是 `QuotaPerUnit` 的整数倍（否则 `/amount`、`/pay` 都返回错误 “With the Tokens display the top-up amount must be a multiple of {{unit}} tokens”，不再静默向下取整），存储 `输入 ÷ QuotaPerUnit` |
| `Money` | 实付金额（元）= `Amount × WooshpayUnitPrice × TopupGroupRatio × 折扣`，四舍五入到分，与 Waffo Pancake 的 `Money: payMoney` 一致 |
| `TradeNo` | `"wsp_" + common.Sha1("new-api-wsp-{uid}-{unixMilli}-{rand4}")` |
| `PaymentMethod` / `PaymentProvider` | `wooshpay` |
| `Status` | 创建时为 `pending` |

不新增数据库列。网关侧 ID（`cs_`、`pi_`）不落库，订单与网关之间靠 metadata 和 `merchant_order_id` 绑定。所以**不需要迁移**，三种数据库都没有兼容性风险。

### 5.3 新增 `RechargeWooshpay`

`Recharge`（`model/topup.go`）写死了只接受 Stripe 订单，还会写 `stripe_customer`，不能直接复用。参照 `RechargeCreem` 和 `RechargeWaffoPancake` 另写一个同级函数：

```go
func RechargeWooshpay(tradeNo string, callerIp string, paymentAmount string, charge GatewayCharge, byAdmin bool) (err error) {
	if tradeNo == "" {
		return errors.New("payment order number not provided")
	}
	var quota int
	topUp := &TopUp{}
	refCol := "`trade_no`"
	if common.UsingMainDatabase(common.DatabaseTypePostgreSQL) {
		refCol = `"trade_no"`
	}
	err = DB.Transaction(func(tx *gorm.DB) error {
		if err := lockForUpdate(tx).Where(refCol+" = ?", tradeNo).First(topUp).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrTopUpNotFound // 只有记录不存在才是 ErrTopUpNotFound
			}
			return err // 其他数据库错误原样返回，调用方让 WooshPay 重投
		}
		if topUp.PaymentProvider != PaymentProviderWooshpay {
			return ErrPaymentMethodMismatch
		}
		// 回查已确认付款成功，只有 pending / expired / failed 可以完成；
		// 其他状态（已 success）返回 ErrTopUpStatusInvalid，调用方据此返回 200，不重复入账
		switch topUp.Status {
		case common.TopUpStatusPending, common.TopUpStatusExpired, common.TopUpStatusFailed:
		default:
			return ErrTopUpStatusInvalid
		}
		// 纵深防御：在行锁内再核对一次，实收（分）必须等于订单 Money 换算的分
		if !decimal.NewFromFloat(topUp.Money).Shift(2).Round(0).Equal(decimal.NewFromInt(charge.Charged)) {
			return ErrTopUpAmountMismatch
		}
		// 测试模式付款（charge.Test）只给管理员的订单入账，Webhook 和管理员补单（byAdmin）都一样，
		// 否则返回 ErrTestPaymentForNonAdmin
		// quota = Amount × QuotaPerUnit，严格换算，与 RechargeWaffoPancake / 易支付相同
		// applyGatewayCharge(tx, topUp, charge)
		// creditTopUpQuota(tx, topUp.UserId, quota, nil)   // 不写 stripe_customer
		// 更新 Status=success、CompleteTime
		return nil
	})
	// 提交后：syncCreditUserQuotaCache(..., "wooshpay topup")
	//         RecordTopupLog(..., topUp.PaymentMethod, PaymentMethodWooshpay)
	//         日志里的“支付金额”就是参数 paymentAmount：amount_received（分）换算成元、保留 2 位小数再加币种，如 "73.00 CNY"
	//         byAdmin（管理员凭 PaymentIntent 补单）时改记 "Administrator completed the order, ..."，
	//         callback_payment_method 为 "admin"，与 ManualCompleteTopUp 一致
	return err
}
```

`ErrTopUpNotFound`、`ErrPaymentMethodMismatch`、`ErrTopUpAmountMismatch`、`ErrTestPaymentForNonAdmin`、`ErrTopUpStatusInvalid` 原样返回给调用方，由调用方确认并返回 200；其余错误按数据库故障处理（Webhook 返回 500 重投）。

额度换算必须用 `common.WalletQuotaFromDecimalStrict`（和 `Recharge` 一致），不允许出现 `int(...)` 这类裸强转。

### 5.4 返佣快照：`model/rebate.go`

- `paidQuotaFromCharge`：加 `case PaymentProviderWooshpay: unitPrice = setting.WooshpayUnitPrice`，与 Waffo Pancake 相同，实付快照 = `Money ÷ WooshpayUnitPrice × QuotaPerUnit`。`Money` 已经是折扣后的实付金额，所以 Webhook 传入的 `GatewayCharge` 是“期望扣款 vs 实际到账”（两者经回查校验相等），只有测试模式会把快照清零。
- `resolveTopUpProvider`：加 `case PaymentMethodWooshpay: return PaymentProviderWooshpay`。
- `TopUp.AfterCreate`：测试密钥（`sk_test_`）下单时，`paid = 0`，与 Waffo 沙箱、Creem 测试模式的处理一致。单价和测试模式取下单请求开始时读到的值：`RequestWooshpayPay` 把它们放进不落库的 `TopUp.Pricing`（`gorm:"-"`），`AfterCreate` 优先用它，避免下单过程中管理员改了单价或密钥导致快照与实际扣款不一致。

### 5.5 管理员补单：只能凭 PaymentIntent

WooshPay 订单不走 `model.ManualCompleteTopUp`：无论订单是 `pending`、`expired` 还是 `failed`（会话过期只关单，钱仍可能到账，而 Webhook 72 小时后停止重投），管理员都必须提供付款证明。管理员在 `POST /api/user/topup/complete` 里带上 `payment_intent_id`（`pi_...`），`controller.completeWooshpayOrderByIntent` 用当前密钥回查该 PaymentIntent，要求 `status = succeeded`，`metadata.new_api_trade_no`、`merchant_order_id` 都等于本订单号，币种为 CNY（不区分大小写），`amount_received` 等于 metadata 里的扣款金额且等于订单 `Money × 100` 四舍五入后的分，`livemode` 与密钥模式一致，然后走 `RechargeWooshpay(..., byAdmin=true)` 入账（测试模式的 PaymentIntent 只能给管理员的订单入账，见 §1 测试模式说明）（同样的 `LockOrder` 和行锁，只入账一次），充值日志记为 “Administrator completed the order, amount added: …, payment amount: 73.00 CNY”（`callback_payment_method = admin`）；路由 `POST /api/user/topup/complete` 的管理审计由审计中间件按 `user.topup_complete` 兜底记录。返佣快照按 Webhook 的规则处理（测试模式清零），不接受 `paid_money`。

补单失败返回给后台的都是可翻译的英文原文（`common.NewMessage`），不暴露内部字段名：

| 情况 | 提示 |
|---|---|
| 没带 `payment_intent_id`（任何状态） | A WooshPay order can only be completed with the ID of its succeeded PaymentIntent |
| PaymentIntent 的 id / 订单号不是本订单 | The WooshPay PaymentIntent belongs to another order |
| 币种不是 CNY | The WooshPay PaymentIntent was not charged in CNY |
| `livemode` 与密钥模式不一致 | The WooshPay PaymentIntent is not in the mode of the configured secret key |
| 实收与 metadata 或订单 `Money` 不符（含 `ErrTopUpAmountMismatch`） | The amount the WooshPay PaymentIntent received does not match this order |
| `ErrTopUpNotFound` | Top-up order does not exist |
| `ErrPaymentMethodMismatch` | This order was not created by WooshPay |
| 测试模式付款而订单用户不是管理员（`ErrTestPaymentForNonAdmin`） | This is a WooshPay test payment and cannot credit a non-administrator account; adjust the user's quota directly if needed |
| 订单用户已不存在 | User does not exist |
| 额度校验失败（`ErrInvalidTopUpQuota`、`ErrTopUpQuotaLimitExceeded`） | 原样返回（本身就是 `common.Message`） |
| 订单已 success（`ErrTopUpStatusInvalid`） | 视为成功，不重复入账 |
| 其他数据库错误 | Failed to complete order（服务端记 Error 日志） |

不带有效 PaymentIntent 的 WooshPay 订单无法补单；`payment_intent_id` 不能与 `paid_money` 同时提交，也不能用于其他渠道的订单。后台“账单记录”里 WooshPay 的 `pending`/`expired`/`failed` 订单显示“完成订单”按钮，确认框里只有必填的 PaymentIntent ID，没有实收金额。

---

## 6. 后端：`controller/topup_wooshpay.go`（新增）

### 6.1 请求体与金额

```go
type WooshpayPayRequest struct {
	Amount        int64  `json:"amount"`
	PaymentMethod string `json:"payment_method"`
	SuccessURL    string `json:"success_url,omitempty"`
	CancelURL     string `json:"cancel_url,omitempty"`
}
```

应付金额与易支付（`getPayMoney`）语义相同：分组倍率只放大应付金额，入账额度不乘倍率。

```
units    = amount                                   (USD/CNY 等货币展示)
         = amount ÷ QuotaPerUnit                    (Tokens 展示，amount 是 token 数，必须是 QuotaPerUnit 的整数倍，否则拒绝)
payMoney = units × WooshpayUnitPrice × topupGroupRatio × discount   (元)
           discount 取 AmountDiscount[amount]（按用户输入的展示值查，与易支付相同）
listed   = 同上，但 discount = 1
credited = units × QuotaPerUnit
```

与易支付的一处差异：易支付在 Tokens 模式下按未取整的 `amount ÷ QPU` 计价、按取整后的单位入账；WooshPay 直接拒绝不是整数单位的输入，计价和入账都用同一个 `units`，保证付的钱与到账额度一致。

`unit_amount` 换算成分，**必须用 decimal**，不允许用 float 直接乘（`wooshpayFen`）：

```go
fen := payMoney.Shift(2).Round(0) // ×100，四舍五入（half away from zero）
```

金额边界（计费安全要求）：

- `amount` 不小于 `getWooshpayMinTopup()`（展示单位；Tokens 模式下为 `WooshpayMinTopUp × QPU`），且 `units` 在 `[WooshpayMinTopUp, 10000]` 区间内；上限作用于购买数量，超限提示按展示单位给出。
- Tokens 展示模式下 `amount` 必须是 `QuotaPerUnit` 的整数倍。
- 只约束实际扣款：折扣后的 `fen` 必须 `> 0`（否则 “Payment amount is too low”），并且不超过 `99,999,999` 分（即 999,999.99 元；WooshPay PaymentIntent 的 amount 最多 8 位，`setting.WooshpayMaxMinorAmount`），否则 `/amount` 和 `/pay` 都返回 “Payment amount is too high”。未折扣的标价（`listed`）不参与校验：例如单价 160,000 元、购买 100 个单位时标价是 1,600,000,000 分，超出 8 位，但 0.05 的预设折扣后实扣 80,000,000 分，订单照常创建，metadata 里不写 `new_api_amount_listed`。
- 调用 `rejectInvalidCreditedQuota(c, uid, getWooshpayCreditedQuota(amount))`（`units × QPU`，不含分组倍率），提前挡住会让钱包溢出的订单。

### 6.2 `RequestWooshpayAmount`：`POST /api/user/wooshpay/amount`

请求开始时读一次账号快照，先做与 `/pay` 相同的可用性检查（`wooshpayTopUpUnavailable(account, role)`：未启用返回 “WooshPay payment is not enabled”，测试密钥下非管理员返回测试模式提示），金额校验也与 `/pay` 相同，返回实际扣款的人民币金额（2 位小数字符串，与其他渠道一致）：`{"message":"success","data":{"amount":"73.00"}}`。`amount` 由扣款的分换算，与收银台金额一致。前端确认框和金额栏按 CNY、2 位小数显示。

### 6.3 `RequestWooshpayPay`：`POST /api/user/wooshpay/pay`

1. 请求开始时读一次账号（密钥、API Base），后续的管理员判断、建会话和返佣快照里的测试模式都用这份快照。
2. 只有 WooshPay 已打开且账号配置完整（`setting.WooshpayEnabled && isWooshpayWebhookEnabled()`）时：测试密钥下的非管理员先被拒绝（“WooshPay is in test mode; only administrators can place test orders”），他们看不到服务器地址的提示，也不会因此记 Error；然后按 §4.1 的规则校验 `ServerAddress`：不合格就记 Error 日志（带 `alert=wooshpay_checkout_failed`，见 §10）并返回 “Payment return address is not configured; set the server address in system settings”，不调网关、不建订单。放在可用性检查之前是为了把原因说清楚；WooshPay 没打开的站点不会记这条 Error。
3. `wooshpayTopUpUnavailable(account, role)` 不为空时直接返回：未启用是 “WooshPay payment is not enabled”，测试密钥下非管理员是测试模式提示。
4. 要求 `req.PaymentMethod == model.PaymentMethodWooshpay`；检查金额上下限。
5. 用 `common.ValidateRedirectURL` 校验 SuccessURL 和 CancelURL。为空时 SuccessURL 默认 `<ServerAddress>/wallet?show_history=true`（与 Waffo 一致，付款后打开充值记录），CancelURL 默认 `<ServerAddress>/wallet`；返回地址前缀是 `ServerAddress` 去掉末尾 `/`。
6. 读取单价，后续计价、建会话、建订单和返佣快照都用它和第 1 步的账号。生成 TradeNo，按 §6.1 计算 `listed`、`fen`。
7. **先调网关，成功后再插入订单**（顺序和 Stripe 一样，避免留下没有支付链接的悬空订单）：
   - 调用 `POST /v1/checkout/sessions`，带 `Idempotency-Key: <TradeNo>`，请求体见 §2.2；不传 `expires_at`，会话按 WooshPay 默认 24h 过期（WooshPay 时间戳是毫秒级，传秒级会出错）。
   - 网关失败时返回通用错误。Error 日志带 `alert=wooshpay_checkout_failed`，记下 `error.type`、`error.code`，但不要记密钥。
   - 校验响应里的 `url`：必须是 `https://`，主机为 `wooshpay.com` 或以 `.wooshpay.com` 结尾，否则记 Error 日志、返回 “Failed to start payment”，不建订单。
8. `model.TopUp{...}.Insert()`，返回 `{"message":"success","data":{"pay_link": session.url}}`。插入失败时会话已存在，用 Error 日志记录 trade_no 和 session_id，带 `alert=wooshpay_paid_not_credited`（见 §10 告警）。

### 6.4 WooshPay HTTP 客户端

逻辑留在本文件里；如果之后订阅支付也要用，再移到 `service/wooshpay/`。要求：

- 使用项目里现有的 HTTP client（能读到代理配置的那个），超时 15 秒。
- 请求头：`Authorization: Basic base64(secret+":")`、`Content-Type: application/json`、`Accept: application/json`。
- 序列化和反序列化**必须用 `common.Marshal` / `common.Unmarshal`**，不允许直接调用 `encoding/json`。
- 非 2xx 响应只解析 `{error:{type,code}}` 并返回 error（`wooshpayAPIError` 只保留这两个字段，日志不记 message）。

只解析会用到的字段：

```go
type wooshpaySession struct {
	ID       string            `json:"id"`
	URL      string            `json:"url"`
	Metadata map[string]string `json:"metadata"` // 只有过期事件读它
}

type wooshpayPaymentIntent struct {
	ID              string            `json:"id"`
	Status          string            `json:"status"`
	Amount          int64             `json:"amount"`
	AmountReceived  *int64            `json:"amount_received"` // 缺失时为 nil；succeeded 时按 amount 计实收（自动扣款）
	Currency        string            `json:"currency"`
	Livemode        *bool             `json:"livemode"`        // 缺失时为 nil，按密钥模式处理并记 Warn
	MerchantOrderID string            `json:"merchant_order_id"`
	Metadata        map[string]string `json:"metadata"`
}
```

### 6.5 `WooshpayWebhook`：`POST /api/wooshpay/webhook`

```go
func WooshpayWebhook(c *gin.Context) {
	// 回查和入账不能因为 WooshPay 断开连接而被取消
	ctx := context.WithoutCancel(c.Request.Context())
	if !isWooshpayWebhookEnabled() {
		logger.LogWarn(ctx, ...)                     // alert=wooshpay_webhook_rejected reason=webhook_disabled path=<URL.Path，最多 256 字节>
		c.AbortWithStatus(http.StatusForbidden)
		return
	}
	// 密钥、API Base、Webhook Secret 只读一次（Secret 去掉首尾空白），
	// 验签、回查和模式判断都用这份快照
	account := currentWooshpayAccount()
	webhookSecret := strings.TrimSpace(setting.WooshpayWebhookSecret)

	payload, err := io.ReadAll(c.Request.Body)       // 路由层已加 anonymousRequestBodyLimit
	if err != nil { c.AbortWithStatus(http.StatusServiceUnavailable); return }

	// 先读 Wooshpay-Signature，没有时读 Signature
	if err := verifyWooshpaySignature(payload, wooshpaySignatureHeader(c.Request.Header), webhookSecret); err != nil {
		// alert=wooshpay_webhook_rejected；只记 client_ip、error、has_signature_header、body_length，不记 secret 和签名
		logger.LogWarn(ctx, ...)
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	var event struct {
		ID   string `json:"id"`
		Type string `json:"type"`
		Livemode bool `json:"livemode"`
		Data struct{ Object json.RawMessage `json:"object"` } `json:"data"`
	}
	if err := common.Unmarshal(payload, &event); err != nil { c.AbortWithStatus(http.StatusBadRequest); return }

	switch event.Type {
	case "payment_intent.succeeded":
		err = wooshpayFulfill(ctx, account, event.Data.Object, c.ClientIP())
	case "checkout.session.expired":
		err = wooshpayExpireOrder(ctx, event.Data.Object)
	case "payment_intent.payment_failed", "payment_intent.canceled":
		// 只记日志，订单保持 pending
	default:
		// 记日志后忽略
	}
	if errors.Is(err, errWooshpayRetry) {
		c.AbortWithStatus(http.StatusInternalServerError) // 让 WooshPay 重投
		return
	}
	c.Status(http.StatusOK)
}
```

**`verifyWooshpaySignature`** 是有独立测试价值的安全边界，单独写成函数：

1. header 用 `strings.SplitSeq` 按 `,` 拆开，每段用 `strings.Cut` 按第一个 `=` 拆开；取 `t`，以及所有 `v1`（可能不止一个）。缺少 `t` 或 `v1` 直接拒绝。
2. `expected = hex(HMAC_SHA256([]byte(secret), t + "." + payload))`，其中 `t` 是 header 里的**原始字符串**，不做数值解析，也不做时间窗口校验。
3. 只要有一个 `v1` 满足 `hmac.Equal([]byte(expected), []byte(v1))` 就通过。

**`wooshpayFulfill`**（核心）：

1. 从事件对象里取出 `pi.id`；如果为空或者不是 `pi_` 开头，忽略并返回 200。
2. **回查**：`GET /v1/payment_intents/{id}`。失败时返回可重试错误（500），由 WooshPay 重投。
3. 按顺序判断：
   - `pi.Metadata["new_api_trade_no"]` 为空（不是本站创建的 PaymentIntent）：Warn 日志，返回 200。
   - `status != "succeeded"`（如 `processing`、`requires_*`）：还不能下结论，Warn 日志后返回 500（`errWooshpayRetry`），让 WooshPay 重投。
   - 已 `succeeded` 但缺少 `amount_received`：按自动扣款处理，取 `amount` 作为实收；缺少 `livemode`：按密钥模式处理并记 Warn 日志。`amount_received` 缺失且 `amount` 也不是正数时（`amount_unknown`）仍无法下结论，记 Error 日志（带 `alert=wooshpay_paid_not_credited`）并返回 500 重投。
   - 已 `succeeded`，但以下任一项不一致：Error 日志（“已付款未入账，需要人工处理”，带 `alert=wooshpay_paid_not_credited`、trade_no、pi id 和不一致的字段），返回 200，不入账：
     - `pi.ID` 等于事件里的 id
     - `tradeNo` 等于 `pi.MerchantOrderID`
     - `strings.EqualFold(pi.Currency, "CNY")`（例如回查到 USD 时不入账）
     - `*pi.AmountReceived == parse(pi.Metadata["new_api_amount_charged"])`
     - `livemode == !account.testMode()`（`account` 是 Webhook 开始时读取的密钥和 API Base 快照，回查和模式判断都用它）
4. `LockOrder(tradeNo)`，函数退出时 `UnlockOrder(tradeNo)`。
5. `charge := model.GatewayCharge{Listed: received, Charged: received, Test: !livemode}`，`received` 是上一步得到的实收（分），`livemode` 是回查结果（缺失时按密钥模式）。实收已与 metadata 里的扣款金额校验相等，所以只有测试模式会改快照；`new_api_amount_listed` 只用于对账，不参与计算。
6. 调用 `model.RechargeWooshpay(tradeNo, callerIp, paymentAmount, charge, false)`（最后一个参数 `byAdmin`，Webhook 为 false），`paymentAmount` 是实收（分）用 decimal 换算成元、保留 2 位小数后加 ` CNY`，如 `"73.00 CNY"`。`RechargeWooshpay` 在行锁内再要求实收等于订单 `Money` 换算的分（与管理员补单同一检查），不一致返回 `ErrTopUpAmountMismatch`：
   - 返回 `ErrTopUpStatusInvalid`（订单已 success）：属于重复通知，Info 日志后返回 200。
   - 返回 `ErrTestPaymentForNonAdmin`（测试模式付款，订单用户不是管理员）：没有真实资金，Warn 日志（带 `alert=wooshpay_test_payment_refused`、trade_no、payment_intent、user_id、client_ip）后返回 200，不入账。
   - 返回 `ErrTopUpNotFound` / `ErrPaymentMethodMismatch` / `ErrTopUpAmountMismatch` / `ErrInvalidTopUpQuota` / `ErrTopUpQuotaLimitExceeded`，或订单的用户已不存在（`gorm.ErrRecordNotFound`）：Error 日志（已付款未入账，需要人工处理，带 `alert=wooshpay_paid_not_credited`）后返回 200，重投也不会改变结果。
   - 其他 DB 错误（真正的临时故障）：Error 日志后返回 500。

> metadata 由我方用私钥写入，而且是**从 API 回查**拿到的（不是用回调体里的数据），客户无法篡改。“实际到账金额”与“下单时我方期望扣款金额”相互印证，所以不需要新增数据库列来存期望金额。

**`wooshpayExpireOrder`**：

- 只处理 `checkout.session.expired`，从 Session 的 `metadata.new_api_trade_no` 取订单号。
- 先 `LockOrder`，再调 `model.UpdatePendingTopUpStatus(tradeNo, model.PaymentProviderWooshpay, common.TopUpStatusExpired)`。这个函数已经做了服务商校验，并且只改 pending 订单；只有记录不存在才返回 `ErrTopUpNotFound`，其他数据库错误原样返回，Webhook 回 500 让 WooshPay 重投。

### 6.6 充值信息接口：`controller/topup.go`

- `GetTopUpInfo`：先复制全局 `PayMethods`（`slices.Clone`，避免追加时写进全局切片的底层数组）。`wooshpayTopUpUnavailable(currentWooshpayAccount(), role) == nil` 且能读到用户分组时，`pay_methods` 里没有 wooshpay 就追加 `{"name":"WooshPay","type":"wooshpay","color":"#1F6FEB","min_topup":getWooshpayMinTopup()}`；否则（未启用、配置不完整、测试密钥下的非管理员、读分组失败）把 `PayMethods` 里配置的 `type == "wooshpay"` 项也删掉，`enable_wooshpay_topup` 为 false。读分组失败只记 Warn 并隐藏 WooshPay，不让整个接口失败。
- 同时返回 `enable_wooshpay_topup`、`wooshpay_min_topup`，以及按当前用户计算的 `wooshpay_unit_price`（人民币单价 × 用户的充值分组倍率），供预设金额卡片计价和显示。不返回币种和小数位：前端固定按 CNY、2 位小数显示。`/api/status` 不再返回 WooshPay 价格。
- 易支付下单（`RequestEpay`、订阅易支付）拒绝 `stripe`、`creem`、`waffo`、`waffo_pancake`、`wooshpay`、`balance` 这些有独立收银流程的类型，即使它们出现在 `PayMethods` 里。

---

## 7. 路由：`router/api-router.go`

```go
apiRouter.POST("/wooshpay/webhook", anonymousRequestBodyLimit, controller.WooshpayWebhook)
// selfRoute
selfRoute.POST("/wooshpay/pay", middleware.CriticalRateLimit(), controller.RequestWooshpayPay)
selfRoute.POST("/wooshpay/amount", controller.RequestWooshpayAmount)
```

另外把 `"POST /api/wooshpay/webhook"` 加进 `router/access_token_scope_test.go` 里的 "Payment callbacks" 列表。

---

## 8. 前端（`web/src`）

### 8.1 后台设置

文件：`features/system-settings/integrations/payment-settings-section.tsx`

- zod schema、sanitize、初始值、diff 提交：都按 Stripe 字段的写法加一份 `Wooshpay*`。密钥字段只在非空且有改动时才提交。
- 新增 Tab：`value='wooshpay'`，内容包括：
  - Webhook 地址提示 `<ServerAddress>/api/wooshpay/webhook`
  - Secret Key（密码框）
  - Webhook Secret（密码框）
  - API Base（下拉选择 测试 / 生产）
  - 必需事件 `payment_intent.succeeded`、`checkout.session.expired`；可选事件 `payment_intent.canceled`、`payment_intent.payment_failed`（只记日志），与 §10 的注册命令一致
  - 已保存的密钥是测试密钥时显示醒目警告（测试付款充入真实额度，只有管理员能下单）。前端不拿密钥：`GET /api/option/` 返回派生的布尔项 `WooshpayTestMode`（由 `setting.IsWooshpayTestSecretKey` 算出），密钥本身照旧不回显
  - 开关已打开但 WooshPay 不可用时，按 `GET /api/option/` 返回的 `WooshpayUnavailableReason` 机器码显示一条翻译后的警告“WooshPay is not available”，说明是哪一项没满足（未确认合规、缺密钥、缺 Webhook Secret、密钥与 API Base 不同环境、服务器地址不合格、单价不大于 0）；未知的机器码显示通用说明
  - 一条静态提示：密钥和 API Base 属于同一环境之前 WooshPay 保持停用（`sk_test_` 配 `apitest`，`sk_live_` 配 `api`）
  - 启用开关（`WooshpayEnabled`）
  - 保存：所有有改动的 `Wooshpay*` 项通过 `PUT /api/option/wooshpay/environment` 一次提交（见 §4.1），被拒绝时一项都不写；某个渠道保存失败不会跳过其他渠道（包括 Waffo Pancake 绑定）的保存
  - 单价（每 1 单位的人民币价格；没有币种配置项）
  - 最低充值
- 同步修改这几个文件：`features/system-settings/billing/index.tsx`（默认值）、`billing/section-registry.tsx`（映射）、`features/system-settings/types.ts`（类型）。
- 支付方式编辑器 `payment-methods-visual-editor.tsx`、`payment-method-dialog.tsx`：加入 `wooshpay` 类型。

### 8.2 钱包充值

| 文件 | 改动 |
|---|---|
| `features/wallet/api.ts` | `calculateWooshpayAmount` → `/api/user/wooshpay/amount`；`requestWooshpayPayment` → `/api/user/wooshpay/pay` |
| `hooks/use-payment.ts` | calculators 映射加 `wooshpay`；`processPayment` 拿到 `pay_link` 后与 Waffo Pancake 一样在当前页跳转（`window.location.href`），避免 await 之后 `window.open` 被拦截；Stripe 仍新窗口打开 |
| `lib/payment.ts` | `isWooshpayPayment`；默认类型取 `pay_methods` 第一项（WooshPay 启用时服务端一定把它列进 `pay_methods`，所以 `getDefaultPaymentType` 不需要 WooshPay 分支）；最低充值的推导（WooshPay 排在 Waffo、Waffo Pancake 之后，不抬高其他方式的最低充值；WooshPay 按钮自身的最低值来自 `pay_methods[].min_topup`）；`getWooshpayPresetPricing`：WooshPay 是已选的支付方式，或未选择时的默认方式（`pay_methods` 第一项）时，预设金额卡片按充值信息里的 `wooshpay_unit_price`（已含分组倍率）计价、按 CNY 2 位小数显示（`formatCnyAmount`）；`getWooshpayAmountError`：超过 10000 个单位、Tokens 展示下不是 `QuotaPerUnit` 整数倍时在前端直接提示（与后端同一文案） |
| `hooks/use-payment.ts` | `/amount` 失败时不再当作 0：WooshPay 提示错误且不打开确认框；确认框和金额栏把 `/amount` 返回的金额按 CNY 2 位小数显示 |
| `hooks/use-topup-info.ts`、`types.ts` | `enable_wooshpay_topup`、`wooshpay_min_topup` |
| `constants.ts` | `PAYMENT_TYPES` 加 `WOOSHPAY` 和颜色 |
| `lib/ui.tsx`、`assets/brand-icons/` | 图标（没有官方图标时用通用卡片图标） |
| `lib/billing.ts` | 显示名 |
| `components/recharge-form-card.tsx` | 按 `enable_wooshpay_topup` 控制是否显示 |

### 8.3 i18n

- 所有新增文案都用 `t('English key')`。
- 在 `web/src/i18n/locales/{en,zh,zh-TW,ja,fr,ru,vi}.json` 里补齐，然后在 `web/` 下执行 `bun run i18n:sync`。
- 后端返回给 Web 控制台的错误提示用 `common.NewMessage` / `common.ApiErrorT` 写英文原文，由前端翻译，所以每个 key 都要加进前端全部 7 个语言文件。
- 服务端日志用 `common.LogText` 写英文；需要中文日志的行，在 `common/log_text.zh-CN.json` 里补对应的中文。

---

## 9. 测试

后端统一使用 `testify/require` 和 `testify/assert`，写成确定性的表驱动测试。

| 测试 | 文件 | 保护的不变量 |
|---|---|---|
| `TestVerifyWooshpaySignature` | `controller/topup_wooshpay_test.go` | 用 `whsec_` 密钥和毫秒 `t` 签出的正确签名能通过；篡改 body、错误密钥、缺少 `t`/`v1`、多个 `v1` 中有一个正确，各情况的结果都符合预期 |
| `TestVerifyWooshpaySignatureMatchesWooshpayExample` | 同上 | WooshPay “Signature Check” 文档的示例（公开的示例密钥，不是本站密钥）能通过验签 |
| `TestWooshpaySignatureHeader` | 同上 | 先读 `Wooshpay-Signature`，没有时读 `Signature` |
| `TestWooshpayUnitAmountFen` | 同上 | 73 元 → 7300 分；不足 1 分的部分四舍五入；舍入后为 0 拒绝；超过 99,999,999 分（8 位）拒绝 |
| `TestRequestWooshpayPayCreatesSessionBeforeOrder` | 同上 | 先建会话后建订单；返回地址由 `ServerAddress` 拼成；`ServerAddress` 不合格（非绝对地址、localhost、live 密钥配 http）或 `pay_link` 不是 `https://*.wooshpay.com` 时拒绝且不建订单；8 位金额上限只约束实扣 |
| `TestRequestWooshpayPayRefusesTestOrderBeforeAddressDiagnosis` | 同上 | 测试密钥下非管理员先得到测试模式提示，拿不到服务器地址提示，也不记地址 Error；管理员得到地址提示 |
| `TestWooshpayTestPaymentCreditsOnlyAdministratorOrders` | 同上 | 测试模式付款只给管理员的订单入账：管理员凭 PaymentIntent 补单时，非管理员订单返回可翻译的拒绝提示、不入账，管理员订单照常补单；Webhook 对非管理员订单返回 200 不入账，英文和 zh-CN 日志都带 `alert=wooshpay_test_payment_refused`，不带 `alert=wooshpay_paid_not_credited` |
| `TestWooshpayCheckoutFailureLogsAlertMarker` | 同上 | 建收银台失败、返回地址无法生成的 Error 在英文和 `zh-CN` 日志里都带 `alert=wooshpay_checkout_failed` |
| `TestRequestWooshpayPayOnDisabledSiteLogsNoAddressError` | 同上 | WooshPay 没打开的站点 `/pay` 返回 “WooshPay payment is not enabled”，不记 `ServerAddress` 的 Error |
| `TestWooshpayTestModeIsAdminOnly` | 同上 | 测试密钥下非管理员 `/amount`、`/pay` 被拒、充值信息里不显示 WooshPay（包括 `PayMethods` 里配置的项）；管理员可以下单；`GetOptions` 只返回派生的 `WooshpayTestMode`，不回显密钥和库里的同名行；`WooshpayTestMode` 不能被任何保存接口写入 |
| `TestWooshpayTopUpInfoPricesForTheUser` | 同上 | `wooshpay_unit_price` = 单价 × 用户充值分组倍率；追加 WooshPay 不写入全局 `PayMethods` |
| `TestWooshpayTopUpInfoHidesUnavailableWooshpay` | 同上 | 不可用时删掉 `PayMethods` 里的 wooshpay 项、`/amount` 拒绝；已配置时不重复追加；读分组失败只隐藏 WooshPay |
| `TestWooshpayTokensDisplayOrderCreditsRequestedTokens` | 同上 | Tokens 展示模式下按购买数量限额、计价，不是 `QuotaPerUnit` 整数倍时 `/amount`、`/pay` 拒绝；分组倍率只放大价格，入账额度等于输入的 token 数 |
| `TestWooshpayFulfillRejectsMismatchedIntent` | 同上（用 httptest 模拟 WooshPay API） | 状态不是 succeeded、订单号不一致、金额不一致、币种不是 CNY、livemode 不一致、非本站 PaymentIntent、订单用户已删除，以及 metadata 与实收一致但不等于订单 `Money` 时，都**不入账** |
| `TestWooshpayFulfillIsIdempotent` | 同上 | 同一事件投递两次只入账一次；WooshPay 断开连接不中断入账；`payment_intent.canceled`/`payment_failed` 不关单；会话过期后到达的成功付款仍入账一次 |
| `TestWooshpayFulfillFallsBackForMissingFields` | 同上 | succeeded 但缺 `amount_received` 按 `amount` 入账；缺 `livemode` 按密钥模式 |
| `TestWooshpayWebhookRetriesTransientDatabaseError` | 同上 | 入账时数据库临时故障返回 500，重投后只入账一次 |
| `TestWooshpayRequestsUseOneSettingsSnapshot` | 同上 | 下单和回查过程中改设置，不会混用新旧单价、密钥和模式 |
| `TestWooshpayRejectedWebhookLogsAlertMarker` | 同上 | `webhook_disabled` 和验签失败的 Warn 在英文和 `zh-CN` 日志里都带 `alert=wooshpay_webhook_rejected`；路径只记 `URL.Path`，最多 256 字节，不含查询串 |
| `TestWooshpayOptionsRejectInvalidValues` | 同上 | 各配置项的取值校验；密钥与 API Base 交叉校验；整组保存被拒时一项不写；打开 WooshPay 时 `ServerAddress` 的三条规则；WooshPay 打开时单独保存不合格的 `ServerAddress` 照常成功并给出 `invalid_server_address`，此时保存 WooshPay 项被拒；审计日志不记值；库里密钥与 API Base 错配时仍可关闭 WooshPay |
| `TestAdminCompleteWooshpayOrderRequiresPaymentProof` | 同上 | `pending`/`expired`/`failed` 订单都只能凭回查成功的 PaymentIntent 补单，只入账一次；不带 PaymentIntent 和各种不符返回对应的可翻译提示；充值日志记为管理员补单 |
| `TestWooshpayGroupRatioScalesPriceNotCredit` | `controller/topup_quota_limit_test.go` | 分组倍率只放大应付金额，入账 = 购买数量 × QPU |
| `TestWooshpayWebhookEnabledRequiresTopUpAndWebhookConfig` | `controller/payment_webhook_availability_test.go` | 必须同时满足合规确认、配置完整，且密钥模式与 API Base 对应；关掉开关仍收款；新充值还要求 `ServerAddress` 满足 §4.1 的三条规则（含 IPv6 zone 和 `localhost.localdomain`） |
| `TestWooshpayUnavailableReasonNamesTheUnmetRequirement` | 同上 | 开关打开时 `WooshpayUnavailableReason` 给出各条件对应的机器码，可用或关闭时为空；该项只读 |
| `TestRechargeWooshpay_RejectsMismatchedPaymentMethod` | `model/payment_method_guard_test.go` | 其他渠道的订单不能走 wooshpay 入账 |
| `TestRechargeWooshpay_CompletesClosedOrdersOnce` | 同上 | pending / expired / failed 订单回查成功后都入账一次，已 success 不重复入账；实收不等于 `Money` 时不入账；测试模式付款不给非管理员订单入账（含 `byAdmin=true`） |
| `UpdatePendingTopUpStatus` 表新增 wooshpay 用例 | 同上 | expire 和 fail 只对本渠道的 pending 订单生效 |
| `TestPaidQuotaFromCharge` / `TestResolveTopUpProvider` 新增用例 | `model/rebate_test.go` | 返佣快照 = `Money ÷ WooshpayUnitPrice × QPU`；测试密钥下快照为 0 |
| `TestGatewayChargeAdjustsPaidSnapshot` 新增 wooshpay 用例 | 同上 | Webhook 入账时测试模式把快照清零 |
| 路由列表 | `router/access_token_scope_test.go` | webhook 是匿名回调路由 |

前端测试：

| 文件 | 内容 |
|---|---|
| `features/wallet/lib/payment.test.ts` | 默认类型、最低充值、预设卡片计价、前端金额校验 |
| `features/wallet/hooks/use-payment.test.ts` | `/amount` 走 WooshPay 计算器、被拒时不当作 0、CNY 标记、当前页跳转 |
| `features/wallet/__tests__/payment-confirm-currency.test.tsx` | 确认框、预设卡片、金额栏按 CNY 显示 |
| `features/wallet/__tests__/billing-history-complete.test.tsx` | WooshPay 订单（`pending`/`expired`/`failed`）补单必须填 PaymentIntent ID |
| `features/system-settings/integrations/__tests__/payment-settings-wooshpay.test.tsx` | 没有币种项；一次请求保存全部改动；保存失败不影响 Waffo Pancake；测试密钥警告；每个不可用机器码的警告 |

检查命令（Go 1.26，与 `go.mod` 一致；`CGO_ENABLED=0`）：

```bash
go build ./... && go vet ./controller/ ./model/ ./setting/...
go test ./controller/... ./model/... ./setting/... ./middleware/... ./router/... -count=1
cd web && bunx vitest run src/features/wallet src/features/system-settings && bun run lint && bun run build
```

`./router/...` 里的 `TestAccessTokenRouteRulesCoverEveryDashboardRoute`（“panel route GET /api/system_banner must be declared exactly once”）在 `ikun-new` 上本来就失败，与 WooshPay 无关。

`bun run i18n:sync` 会按 `en.json` 重排并改写全部语言文件，不是只读检查；只在增删文案后需要整理时运行，运行后检查 diff，并确认报告（`web/src/i18n/locales/_reports/_sync-report.json`）里各语言 `missingCount`、`extrasCount` 都是 0。

**三种数据库**：`controller` 包里用 `setupManageUserTestDB` 建库的测试（包括上面所有 WooshPay 控制器测试）按环境变量切换方言，默认 SQLite。MySQL / PostgreSQL 可以用 `ikun_rebate_v2` 仓库的 `docker-compose.test.yml` 起测试容器（MySQL 8.0 映射 `13306`，PostgreSQL 16 映射 `15432`，密码都是 `rebate`）：

```bash
# 在 ikun_rebate_v2 目录
docker compose -f docker-compose.test.yml up -d mysql pg

# 回到 new-api 目录
TEST_MANAGE_USER_DIALECT=sqlite   go test ./controller/ -run 'Wooshpay|TopUp|Rebate|Payment|ManageUser' -count=1
TEST_MANAGE_USER_DIALECT=mysql    TEST_MYSQL_DSN='root:rebate@tcp(127.0.0.1:13306)/mysql?parseTime=true' \
  go test -p 1 ./controller/ -run 'Wooshpay|TopUp|Rebate|Payment|ManageUser' -count=1
TEST_MANAGE_USER_DIALECT=postgres TEST_POSTGRES_DSN='postgres://postgres:rebate@127.0.0.1:15432/postgres?sslmode=disable' \
  go test -p 1 ./controller/ -run 'Wooshpay|TopUp|Rebate|Payment|ManageUser' -count=1
# 主库和日志库分开的情形：上面任一条再加 TEST_MANAGE_USER_SEPARATE_LOG_DB=1
```

夹具在 DSN 指向的实例里为每个测试另建临时库。`model` 包的测试只跑 SQLite（不读这些变量）。`REBATE_MYSQL_TEST_DSN` 是另一回事：它只启用 `TestRebateMySQLControllerContracts`，要求库名以 `rebate_` 开头、是已初始化返利表的专用回放库（例如 `.../rebate_replay`），不能指向上面的临时库实例根库。

---

## 10. 部署与运维

0. **先升级返佣结算服务**：`ikun_rebate_v2` 升级到 `feat/wooshpay-provider` 分支的 `cdd10a1`（支持 `wooshpay` 渠道）之后，才能产生任何 WooshPay 订单，`sk_test_` 测试订单也一样。WooshPay 开关默认关闭，确认结算服务已升级后再打开。
1. 在 WooshPay 控制台拿到 `sk_test_` 密钥（Secret Key）。
2. 注册 Webhook。可以在控制台 Developer → Webhook 里创建，也可以调接口。测试环境和生产环境要**各注册一次**，同一个 URL 不要重复注册，否则会收到重复通知：
   ```bash
   curl https://apitest.wooshpay.com/v1/webhook_endpoints -u sk_test_xxx: \
     -H 'Content-Type: application/json' \
     -d '{"url":"https://<你的域名>/api/wooshpay/webhook","enabled_events":["payment_intent.succeeded","checkout.session.expired","payment_intent.canceled","payment_intent.payment_failed"],"api_version":"1.0.1"}'
   ```
   必需事件是 `payment_intent.succeeded`（入账）和 `checkout.session.expired`（关单）；`payment_intent.canceled`、`payment_intent.payment_failed` 可选，只记日志，与后台 WooshPay 页的提示一致。返回的 `secret`（`whsec_...`）就是 Webhook Secret。
   后台 WooshPay 页要填两个密钥：第 1 步的 Secret Key，以及这里的 Webhook Secret（整串填写）。
3. 在系统设置里把服务器地址（`ServerAddress`）设为站点对外的绝对 `https://` 地址（如 `https://<你的域名>`）。收银台的 `success_url` / `cancel_url` 由它拼成。它不是绝对 `http(s)` 地址、主机是本机（§4.1 表里的各种写法）、或用 `sk_live_` 密钥却不是 `https://` 时，WooshPay 无法打开，充值按钮也不显示；WooshPay 打开后再把它改成这样的地址，保存不会被拒，但新充值会停止，后台显示 `invalid_server_address` 警告（§4.1）。
   - 本地开发也一样：需要一个公网可访问的 `https://` 隧道域名（如 cloudflared、ngrok）作为 `ServerAddress`，WooshPay 的 Webhook 也注册到这个域名。
   - 修改 `ServerAddress` 同时会更新 Passkey 的默认 origins / RP ID（保存走 `UpdatePasskeyDomainOptions`），已注册的 Passkey 可能需要按提示确认域名移除；在生产站点上改这个值前先确认影响。
4. 确认 WooshPay 账号已开通 CNY 收款（报价单 / 入网结果）。后台填写 API Base、人民币单价、最低充值，打开 WooshPay 开关并保存（各项一次保存）。
5. **支付合规确认是单独的一步**：在支付设置页顶部的合规确认对话框里确认条款。没确认时 WooshPay 开关可以保存，但不会生效（充值页没有 WooshPay，Webhook 返回 403），后台 WooshPay 页会显示 “WooshPay is not available” 警告，原因是 `compliance_not_confirmed`。其他未满足的条件也会在这里按机器码提示（§4.1）。
6. Webhook URL 必须是公网可访问的 HTTPS 地址；如果前面有 CDN 或 WAF，要放行 `/api/wooshpay/webhook`，并且**不能改写请求体**，否则验签必然失败。
7. **测试付款会充入真实额度**：`sk_test_` 下只有管理员能下单（普通用户看不到 WooshPay），联调完要处理测试账号上多出来的额度。测试订单在系统里是真实的 `success` 充值记录，会计入充值和营收统计，对账时要排除（返佣快照为 0，不产生返佣）。测试环境只能用报价单上列出的支付方式；用测试卡联调（官方 Testing cards 页面）：成功、失败后换卡重试、放着不付等它过期、重复投递，这四种情况都要走一遍。
8. 上线生产：换成 `sk_live_` 密钥和生产 API Base，重新注册生产 Webhook 并更新 secret。上线后在测试环境（`apitest`）控制台里停用或删除指向本站的 Webhook 端点，避免测试环境的事件继续投递到生产站点（验签会失败并触发 `alert=wooshpay_webhook_rejected`）。
9. **上线后不要再换回 `sk_test_` 密钥**：密钥、API Base 和 Webhook Secret 换回测试环境后，正在进行的生产付款的 Webhook 会验签失败（或回查时用测试密钥查不到生产的 PaymentIntent），返回非 2xx，WooshPay 只在 72 小时内重投；管理员凭 PaymentIntent 补单也同样无法核实这些生产付款。只有换回生产配置后它们才能入账，超过 72 小时的只能靠管理员补单。

**日志告警**：

- `alert=wooshpay_paid_not_credited`：钱可能已经到账但没有入账，需要人工处理。这个标记不翻译，在英文日志和 `DEFAULT_LANGUAGE=zh-CN` 的中文日志里位置相同（紧跟在说明文字之后），按它做关键字告警即可。出现在：会话已建但订单写入失败；PaymentIntent 已成功但与订单不一致（`mismatch=` 字段说明哪一项）；已成功但没有金额（会重投，持续出现才需处理）；订单无法完成（订单不存在、渠道不符、实收与订单金额不符、额度超限、用户已删除，见 `error=`）。测试付款给非管理员订单的情况改记 `alert=wooshpay_test_payment_refused`（见下）。
- `alert=wooshpay_test_payment_refused`：Webhook 收到测试模式（`livemode=false`）的付款，但订单的用户不是管理员，不入账，返回 200。测试付款没有真实资金，所以不算“已付款未入账”，不带 `alert=wooshpay_paid_not_credited`。Warn 日志：`WooshPay test payment for an order of a non-administrator refused, not credited alert=wooshpay_test_payment_refused trade_no=... payment_intent=... user_id=... client_ip=...`，标记同样不翻译，英文和中文日志里位置相同。一般是订单创建后才换成测试密钥，或测试环境的事件投递到了生产站点；确需补偿时直接调整该用户额度。
- `alert=wooshpay_checkout_failed`：顾客点了 WooshPay 但收银台没建成，钱没有付出去，但 WooshPay 实际上无法使用。同样不翻译，英文和中文日志里位置相同。出现在两处：`WooshPay failed to create Checkout Session alert=wooshpay_checkout_failed ...`（WooshPay 接口返回错误，`error=` 里有 `type`、`code`，例如账号未开通 CNY）；`WooshPay checkout refused, the return URL cannot be built, set the server address in system settings alert=wooshpay_checkout_failed ...`（`ServerAddress` 不合格，只有管理员或正式密钥下的用户会触发）。持续出现时检查 WooshPay 账号和系统设置；后台 WooshPay 页的不可用警告也会说明配置上的原因。
- 持续出现的重投错误：`WooshPay failed to retrieve PaymentIntent`（回查失败）、`WooshPay top-up processing failed`（入账时数据库故障）、`WooshPay PaymentIntent is not conclusively paid yet`、`WooshPay failed to expire top-up order`。偶发可以忽略，同一 `trade_no` / `payment_intent` 持续出现说明 WooshPay 一直在重投却无法完成。
- `alert=wooshpay_webhook_rejected`：WooshPay 的回调被拒绝，付款可能一直不入账。同样不翻译，英文和中文日志里都紧跟在说明文字之后。出现在两处：
  - `WooshPay webhook rejected alert=wooshpay_webhook_rejected reason=webhook_disabled path=...`：WooshPay 配置不完整（或未确认支付合规）时收到了回调，返回 403，已打开的收银台付款会一直重投直到 72 小时后放弃。注意关掉 WooshPay 开关**不会**导致拒收，开关只管新充值。`path` 只记 `URL.Path`（不含查询串），最多 256 字节。
  - `WooshPay webhook signature verification failed alert=wooshpay_webhook_rejected ...`：验签失败，一般是 Webhook Secret 填错或者 CDN/WAF 改写了请求体（偶发的扫描请求也会触发，持续出现才需处理）。

**对账兜底**：Webhook 重投 72 小时后就停止。如果一直没收到成功通知，管理员可以在 WooshPay 控制台找到该订单的 PaymentIntent，在“账单记录”里用“完成订单”入账：无论 `pending`、`expired` 还是 `failed`，都必须填写 PaymentIntent ID，经回查校验后入账（见 §5.5）。第二期可以加一个定时任务，用 Search PaymentIntents 按 `merchant_order_id` 自动对账。

---

## 11. 不在第一期范围

- **订阅支付**：参照 `controller/subscription_payment_stripe.go`。届时在 `wooshpayFulfill` 里先尝试 `model.CompleteSubscriptionOrder`，返回 `ErrSubscriptionOrderNotFound` 再走充值入账，逻辑与 Stripe 的 `fulfillOrder` 相同。
- **退款**：WooshPay 支持 `POST` refunds；但项目当前没有统一的线上退款流程，暂不接入。
- **拒付**：`charge.dispute.*` 事件暂时只记日志。

---

## 12. 已与 WooshPay 确认的事项

| # | 问题 | 结论 | 对实现的影响 |
|---|---|---|---|
| 1 | 请求体格式 | JSON | 所有请求都用 `Content-Type: application/json` |
| 2 | 签名头名称 | `Wooshpay-Signature`（测试环境联调一致）；部分文档写作 `Signature` | 先读 `Wooshpay-Signature`，没有时读 `Signature` |
| 3 | Webhook 密钥格式 | `whsec_yDjRWBxxxx` | 整串原样当作 HMAC key，不做解码 |
| 4 | 签名时间戳 | 单位是毫秒，只作为盐值参与签名 | 不做时间窗口校验；防重放靠回查和幂等（§2.4） |
| 5 | 币种和支付方式 | 测试环境按报价单，生产环境按入网结果 | 本站固定按 CNY 收款，账号必须开通 CNY；上线前按入网结果核对 |
| 6 | 注册 Webhook | `POST /v1/webhook_endpoints`，或在控制台 Developer → Webhook 里创建 | 见 §10 第 2 步 |
