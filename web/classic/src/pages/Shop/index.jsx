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

import React, { useContext, useState } from 'react';
import { Spin } from '@douyinfe/semi-ui';
import { useTranslation } from 'react-i18next';
import { StatusContext } from '../../context/Status';

const SHOP_CODE = '2F7A86NF';
// Direct cross-site embed of https://9.plus/shop/2F7A86NF breaks the shop's
// captcha (its PHPSESSID cookie is dropped inside a cross-site iframe), so
// the backend reverse-proxies the shop and the iframe loads it same-origin —
// or from the dedicated SHOP_PROXY_HOST when the operator configured one.

const Shop = () => {
  const { t } = useTranslation();
  const [loading, setLoading] = useState(true);
  const [statusState] = useContext(StatusContext);

  const proxyHost = statusState?.status?.shop_proxy_host || '';
  const shopUrl = proxyHost
    ? `${window.location.protocol}//${proxyHost}/shop/${SHOP_CODE}`
    : `/shop/${SHOP_CODE}`;

  // Only mount the iframe once /api/status has resolved: the proxy host is
  // known only then, and mounting earlier would first navigate to the panel
  // origin (which serves the SPA index in dedicated-host mode) and dismiss
  // the spinner for the wrong document.
  const statusReady = statusState?.status !== undefined;

  return (
    <div className='w-full max-w-7xl mx-auto relative mt-[60px] px-2'>
      {(loading || !statusReady) && (
        <div className='absolute inset-0 flex items-center justify-center pointer-events-none'>
          <Spin size='large' />
        </div>
      )}
      {statusReady && (
        <iframe
          src={shopUrl}
          title={t('充值中心')}
          onLoad={() => setLoading(false)}
          className='w-full rounded-2xl'
          style={{ height: 'calc(100vh - 110px)', border: 'none' }}
          allow='payment; clipboard-write'
        />
      )}
    </div>
  );
};

export default Shop;
