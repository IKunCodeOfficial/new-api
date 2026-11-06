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

const mutateAsync = vi.fn(async (_values: unknown) => ({ success: true }))

vi.mock('../../hooks/use-update-system-banner', () => ({
  useUpdateSystemBanner: () => ({ mutateAsync, isPending: false }),
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
  mutateAsync.mockReset()
  mutateAsync.mockResolvedValue({ success: true })
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

test('enabling saves the whole banner in one request', async () => {
  render(<Fixture defaultValues={disabled} />)
  await userEvent.type(await screen.findByLabelText('Chinese text'), '维护')
  await userEvent.type(screen.getByLabelText('English text'), 'Maintenance')
  await userEvent.click(screen.getByRole('switch', { name: 'Show banner' }))
  await save()
  await waitFor(() => expect(mutateAsync).toHaveBeenCalledTimes(1))
  expect(mutateAsync).toHaveBeenCalledWith({
    enabled: true,
    level: 'normal',
    text_zh: '维护',
    text_en: 'Maintenance',
  })
})

test('disabling and clearing text is one request', async () => {
  render(<Fixture defaultValues={enabled} />)
  await userEvent.click(
    await screen.findByRole('switch', { name: 'Show banner' })
  )
  await userEvent.clear(screen.getByLabelText('Chinese text'))
  await save()
  await waitFor(() => expect(mutateAsync).toHaveBeenCalledTimes(1))
  expect(mutateAsync).toHaveBeenCalledWith({
    enabled: false,
    level: 'normal',
    text_zh: '',
    text_en: 'Maintenance',
  })
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

test('a failed save keeps the unsaved edits', async () => {
  mutateAsync.mockRejectedValue(new Error('rejected'))
  render(<Fixture defaultValues={disabled} />)
  await userEvent.type(await screen.findByLabelText('Chinese text'), '维护')
  await userEvent.type(screen.getByLabelText('English text'), 'Maintenance')
  await userEvent.click(screen.getByRole('switch', { name: 'Show banner' }))
  await save()
  await waitFor(() => expect(mutateAsync).toHaveBeenCalledTimes(1))

  expect(screen.getByLabelText('English text')).toHaveValue('Maintenance')
  expect(screen.getByLabelText('Chinese text')).toHaveValue('维护')
  expect(screen.getByRole('switch', { name: 'Show banner' })).toBeChecked()
})
