# WooshPay 充值接入开发文档

> 适用分支：`ikun-new`
> 范围：钱包在线充值（一次性支付）。订阅支付不在第一期范围内，见 §11。
> 参考实现：`controller/topup_stripe.go`。WooshPay 的接口形态与 Stripe 高度一致，本文所有设计都尽量与 Stripe 链路保持对称，降低评审与维护成本。

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
2. **验签 + 回查双保险**：Webhook 验签通过后，再用我方密钥调 `GET /v1/payment_intents/{id}` 回查，校验状态、订单号、金额、币种全部一致才入账。
3. **幂等入账**：复用 `LockOrder` 进程锁和 `lockForUpdate` 行锁；已经 `success` 的订单不再入账，重复通知直接返回 200。回查确认已支付的订单即使已被置为 `expired`/`failed` 也照常入账（钱确实到了）。

---

## 2. WooshPay 接口要点（已对照官方文档核实）

| 项目 | 内容 |
|---|---|
| 测试域名 | `https://apitest.wooshpay.com`，收银台 `checkouttest.wooshpay.com` |
| 生产域名 | `https://api.wooshpay.com` |
| 鉴权 | HTTP Basic：用户名 = `sk_test_xxx` / `sk_live_xxx`，密码为空 → `Authorization: Basic base64("sk_xxx:")` |
| 请求体 | `application/json`（已与 WooshPay 确认） |
| 幂等 | 支持 `Idempotency-Key` 请求头（错误类型 `idempotency_error`、HTTP 409） |
| 金额单位 | `unit_amount` 是**最小货币单位（分）**。各币种小数位见 Supported currencies：USD/CNY/HKD 是 2 位，JPY/KRW 是 0 位 |
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
  "success_url": "https://example.com/usage-logs",
  "cancel_url": "https://example.com/wallet",
  "client_reference_id": "wsp_9f2c...",
  "customer_email": "user@example.com",
  "line_items": [
    {
      "price_data": {
        "currency": "USD",
        "unit_amount": 800,
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
      "new_api_amount_listed": "800",
      "new_api_amount_charged": "800",
      "new_api_currency": "USD"
    }
  },
  "metadata": { "new_api_trade_no": "wsp_9f2c..." }
}
```

响应里会用到这些字段：`id`（`cs_...`）、`url`（跳转地址）、`payment_intent`（`pi_...`）、`status`（`open`）。

`new_api_amount_listed`（未折扣的标价，最小货币单位）只用于对账：超出 int32 范围时不写这一项（见 §6.1）。

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

以下几点已与 WooshPay 确认：

- 请求头：`Signature: t=<毫秒时间戳>,v1=<hex>`，只读这一个头。
- 算法：`hex(HMAC_SHA256(key = webhook.secret, msg = t + "." + rawBody))`。
- 密钥：形如 `whsec_yDjRWBxxxx`，取自创建 Webhook 时返回的 `secret` 字段。**整串原样作为 key 的字节使用**，包括 `whsec_` 前缀，不做解码。
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

var WooshpaySecretKey = ""          // sk_test_xxx / sk_live_xxx
var WooshpayWebhookSecret = ""      // webhook_endpoint.secret
var WooshpayApiBase = "https://api.wooshpay.com" // 测试环境填 https://apitest.wooshpay.com
var WooshpayCurrency = "USD"        // ISO 4217；测试环境按报价单上的币种，生产环境按入网（onboarding）结果
var WooshpayUnitPrice = 8.0         // 每 1 单位充值额对应的标价（WooshpayCurrency 计价）
var WooshpayMinTopUp = 1            // 最低充值数量
```

测试模式由密钥前缀推导（`strings.HasPrefix(WooshpaySecretKey, "sk_test_")`），不再单独设开关，避免“生产密钥 + 测试开关”这种配置不一致。密钥与 API Base 必须对应：`sk_test_` 只能配 `apitest.wooshpay.com`，`sk_live_` 只能配 `api.wooshpay.com`，否则视为未启用。`WooshpayApiBase` 保存时去掉首尾空白和末尾的 `/`；`WooshpayWebhookSecret` 保存时和参与 HMAC 计算时都去掉首尾空白。

保存时交叉校验（`setting.ValidateWooshpayKeyBase`，由 `model.UpdateOption` / `model.UpdateOptionsBulk` 调用）：单独保存密钥或 API Base 时，与库里另一项的当前值组合；组合是已知的错配（`sk_test_` 配非 `apitest` 主机，`sk_live_` 配非 `api` 主机）就拒绝保存。前缀不是这两种的密钥不算已知错配，只是保持未启用。因为两项分开保存，测试与生产之间切换时任何一项先保存都会被拒绝，所以后台填写了新密钥时，前端用 `PUT /api/option/wooshpay/environment`（`{"secret_key","api_base"}`）把密钥和表单里的 API Base 一起保存（`UpdateOptionsBulk`，同一事务，按合并后的组合校验）；只改 API Base 时仍走普通的单项保存。

### 4.2 `model/option.go`

- `InitOptionMap`：把上面 6 个配置项全部注册进去。其中 `WooshpaySecretKey` 和 `WooshpayWebhookSecret` 照 Stripe 密钥的方式处理，不回显给前端。
- `updateOptionMap` 的 switch：加对应的 case，`WooshpayUnitPrice` 用 `strconv.ParseFloat`，`WooshpayMinTopUp` 用 `strconv.Atoi`。
- 保存时校验：`WooshpayCurrency` 必须是 WooshPay 支持的大写 ISO 4217 代码；`WooshpayUnitPrice` 必须大于 0 且是有限值；`WooshpayMinTopUp` 必须是 1 到 10000（单笔购买数量上限）之间的整数；`WooshpayApiBase` 必须是 `https://` 开头；密钥与 API Base 的交叉校验见 §4.1。
- 从数据库加载的 `WooshpayCurrency` 不是受支持的币种时，`isWooshpayTopUpEnabled` 返回 false（WooshPay 不启用），直到管理员改正。

### 4.3 可用性判断：`controller/payment_webhook_availability.go`

```go
func isWooshpayTopUpEnabled() bool {
	if !isPaymentComplianceConfirmed() {
		return false
	}
	return strings.TrimSpace(setting.WooshpaySecretKey) != "" &&
		strings.TrimSpace(setting.WooshpayWebhookSecret) != "" &&
		setting.WooshpayUnitPrice > 0 &&
		setting.ValidateWooshpayOption("WooshpayCurrency", setting.WooshpayCurrency) == nil &&
		setting.WooshpayApiBaseMatchesKey() // sk_test_ ⇔ apitest.wooshpay.com，sk_live_ ⇔ api.wooshpay.com
}

func isWooshpayWebhookEnabled() bool {
	return isWooshpayTopUpEnabled()
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
| `Money` | 实付金额 = `Amount × WooshpayUnitPrice × TopupGroupRatio × 折扣`，按币种小数位取整后的值（`WooshpayCurrency` 计价），与 Waffo Pancake 的 `Money: payMoney` 一致 |
| `TradeNo` | `"wsp_" + common.Sha1("new-api-wsp-{uid}-{unixMilli}-{rand4}")` |
| `PaymentMethod` / `PaymentProvider` | `wooshpay` |
| `Status` | 创建时为 `pending` |

不新增数据库列。网关侧 ID（`cs_`、`pi_`）不落库，订单与网关之间靠 metadata 和 `merchant_order_id` 绑定。所以**不需要迁移**，三种数据库都没有兼容性风险。

### 5.3 新增 `RechargeWooshpay`

`Recharge` 写死了只接受 Stripe 订单（`model/topup.go:270`），还会写 `stripe_customer`，不能直接复用。参照 `RechargeCreem` 和 `RechargeWaffoPancake` 另写一个同级函数：

```go
func RechargeWooshpay(tradeNo string, callerIp string, charge GatewayCharge) (err error) {
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
			return errors.New("top-up order not found")
		}
		if topUp.PaymentProvider != PaymentProviderWooshpay {
			return ErrPaymentMethodMismatch
		}
		// 回查已确认付款成功，pending / expired / failed 都可以完成；
		// 已 success 的返回 ErrTopUpStatusInvalid，调用方据此返回 200，不重复入账
		if topUp.Status == common.TopUpStatusSuccess {
			return ErrTopUpStatusInvalid
		}
		// quota = Amount × QuotaPerUnit，严格换算，与 RechargeWaffoPancake / 易支付相同
		// applyGatewayCharge(tx, topUp, charge)
		// creditTopUpQuota(tx, topUp.UserId, quota, nil)   // 不写 stripe_customer
		// 更新 Status=success、CompleteTime
		return nil
	})
	// 提交后：syncCreditUserQuotaCache(..., "wooshpay topup")
	//         RecordTopupLog(..., topUp.PaymentMethod, PaymentMethodWooshpay)
	//         日志里的“支付金额”是 amount_received 按币种小数位换算后的值加币种，如 "160.00 USD"、"1.234 KWD"、"800 JPY"
	return err
}
```

额度换算必须用 `common.WalletQuotaFromDecimalStrict`（和 `Recharge` 一致），不允许出现 `int(...)` 这类裸强转。

### 5.4 返佣快照：`model/rebate.go`

- `paidQuotaFromCharge`：加 `case PaymentProviderWooshpay: unitPrice = setting.WooshpayUnitPrice`，与 Waffo Pancake 相同，实付快照 = `Money ÷ WooshpayUnitPrice × QuotaPerUnit`。`Money` 已经是折扣后的实付金额，所以 Webhook 传入的 `GatewayCharge` 是“期望扣款 vs 实际到账”（两者经回查校验相等），只有测试模式会把快照清零。
- `resolveTopUpProvider`：加 `case PaymentMethodWooshpay: return PaymentProviderWooshpay`。
- `TopUp.AfterCreate`：测试密钥（`sk_test_`）下单时，`paid = 0`，与 Waffo 沙箱、Creem 测试模式的处理一致。

### 5.5 管理员手工补单：`ManualCompleteTopUp`（`model/topup.go:462`）

三处分支都与 Waffo Pancake 归为一组：

1. 入账额度 = `Amount × QPU`（默认分支）。
2. 填了实付金额时，标价 = `Money`（实付按标价比例缩放快照）。
3. 没填实付金额时，与易支付一样保留下单时的快照（WooshPay 由本站定价，`Money` 就是发给网关的金额；`sk_test_` 订单的快照本来就是 0），不再按 `GatewayCharge{Listed:1, Charged:0}` 清零。

WooshPay 订单在 `pending`、`expired`、`failed` 状态下都可以补单（会话过期只关单，钱仍可能到账，而 Webhook 72 小时后停止重投）；其他渠道仍只处理 `pending`；已 `success` 的订单不会再次入账。后台“账单记录”里 WooshPay 的 `expired`/`failed` 订单同样显示“完成订单”按钮。

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
units    = amount                                   (USD/CNY 展示)
         = amount ÷ QuotaPerUnit                    (Tokens 展示，amount 是 token 数，必须是 QuotaPerUnit 的整数倍，否则拒绝)
payMoney = units × WooshpayUnitPrice × topupGroupRatio × discount
           discount 取 AmountDiscount[amount]（按用户输入的展示值查，与易支付相同）
listed   = 同上，但 discount = 1
credited = units × QuotaPerUnit
```

与易支付的一处差异：易支付在 Tokens 模式下按未取整的 `amount ÷ QPU` 计价、按取整后的单位入账；WooshPay 直接拒绝不是整数单位的输入，计价和入账都用同一个 `units`，保证付的钱与到账额度一致。

`unit_amount` 换算成最小货币单位，**必须用 decimal**，不允许用 float 直接乘：

```go
digits := currencyMinorDigits(setting.WooshpayCurrency) // 0 或 2；未知币种直接拒绝下单
cents := decimal.NewFromFloat(payMoney).Shift(int32(digits)).Round(0).IntPart()
```

金额边界（计费安全要求）：

- `amount` 不小于 `getWooshpayMinTopup()`（展示单位；Tokens 模式下为 `WooshpayMinTopUp × QPU`），且 `units` 在 `[WooshpayMinTopUp, 10000]` 区间内；上限作用于购买数量，超限提示按展示单位给出。
- Tokens 展示模式下 `amount` 必须是 `QuotaPerUnit` 的整数倍。
- 只约束实际扣款：折扣后的 `cents` 必须 `> 0`，并且不超过 `math.MaxInt32`，否则返回错误。未折扣的标价（`listed`）不参与校验：例如 IDR 单价 16,000,000、购买 100 个单位时标价是 160,000,000,000 sen，超出 int32，但 0.01 的预设折扣后实扣 1,600,000,000 sen，订单照常创建，metadata 里不写 `new_api_amount_listed`。
- 调用 `rejectInvalidCreditedQuota(c, uid, getWooshpayCreditedQuota(amount))`（`units × QPU`，不含分组倍率），提前挡住会让钱包溢出的订单。

### 6.2 `RequestWooshpayAmount`：`POST /api/user/wooshpay/amount`

逻辑与 `StripeAdaptor.RequestAmount` 相同，返回 `{"message":"success","data":"8.00"}`。

### 6.3 `RequestWooshpayPay`：`POST /api/user/wooshpay/pay`

1. 如果 `!isWooshpayTopUpEnabled()`，直接返回错误。
2. 要求 `req.PaymentMethod == model.PaymentMethodWooshpay`；检查金额上下限。
3. 用 `common.ValidateRedirectURL` 校验 SuccessURL 和 CancelURL。为空时分别默认 `paymentReturnPath("/usage-logs")` 和 `paymentReturnPath("/wallet")`。
4. 生成 TradeNo，按 §6.1 计算 `listed`、`cents`。
5. **先调网关，成功后再插入订单**（顺序和 Stripe 一样，避免留下没有支付链接的悬空订单）：
   - 调用 `POST /v1/checkout/sessions`，带 `Idempotency-Key: <TradeNo>`，请求体见 §2.2；不传 `expires_at`，会话按 WooshPay 默认 24h 过期（WooshPay 时间戳是毫秒级，传秒级会出错）。
   - 网关失败时返回通用错误。日志里记下 `error.type`、`error.code`，但不要记密钥。
6. `model.TopUp{...}.Insert()`，返回 `{"message":"success","data":{"pay_link": session.url}}`。插入失败时会话已存在，用 Error 日志记录 trade_no 和 session_id，提示“付款不会自动入账，需要人工处理”。

### 6.4 WooshPay HTTP 客户端

逻辑留在本文件里；如果之后订阅支付也要用，再移到 `service/wooshpay/`。要求：

- 使用项目里现有的 HTTP client（能读到代理配置的那个），超时 15 秒。
- 请求头：`Authorization: Basic base64(secret+":")`、`Content-Type: application/json`、`Accept: application/json`。
- 序列化和反序列化**必须用 `common.Marshal` / `common.Unmarshal`**，不允许直接调用 `encoding/json`。
- 非 2xx 响应解析成 `{error:{type,code,message}}` 并返回 error。

只解析会用到的字段：

```go
type wooshpaySession struct {
	ID            string `json:"id"`
	URL           string `json:"url"`
	Status        string `json:"status"`
	PaymentIntent string `json:"payment_intent"`
	Livemode      bool   `json:"livemode"`
	Metadata      map[string]string `json:"metadata"`
}

type wooshpayPaymentIntent struct {
	ID              string            `json:"id"`
	Status          string            `json:"status"`
	Amount          int64             `json:"amount"`
	AmountReceived  *int64            `json:"amount_received"` // 缺失时为 nil，视为尚未确认
	Currency        string            `json:"currency"`
	Livemode        *bool             `json:"livemode"`        // 同上
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
		c.AbortWithStatus(http.StatusForbidden)
		return
	}
	payload, err := io.ReadAll(c.Request.Body)       // 路由层已加 anonymousRequestBodyLimit
	if err != nil { c.AbortWithStatus(http.StatusServiceUnavailable); return }

	sig := c.GetHeader("Signature")
	if err := verifyWooshpaySignature(payload, sig, setting.WooshpayWebhookSecret); err != nil {
		logger.LogWarn(ctx, ...)                     // 不记录 secret
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
		err = wooshpayFulfill(ctx, event.Data.Object, c.ClientIP())
	case "checkout.session.expired":
		err = wooshpayExpireOrder(ctx, event.Data.Object)
	case "payment_intent.payment_failed", "payment_intent.canceled":
		// 只记日志，订单保持 pending
	default:
		// 记日志后忽略
	}
	if err != nil && isRetryable(err) {
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
   - `status != "succeeded"`（如 `processing`、`requires_*`），或 `amount_received` / `livemode` 缺失：还不能下结论，Warn 日志后返回 500（`errWooshpayRetry`），让 WooshPay 重投。
   - 已 `succeeded` 且字段齐全，但以下任一项不一致：Error 日志（“已付款未入账，需要人工处理”，带 trade_no、pi id 和不一致的字段），返回 200，不入账：
     - `pi.ID` 等于事件里的 id
     - `tradeNo` 等于 `pi.MerchantOrderID`
     - `strings.EqualFold(pi.Currency, pi.Metadata["new_api_currency"])`
     - `*pi.AmountReceived == parse(pi.Metadata["new_api_amount_charged"])`
     - `*pi.Livemode == !isWooshpayTestKey()`
4. `LockOrder(tradeNo)`，函数退出时 `UnlockOrder(tradeNo)`。
5. `charge := model.GatewayCharge{Listed: metadata.charged, Charged: pi.AmountReceived, Test: !pi.Livemode}`（两者已校验相等，只有测试模式会改快照）；`new_api_amount_listed` 只用于对账，不参与计算。
6. 调用 `model.RechargeWooshpay(tradeNo, callerIp, paymentAmount, charge)`，`paymentAmount` 是 `amount_received` 用 decimal 按币种小数位换算后的字符串加币种：
   - 返回 `ErrTopUpStatusInvalid`（订单已 success）：属于重复通知，Info 日志后返回 200。
   - 返回 `ErrTopUpNotFound` / `ErrPaymentMethodMismatch`，或订单的用户已不存在（`gorm.ErrRecordNotFound`）：Error 日志（已付款未入账，需要人工处理）后返回 200，重投也不会改变结果。
   - 其他 DB 错误（真正的临时故障）：Error 日志后返回 500。

> metadata 由我方用私钥写入，而且是**从 API 回查**拿到的（不是用回调体里的数据），客户无法篡改。“实际到账金额”与“下单时我方期望扣款金额”相互印证，所以不需要新增数据库列来存期望金额。

**`wooshpayExpireOrder`**：

- 只处理 `checkout.session.expired`，从 Session 的 `metadata.new_api_trade_no` 取订单号。
- 先 `LockOrder`，再调 `model.UpdatePendingTopUpStatus(tradeNo, model.PaymentProviderWooshpay, common.TopUpStatusExpired)`。这个函数已经做了服务商校验，并且只改 pending 订单；只有记录不存在才返回 `ErrTopUpNotFound`，其他数据库错误原样返回，Webhook 回 500 让 WooshPay 重投。

### 6.6 充值信息接口：`controller/topup.go`

- `GetTopUpInfo`：`isWooshpayTopUpEnabled()` 为真时，往 `pay_methods` 追加 `{"name":"WooshPay","type":"wooshpay","color":"#1F6FEB","min_topup":getWooshpayMinTopup()}`。
- 同时返回 `enable_wooshpay_topup`、`wooshpay_min_topup`。
- `controller/misc.go` 的 `GetStatus` 里加 `wooshpay_unit_price`、`wooshpay_currency`，供前端显示价格。

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
  - 一条静态提示：密钥和 API Base 属于同一环境之前 WooshPay 保持停用（`sk_test_` 配 `apitest`，`sk_live_` 配 `api`）
  - 保存：填写了新密钥时，密钥与 API Base 通过 `PUT /api/option/wooshpay/environment` 一起保存（见 §4.1）；`WooshpayCurrency`、`WooshpayUnitPrice` 保存后会刷新 `/api/status` 缓存（`use-update-option.ts` 的 `STATUS_RELATED_KEYS`）
  - 币种
  - 单价
  - 最低充值
- 同步修改这几个文件：`features/system-settings/billing/index.tsx`（默认值）、`billing/section-registry.tsx`（映射）、`features/system-settings/types.ts`（类型）。
- 支付方式编辑器 `payment-methods-visual-editor.tsx`、`payment-method-dialog.tsx`：加入 `wooshpay` 类型。

### 8.2 钱包充值

| 文件 | 改动 |
|---|---|
| `features/wallet/api.ts` | `calculateWooshpayAmount` → `/api/user/wooshpay/amount`；`requestWooshpayPayment` → `/api/user/wooshpay/pay` |
| `hooks/use-payment.ts` | calculators 映射加 `wooshpay`；`processPayment` 拿到 `pay_link` 后与 Waffo Pancake 一样在当前页跳转（`window.location.href`），避免 await 之后 `window.open` 被拦截；Stripe 仍新窗口打开 |
| `lib/payment.ts` | `isWooshpayPayment`；默认类型和最低充值的推导（WooshPay 排在 Waffo、Waffo Pancake 之后，不抬高其他方式的最低充值；WooshPay 按钮自身的最低值来自 `pay_methods[].min_topup`）；`getWooshpayPresetPricing`：WooshPay 是已选或唯一的支付方式时，预设金额卡片按 `/api/status` 的 `wooshpay_unit_price` 计价、按 `wooshpay_currency` 显示 |
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
| `TestWooshpayFulfillRejectsMismatchedIntent` | 同上（用 httptest 模拟 WooshPay API） | 状态不是 succeeded、订单号不一致、金额不一致、币种不一致、livemode 不一致时，都**不入账** |
| `TestWooshpayFulfillIsIdempotent` | 同上 | 同一事件投递两次，只入账一次；`payment_intent.canceled` 不关单；会话过期后到达的成功付款仍入账一次 |
| `TestWooshpayTokensDisplayOrderCreditsRequestedTokens` | 同上 | Tokens 展示模式下按购买数量限额、计价，分组倍率只放大价格，入账额度等于输入的 token 数 |
| `TestWooshpayUnitAmountMinorUnits` | 同上 | USD 8.00 → 800；JPY 800 → 800；未知币种拒绝下单；金额超过 int32 时拒绝 |
| `TestWooshpayGroupRatioScalesPriceNotCredit` | `controller/topup_quota_limit_test.go` | 分组倍率只放大应付金额，入账 = 购买数量 × QPU |
| `TestWooshpayWebhookEnabledRequiresTopUpAndWebhookConfig` | `controller/payment_webhook_availability_test.go` | 必须同时满足合规确认、配置完整，且密钥模式与 API Base 对应 |
| `TestRechargeWooshpay_RejectsMismatchedPaymentMethod` | `model/payment_method_guard_test.go` | 其他渠道的订单不能走 wooshpay 入账 |
| `TestRechargeWooshpay_CompletesClosedOrdersOnce` | 同上 | pending / expired / failed 订单回查成功后都入账一次，已 success 不重复入账 |
| `UpdatePendingTopUpStatus` 表新增 wooshpay 用例 | 同上 | expire 和 fail 只对本渠道的 pending 订单生效 |
| `TestPaidQuotaFromCharge` / `TestResolveTopUpProvider` 新增用例 | `model/rebate_test.go` | 返佣快照 = `Money ÷ WooshpayUnitPrice × QPU`；测试密钥下快照为 0 |
| `TestManualCompleteTopUpCreditsLikeGatewayCallback` 新增 wooshpay 用例 | 同上 | 手工补单入账的额度与回调入账一致 |
| 路由列表 | `router/access_token_scope_test.go` | webhook 是匿名回调路由 |

前端测试：在 `features/wallet/lib/payment.test.ts` 和 `hooks/use-payment.test.ts` 里补 wooshpay 用例。

检查命令：

```bash
go build ./... && go test ./controller/... ./model/... ./router/...
cd web && bun run lint && bun run build && bun run test
```

---

## 10. 部署与运维

1. 在 WooshPay 控制台拿到 `sk_test_` 密钥。
2. 注册 Webhook。可以在控制台 Developer → Webhook 里创建，也可以调接口。测试环境和生产环境要**各注册一次**，同一个 URL 不要重复注册，否则会收到重复通知：
   ```bash
   curl https://apitest.wooshpay.com/v1/webhook_endpoints -u sk_test_xxx: \
     -H 'Content-Type: application/json' \
     -d '{"url":"https://<你的域名>/api/wooshpay/webhook","enabled_events":["payment_intent.succeeded","payment_intent.canceled","payment_intent.payment_failed","checkout.session.expired"],"api_version":"1.0.1"}'
   ```
   把返回的 `secret`（`whsec_...`）整串填到后台的 Webhook Secret 里。
3. 后台填写 API Base、币种、单价、最低充值，确认支付合规，最后在支付方式里启用 WooshPay。
4. Webhook URL 必须是公网可访问的 HTTPS 地址；如果前面有 CDN 或 WAF，要放行 `/api/wooshpay/webhook`，并且**不能改写请求体**，否则验签必然失败。
5. 测试环境只能用报价单上列出的支付方式；用测试卡联调（官方 Testing cards 页面）：成功、失败后换卡重试、放着不付等它过期、重复投递，这四种情况都要走一遍。
6. 上线生产：换成 `sk_live_` 密钥和生产 API Base，重新注册生产 Webhook 并更新 secret。

**对账兜底**：Webhook 重投 72 小时后就停止。如果一直没收到成功通知，管理员可以在 WooshPay 控制台核实到账后，用现有的“手工补单”（`ManualCompleteTopUp`）入账。第二期可以加一个定时任务，用 Search PaymentIntents 按 `merchant_order_id` 自动对账。

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
| 2 | 签名头名称 | `Signature` | 只读 `Signature` 这一个头 |
| 3 | Webhook 密钥格式 | `whsec_yDjRWBxxxx` | 整串原样当作 HMAC key，不做解码 |
| 4 | 签名时间戳 | 单位是毫秒，只作为盐值参与签名 | 不做时间窗口校验；防重放靠回查和幂等（§2.4） |
| 5 | 币种和支付方式 | 测试环境按报价单，生产环境按入网结果 | 由后台配置项决定，上线前按入网结果核对币种 |
| 6 | 注册 Webhook | `POST /v1/webhook_endpoints`，或在控制台 Developer → Webhook 里创建 | 见 §10 第 2 步 |
