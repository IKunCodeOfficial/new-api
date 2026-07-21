package service

import (
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEstimateRequestToken_ExcludesFiles 锁定契约：文件/图片/音视频不参与本地
// token 估算（媒体真实费用以上游 usage 为准）。历史实现按媒体累加 token，
// 图片瓦片计算对病态宽高比可膨胀到百万级，造成兜底计费巨额扣费。
func TestEstimateRequestToken_ExcludesFiles(t *testing.T) {
	gin.SetMode(gin.TestMode)
	oldCountToken := constant.CountToken
	constant.CountToken = true
	t.Cleanup(func() { constant.CountToken = oldCountToken })

	newCtx := func() *gin.Context {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		return c
	}
	info := &relaycommon.RelayInfo{RelayFormat: types.RelayFormatOpenAI}

	textOnly := &types.TokenCountMeta{CombineText: "请分析这张图片的内容"}
	withFiles := &types.TokenCountMeta{
		CombineText: "请分析这张图片的内容",
		Files: []*types.FileMeta{
			{FileType: types.FileTypeImage},
			{FileType: types.FileTypeVideo},
			{FileType: types.FileTypeFile},
			{FileType: ""}, // 未知类型也不得计数或触发文件下载
		},
	}

	base, err := EstimateRequestToken(newCtx(), textOnly, info)
	require.NoError(t, err)
	require.Greater(t, base, 0)

	got, err := EstimateRequestToken(newCtx(), withFiles, info)
	require.NoError(t, err)
	assert.Equal(t, base, got, "media files must not contribute to the local token estimate")
}
