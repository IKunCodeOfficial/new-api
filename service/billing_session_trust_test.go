package service

import (
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newWalletOnlyRelayInfo(userId int, tokenId int, tokenKey string) *relaycommon.RelayInfo {
	return &relaycommon.RelayInfo{
		UserId:         userId,
		TokenId:        tokenId,
		TokenKey:       tokenKey,
		TokenUnlimited: true,
		UserSetting:    dto.UserSetting{BillingPreference: "wallet_only"},
	}
}

// 余额高于信任额度的用户不预扣，预扣估算超过余额也不应拒绝请求。
func TestPreConsumeBillingTrustedUserSkipsBalanceCheck(t *testing.T) {
	truncate(t)
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/messages", nil)

	balance := int(80 * common.QuotaPerUnit)
	preConsume := int(81 * common.QuotaPerUnit)
	require.Greater(t, balance, common.GetTrustQuota())
	seedUser(t, 9101, balance)
	seedToken(t, 9101, 9101, "trust-key", 0)

	info := newWalletOnlyRelayInfo(9101, 9101, "trust-key")
	apiErr := PreConsumeBilling(c, preConsume, info)

	require.Nil(t, apiErr)
	require.NotNil(t, info.Billing)
	assert.Equal(t, 0, info.Billing.GetPreConsumedQuota())
	assert.Equal(t, 0, info.FinalPreConsumedQuota)
	quota, err := model.GetUserQuota(9101, true)
	require.NoError(t, err)
	assert.Equal(t, balance, quota)
}

// 余额未达信任额度的用户仍需预扣，预扣额超过余额时拒绝请求。
func TestPreConsumeBillingUntrustedUserRejectsWhenBalanceInsufficient(t *testing.T) {
	truncate(t)
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/messages", nil)

	balance := common.GetTrustQuota() / 2
	seedUser(t, 9102, balance)
	seedToken(t, 9102, 9102, "untrusted-key", 0)

	info := newWalletOnlyRelayInfo(9102, 9102, "untrusted-key")
	apiErr := PreConsumeBilling(c, balance+1, info)

	require.NotNil(t, apiErr)
	assert.Equal(t, types.ErrorCodeInsufficientUserQuota, apiErr.GetErrorCode())
	assert.Nil(t, info.Billing)
	quota, err := model.GetUserQuota(9102, true)
	require.NoError(t, err)
	assert.Equal(t, balance, quota)
}
