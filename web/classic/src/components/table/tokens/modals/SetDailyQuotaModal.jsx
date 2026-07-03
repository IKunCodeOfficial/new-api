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

import React, { useEffect, useState } from 'react';
import { Modal, InputNumber } from '@douyinfe/semi-ui';
import { API, showError, showSuccess, getCurrencyConfig } from '../../../../helpers';
import { displayAmountToQuota } from '../../../../helpers/quota';

const SetDailyQuotaModal = ({ visible, onCancel, selectedKeys, refresh, t }) => {
  const [amount, setAmount] = useState(0);
  const [loading, setLoading] = useState(false);

  useEffect(() => {
    if (visible) {
      setAmount(0);
    }
  }, [visible]);

  const handleConfirm = async () => {
    if (!selectedKeys || selectedKeys.length === 0) {
      showError(t('请至少选择一个令牌！'));
      return;
    }
    setLoading(true);
    try {
      const ids = selectedKeys.map((token) => token.id);
      const daily_quota_limit = displayAmountToQuota(amount || 0);
      const res = await API.post('/api/token/batch/daily_quota', {
        ids,
        daily_quota_limit,
      });
      if (res?.data?.success) {
        const count = res.data.data || 0;
        showSuccess(t('已为 {{count}} 个令牌设置每日额度限制！', { count }));
        await refresh();
        onCancel();
      } else {
        showError(res?.data?.message || t('设置失败'));
      }
    } catch (error) {
      showError(error.message);
    } finally {
      setLoading(false);
    }
  };

  return (
    <Modal
      title={t('批量设置每日额度限制')}
      visible={visible}
      onCancel={onCancel}
      onOk={handleConfirm}
      confirmLoading={loading}
    >
      <div
        className='mb-3 text-xs'
        style={{ color: 'var(--semi-color-text-2)' }}
      >
        {t(
          '将为所选的 {{count}} 个令牌统一设置每日额度限制，每日 0 点自动重置，0 表示取消限制。',
          { count: selectedKeys.length },
        )}
      </div>
      <InputNumber
        prefix={getCurrencyConfig().symbol}
        placeholder={t('0 表示不限制')}
        precision={6}
        min={0}
        step={0.000001}
        value={amount}
        onChange={(val) => setAmount(val === '' || val == null ? 0 : val)}
        style={{ width: '100%' }}
        showClear
      />
    </Modal>
  );
};

export default SetDailyQuotaModal;
