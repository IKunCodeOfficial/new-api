package controller

import (
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"

	"github.com/gin-gonic/gin"
)

type setTokenDailyQuotaBatchRequest struct {
	Ids             []int `json:"ids"`
	DailyQuotaLimit int   `json:"daily_quota_limit"`
}

// validateDailyTokenQuota checks a requested daily quota limit (in quota units) and writes
// an i18n error response when it is out of range, returning false. Shared by the three
// daily-quota entry points (AddToken, UpdateToken, SetTokenDailyQuotaBatch).
func validateDailyTokenQuota(c *gin.Context, limit int) bool {
	if limit < 0 {
		common.ApiErrorT(c, "Quota value cannot be negative")
		return false
	}
	maxDailyQuota := common.GetMaxTokenQuota()
	if limit > maxDailyQuota {
		common.ApiErrorT(c, "Quota value exceeds valid range, maximum is {{max}}", map[string]any{"max": logger.FormatQuota(maxDailyQuota)})
		return false
	}
	return true
}

// SetTokenDailyQuotaBatch sets (or clears, when daily_quota_limit <= 0) the daily quota
// limit for a batch of the caller's own tokens. Ownership is enforced in the model layer.
func SetTokenDailyQuotaBatch(c *gin.Context) {
	req := setTokenDailyQuotaBatchRequest{}
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ApiErrorT(c, "Invalid parameters")
		return
	}
	params := tokenBatchAuditParams(c, req.Ids)
	if len(req.Ids) == 0 {
		common.ApiErrorT(c, "Invalid parameters")
		return
	}
	params["daily_quota_limit"] = req.DailyQuotaLimit
	if !validateDailyTokenQuota(c, req.DailyQuotaLimit) {
		return
	}
	userId := c.GetInt("id")
	count, err := model.BatchSetTokenDailyQuotaLimit(req.Ids, userId, req.DailyQuotaLimit)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	params["count"] = count
	common.SetContextKey(c, constant.ContextKeyTokenAuditSucceeded, true)
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data":    count,
	})
}
