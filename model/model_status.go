package model

// GetPerfMetricRows returns perf_metrics counters with bucket_ts in
// [startTs, endTs), summed per model, group and hour. Summing in SQL keeps the
// row count hourly even when perf_metrics_setting.bucket_time is sub-hour.
// The 3600 literal must stay inline: PostgreSQL treats separate bind
// parameters as different expressions in SELECT and GROUP BY.
func GetPerfMetricRows(startTs, endTs int64) ([]PerfMetric, error) {
	const hourExpr = "(bucket_ts - bucket_ts % 3600)"
	var rows []PerfMetric
	err := DB.Model(&PerfMetric{}).
		Select("model_name, "+commonGroupCol+", "+hourExpr+" AS bucket_ts, "+
			"SUM(request_count) AS request_count, SUM(success_count) AS success_count, "+
			"SUM(total_latency_ms) AS total_latency_ms, SUM(ttft_sum_ms) AS ttft_sum_ms, "+
			"SUM(ttft_count) AS ttft_count, SUM(output_tokens) AS output_tokens, "+
			"SUM(generation_ms) AS generation_ms").
		Where("bucket_ts >= ? AND bucket_ts < ?", startTs, endTs).
		// Group by the expression, not the alias: PostgreSQL resolves an alias
		// that matches an input column name to the input column.
		Group("model_name, " + commonGroupCol + ", " + hourExpr).
		Find(&rows).Error
	return rows, err
}
