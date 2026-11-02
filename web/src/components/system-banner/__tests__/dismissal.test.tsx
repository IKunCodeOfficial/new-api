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
  notifyManager,
  QueryClient,
  QueryClientProvider,
} from '@tanstack/react-query'
import { act, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'

import {
  SYSTEM_BANNER_DISMISSED_KEY,
  type SystemBannerStatus,
} from '@/components/system-banner/lib'
import { SystemBanner } from '@/components/system-banner/system-banner'
import type { SystemStatus } from '@/features/auth/types'
import { STATUS_QUERY_KEY } from '@/lib/status-query'

const BANNER: SystemBannerStatus = {
  level: 'normal',
  text_zh: '中文公告',
  text_en: 'Maintenance tonight',
  published_at: 1000,
}

let queryClient: QueryClient
const originals = {
  scrollWidth: Object.getOwnPropertyDescriptor(
    HTMLElement.prototype,
    'scrollWidth'
  ),
  clientWidth: Object.getOwnPropertyDescriptor(
    HTMLElement.prototype,
    'clientWidth'
  ),
}
function stubReducedMotion(matches: boolean) {
  vi.stubGlobal(
    'matchMedia',
    vi.fn(() => ({
      matches,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    }))
  )
}
let textWidth = 100
let trackWidth = 400

beforeEach(() => {
  // Deliver cache updates in a microtask so async act() flushes them.
  notifyManager.setScheduler(queueMicrotask)
  queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  stubReducedMotion(false)
  vi.stubGlobal(
    'ResizeObserver',
    class {
      observe() {}
      unobserve() {}
      disconnect() {}
    }
  )
  vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockReturnValue({
    height: 36,
    width: 800,
    top: 0,
    left: 0,
    right: 800,
    bottom: 36,
    x: 0,
    y: 0,
    toJSON: () => ({}),
  })
  Object.defineProperty(HTMLElement.prototype, 'scrollWidth', {
    configurable: true,
    get() {
      return textWidth
    },
  })
  Object.defineProperty(HTMLElement.prototype, 'clientWidth', {
    configurable: true,
    get() {
      return trackWidth
    },
  })
})

afterEach(() => {
  notifyManager.setScheduler((callback) => setTimeout(callback, 0))
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
  for (const key of ['scrollWidth', 'clientWidth'] as const) {
    if (originals[key]) {
      Object.defineProperty(HTMLElement.prototype, key, originals[key])
    } else {
      delete (HTMLElement.prototype as unknown as Record<string, unknown>)[key]
    }
  }
  textWidth = 100
  trackWidth = 400
  window.localStorage.clear()
  document.documentElement.style.removeProperty('--system-banner-height')
  queryClient.clear()
})

async function seed(systemBanner?: unknown) {
  const status = {
    success: true,
    data: { system_name: 'Test', system_banner: systemBanner },
  } as SystemStatus
  await act(async () => {
    queryClient.setQueryData(STATUS_QUERY_KEY, status)
  })
}

function renderBanner(systemBanner?: unknown) {
  queryClient.setQueryData(STATUS_QUERY_KEY, {
    success: true,
    data: { system_name: 'Test', system_banner: systemBanner },
  })
  return render(
    <QueryClientProvider client={queryClient}>
      <SystemBanner />
    </QueryClientProvider>
  )
}

function bannerHeight() {
  return document.documentElement.style.getPropertyValue(
    '--system-banner-height'
  )
}

describe('SystemBanner', () => {
  test('renders nothing when status has no banner', () => {
    renderBanner(undefined)
    expect(screen.queryByRole('region')).toBeNull()
    expect(bannerHeight()).toBe('0px')
  })

  test.each([
    ['missing fields', { level: 'normal' }],
    ['wrong types', { ...BANNER, published_at: '1000' }],
    ['unknown level', { ...BANNER, level: 'urgent' }],
  ])('renders nothing for a malformed banner with %s', (_name, value) => {
    renderBanner(value)
    expect(screen.queryByRole('region')).toBeNull()
  })

  test('shows banner with accessible region and close button', () => {
    renderBanner(BANNER)
    expect(screen.getByRole('region', { name: 'System notice' })).toBeVisible()
    expect(screen.getByRole('button', { name: 'Close notice' })).toBeVisible()
    expect(screen.getByText('Maintenance tonight')).toBeVisible()
    expect(bannerHeight()).toBe('36px')
  })

  test.each(['normal', 'important'] as const)(
    'marks the %s level on the region so it is styled distinctly',
    (level) => {
      renderBanner({ ...BANNER, level })
      expect(screen.getByRole('region')).toHaveAttribute('data-level', level)
    }
  )

  test('dismiss hides banner and stores published_at', async () => {
    renderBanner(BANNER)
    await userEvent.click(screen.getByRole('button', { name: 'Close notice' }))
    expect(screen.queryByRole('region')).toBeNull()
    expect(window.localStorage.getItem(SYSTEM_BANNER_DISMISSED_KEY)).toBe(
      '1000'
    )
  })

  test('dismiss sets banner height to 0px', async () => {
    renderBanner(BANNER)
    expect(bannerHeight()).toBe('36px')
    await userEvent.click(screen.getByRole('button', { name: 'Close notice' }))
    expect(bannerHeight()).toBe('0px')
  })

  test('already dismissed published_at stays hidden on mount', () => {
    window.localStorage.setItem(SYSTEM_BANNER_DISMISSED_KEY, '1000')
    renderBanner(BANNER)
    expect(screen.queryByRole('region')).toBeNull()
    expect(bannerHeight()).toBe('0px')
  })

  test('same published_at stays hidden after rerender', async () => {
    renderBanner(BANNER)
    await userEvent.click(screen.getByRole('button', { name: 'Close notice' }))
    await seed({ ...BANNER, level: 'important' })
    expect(screen.queryByRole('region')).toBeNull()
  })

  test('new published_at shows again', async () => {
    renderBanner(BANNER)
    await userEvent.click(screen.getByRole('button', { name: 'Close notice' }))
    await seed({ ...BANNER, published_at: 1001 })
    expect(screen.getByRole('region')).toBeVisible()
  })

  test('short text does not scroll, overflowing text scrolls', () => {
    textWidth = 100
    trackWidth = 400
    const short = renderBanner(BANNER)
    expect(short.container.querySelector('.animate-marquee')).toBeNull()
    short.unmount()

    textWidth = 900
    const long = renderBanner(BANNER)
    expect(long.container.querySelector('.animate-marquee')).not.toBeNull()
  })

  test('reduced motion never scrolls long text and lets it wrap', () => {
    stubReducedMotion(true)
    textWidth = 900
    trackWidth = 400
    const view = renderBanner(BANNER)
    expect(view.container.querySelector('.animate-marquee')).toBeNull()
    expect(
      view.container.querySelector('[data-slot="system-banner-track"]')
    ).not.toHaveClass('whitespace-nowrap')
    expect(bannerHeight()).toBe('36px')
  })
})
