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
import { act, useState } from 'react'
import { createRoot } from 'react-dom/client'
import { describe, expect, it } from 'vitest'

import { useLegalConsentGate } from '../use-legal-consent-gate'

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
  it('waits for confirmation before starting a third-party login', async () => {
    const container = document.createElement('div')
    document.body.append(container)
    const root = createRoot(container)

    await act(async () => root.render(<ConsentGateHarness />))

    const loginButton = container.querySelector<HTMLButtonElement>('button')
    expect(loginButton).toBeTruthy()
    await act(async () => loginButton?.click())

    const stateBeforeConsent = container.querySelector('span')
    expect(stateBeforeConsent).toHaveAttribute('data-agreed', 'false')
    expect(stateBeforeConsent).toHaveAttribute('data-login-started', 'false')

    const confirmButton = [...container.querySelectorAll('button')].find(
      (button) => button.textContent === 'Agree and continue'
    )
    expect(confirmButton).toBeDefined()
    await act(async () => confirmButton?.click())

    const stateAfterConsent = container.querySelector('span')
    expect(stateAfterConsent).toHaveAttribute('data-agreed', 'true')
    expect(stateAfterConsent).toHaveAttribute('data-login-started', 'true')
    expect(container).not.toHaveTextContent('Agree and continue')

    await act(async () => root.unmount())
    container.remove()
  })
})
