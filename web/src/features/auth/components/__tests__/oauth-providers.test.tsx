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
import {
  createMemoryHistory,
  createRootRoute,
  createRouter,
  RouterProvider,
} from '@tanstack/react-router'
import { createInstance } from 'i18next'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { I18nextProvider, initReactI18next } from 'react-i18next'
import { describe, expect, it } from 'vitest'

import { OAuthProviders } from '../oauth-providers'

const i18n = createInstance()
await i18n.use(initReactI18next).init({
  lng: 'en',
  resources: {
    en: {
      translation: {
        'Continue with GitHub': 'Continue with GitHub',
        'Continue with LinuxDO': 'Continue with LinuxDO',
        'Or continue with': 'Or continue with',
      },
    },
  },
})

describe('OAuth providers', () => {
  it('keeps LinuxDO enabled and sends its login through the consent gate', async () => {
    const container = document.createElement('div')
    document.body.append(container)
    const root = createRoot(container)
    let requestedLogin: (() => void) | null = null
    const rootRoute = createRootRoute({
      component: () => (
        <I18nextProvider i18n={i18n}>
          <OAuthProviders
            status={{ linuxdo_oauth: true, linuxdo_client_id: 'client-id' }}
            onLoginRequest={(login) => {
              requestedLogin = login
            }}
          />
        </I18nextProvider>
      ),
    })
    const router = createRouter({
      routeTree: rootRoute,
      history: createMemoryHistory({ initialEntries: ['/'] }),
    })

    await act(async () => root.render(<RouterProvider router={router} />))

    const linuxDOButton = [...container.querySelectorAll('button')].find(
      (button) => button.textContent?.includes('Continue with LinuxDO')
    )
    expect(linuxDOButton).toBeDefined()
    expect(linuxDOButton).toBeEnabled()

    await act(async () => linuxDOButton?.click())
    expect(requestedLogin).toBeTypeOf('function')

    await act(async () => root.unmount())
    container.remove()
  })
})
