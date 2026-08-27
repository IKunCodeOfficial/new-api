package common

import "github.com/shopspring/decimal"

func GetTrustQuota() int {
	return int(10 * QuotaPerUnit)
}

// GetMaxTokenQuota returns the maximum quota (in quota units) a single token may hold, or
// cap per day. QuotaPerUnit is a runtime-configurable var, so this must be a func, not a
// const (mirrors GetTrustQuota).
func GetMaxTokenQuota() int {
	quota, err := WalletQuotaFromDecimalStrict(
		decimal.NewFromInt(1_000_000_000).Mul(decimal.NewFromFloat(QuotaPerUnit)),
	)
	if err != nil {
		return MaxWalletQuota
	}
	return quota
}
