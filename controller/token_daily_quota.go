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

// SetTokenDailyQuotaBatch sets (or clears, when daily_quota_limit <= 0) the daily quota
// limit for a batch of the caller's own tokens. Ownership is enforced in the model layer.
func SetTokenDailyQuotaBatch(c *gin.Context) {
	req := setTokenDailyQuotaBatchRequest{}
	if err := c.ShouldBindJSON(&req); err != nil || len(req.Ids) == 0 {
		common.ApiErrorI18n(c, i18n.MsgInvalidParams)
		return
	}
	if req.DailyQuotaLimit < 0 {
		common.ApiErrorI18n(c, i18n.MsgTokenQuotaNegative)
		return
	}
	maxDailyQuota := int((1000000000 * common.QuotaPerUnit))
	if req.DailyQuotaLimit > maxDailyQuota {
		common.ApiErrorI18n(c, i18n.MsgTokenQuotaExceedMax, map[string]any{"Max": maxDailyQuota})
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
