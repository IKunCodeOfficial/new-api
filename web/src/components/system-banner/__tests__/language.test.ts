/*
Copyright (C) 2023-2026 QuantumNous

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
import { describe, expect, test } from 'vitest'

import {
  pickSystemBannerText,
  type SystemBannerStatus,
} from '@/components/system-banner/lib'

const banner: SystemBannerStatus = {
  level: 'normal',
  text_zh: '中文公告',
  text_en: 'English notice',
  published_at: 1000,
}

describe('pickSystemBannerText', () => {
  test.each(['zhCN', 'zhTW'])('uses the Chinese text for %s', (language) => {
    expect(pickSystemBannerText(banner, language)).toBe('中文公告')
  })

  test.each(['en', 'fr', 'ru', 'ja', 'vi'])(
    'uses the English text for %s',
    (language) => {
      expect(pickSystemBannerText(banner, language)).toBe('English notice')
    }
  )

  test.each([undefined, 'xx'])(
    'falls back to the English text for %s',
    (language) => {
      expect(pickSystemBannerText(banner, language)).toBe('English notice')
    }
  )
})
