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

import React, { useState } from 'react';
import { Spin } from '@douyinfe/semi-ui';
import { useTranslation } from 'react-i18next';

const SHOP_URL = 'https://9.plus/shop/2F7A86NF';

const Shop = () => {
  const { t } = useTranslation();
  const [loading, setLoading] = useState(true);

  return (
    <div className='w-full max-w-7xl mx-auto relative mt-[60px] px-2'>
      {loading && (
        <div className='absolute inset-0 flex items-center justify-center pointer-events-none'>
          <Spin size='large' />
        </div>
      )}
      <iframe
        src={SHOP_URL}
        title={t('充值中心')}
        onLoad={() => setLoading(false)}
        className='w-full rounded-2xl'
        style={{ height: 'calc(100vh - 110px)', border: 'none' }}
        allow='payment; clipboard-write'
      />
    </div>
  );
};

export default Shop;
