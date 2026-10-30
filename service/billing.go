package service

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
)

const (
	BillingSourceWallet       = "wallet"
	BillingSourceSubscription = "subscription"
)

// PreConsumeBilling 根据用户计费偏好创建 BillingSession 并执行预扣费。
// 会话存储在 relayInfo.Billing 上，供后续 Settle / Refund 使用。
// 会话已存在时（任务重试换到另一个渠道）把预扣补到 preConsumedQuota，
// 和首次预扣一样必须由余额覆盖。
func PreConsumeBilling(c *gin.Context, preConsumedQuota int, relayInfo *relaycommon.RelayInfo) *types.NewAPIError {
	if relayInfo != nil && relayInfo.QuotaClamp != nil {
		return types.NewErrorWithStatusCode(
			relayInfo.QuotaClamp,
			types.ErrorCodeModelPriceError,
			http.StatusBadRequest,
			types.ErrOptionWithSkipRetry(),
		)
	}
	if preConsumedQuota < 0 {
		return types.NewErrorWithStatusCode(
			fmt.Errorf("pre-consume quota cannot be negative: %d", preConsumedQuota),
			types.ErrorCodeModelPriceError,
			http.StatusBadRequest,
			types.ErrOptionWithSkipRetry(),
		)
	}
	if relayInfo != nil && relayInfo.Billing != nil {
		if err := relayInfo.Billing.ReserveBeforeSubmit(preConsumedQuota); err != nil {
			var apiErr *types.NewAPIError
			if errors.As(err, &apiErr) {
				return apiErr
			}
			return types.NewErrorWithStatusCode(err, types.ErrorCodeInsufficientUserQuota, http.StatusForbidden, types.ErrOptionWithSkipRetry())
		}
		return nil
	}
	session, apiErr := NewBillingSession(c, relayInfo, preConsumedQuota)
	if apiErr != nil {
		return apiErr
	}
	relayInfo.Billing = session
	return nil
}

// ---------------------------------------------------------------------------
// SettleBilling — 后结算辅助函数
// ---------------------------------------------------------------------------

// ErrBillingFundingNotSettled marks a settlement whose wallet or subscription
// adjustment was not committed: the balance moved only by the pre-consumed
// amount.
var ErrBillingFundingNotSettled = errors.New("billing funding source was not settled")

// errBillingSessionRefunded is returned when a refunded session is settled:
// nothing remains charged, so the consume log records zero.
var errBillingSessionRefunded = fmt.Errorf("%w: session already refunded", ErrBillingFundingNotSettled)

// settleForConsumeLog settles the request and returns the quota its consume
// log must record. When the funding source could not be adjusted, the balance
// moved only by the pre-consumed amount, so the log records that amount: consume
// logs keep reconciling with balances, and the invite-rebate consumption base
// never counts a difference that was not charged. The difference is kept on
// relayInfo so attachSettlementShortfall can put it under admin_info.
func settleForConsumeLog(ctx *gin.Context, relayInfo *relaycommon.RelayInfo, quota int) int {
	err := SettleBilling(ctx, relayInfo, quota)
	if err == nil {
		return quota
	}
	logger.LogError(ctx, common.LogText("error settling billing: %s", err.Error()))
	if !errors.Is(err, ErrBillingFundingNotSettled) {
		return quota
	}
	charged := relayInfo.FinalPreConsumedQuota
	if relayInfo.Billing != nil {
		charged = relayInfo.Billing.GetPreConsumedQuota()
	}
	if errors.Is(err, errBillingSessionRefunded) {
		charged = 0
	}
	relayInfo.UnsettledQuota = quota - charged
	logger.LogError(ctx, common.LogText("billing settlement not committed, consume log records the charged amount: user=%d quota=%d charged=%d unsettled=%d",
		relayInfo.UserId, quota, charged, relayInfo.UnsettledQuota))
	return charged
}

// SettleBilling 执行计费结算。如果 RelayInfo 上有 BillingSession 则通过 session 结算，
// 否则回退到旧的 PostConsumeQuota 路径（兼容按次计费等场景）。
func SettleBilling(ctx *gin.Context, relayInfo *relaycommon.RelayInfo, actualQuota int) error {
	if relayInfo.Billing != nil {
		preConsumed := relayInfo.Billing.GetPreConsumedQuota()
		delta := actualQuota - preConsumed

		if delta > 0 {
			logger.LogInfo(ctx, common.LogText("charging the difference after pre-consume: %s (actual %s, pre-consumed %s)",
				logger.FormatQuota(delta),
				logger.FormatQuota(actualQuota),
				logger.FormatQuota(preConsumed),
			))
		} else if delta < 0 {
			logger.LogInfo(ctx, common.LogText("refunding the difference after pre-consume: %s (actual %s, pre-consumed %s)",
				logger.FormatQuota(-delta),
				logger.FormatQuota(actualQuota),
				logger.FormatQuota(preConsumed),
			))
		} else {
			logger.LogInfo(ctx, common.LogText("pre-consumed quota matches the actual quota, no adjustment: %s (per-request billing)",
				logger.FormatQuota(actualQuota),
			))
		}

		if err := relayInfo.Billing.Settle(actualQuota); err != nil {
			return err
		}

		// 发送额度通知（订阅计费使用订阅剩余额度）。实时会话按轮已扣的部分
		// （WssChargedQuota）也属于本次请求的花费，提醒按整段会话计算。
		if requestCost := actualQuota + relayInfo.WssChargedQuota; requestCost != 0 {
			if relayInfo.BillingSource == BillingSourceSubscription {
				checkAndSendSubscriptionQuotaNotify(relayInfo)
			} else {
				checkAndSendQuotaNotify(relayInfo, requestCost-preConsumed, preConsumed)
			}
		}
		return nil
	}

	// 回退：无 BillingSession 时使用旧路径
	quotaDelta := actualQuota - relayInfo.FinalPreConsumedQuota
	if quotaDelta != 0 {
		result, err := postConsumeQuotaWithResult(relayInfo, quotaDelta, relayInfo.FinalPreConsumedQuota+relayInfo.WssChargedQuota, true)
		if err != nil && !result.FundingApplied {
			return fmt.Errorf("%w: %w", ErrBillingFundingNotSettled, err)
		}
		return err
	}
	return nil
}
