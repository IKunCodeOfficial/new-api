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
  focusManager,
  notifyManager,
  QueryClient,
  QueryClientProvider,
} from '@tanstack/react-query'
import { act, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'

import {
  SYSTEM_BANNER_DISMISSED_KEY,
  SYSTEM_BANNER_QUERY_KEY,
  SYSTEM_BANNER_REFRESH_MS,
  type SystemBannerStatus,
} from '@/components/system-banner/lib'
import { SystemBanner } from '@/components/system-banner/system-banner'
import { getSystemBanner } from '@/lib/api'
import { STATUS_QUERY_KEY } from '@/lib/status-query'

vi.mock('@/lib/api', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/lib/api')>()),
  getSystemBanner: vi.fn(),
}))

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
  vi.useRealTimers()
  focusManager.setFocused(undefined)
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

/** What `/api/system_banner` returns on the next request. */
function serve(systemBanner?: unknown) {
  vi.mocked(getSystemBanner).mockResolvedValue(systemBanner ?? null)
}

/** The banner changes on the server and the page polls once. */
async function republish(systemBanner: unknown) {
  serve(systemBanner)
  await act(async () => {
    await queryClient.refetchQueries({ queryKey: SYSTEM_BANNER_QUERY_KEY })
  })
}

/** Status snapshot and banner endpoint agree, as on a normal page load. */
async function renderBanner(systemBanner?: unknown) {
  queryClient.setQueryData(STATUS_QUERY_KEY, {
    success: true,
    data: { system_name: 'Test', system_banner: systemBanner },
  })
  serve(systemBanner)
  const view = render(
    <QueryClientProvider client={queryClient}>
      <SystemBanner />
    </QueryClientProvider>
  )
  await act(async () => {})
  return view
}

function bannerHeight() {
  return document.documentElement.style.getPropertyValue(
    '--system-banner-height'
  )
}

describe('SystemBanner', () => {
  test('renders nothing when status has no banner', async () => {
    await renderBanner(undefined)
    expect(screen.queryByRole('region')).toBeNull()
    expect(bannerHeight()).toBe('0px')
  })

  test.each([
    ['missing fields', { level: 'normal' }],
    ['wrong types', { ...BANNER, published_at: '1000' }],
    ['unknown level', { ...BANNER, level: 'urgent' }],
  ])('renders nothing for a malformed banner with %s', async (_name, value) => {
    await renderBanner(value)
    expect(screen.queryByRole('region')).toBeNull()
  })

  test('shows banner with accessible region and close button', async () => {
    await renderBanner(BANNER)
    expect(screen.getByRole('region', { name: 'System notice' })).toBeVisible()
    expect(screen.getByRole('button', { name: 'Close notice' })).toBeVisible()
    expect(screen.getByText('Maintenance tonight')).toBeVisible()
    expect(bannerHeight()).toBe('36px')
  })

  test.each(['normal', 'important'] as const)(
    'marks the %s level on the region so it is styled distinctly',
    async (level) => {
      await renderBanner({ ...BANNER, level })
      expect(screen.getByRole('region')).toHaveAttribute('data-level', level)
    }
  )

  test.each([
    ['normal', 'Notice'],
    ['important', 'Important'],
  ] as const)(
    'exposes the %s level to assistive tech as hidden-from-view text',
    async (level, label) => {
      await renderBanner({ ...BANNER, level })
      expect(
        screen.getByRole('region', { name: 'System notice' })
      ).toHaveTextContent(label)
      expect(screen.getByText(label)).toHaveClass('sr-only')
    }
  )

  test('dismiss hides banner and stores published_at', async () => {
    await renderBanner(BANNER)
    await userEvent.click(screen.getByRole('button', { name: 'Close notice' }))
    expect(screen.queryByRole('region')).toBeNull()
    expect(window.localStorage.getItem(SYSTEM_BANNER_DISMISSED_KEY)).toBe(
      '1000'
    )
  })

  test('dismiss sets banner height to 0px', async () => {
    await renderBanner(BANNER)
    expect(bannerHeight()).toBe('36px')
    await userEvent.click(screen.getByRole('button', { name: 'Close notice' }))
    expect(bannerHeight()).toBe('0px')
  })

  test('already dismissed published_at stays hidden on mount', async () => {
    window.localStorage.setItem(SYSTEM_BANNER_DISMISSED_KEY, '1000')
    await renderBanner(BANNER)
    expect(screen.queryByRole('region')).toBeNull()
    expect(bannerHeight()).toBe('0px')
  })

  test('same published_at stays hidden after rerender', async () => {
    await renderBanner(BANNER)
    await userEvent.click(screen.getByRole('button', { name: 'Close notice' }))
    await republish({ ...BANNER, level: 'important' })
    expect(screen.queryByRole('region')).toBeNull()
  })

  test('new published_at shows again', async () => {
    await renderBanner(BANNER)
    await userEvent.click(screen.getByRole('button', { name: 'Close notice' }))
    await republish({ ...BANNER, published_at: 1001 })
    expect(screen.getByRole('region')).toBeVisible()
  })

  test('short text does not scroll', async () => {
    textWidth = 100
    trackWidth = 400
    const view = await renderBanner(BANNER)
    expect(view.container.querySelector('.animate-marquee')).toBeNull()
  })

  test('overflowing text scrolls', async () => {
    textWidth = 900
    trackWidth = 400
    const view = await renderBanner(BANNER)
    expect(view.container.querySelector('.animate-marquee')).not.toBeNull()
  })

  test('reduced motion never scrolls long text and lets it wrap', async () => {
    stubReducedMotion(true)
    textWidth = 900
    trackWidth = 400
    const view = await renderBanner(BANNER)
    expect(view.container.querySelector('.animate-marquee')).toBeNull()
    expect(
      view.container.querySelector('[data-slot="system-banner-track"]')
    ).not.toHaveClass('whitespace-nowrap')
    expect(bannerHeight()).toBe('36px')
  })

  test('shows a banner published after load on the next scheduled refresh', async () => {
    vi.useFakeTimers()
    await renderBanner(undefined)
    expect(screen.queryByRole('region')).toBeNull()

    serve(BANNER)
    await act(async () => {
      await vi.advanceTimersByTimeAsync(SYSTEM_BANNER_REFRESH_MS)
    })

    expect(screen.getByRole('region')).toBeVisible()
  })

  test('refreshes the banner when the tab regains focus', async () => {
    await renderBanner(undefined)

    serve(BANNER)
    await act(async () => {
      focusManager.setFocused(false)
      focusManager.setFocused(true)
    })

    expect(screen.getByRole('region')).toBeVisible()
  })

  test('hides the banner once a refresh reports it turned off', async () => {
    await renderBanner(BANNER)
    await republish(null)
    expect(screen.queryByRole('region')).toBeNull()
    expect(bannerHeight()).toBe('0px')
  })

  test('keeps the last banner when a refresh fails', async () => {
    await renderBanner(BANNER)
    vi.mocked(getSystemBanner).mockRejectedValue(new Error('offline'))
    await act(async () => {
      await queryClient.refetchQueries({ queryKey: SYSTEM_BANNER_QUERY_KEY })
    })
    expect(screen.getByRole('region')).toBeVisible()
  })

  test('keeps the status banner when the first banner request fails', async () => {
    queryClient.setQueryData(STATUS_QUERY_KEY, {
      success: true,
      data: { system_name: 'Test', system_banner: BANNER },
    })
    vi.mocked(getSystemBanner).mockRejectedValue(new Error('rate limited'))
    render(
      <QueryClientProvider client={queryClient}>
        <SystemBanner />
      </QueryClientProvider>
    )
    await act(async () => {})
    expect(screen.getByRole('region')).toBeVisible()
  })
})
