package common

func GetTrustQuota() int {
	return int(10 * QuotaPerUnit)
}

// GetMaxTokenQuota returns the maximum quota (in quota units) a single token may hold, or
// cap per day. QuotaPerUnit is a runtime-configurable var, so this must be a func, not a
// const (mirrors GetTrustQuota).
func GetMaxTokenQuota() int {
	return int(1000000000 * QuotaPerUnit)
}
