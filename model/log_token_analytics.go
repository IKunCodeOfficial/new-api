package model

import (
	"cmp"
	"slices"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

type UserTokenAnalyticsItem struct {
	TokenId             int    `json:"token_id"`
	TokenName           string `json:"token_name"`
	TokenStatus         int    `json:"token_status"`
	TokenGroup          string `json:"token_group"`
	TokenCreatedTime    int64  `json:"token_created_time"`
	TokenExpiredTime    int64  `json:"token_expired_time"`
	RequestCount        int64  `json:"request_count"`
	QuotaSum            int64  `json:"quota_sum"`
	PromptTokensSum     int64  `json:"prompt_tokens_sum"`
	CompletionTokensSum int64  `json:"completion_tokens_sum"`
	LastUsedAt          int64  `json:"last_used_at"`
}

type UserTokenAnalyticsTrendPoint struct {
	BucketStart  int64 `json:"bucket_start"`
	RequestCount int64 `json:"request_count"`
	QuotaSum     int64 `json:"quota_sum"`
}

type UserTokenAnalyticsTopToken struct {
	TokenId      int    `json:"token_id"`
	TokenName    string `json:"token_name"`
	RequestCount int64  `json:"request_count"`
	QuotaSum     int64  `json:"quota_sum"`
}

func escapeLikeKeyword(keyword string) string {
	keyword = strings.ReplaceAll(keyword, "!", "!!")
	keyword = strings.ReplaceAll(keyword, "%", "!%")
	keyword = strings.ReplaceAll(keyword, "_", "!_")
	return "%" + keyword + "%"
}

func applyTokenKeywordFilter(query *gorm.DB, keyword string) *gorm.DB {
	trimmed := strings.TrimSpace(keyword)
	if trimmed == "" {
		return query
	}
	likePattern := escapeLikeKeyword(trimmed)
	if tokenId, err := strconv.Atoi(trimmed); err == nil && tokenId > 0 {
		return query.Where("tokens.id = ? OR tokens.name LIKE ? ESCAPE '!'", tokenId, likePattern)
	}
	return query.Where("tokens.name LIKE ? ESCAPE '!'", likePattern)
}

func getUserTokenIDsByKeyword(userId int, keyword string) ([]int, error) {
	query := DB.Model(&Token{}).Where("tokens.user_id = ?", userId)
	query = applyTokenKeywordFilter(query, keyword)

	tokenIds := make([]int, 0)
	err := query.Pluck("tokens.id", &tokenIds).Error
	if err != nil {
		return nil, err
	}
	return tokenIds, nil
}

func compareTokenAnalyticsItems(a, b *UserTokenAnalyticsItem, sortBy, sortOrder string) int {
	var order int
	switch sortBy {
	case "request_count":
		order = cmp.Compare(a.RequestCount, b.RequestCount)
	case "last_used_at":
		order = cmp.Compare(a.LastUsedAt, b.LastUsedAt)
	case "prompt_tokens_sum":
		order = cmp.Compare(a.PromptTokensSum, b.PromptTokensSum)
	case "completion_tokens_sum":
		order = cmp.Compare(a.CompletionTokensSum, b.CompletionTokensSum)
	default:
		order = cmp.Compare(a.QuotaSum, b.QuotaSum)
	}
	if !strings.EqualFold(sortOrder, "asc") {
		order = -order
	}
	if order == 0 {
		return cmp.Compare(b.TokenId, a.TokenId)
	}
	return order
}

func resolveBucketExpr(granularity string) string {
	isHour := granularity == "hour"
	switch {
	case common.UsingLogDatabase(common.DatabaseTypePostgreSQL):
		if isHour {
			return "EXTRACT(EPOCH FROM DATE_TRUNC('hour', TO_TIMESTAMP(created_at)))::bigint"
		}
		return "EXTRACT(EPOCH FROM DATE_TRUNC('day', TO_TIMESTAMP(created_at)))::bigint"
	case common.UsingLogDatabase(common.DatabaseTypeClickHouse):
		if isHour {
			return "toInt64(toUnixTimestamp(toStartOfHour(toDateTime(created_at))))"
		}
		return "toInt64(toUnixTimestamp(toStartOfDay(toDateTime(created_at))))"
	case common.UsingLogDatabase(common.DatabaseTypeSQLite):
		if isHour {
			return "CAST(strftime('%s', strftime('%Y-%m-%d %H:00:00', created_at, 'unixepoch', 'localtime')) AS INTEGER)"
		}
		return "CAST(strftime('%s', date(created_at, 'unixepoch', 'localtime')) AS INTEGER)"
	default:
		if isHour {
			return "CAST(UNIX_TIMESTAMP(DATE_FORMAT(FROM_UNIXTIME(created_at), '%Y-%m-%d %H:00:00')) AS SIGNED)"
		}
		return "CAST(UNIX_TIMESTAMP(DATE(FROM_UNIXTIME(created_at))) AS SIGNED)"
	}
}

func validateTokenAnalyticsTimeRange(startTimestamp int64, endTimestamp int64) error {
	if startTimestamp == 0 || endTimestamp == 0 {
		return common.NewMessage("Invalid parameters")
	}
	if startTimestamp > endTimestamp {
		return common.NewMessage("End time must be after start time")
	}
	return nil
}

func GetUserTokenAnalytics(
	userId int,
	startTimestamp int64,
	endTimestamp int64,
	keyword string,
	sortBy string,
	sortOrder string,
	startIdx int,
	num int,
) (items []*UserTokenAnalyticsItem, total int64, err error) {
	if userId == 0 {
		return nil, 0, common.NewMessage("Invalid user ID")
	}
	if err = validateTokenAnalyticsTimeRange(startTimestamp, endTimestamp); err != nil {
		return nil, 0, err
	}
	if num <= 0 {
		num = common.ItemsPerPage
	}
	if num > 100 {
		num = 100
	}
	if startIdx < 0 {
		startIdx = 0
	}

	tokenQuery := applyTokenKeywordFilter(DB.Model(&Token{}).Where("tokens.user_id = ?", userId), keyword)
	if err = tokenQuery.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	items = make([]*UserTokenAnalyticsItem, 0)
	if int64(startIdx) >= total {
		return items, total, nil
	}
	window := startIdx + min(num, int(total)-startIdx)
	resultOffset := startIdx
	metadataOrder := ""
	switch sortBy {
	case "token_created_time":
		metadataOrder = "tokens.created_time"
	case "token_name":
		metadataOrder = "tokens.name"
	case "token_id":
		metadataOrder = "tokens.id"
	case "token_status":
		metadataOrder = "tokens.status"
	}
	if metadataOrder != "" {
		direction := " DESC"
		if strings.EqualFold(sortOrder, "asc") {
			direction = " ASC"
		}
		// Metadata ordering can page in the primary store and keeps its name
		// collation. Usage ordering needs aggregates before choosing the page.
		tokenQuery = tokenQuery.Order(metadataOrder + direction).Order("tokens.id DESC").Offset(startIdx).Limit(num)
		window, resultOffset = num, 0
	}
	// Query each store on its own connection. Batches bound token metadata and
	// grouped usage; retain only the best rows needed for this page.
	var tokens []Token
	err = tokenQuery.Select("id", "name", "status", commonGroupCol, "created_time", "expired_time").FindInBatches(&tokens, 500, func(tx *gorm.DB, batch int) error {
		ids := make([]int, len(tokens))
		for i := range tokens {
			ids[i] = tokens[i].Id
		}
		var stats []*UserTokenAnalyticsItem
		if err := LOG_DB.Table("logs").Select(
			"token_id, COUNT(1) AS request_count, COALESCE(SUM(quota), 0) AS quota_sum, "+
				"COALESCE(SUM(prompt_tokens), 0) AS prompt_tokens_sum, COALESCE(SUM(completion_tokens), 0) AS completion_tokens_sum, "+
				"COALESCE(MAX(created_at), 0) AS last_used_at").
			Where("user_id = ? AND type = ? AND token_id IN ?", userId, LogTypeConsume, ids).
			Where("created_at >= ? AND created_at <= ?", startTimestamp, endTimestamp).
			Group("token_id").Scan(&stats).Error; err != nil {
			return err
		}
		byID := make(map[int]*UserTokenAnalyticsItem, len(stats))
		for _, stat := range stats {
			byID[stat.TokenId] = stat
		}
		for _, token := range tokens {
			item := byID[token.Id]
			if item == nil {
				item = &UserTokenAnalyticsItem{TokenId: token.Id}
			}
			item.TokenName, item.TokenStatus, item.TokenGroup = token.Name, token.Status, token.Group
			item.TokenCreatedTime, item.TokenExpiredTime = token.CreatedTime, token.ExpiredTime
			items = append(items, item)
		}
		if metadataOrder == "" {
			slices.SortFunc(items, func(a, b *UserTokenAnalyticsItem) int { return compareTokenAnalyticsItems(a, b, sortBy, sortOrder) })
		}
		if len(items) > window {
			clear(items[window:])
			items = items[:window]
		}
		return nil
	}).Error
	if err != nil {
		common.SysError("failed to query user token analytics: " + err.Error())
		return nil, 0, common.NewMessage("Failed to query statistics")
	}
	// A token may have been deleted between counting and reading the batches.
	items = items[min(resultOffset, len(items)):]

	return items, total, nil
}

func GetUserTokenAnalyticsTrend(
	userId int,
	startTimestamp int64,
	endTimestamp int64,
	keyword string,
) (points []*UserTokenAnalyticsTrendPoint, topTokens []*UserTokenAnalyticsTopToken, granularity string, err error) {
	if userId == 0 {
		return nil, nil, "", common.NewMessage("Invalid user ID")
	}
	if err = validateTokenAnalyticsTimeRange(startTimestamp, endTimestamp); err != nil {
		return nil, nil, "", err
	}

	granularity = "day"
	if endTimestamp-startTimestamp <= 2*24*3600 {
		granularity = "hour"
	}

	trimmedKeyword := strings.TrimSpace(keyword)
	filterTokenIds := make([]int, 0)
	if trimmedKeyword != "" {
		filterTokenIds, err = getUserTokenIDsByKeyword(userId, trimmedKeyword)
		if err != nil {
			return nil, nil, "", err
		}
		if len(filterTokenIds) == 0 {
			return []*UserTokenAnalyticsTrendPoint{}, []*UserTokenAnalyticsTopToken{}, granularity, nil
		}
	}

	bucketExpr := resolveBucketExpr(granularity)
	logQuery := LOG_DB.Table("logs").
		Where("user_id = ? AND type = ? AND token_id > 0", userId, LogTypeConsume).
		Where("created_at >= ? AND created_at <= ?", startTimestamp, endTimestamp)

	if len(filterTokenIds) > 0 {
		logQuery = logQuery.Where("token_id IN ?", filterTokenIds)
	}

	err = logQuery.
		Select(
			bucketExpr + " AS bucket_start, " +
				"COUNT(1) AS request_count, " +
				"COALESCE(SUM(quota), 0) AS quota_sum",
		).
		Group("bucket_start").
		Order("bucket_start ASC").
		Scan(&points).Error
	if err != nil {
		common.SysError("failed to query token analytics trend: " + err.Error())
		return nil, nil, "", common.NewMessage("Failed to query statistics")
	}

	topQuery := LOG_DB.Table("logs").
		Select(
			"logs.token_id AS token_id, "+
				"COALESCE(MAX(logs.token_name), '') AS token_name, "+
				"COUNT(1) AS request_count, "+
				"COALESCE(SUM(logs.quota), 0) AS quota_sum",
		).
		Where("logs.user_id = ? AND logs.type = ? AND logs.token_id > 0", userId, LogTypeConsume).
		Where("logs.created_at >= ? AND logs.created_at <= ?", startTimestamp, endTimestamp)

	if len(filterTokenIds) > 0 {
		topQuery = topQuery.Where("logs.token_id IN ?", filterTokenIds)
	}

	err = topQuery.
		Group("logs.token_id").
		Order("quota_sum DESC").
		Order("request_count DESC").
		Order("logs.token_id DESC").
		Limit(10).
		Scan(&topTokens).Error
	if err != nil {
		common.SysError("failed to query token analytics top tokens: " + err.Error())
		return nil, nil, "", common.NewMessage("Failed to query statistics")
	}

	if len(topTokens) > 0 {
		ids := make([]int, len(topTokens))
		for i, token := range topTokens {
			ids[i] = token.TokenId
		}
		var tokens []Token
		if err = DB.Select("id", "name").Where("user_id = ? AND id IN ?", userId, ids).Find(&tokens).Error; err != nil {
			return nil, nil, "", err
		}
		names := make(map[int]string, len(tokens))
		for _, token := range tokens {
			names[token.Id] = token.Name
		}
		for _, token := range topTokens {
			if name, ok := names[token.TokenId]; ok {
				token.TokenName = name
			}
		}
	}

	return points, topTokens, granularity, nil
}
