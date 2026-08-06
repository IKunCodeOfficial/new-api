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
  'Node',
  'Element',
  'Event',
] as const

for (const key of domGlobals) {
  Object.defineProperty(globalThis, key, {
    configurable: true,
    value: domWindow[key],
  })
}

const { act, useState } = await import('react')
const { createRoot } = await import('react-dom/client')
const { useLegalConsentGate } = await import('../use-legal-consent-gate')

const reactTestGlobals = globalThis as typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean
}
reactTestGlobals.IS_REACT_ACT_ENVIRONMENT = true

function ConsentGateHarness() {
  const [agreed, setAgreed] = useState(false)
  const [loginStarted, setLoginStarted] = useState(false)
  const gate = useLegalConsentGate({
    requiresLegalConsent: true,
    agreedToLegal: agreed,
    onAgree: () => setAgreed(true),
  })

  return (
    <div>
      <button
        type='button'
        onClick={() => gate.requestLegalConsent(() => setLoginStarted(true))}
      >
        Third-party sign in
      </button>
      {gate.isConsentDialogOpen && (
        <button type='button' onClick={gate.confirmLegalConsent}>
          Agree and continue
        </button>
      )}
      <span data-agreed={agreed} data-login-started={loginStarted} />
    </div>
  )
}

describe('legal consent gate', () => {
  after(() => {
    domWindow.close()
  })

  test('waits for confirmation before starting a third-party login', async () => {
    const container = document.createElement('div')
    document.body.append(container)
    const root = createRoot(container)

    await act(async () => root.render(<ConsentGateHarness />))

    const loginButton = container.querySelector<HTMLButtonElement>('button')
    assert.ok(loginButton)
    await act(async () => loginButton.click())

    const stateBeforeConsent = container.querySelector('span')
    assert.equal(stateBeforeConsent?.dataset.agreed, 'false')
    assert.equal(stateBeforeConsent?.dataset.loginStarted, 'false')

    const confirmButton = [...container.querySelectorAll('button')].find(
      (button) => button.textContent === 'Agree and continue'
    )
    assert.ok(confirmButton)
    await act(async () => confirmButton.click())

    const stateAfterConsent = container.querySelector('span')
    assert.equal(stateAfterConsent?.dataset.agreed, 'true')
    assert.equal(stateAfterConsent?.dataset.loginStarted, 'true')
    assert.equal(container.textContent?.includes('Agree and continue'), false)

    await act(async () => root.unmount())
    container.remove()
  })
})
