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
import { act, renderHook } from '@testing-library/react'
import { createInstance } from 'i18next'
import type { PropsWithChildren } from 'react'
import { I18nextProvider, initReactI18next } from 'react-i18next'
import { afterEach, describe, expect, it } from 'vitest'

import type { NavLink, SidebarData } from '@/components/layout/types'
import { useSystemConfigStore } from '@/stores/system-config-store'

import { useSidebarData } from '../use-sidebar-data'

const i18n = createInstance()
await i18n.use(initReactI18next).init({ lng: 'en', resources: {} })

function Wrapper(props: PropsWithChildren) {
  return <I18nextProvider i18n={i18n}>{props.children}</I18nextProvider>
}

function getRechargeItem(sidebarData: SidebarData): NavLink {
  const item = sidebarData.navGroups
    .flatMap((group) => group.items)
    .find((candidate) => candidate.title === 'Recharge Center')

  if (!item || !('externalUrl' in item)) {
    throw new Error('Expected the Recharge Center navigation link')
  }
  return item
}

afterEach(() => {
  useSystemConfigStore.getState().setConfig({ topUpLink: '' })
})

describe('Recharge Center sidebar link', () => {
  it('updates when the configured top-up link changes', () => {
    act(() => {
      useSystemConfigStore.getState().setConfig({
        topUpLink: 'https://billing.example.com/first',
      })
    })
    const { result } = renderHook(() => useSidebarData(), { wrapper: Wrapper })

    expect(getRechargeItem(result.current).externalUrl).toBe(
      'https://billing.example.com/first'
    )

    act(() => {
      useSystemConfigStore.getState().setConfig({
        topUpLink: 'https://billing.example.com/second',
      })
    })
    expect(getRechargeItem(result.current).externalUrl).toBe(
      'https://billing.example.com/second'
    )
  })
})
