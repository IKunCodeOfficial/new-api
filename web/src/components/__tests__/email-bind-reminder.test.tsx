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
  createRoute,
  createRouter,
  Outlet,
  RouterProvider,
} from '@tanstack/react-router'
import { render, screen, waitFor, cleanup } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, expect, it, vi } from 'vitest'

import { useAuthStore } from '@/stores/auth-store'

import { EmailBindReminder } from '../email-bind-reminder'

const originalAuth = useAuthStore.getState().auth
function renderReminder(userId = 7) {
  useAuthStore.setState({
    auth: { ...originalAuth, user: { id: userId, username: 'user', role: 1 } },
  })
  const root = createRootRoute({
    component: () => (
      <>
        <EmailBindReminder />
        <Outlet />
      </>
    ),
  })
  const index = createRoute({
    getParentRoute: () => root,
    path: '/',
    component: () => <div>Home</div>,
  })
  const security = createRoute({
    getParentRoute: () => root,
    path: '/security',
    component: () => <div>Security settings</div>,
  })
  const router = createRouter({
    routeTree: root.addChildren([index, security]),
    history: createMemoryHistory({ initialEntries: ['/'] }),
  })
  render(<RouterProvider router={router} />)
  return router
}
afterEach(() => {
  useAuthStore.setState({ auth: originalAuth })
  vi.restoreAllMocks()
})
it('bind email opens the security page containing the verified email binding flow', async () => {
  const router = renderReminder()
  await userEvent.click(
    await screen.findByRole('button', { name: 'Go to Bind Email' })
  )
  await waitFor(() => expect(router.state.location.pathname).toBe('/security'))
  expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
})
it('snoozing suppresses the reminder for seven days only for the current account', async () => {
  vi.spyOn(Date, 'now').mockReturnValue(1000000000)
  renderReminder()
  await userEvent.click(
    await screen.findByRole('checkbox', {
      name: 'Do not remind me again for 7 days',
    })
  )
  await userEvent.click(screen.getByRole('button', { name: 'Not Now' }))
  expect(localStorage.getItem('email-bind-reminder-snooze-until:7')).toBe(
    String(1000000000 + 7 * 24 * 60 * 60 * 1000)
  )
  cleanup()
  renderReminder()
  await screen.findByText('Home')
  expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  cleanup()
  renderReminder(8)
  expect(
    await screen.findByRole('dialog', { name: 'Account Security Reminder' })
  ).toBeVisible()
  cleanup()
  vi.mocked(Date.now).mockReturnValue(1000000000 + 7 * 24 * 60 * 60 * 1000)
  renderReminder()
  expect(
    await screen.findByRole('dialog', { name: 'Account Security Reminder' })
  ).toBeVisible()
})
