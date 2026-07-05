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
import {
  API,
  showError,
  showSuccess,
  getCurrencyConfig,
} from '../../../../helpers';
import { displayAmountToQuota } from '../../../../helpers/quota';

const SetDailyQuotaModal = ({
  visible,
  onCancel,
  selectedKeys,
  refresh,
  t,
}) => {
  // null (empty) is the default so an untouched dialog can never be submitted: 0 means "clear the
  // limit for every selected token", which would silently wipe existing caps if it were the default.
  const [amount, setAmount] = useState(null);
  const [loading, setLoading] = useState(false);

  useEffect(() => {
    if (visible) {
      setAmount(null);
    }
  }, [visible]);

  const handleConfirm = async () => {
    if (!selectedKeys || selectedKeys.length === 0) {
      showError(t('请至少选择一个令牌！'));
      return;
    }
    if (amount === null || amount === '') {
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
        if (count === 0) {
          // 所选令牌均不属于当前账户（后端按 user_id 过滤），不应提示成功。
          showError(t('没有令牌被更新'));
          return;
        }
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
      okButtonProps={{ disabled: amount === null || amount === '' }}
    >
      <div
        className='mb-3 text-xs'
        style={{
          color:
            amount === 0
              ? 'var(--semi-color-danger)'
              : 'var(--semi-color-text-2)',
        }}
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
        value={amount === null ? undefined : amount}
        onChange={(val) => setAmount(val === '' || val == null ? null : val)}
        style={{ width: '100%' }}
        showClear
      />
    </Modal>
  );
};

export default SetDailyQuotaModal;
