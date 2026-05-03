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
import { Empty, Tag } from '@douyinfe/semi-ui';
import CardTable from '../../common/ui/CardTable';
import {
  IllustrationNoResult,
  IllustrationNoResultDark,
} from '@douyinfe/semi-illustrations';
import { renderGroup, renderQuota, timestamp2string } from '../../../helpers';

const renderTokenStatus = (status, t) => {
  if (status === 1) {
    return (
      <Tag color='green' shape='circle'>
        {t('已启用')}
      </Tag>
    );
  }
  if (status === 2) {
    return (
      <Tag color='red' shape='circle'>
        {t('已禁用')}
      </Tag>
    );
  }
  if (status === 3) {
    return (
      <Tag color='yellow' shape='circle'>
        {t('已过期')}
      </Tag>
    );
  }
  if (status === 4) {
    return (
      <Tag color='grey' shape='circle'>
        {t('已耗尽')}
      </Tag>
    );
  }
  return (
    <Tag color='black' shape='circle'>
      {t('未知状态')}
    </Tag>
  );
};

const formatNumber = (value) => {
  const num = Number(value);
  if (!Number.isFinite(num)) {
    return '0';
  }
  return num.toLocaleString();
};

const formatTimestamp = (timestamp) => {
  if (!timestamp || timestamp <= 0) return '-';
  return timestamp2string(timestamp);
};

const TokenAnalyticsTable = ({ items, loading, compactMode, t }) => {
  const columns = useMemo(() => {
    return [
      {
        title: t('令牌 ID'),
        dataIndex: 'token_id',
        render: (value) => (
          <Tag color='white' shape='circle'>
            #{value}
          </Tag>
        ),
      },
      {
        title: t('令牌名称'),
        dataIndex: 'token_name',
        render: (value) => value || '-',
      },
      {
        title: t('状态'),
        dataIndex: 'token_status',
        render: (value) => renderTokenStatus(value, t),
      },
      {
        title: t('分组'),
        dataIndex: 'token_group',
        render: (value) => (value ? renderGroup(value) : '-'),
      },
      {
        title: t('创建时间'),
        dataIndex: 'token_created_time',
        render: (value) => formatTimestamp(value),
      },
      {
        title: t('到期时间'),
        dataIndex: 'token_expired_time',
        render: (value) => {
          if (value === -1) {
            return (
              <Tag color='white' shape='circle'>
                {t('永不过期')}
              </Tag>
            );
          }
          return formatTimestamp(value);
        },
      },
      {
        title: t('请求次数'),
        dataIndex: 'request_count',
        render: (value) => formatNumber(value),
      },
      {
        title: t('花费'),
        dataIndex: 'quota_sum',
        render: (value) => renderQuota(value, 6),
      },
      {
        title: t('输入'),
        dataIndex: 'prompt_tokens_sum',
        render: (value) => formatNumber(value),
      },
      {
        title: t('输出'),
        dataIndex: 'completion_tokens_sum',
        render: (value) => formatNumber(value),
      },
      {
        title: t('最后调用时间'),
        dataIndex: 'last_used_at',
        render: (value) => formatTimestamp(value),
      },
    ];
  }, [t]);

  return (
    <CardTable
      columns={columns}
      dataSource={items}
      rowKey='token_id'
      loading={loading}
      scroll={compactMode ? undefined : { x: 'max-content' }}
      className='rounded-xl overflow-hidden'
      size='middle'
      empty={
        <Empty
          image={<IllustrationNoResult style={{ width: 150, height: 150 }} />}
          darkModeImage={
            <IllustrationNoResultDark style={{ width: 150, height: 150 }} />
          }
          description={t('搜索无结果')}
          style={{ padding: 30 }}
        />
      }
      hidePagination={true}
    />
  );
};

export default TokenAnalyticsTable;
