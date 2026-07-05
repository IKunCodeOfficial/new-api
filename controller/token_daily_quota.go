package controller

import (
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
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
		common.ApiErrorI18n(c, i18n.MsgTokenQuotaNegative)
		return false
	}
	maxDailyQuota := common.GetMaxTokenQuota()
	if limit > maxDailyQuota {
		common.ApiErrorI18n(c, i18n.MsgTokenQuotaExceedMax, map[string]any{"Max": maxDailyQuota})
		return false
	}
	return true
}

// SetTokenDailyQuotaBatch sets (or clears, when daily_quota_limit <= 0) the daily quota
// limit for a batch of the caller's own tokens. Ownership is enforced in the model layer.
func SetTokenDailyQuotaBatch(c *gin.Context) {
	req := setTokenDailyQuotaBatchRequest{}
	if err := c.ShouldBindJSON(&req); err != nil || len(req.Ids) == 0 {
		common.ApiErrorI18n(c, i18n.MsgInvalidParams)
		return
	}
	if !validateDailyTokenQuota(c, req.DailyQuotaLimit) {
		return
	}
	userId := c.GetInt("id")
	count, err := model.BatchSetTokenDailyQuotaLimit(req.Ids, userId, req.DailyQuotaLimit)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data":    count,
	})
}
