package model

import (
	"errors"
	"fmt"
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

func normalizeSortOrder(sortOrder string) string {
	if strings.EqualFold(sortOrder, "asc") {
		return "ASC"
	}
	return "DESC"
}

func resolveTokenAnalyticsSortExpr(sortBy string) string {
	switch sortBy {
	case "request_count":
		return "COALESCE(log_stats.request_count, 0)"
	case "last_used_at":
		return "COALESCE(log_stats.last_used_at, 0)"
	case "token_created_time":
		return "tokens.created_time"
	case "token_name":
		return "tokens.name"
	case "token_id":
		return "tokens.id"
	case "prompt_tokens_sum":
		return "COALESCE(log_stats.prompt_tokens_sum, 0)"
	case "completion_tokens_sum":
		return "COALESCE(log_stats.completion_tokens_sum, 0)"
	case "token_status":
		return "tokens.status"
	default:
		return "COALESCE(log_stats.quota_sum, 0)"
	}
}

func resolveBucketExpr(granularity string) string {
	isHour := granularity == "hour"
	switch {
	case common.UsingLogDatabase(common.DatabaseTypePostgreSQL):
		if isHour {
			return "EXTRACT(EPOCH FROM DATE_TRUNC('hour', TO_TIMESTAMP(created_at)))::bigint"
		}
		return "EXTRACT(EPOCH FROM DATE_TRUNC('day', TO_TIMESTAMP(created_at)))::bigint"
	case common.UsingLogDatabase(common.DatabaseTypeSQLite):
		if isHour {
			return "CAST(strftime('%s', strftime('%Y-%m-%d %H:00:00', created_at, 'unixepoch', 'localtime')) AS INTEGER)"
		}
		return "CAST(strftime('%s', date(created_at, 'unixepoch', 'localtime')) AS INTEGER)"
	default:
		if isHour {
			return "UNIX_TIMESTAMP(DATE_FORMAT(FROM_UNIXTIME(created_at), '%Y-%m-%d %H:00:00'))"
		}
		return "UNIX_TIMESTAMP(DATE(FROM_UNIXTIME(created_at)))"
	}
}

func validateTokenAnalyticsTimeRange(startTimestamp int64, endTimestamp int64) error {
	if startTimestamp == 0 || endTimestamp == 0 {
		return errors.New("开始时间和结束时间不能为空")
	}
	if startTimestamp > endTimestamp {
		return errors.New("开始时间不能大于结束时间")
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
		return nil, 0, errors.New("无效的用户")
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

	trimmedKeyword := strings.TrimSpace(keyword)
	filterTokenIds := make([]int, 0)
	if trimmedKeyword != "" {
		filterTokenIds, err = getUserTokenIDsByKeyword(userId, trimmedKeyword)
		if err != nil {
			return nil, 0, err
		}
		if len(filterTokenIds) == 0 {
			return []*UserTokenAnalyticsItem{}, 0, nil
		}
	}

	logStatsQuery := LOG_DB.Table("logs").
		Select(
			"token_id, COUNT(1) AS request_count, "+
				"COALESCE(SUM(quota), 0) AS quota_sum, "+
				"COALESCE(SUM(prompt_tokens), 0) AS prompt_tokens_sum, "+
				"COALESCE(SUM(completion_tokens), 0) AS completion_tokens_sum, "+
				"COALESCE(MAX(created_at), 0) AS last_used_at",
		).
		Where("user_id = ? AND type = ? AND token_id > 0", userId, LogTypeConsume).
		Where("created_at >= ? AND created_at <= ?", startTimestamp, endTimestamp)

	if len(filterTokenIds) > 0 {
		logStatsQuery = logStatsQuery.Where("token_id IN ?", filterTokenIds)
	}
	logStatsQuery = logStatsQuery.Group("token_id")

	tokenQuery := DB.Model(&Token{}).
		Where("tokens.user_id = ?", userId).
		Joins("LEFT JOIN (?) AS log_stats ON log_stats.token_id = tokens.id", logStatsQuery)

	if len(filterTokenIds) > 0 {
		tokenQuery = tokenQuery.Where("tokens.id IN ?", filterTokenIds)
	}

	err = tokenQuery.Count(&total).Error
	if err != nil {
		common.SysError("failed to count user token analytics: " + err.Error())
		return nil, 0, errors.New("查询令牌统计失败")
	}

	orderExpr := resolveTokenAnalyticsSortExpr(sortBy)
	orderDirection := normalizeSortOrder(sortOrder)
	selectExpr := fmt.Sprintf(
		"tokens.id AS token_id, tokens.name AS token_name, tokens.status AS token_status, "+
			"tokens.%s AS token_group, tokens.created_time AS token_created_time, tokens.expired_time AS token_expired_time, "+
			"COALESCE(log_stats.request_count, 0) AS request_count, COALESCE(log_stats.quota_sum, 0) AS quota_sum, "+
			"COALESCE(log_stats.prompt_tokens_sum, 0) AS prompt_tokens_sum, "+
			"COALESCE(log_stats.completion_tokens_sum, 0) AS completion_tokens_sum, "+
			"COALESCE(log_stats.last_used_at, 0) AS last_used_at",
		commonGroupCol,
	)

	err = tokenQuery.
		Select(selectExpr).
		Order(orderExpr + " " + orderDirection).
		Order("tokens.id DESC").
		Limit(num).
		Offset(startIdx).
		Scan(&items).Error
	if err != nil {
		common.SysError("failed to query user token analytics: " + err.Error())
		return nil, 0, errors.New("查询令牌统计失败")
	}

	return items, total, nil
}

func GetUserTokenAnalyticsTrend(
	userId int,
	startTimestamp int64,
	endTimestamp int64,
	keyword string,
) (points []*UserTokenAnalyticsTrendPoint, topTokens []*UserTokenAnalyticsTopToken, granularity string, err error) {
	if userId == 0 {
		return nil, nil, "", errors.New("无效的用户")
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
		return nil, nil, "", errors.New("查询趋势数据失败")
	}

	topQuery := LOG_DB.Table("logs").
		Select(
			"logs.token_id AS token_id, "+
				"COALESCE(MAX(tokens.name), MAX(logs.token_name), '') AS token_name, "+
				"COUNT(1) AS request_count, "+
				"COALESCE(SUM(logs.quota), 0) AS quota_sum",
		).
		Joins("LEFT JOIN tokens ON tokens.id = logs.token_id AND tokens.user_id = logs.user_id AND tokens.deleted_at IS NULL").
		Where("logs.user_id = ? AND logs.type = ? AND logs.token_id > 0", userId, LogTypeConsume).
		Where("logs.created_at >= ? AND logs.created_at <= ?", startTimestamp, endTimestamp)

	if len(filterTokenIds) > 0 {
		topQuery = topQuery.Where("logs.token_id IN ?", filterTokenIds)
	}

	err = topQuery.
		Group("logs.token_id").
		Order("quota_sum DESC").
		Order("request_count DESC").
		Limit(10).
		Scan(&topTokens).Error
	if err != nil {
		common.SysError("failed to query token analytics top tokens: " + err.Error())
		return nil, nil, "", errors.New("查询排行数据失败")
	}

	return points, topTokens, granularity, nil
}
