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

import React, { useMemo } from 'react';
import dayjs from 'dayjs';
import { Card, Empty, Spin } from '@douyinfe/semi-ui';
import { VChart } from '@visactor/react-vchart';
import { CHART_CONFIG } from '../../../constants/dashboard.constants';
import { getCurrencyConfig, getQuotaPerUnit, renderNumber } from '../../../helpers';

const formatBucketLabel = (timestamp, granularity) => {
  if (!timestamp || timestamp <= 0) return '-';
  if (granularity === 'hour') {
    return dayjs.unix(timestamp).format('MM-DD HH:00');
  }
  return dayjs.unix(timestamp).format('YYYY-MM-DD');
};

const normalizeNumber = (value) => {
  const num = Number(value);
  if (!Number.isFinite(num)) {
    return 0;
  }
  return num;
};

const formatMoneyAmount = (quota, digits = 2) => {
  const quotaValue = normalizeNumber(quota);
  const quotaPerUnit = Number(getQuotaPerUnit()) || 1;
  const usdAmount = quotaValue / quotaPerUnit;
  const currency = getCurrencyConfig();

  if (currency.type === 'TOKENS') {
    return `$${usdAmount.toFixed(digits)}`;
  }
  const symbol = currency.symbol || '$';
  const rate = Number(currency.rate) || 1;
  return `${symbol}${(usdAmount * rate).toFixed(digits)}`;
};

const formatMoneyTick = (value) => {
  return formatMoneyAmount(value, 2);
};

const TokenAnalyticsCharts = ({
  trendSeries,
  topTokens,
  trendGranularity,
  loading,
  t,
}) => {
  const trendValues = useMemo(
    () =>
      trendSeries.map((item) => ({
        Time: formatBucketLabel(item.bucket_start, trendGranularity),
        Quota: normalizeNumber(item.quota_sum),
      })),
    [trendSeries, trendGranularity],
  );

  const rankValues = useMemo(
    () =>
      topTokens.map((item, index) => ({
        Token: item.token_name || `#${item.token_id}`,
        Quota: normalizeNumber(item.quota_sum),
        RequestCount: normalizeNumber(item.request_count),
        Rank: index + 1,
      })),
    [topTokens],
  );

  const trendSpec = useMemo(
    () => ({
      type: 'line',
      data: [{ id: 'trend', values: trendValues }],
      xField: 'Time',
      yField: 'Quota',
      point: { visible: true },
      axes: [
        {
          orient: 'left',
          label: {
            formatMethod: (value) => formatMoneyTick(value),
          },
        },
      ],
      title: {
        visible: true,
        text: t('花费趋势'),
      },
      tooltip: {
        mark: {
          content: [
            {
              key: () => t('花费'),
              value: (datum) => formatMoneyAmount(datum['Quota'], 6),
            },
          ],
        },
        dimension: {
          content: [
            {
              key: () => t('花费'),
              value: (datum) => formatMoneyAmount(datum['Quota'], 6),
            },
          ],
        },
      },
    }),
    [trendValues, t],
  );

  const rankSpec = useMemo(
    () => ({
      type: 'bar',
      data: [{ id: 'rank', values: rankValues }],
      xField: 'Token',
      yField: 'Quota',
      axes: [
        {
          orient: 'left',
          label: {
            formatMethod: (value) => formatMoneyTick(value),
          },
        },
      ],
      title: {
        visible: true,
        text: t('令牌花费 Top10'),
      },
      bar: {
        state: {
          hover: {
            stroke: '#000',
            lineWidth: 1,
          },
        },
      },
      tooltip: {
        mark: {
          content: [
            {
              key: () => t('花费'),
              value: (datum) => formatMoneyAmount(datum['Quota'], 6),
            },
            {
              key: () => t('请求数'),
              value: (datum) => renderNumber(datum['RequestCount']),
            },
          ],
        },
        dimension: {
          content: [
            {
              key: () => t('花费'),
              value: (datum) => formatMoneyAmount(datum['Quota'], 6),
            },
            {
              key: () => t('请求数'),
              value: (datum) => renderNumber(datum['RequestCount']),
            },
          ],
        },
      },
    }),
    [rankValues, t],
  );

  const showTrendEmpty = !loading && trendValues.length === 0;
  const showRankEmpty = !loading && rankValues.length === 0;

  return (
    <div className='grid grid-cols-1 xl:grid-cols-2 gap-3'>
      <Card className='!rounded-2xl' bodyStyle={{ padding: 12 }}>
        <Spin spinning={loading}>
          {showTrendEmpty ? (
            <Empty description={t('暂无趋势数据')} style={{ padding: 40 }} />
          ) : (
            <div className='h-80'>
              <VChart spec={trendSpec} option={CHART_CONFIG} />
            </div>
          )}
        </Spin>
      </Card>

      <Card className='!rounded-2xl' bodyStyle={{ padding: 12 }}>
        <Spin spinning={loading}>
          {showRankEmpty ? (
            <Empty description={t('暂无排行数据')} style={{ padding: 40 }} />
          ) : (
            <div className='h-80'>
              <VChart spec={rankSpec} option={CHART_CONFIG} />
            </div>
          )}
        </Spin>
      </Card>
    </div>
  );
};

export default TokenAnalyticsCharts;
