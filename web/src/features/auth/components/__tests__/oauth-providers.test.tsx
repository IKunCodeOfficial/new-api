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
import assert from 'node:assert/strict'
import { after, describe, test } from 'node:test'

import { Window } from 'happy-dom'

const domWindow = new Window()
const domGlobals = [
  'window',
  'document',
  'navigator',
  'HTMLElement',
  'HTMLButtonElement',
  'SVGElement',
  'Node',
  'Element',
  'Event',
  'CustomEvent',
  'MutationObserver',
] as const

for (const key of domGlobals) {
  Object.defineProperty(globalThis, key, {
    configurable: true,
    value: domWindow[key],
  })
}

const { act } = await import('react')
const { createRoot } = await import('react-dom/client')
const { createInstance } = await import('i18next')
const { I18nextProvider, initReactI18next } = await import('react-i18next')
const { createMemoryHistory, createRootRoute, createRouter, RouterProvider } =
  await import('@tanstack/react-router')
const { OAuthProviders } = await import('../oauth-providers')

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

const reactTestGlobals = globalThis as typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean
}
reactTestGlobals.IS_REACT_ACT_ENVIRONMENT = true

describe('OAuth providers', () => {
  after(() => {
    domWindow.close()
  })

  test('keeps LinuxDO enabled and sends its login through the consent gate', async () => {
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
    assert.ok(linuxDOButton)
    assert.equal(linuxDOButton.disabled, false)

    await act(async () => linuxDOButton.click())
    assert.equal(typeof requestedLogin, 'function')

    await act(async () => root.unmount())
    container.remove()
  })
})
