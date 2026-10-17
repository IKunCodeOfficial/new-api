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
import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactNode } from 'react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { ModelStatus } from '..'
import type { ModelStatusModel, ModelStatusResponse } from '../types'

const mocks = vi.hoisted(() => ({
  search: {} as Record<string, string>,
  data: undefined as unknown,
  pageRenders: 0,
}))

// The legend is a plain child of the page, so its render count tracks how
// often the page component itself renders.
vi.mock('../components/status-legend', () => ({
  StatusLegend: () => {
    mocks.pageRenders++
    return null
  },
}))

vi.mock('@tanstack/react-router', () => ({
  getRouteApi: () => ({
    useSearch: () => mocks.search,
    useNavigate: () => vi.fn(),
  }),
}))

vi.mock('@/components/layout', () => ({
  PublicLayout: (props: { children: ReactNode }) => <div>{props.children}</div>,
}))

vi.mock('../api', () => ({
  useModelStatus: () => ({
    data: mocks.data,
    isLoading: false,
    isError: false,
    isFetching: false,
    refetch: vi.fn(),
  }),
}))

function model(
  name: string,
  rate: number,
  detail: ModelStatusModel['detail']
): ModelStatusModel {
  return {
    name,
    vendor_icon: '',
    availability: rate,
    current: rate,
    ttft_avg_ms: 820,
    latency_avg_ms: 4200,
    tps: 64,
    buckets: [{ rate, ttft_ms: 820 }, null, { rate, ttft_ms: 820 }],
    detail,
  }
}

const detail = {
  ttft_p50: 700,
  ttft_p95: 1900,
  ttft_p99: 2600,
  latency_p50: 3800,
  latency_p95: 8100,
  latency_p99: 12000,
  fail_share: { '5xx': 0.5, '429': 0.25, timeout: 0.25, other: 0 },
}

function payload(range: '1h' | '24h'): ModelStatusResponse {
  const hasDetail = range === '1h'
  return {
    range,
    bucket_seconds: range === '1h' ? 60 : 3600,
    window_start: 1_760_000_000,
    updated_at: 1_760_003_600,
    has_detail: hasDetail,
    groups: [
      {
        key: 'default',
        desc: 'Default group',
        ratio: 1,
        availability: 0.9,
        models: [
          model('healthy-model', 1, hasDetail ? detail : undefined),
          model('broken-model', 0.5, hasDetail ? detail : undefined),
        ],
      },
    ],
  }
}

function modelRows() {
  return screen
    .getAllByRole('button')
    .filter((el) => el.hasAttribute('aria-expanded'))
}

afterEach(() => {
  mocks.search = {}
  mocks.data = undefined
  mocks.pageRenders = 0
})

describe('ModelStatus page', () => {
  it('summarises the failing model in the banner and lists it first', () => {
    mocks.data = payload('1h')
    render(<ModelStatus />)

    expect(screen.getByText('1 model failing')).toBeInTheDocument()
    expect(within(modelRows()[0]).getByText('broken-model')).toBeVisible()
  })

  it('never renders a request count', () => {
    mocks.data = payload('1h')
    const { container } = render(<ModelStatus />)

    expect(container.textContent).not.toMatch(/\d[\d,.]*\s*requests?\b/i)
  })

  it('reveals latency percentiles when a 1h row is expanded', async () => {
    mocks.data = payload('1h')
    render(<ModelStatus />)

    await userEvent.click(modelRows()[0])

    expect(modelRows()[0]).toHaveAttribute('aria-expanded', 'true')
    expect(screen.getByText('P95')).toBeInTheDocument()
  })

  it('shows the bucket tooltip on bar hover without re-rendering the page', async () => {
    mocks.data = payload('1h')
    const { container } = render(<ModelStatus />)
    const bar = container.querySelector<HTMLElement>('[data-bucket="0"]')
    if (!bar) throw new Error('timeline bar not rendered')
    const rendersBeforeHover = mocks.pageRenders
    expect(rendersBeforeHover).toBeGreaterThan(0)

    await userEvent.hover(bar)

    const tooltip = screen.getByRole('tooltip')
    expect(tooltip).toHaveTextContent(/\d{2}:\d{2} – \d{2}:\d{2}/)
    expect(within(tooltip).getByText('Success rate')).toBeInTheDocument()
    expect(mocks.pageRenders).toBe(rendersBeforeHover)

    await userEvent.unhover(bar)

    expect(screen.queryByRole('tooltip')).not.toBeInTheDocument()
  })

  it('shows only throughput when a 24h row is expanded', async () => {
    mocks.search = { range: '24h' }
    mocks.data = payload('24h')
    render(<ModelStatus />)

    await userEvent.click(modelRows()[0])

    expect(screen.getByText('Output speed')).toBeInTheDocument()
    expect(screen.queryByText('P95')).not.toBeInTheDocument()
  })
})
