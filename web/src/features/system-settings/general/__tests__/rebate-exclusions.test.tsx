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
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  createMemoryHistory,
  createRootRoute,
  createRouter,
  RouterProvider,
} from '@tanstack/react-router'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { Toaster, toast } from 'sonner'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'

import { api } from '@/lib/api'

import { SettingsPageProvider } from '../../components/settings-page-context'
import { RebateSettingsSection } from '../rebate-settings-section'

// Fork-only (ikun): exclusion edits must report server failures instead of
// failing silently, an edit in flight must not be sent twice, and a list that
// failed to load must not read as an empty list.

type Exclusion = { id: number; user_id: number; role: string; note: string }

let rows: Exclusion[] = []

function Fixture() {
  return (
    <SettingsPageProvider actionsContainer={null}>
      <RebateSettingsSection defaultValues={{ enabled: false, rate: 0.05 }} />
      <Toaster duration={60_000} />
    </SettingsPageProvider>
  )
}

async function renderSection() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  const router = createRouter({
    routeTree: createRootRoute({ component: Fixture }),
    history: createMemoryHistory({ initialEntries: ['/'] }),
  })
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  )
  return screen.findByRole('button', { name: 'Add exclusion' })
}

function mockExclusionList(
  list: (url: string) => Promise<{ data: unknown }>
): void {
  vi.spyOn(api, 'get').mockImplementation(async (url: string) =>
    url === '/api/rebate/exclusions' ? list(url) : { data: { enabled: false } }
  )
}

beforeEach(() => {
  rows = [{ id: 9, user_id: 1247, role: 'inviter', note: 'staff account' }]
  mockExclusionList(async () => ({ data: { success: true, data: rows } }))
})

afterEach(() => {
  vi.restoreAllMocks()
  toast.dismiss()
})

test('shows the server message when adding an exclusion fails', async () => {
  vi.spyOn(api, 'post').mockResolvedValue({
    data: {
      success: false,
      message: 'User does not exist',
      message_key: 'User does not exist',
    },
  })
  const addButton = await renderSection()
  const user = userEvent.setup()
  await user.type(screen.getByRole('spinbutton', { name: 'User ID' }), '4242')
  await user.click(addButton)

  expect(await screen.findByText('User does not exist')).not.toBeNull()
})

test('an add in flight disables the button and is sent once', async () => {
  let release: (() => void) | undefined
  const post = vi.spyOn(api, 'post').mockImplementation(
    () =>
      new Promise((resolve) => {
        release = () => resolve({ data: { success: true, data: {} } })
      })
  )
  const addButton = await renderSection()
  const user = userEvent.setup()
  await user.type(screen.getByRole('spinbutton', { name: 'User ID' }), '1247')
  await user.click(addButton)

  await waitFor(() =>
    expect((addButton as HTMLButtonElement).disabled).toBe(true)
  )
  await user.click(addButton)
  expect(post).toHaveBeenCalledTimes(1)
  release?.()
  await waitFor(() =>
    expect((addButton as HTMLButtonElement).disabled).toBe(false)
  )
})

test('a removal in flight is sent once and a failure is reported', async () => {
  let release: (() => void) | undefined
  const del = vi.spyOn(api, 'delete').mockImplementation(
    () =>
      new Promise((resolve) => {
        release = () =>
          resolve({
            data: {
              success: false,
              message: 'Invalid parameters',
              message_key: 'Invalid parameters',
            },
          })
      })
  )
  await renderSection()
  const removeButton = await screen.findByRole('button', { name: 'Remove' })
  const user = userEvent.setup()
  await user.click(removeButton)
  await waitFor(() =>
    expect((removeButton as HTMLButtonElement).disabled).toBe(true)
  )
  await user.click(removeButton)
  expect(del).toHaveBeenCalledTimes(1)
  release?.()

  expect(await screen.findByText('Invalid parameters')).not.toBeNull()
})

test('the add form fields have accessible names', async () => {
  await renderSection()

  expect(screen.getByRole('spinbutton', { name: 'User ID' })).toBeVisible()
  expect(screen.getByRole('combobox', { name: 'Role' })).toHaveTextContent(
    'As inviter'
  )
  expect(screen.getByRole('textbox', { name: 'Note' })).toBeVisible()
})

test('the list shows a loading state, not the empty message, until it loads', async () => {
  let release: (() => void) | undefined
  mockExclusionList(
    () =>
      new Promise((resolve) => {
        release = () => resolve({ data: { success: true, data: rows } })
      })
  )
  await renderSection()

  expect(screen.getByText('Loading...')).toBeInTheDocument()
  expect(screen.queryByText('No excluded users')).toBeNull()
  release?.()
  expect(await screen.findByText('staff account')).toBeInTheDocument()
  expect(screen.queryByText('Loading...')).toBeNull()
})

test.each([
  {
    failure: 'a success:false response',
    reason: 'Database unavailable',
    respond: async () => ({
      data: { success: false, message: 'Database unavailable' },
    }),
  },
  {
    failure: 'a rejected request',
    reason: 'Network Error',
    respond: async () => {
      throw new Error('Network Error')
    },
  },
])(
  'a list that fails to load with $failure shows the reason with Retry instead of the empty message',
  async (scenario) => {
    mockExclusionList(scenario.respond)
    await renderSection()

    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent('Failed to load excluded users')
    expect(alert).toHaveTextContent(scenario.reason)
    expect(screen.getByRole('button', { name: 'Retry' })).toBeVisible()
    expect(screen.queryByText('No excluded users')).toBeNull()
  }
)

test('Retry after a failed load shows the rows once the list recovers', async () => {
  let failing = true
  mockExclusionList(async () =>
    failing
      ? { data: { success: false, message: 'Database unavailable' } }
      : { data: { success: true, data: rows } }
  )
  await renderSection()
  const retry = await screen.findByRole('button', { name: 'Retry' })

  failing = false
  await userEvent.setup().click(retry)

  expect(await screen.findByText('staff account')).toBeInTheDocument()
  expect(screen.getByRole('button', { name: 'Remove' })).toBeVisible()
  expect(screen.queryByRole('alert')).toBeNull()
})
