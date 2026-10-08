package relay

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/service/privacy"
	pb "github.com/QuantumNous/new-api/service/privacy/filterpb"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"google.golang.org/grpc"
)

type billingPrivacyClient struct {
	pb.PrivacyFilterClient
}

func (billingPrivacyClient) RedactBatch(_ context.Context, in *pb.RedactBatchRequest, _ ...grpc.CallOption) (*pb.RedactBatchResponse, error) {
	results := make([]*pb.RedactResponse, len(in.Texts))
	for i, text := range in.Texts {
		results[i] = &pb.RedactResponse{Redacted: strings.ReplaceAll(text, "secret@example.com", "[EMAIL]"), Hit: true}
	}
	return &pb.RedactBatchResponse{Results: results}, nil
}

func TestPrepareRequestBillingRedactsBeforePricingAndWebSocketForwarding(t *testing.T) {
	quotaSetting := operation_setting.GetQuotaSetting()
	previousFreePreConsume := quotaSetting.EnableFreeModelPreConsume
	quotaSetting.EnableFreeModelPreConsume = false
	t.Cleanup(func() { quotaSetting.EnableFreeModelPreConsume = previousFreePreConsume })
	privacy.SetClientForTest(billingPrivacyClient{})
	t.Cleanup(privacy.ResetClientForTest)
	saved := map[string]string{}
	require.NoError(t, config.GlobalConfig.SaveToDB(func(key, value string) error {
		saved[key] = value
		return nil
	}))
	t.Cleanup(func() { require.NoError(t, config.GlobalConfig.LoadFromDB(saved)) })
	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{
		"group_ratio_setting.group_ratio": `{"default":0}`,
		"billing_setting.billing_mode":    `{"privacy-test":"tiered_expr"}`,
		"billing_setting.billing_expr":    `{"privacy-test":"param(\"input\") == \"[EMAIL]\" ? tier(\"redacted\", fixed(0)) : tier(\"unredacted\", fixed(0))"}`,
	}))
	for _, passthrough := range []bool{false, true} {
		name := "converted"
		if passthrough {
			name = "passthrough"
		}
		t.Run(name, func(t *testing.T) {
			create, _, err := normalizeResponsesWSTestMessage([]byte(`{"type":"response.create","response":{"model":"privacy-test","input":"secret@example.com","stream":true}}`))
			require.NoError(t, err)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(string(create.Body)))
			c.Request.Header.Set("Content-Type", "application/json")
			common.SetContextKey(c, constant.ContextKeyOriginalModel, create.Request.Model)
			common.SetContextKey(c, constant.ContextKeyUsingGroup, "default")
			common.SetContextKey(c, constant.ContextKeyUserGroup, "default")
			common.SetContextKey(c, constant.ContextKeyUserSetting, dto.UserSetting{PrivacyFilterEnabled: true})
			common.SetContextKey(c, constant.ContextKeyChannelType, constant.ChannelTypeOpenAI)
			common.SetContextKey(c, constant.ContextKeyChannelSetting, dto.ChannelSettings{PassThroughBodyEnabled: passthrough})
			info := relaycommon.GenRelayInfoResponses(c, &create.Request)
			require.Nil(t, PrepareRequestBilling(c, info))
			require.NotNil(t, info.TieredBillingSnapshot)
			assert.Equal(t, "redacted", info.TieredBillingSnapshot.EstimatedTier)
			assert.Equal(t, "[EMAIL]", gjson.GetBytes(info.BillingRequestInput.Body, "input").String())
			assert.NotContains(t, info.Request.GetTokenCountMeta().CombineText, "secret@example.com")
			payload, apiErr := buildResponsesWSCreatePayload(c, info, create.Request, create.Generate, create.StreamID)
			require.Nil(t, apiErr)
			assert.Equal(t, "[EMAIL]", gjson.GetBytes(payload, "input").String())
			assert.NotContains(t, string(payload), "secret@example.com")
			storage, err := common.GetBodyStorage(c)
			require.NoError(t, err)
			require.NoError(t, storage.Close())
		})
	}
}
