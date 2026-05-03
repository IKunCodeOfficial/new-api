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
import CardPro from '../../common/ui/CardPro';
import TokenAnalyticsActions from './TokenAnalyticsActions';
import TokenAnalyticsFilters from './TokenAnalyticsFilters';
import TokenAnalyticsCharts from './TokenAnalyticsCharts';
import TokenAnalyticsTable from './TokenAnalyticsTable';
import { useTokenAnalyticsData } from '../../../hooks/token-analytics/useTokenAnalyticsData';
import { useIsMobile } from '../../../hooks/common/useIsMobile';
import { createCardProPagination } from '../../../helpers/utils';

const TokenAnalyticsPage = () => {
  const analyticsData = useTokenAnalyticsData();
  const isMobile = useIsMobile();

  return (
    <CardPro
      type='type2'
      statsArea={<TokenAnalyticsActions {...analyticsData} />}
      searchArea={<TokenAnalyticsFilters {...analyticsData} />}
      paginationArea={createCardProPagination({
        currentPage: analyticsData.activePage,
        pageSize: analyticsData.pageSize,
        total: analyticsData.totalCount,
        onPageChange: analyticsData.handlePageChange,
        onPageSizeChange: analyticsData.handlePageSizeChange,
        isMobile,
        t: analyticsData.t,
      })}
      t={analyticsData.t}
    >
      <div className='flex flex-col gap-3'>
        <TokenAnalyticsCharts {...analyticsData} />
        <TokenAnalyticsTable {...analyticsData} />
      </div>
    </CardPro>
  );
};

export default TokenAnalyticsPage;
