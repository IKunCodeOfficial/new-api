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

import React from 'react';
import { TabPane, Tabs } from '@douyinfe/semi-ui';
import { useTranslation } from 'react-i18next';
import UsageLogsTable from '../../components/table/usage-logs';
import TokenAnalyticsPage from '../../components/table/token-analytics';

const Log = () => {
  const { t } = useTranslation();
  return (
    <div className='mt-[60px] px-2'>
      <Tabs type='line' defaultActiveKey='usage-logs'>
        <TabPane tab={t('使用日志')} itemKey='usage-logs'>
          <UsageLogsTable />
        </TabPane>
        <TabPane tab={t('令牌用量分析')} itemKey='token-analytics'>
          <TokenAnalyticsPage />
        </TabPane>
      </Tabs>
    </div>
  );
};

export default Log;
