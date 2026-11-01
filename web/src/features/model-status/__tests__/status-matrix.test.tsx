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
import { describe, expect, it } from 'vitest'

import { StatusMatrix } from '../components/status-matrix'
import type { ModelStatusGroup, ModelStatusModel } from '../types'

function model(name: string): ModelStatusModel {
  return {
    name,
    vendor_icon: '',
    availability: 1,
    current: 1,
    ttft_avg_ms: 7600,
    latency_avg_ms: 9000,
    tps: 40,
    buckets: [],
  }
}

function group(key: string, desc: string, models: string[]): ModelStatusGroup {
  return { key, desc, ratio: 1, availability: 1, models: models.map(model) }
}

describe('StatusMatrix', () => {
  it('keeps every group column at its minimum width when there are many groups', () => {
    const groups = Array.from({ length: 22 }, (_, i) =>
      group(`group-${i}`, '', ['claude-opus-5-5'])
    )

    render(<StatusMatrix groups={groups} query='' />)

    const header = screen.getAllByRole('row')[0]
    expect(header.style.gridTemplateColumns).toBe(
      '220px repeat(22, minmax(112px, 1fr))'
    )
    // Model column + 22 × (column + gap) + row padding.
    expect(header.parentElement).toHaveStyle({ minWidth: '2880px' })
  })

  it('clamps a long group description and exposes it in the header title', () => {
    const desc =
      'Claude Code Max, only for official client | unstable, may be throttled'
    render(<StatusMatrix groups={[group('cc-max', desc, ['m'])]} query='' />)

    const header = screen.getAllByRole('columnheader')[1]
    expect(header).toHaveAttribute('title', `cc-max\n${desc}`)
    expect(within(header).getByText(desc)).toHaveClass('line-clamp-2')
  })

  it('renders a group without the model as a quiet cell with an accessible label', () => {
    render(
      <StatusMatrix
        groups={[
          group('a', '', ['shared', 'only-a']),
          group('b', '', ['shared']),
        ]}
        query=''
      />
    )

    // Row 0 is the header; rows keep first-appearance order.
    const row = screen.getAllByRole('row')[2]
    expect(within(row).getByRole('rowheader')).toHaveTextContent('only-a')
    const cells = within(row).getAllByRole('cell')
    expect(cells).toHaveLength(2)
    expect(cells[0]).toHaveTextContent('100.00%')
    expect(cells[0]).toHaveTextContent('7.6 s')
    expect(cells[1]).toHaveAttribute('title', 'b does not offer this model')
    expect(within(cells[1]).getByText('Not offered')).toHaveClass('sr-only')
  })
})
