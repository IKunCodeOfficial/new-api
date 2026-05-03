/*
Copyright (C) 2025 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/

import { useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { initVChartSemiTheme } from '@visactor/vchart-semi-theme';
import {
  API,
  getTodayStartTimestamp,
  showError,
  timestamp2string,
} from '../../helpers';
import { ITEMS_PER_PAGE } from '../../constants';
import { useTableCompactMode } from '../common/useTableCompactMode';

const normalizeNumeric = (value) => {
  const num = Number(value);
  if (!Number.isFinite(num)) {
    return 0;
  }
  return num;
};

const normalizeAnalyticsItem = (item) => ({
  ...item,
  token_id: normalizeNumeric(item.token_id),
  token_status: normalizeNumeric(item.token_status),
  token_created_time: normalizeNumeric(item.token_created_time),
  token_expired_time: normalizeNumeric(item.token_expired_time),
  request_count: normalizeNumeric(item.request_count),
  quota_sum: normalizeNumeric(item.quota_sum),
  prompt_tokens_sum: normalizeNumeric(item.prompt_tokens_sum),
  completion_tokens_sum: normalizeNumeric(item.completion_tokens_sum),
  last_used_at: normalizeNumeric(item.last_used_at),
});

const normalizeTrendPoint = (point) => ({
  bucket_start: normalizeNumeric(point.bucket_start),
  request_count: normalizeNumeric(point.request_count),
  quota_sum: normalizeNumeric(point.quota_sum),
});

const toTimestamp = (value, fallback) => {
  const parsed = Date.parse(value);
  if (Number.isFinite(parsed) && parsed > 0) {
    return Math.floor(parsed / 1000);
  }
  return fallback;
};

export const useTokenAnalyticsData = () => {
  const { t } = useTranslation();

  const [items, setItems] = useState([]);
  const [loading, setLoading] = useState(false);
  const [chartLoading, setChartLoading] = useState(false);
  const [activePage, setActivePage] = useState(1);
  const [totalCount, setTotalCount] = useState(0);
  const [pageSize, setPageSize] = useState(ITEMS_PER_PAGE);
  const [formApi, setFormApi] = useState(null);
  const [trendSeries, setTrendSeries] = useState([]);
  const [topTokens, setTopTokens] = useState([]);
  const [trendGranularity, setTrendGranularity] = useState('day');
  const [compactMode, setCompactMode] = useTableCompactMode('token-analytics');

  const nowTimestamp = Math.floor(Date.now() / 1000);
  const formInitValues = {
    keyword: '',
    dateRange: [
      timestamp2string(getTodayStartTimestamp()),
      timestamp2string(nowTimestamp + 3600),
    ],
    sort_by: 'quota_sum',
    sort_order: 'desc',
  };

  const getFormValues = () => {
    const values = formApi ? formApi.getValues() : {};
    const start = values?.dateRange?.[0];
    const end = values?.dateRange?.[1];
    return {
      keyword: values.keyword || '',
      start_timestamp: toTimestamp(start, getTodayStartTimestamp()),
      end_timestamp: toTimestamp(end, nowTimestamp + 3600),
      sort_by: values.sort_by || 'quota_sum',
      sort_order: values.sort_order || 'desc',
    };
  };

  const buildBaseSearchParams = () => {
    const values = getFormValues();
    const params = new URLSearchParams();
    params.set('keyword', values.keyword);
    params.set('start_timestamp', String(values.start_timestamp));
    params.set('end_timestamp', String(values.end_timestamp));
    params.set('sort_by', values.sort_by);
    params.set('sort_order', values.sort_order);
    return params;
  };

  const loadAnalyticsList = async (page = 1, size = pageSize) => {
    setLoading(true);
    const params = buildBaseSearchParams();
    params.set('p', String(page));
    params.set('page_size', String(size));
    try {
      const res = await API.get(`/api/log/self/token-analytics?${params.toString()}`);
      const { success, message, data } = res.data;
      if (!success) {
        showError(message);
        return;
      }
      const pageData = data || {};
      const list = Array.isArray(pageData.items) ? pageData.items : [];
      setItems(list.map(normalizeAnalyticsItem));
      setActivePage(pageData.page || page);
      setPageSize(pageData.page_size || size);
      setTotalCount(pageData.total || 0);
    } catch (error) {
      showError(error);
    } finally {
      setLoading(false);
    }
  };

  const loadTrendData = async () => {
    setChartLoading(true);
    const params = buildBaseSearchParams();
    try {
      const res = await API.get(
        `/api/log/self/token-analytics/trend?${params.toString()}`,
      );
      const { success, message, data } = res.data;
      if (!success) {
        showError(message);
        return;
      }
      const series = Array.isArray(data?.series) ? data.series : [];
      const top = Array.isArray(data?.top_tokens) ? data.top_tokens : [];
      setTrendSeries(series.map(normalizeTrendPoint));
      setTopTokens(top.map(normalizeAnalyticsItem));
      setTrendGranularity(data?.granularity || 'day');
    } catch (error) {
      showError(error);
    } finally {
      setChartLoading(false);
    }
  };

  const refresh = async () => {
    setActivePage(1);
    await Promise.all([loadAnalyticsList(1, pageSize), loadTrendData()]);
  };

  const resetFilters = () => {
    if (!formApi) return;
    formApi.reset();
    setTimeout(() => {
      refresh().then();
    }, 0);
  };

  const handlePageChange = (page) => {
    setActivePage(page);
    loadAnalyticsList(page, pageSize).then();
  };

  const handlePageSizeChange = (size) => {
    setPageSize(size);
    setActivePage(1);
    loadAnalyticsList(1, size).then();
  };

  const summary = useMemo(() => {
    const totalQuota = trendSeries.reduce(
      (sum, point) => sum + normalizeNumeric(point.quota_sum),
      0,
    );
    const totalRequests = trendSeries.reduce(
      (sum, point) => sum + normalizeNumeric(point.request_count),
      0,
    );
    return {
      totalQuota,
      totalRequests,
      totalTokens: totalCount,
    };
  }, [trendSeries, totalCount]);

  useEffect(() => {
    initVChartSemiTheme({
      isWatchingThemeSwitch: true,
    });
  }, []);

  useEffect(() => {
    if (!formApi) return;
    refresh().then();
  }, [formApi]);

  return {
    t,
    formInitValues,
    formApi,
    setFormApi,
    loading,
    chartLoading,
    items,
    activePage,
    totalCount,
    pageSize,
    trendSeries,
    topTokens,
    trendGranularity,
    summary,
    compactMode,
    setCompactMode,
    refresh,
    resetFilters,
    handlePageChange,
    handlePageSizeChange,
  };
};
