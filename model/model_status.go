package model

// GetPerfMetricRows returns the counter columns of every perf_metrics row with
// bucket_ts in [startTs, endTs), across all models and groups.
func GetPerfMetricRows(startTs, endTs int64) ([]PerfMetric, error) {
	var rows []PerfMetric
	err := DB.Model(&PerfMetric{}).
		Select("model_name, "+commonGroupCol+", bucket_ts, request_count, success_count, total_latency_ms, ttft_sum_ms, ttft_count, output_tokens, generation_ms").
		Where("bucket_ts >= ? AND bucket_ts < ?", startTs, endTs).
		Find(&rows).Error
	return rows, err
}
