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
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { beforeEach, expect, test, vi } from 'vitest'

import { SettingsPageProvider } from '../../components/settings-page-context'
import { SystemBannerSection } from '../system-banner-section'

const calls: Array<{ key: string; value: unknown }> = []
type Request = { key: string; value: unknown }
const succeed = async (request: Request) => {
  calls.push(request)
  return { success: true }
}
const mutateAsync = vi.fn(succeed)

vi.mock('../../hooks/use-update-option', () => ({
  useUpdateOption: () => ({ mutateAsync, isPending: false }),
}))

type Defaults = Parameters<typeof SystemBannerSection>[0]['defaultValues']

function Fixture(props: { defaultValues: Defaults }) {
  const [container, setContainer] = useState<HTMLDivElement | null>(null)
  return (
    <>
      <div ref={setContainer} />
      <SettingsPageProvider actionsContainer={container}>
        <SystemBannerSection defaultValues={props.defaultValues} />
      </SettingsPageProvider>
    </>
  )
}

const disabled: Defaults = {
  enabled: false,
  level: 'normal',
  text_zh: '',
  text_en: '',
}

const enabled: Defaults = {
  enabled: true,
  level: 'normal',
  text_zh: '维护通知',
  text_en: 'Maintenance',
}

async function save() {
  await userEvent.click(await screen.findByRole('button', { name: /save/i }))
}

beforeEach(() => {
  calls.length = 0
  mutateAsync.mockReset()
  mutateAsync.mockImplementation(succeed)
})

test('fields are reachable by their labels', async () => {
  render(<Fixture defaultValues={disabled} />)
  expect(
    await screen.findByRole('switch', { name: 'Show banner' })
  ).toBeInTheDocument()
  expect(screen.getByRole('combobox', { name: 'Level' })).toBeInTheDocument()
  expect(screen.getByLabelText('Chinese text')).toBeInTheDocument()
  expect(screen.getByLabelText('English text')).toBeInTheDocument()
})

test('enabling saves texts and level before enabled', async () => {
  render(<Fixture defaultValues={disabled} />)
  await userEvent.type(await screen.findByLabelText('Chinese text'), '维护')
  await userEvent.type(screen.getByLabelText('English text'), 'Maintenance')
  await userEvent.click(screen.getByRole('switch', { name: 'Show banner' }))
  await save()
  await waitFor(() => expect(calls).toHaveLength(3))
  expect(calls).toEqual([
    { key: 'system_banner.text_zh', value: '维护' },
    { key: 'system_banner.text_en', value: 'Maintenance' },
    { key: 'system_banner.enabled', value: true },
  ])
})

test('disabling saves enabled before the texts', async () => {
  render(<Fixture defaultValues={enabled} />)
  await userEvent.click(
    await screen.findByRole('switch', { name: 'Show banner' })
  )
  await userEvent.clear(screen.getByLabelText('Chinese text'))
  await save()
  await waitFor(() => expect(calls).toHaveLength(2))
  expect(calls).toEqual([
    { key: 'system_banner.enabled', value: false },
    { key: 'system_banner.text_zh', value: '' },
  ])
})

test('enabled with empty English text shows a field error and saves nothing', async () => {
  render(<Fixture defaultValues={disabled} />)
  await userEvent.type(await screen.findByLabelText('Chinese text'), '维护')
  await userEvent.click(screen.getByRole('switch', { name: 'Show banner' }))
  await save()
  expect(
    await screen.findByText('Required while the banner is shown')
  ).toBeInTheDocument()
  expect(mutateAsync).not.toHaveBeenCalled()
})

test('text over 500 characters shows a field error and the counter', async () => {
  render(<Fixture defaultValues={disabled} />)
  const field = await screen.findByLabelText('English text')
  await userEvent.click(field)
  await userEvent.paste('a'.repeat(501))
  expect(screen.getByText('501/500')).toBeInTheDocument()
  await save()
  expect(
    await screen.findByText('System banner text must be at most 500 characters')
  ).toBeInTheDocument()
  expect(mutateAsync).not.toHaveBeenCalled()
})

test('unchanged form saves nothing', async () => {
  render(<Fixture defaultValues={enabled} />)
  await screen.findByRole('switch', { name: 'Show banner' })
  await save()
  expect(mutateAsync).not.toHaveBeenCalled()
})

test('a failed save keeps the unsaved edits and later keys unsaved', async () => {
  mutateAsync.mockImplementation(async (request) => {
    calls.push(request)
    if (request.key === 'system_banner.text_en') throw new Error('rejected')
    return { success: true }
  })
  const view = render(<Fixture defaultValues={disabled} />)
  await userEvent.type(await screen.findByLabelText('Chinese text'), '维护')
  await userEvent.type(screen.getByLabelText('English text'), 'Maintenance')
  await userEvent.click(screen.getByRole('switch', { name: 'Show banner' }))
  await save()
  await waitFor(() => expect(calls).toHaveLength(2))

  // The refetch after the first success changes the saved defaults.
  view.rerender(<Fixture defaultValues={{ ...disabled, text_zh: '维护' }} />)

  expect(screen.getByLabelText('English text')).toHaveValue('Maintenance')
  expect(screen.getByLabelText('Chinese text')).toHaveValue('维护')
  expect(screen.getByRole('switch', { name: 'Show banner' })).toBeChecked()
  expect(calls.map((c) => c.key)).toEqual([
    'system_banner.text_zh',
    'system_banner.text_en',
  ])
})
