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
import { describe, expect, it } from 'vitest'

import {
  bannerSummary,
  bucketTimeFormat,
  EMPTY_VALUE,
  formatDuration,
  problemChips,
  severityRank,
  sortModels,
  statusOf,
} from '../lib/status'
import type { ModelStatusGroup, ModelStatusModel } from '../types'

function model(name: string, current: number | null): ModelStatusModel {
  return {
    name,
    vendor_icon: '',
    availability: current,
    current,
    ttft_avg_ms: 0,
    latency_avg_ms: 0,
    tps: 0,
    buckets: [],
  }
}

function group(key: string, models: ModelStatusModel[]): ModelStatusGroup {
  return { key, desc: '', ratio: 1, availability: null, models }
}

function healthy(count: number): ModelStatusModel[] {
  return Array.from({ length: count }, (_, i) => model(`ok${i}`, 1))
}

describe('statusOf', () => {
  it('maps rates to levels at the 0.99 and 0.95 thresholds', () => {
    expect(statusOf(0.99)).toBe('success')
    expect(statusOf(0.9899)).toBe('warning')
    expect(statusOf(0.95)).toBe('warning')
    expect(statusOf(0.9499)).toBe('danger')
  })

  it('returns neutral when there is no rate', () => {
    expect(statusOf(null)).toBe('neutral')
    expect(statusOf(undefined)).toBe('neutral')
  })
})

describe('severityRank', () => {
  it('orders danger before warning before success before neutral', () => {
    expect(severityRank('danger')).toBe(0)
    expect(severityRank('warning')).toBe(1)
    expect(severityRank('success')).toBe(2)
    expect(severityRank('neutral')).toBe(3)
  })
})

describe('sortModels', () => {
  it('sorts by severity and keeps backend volume order within a level', () => {
    const sorted = sortModels([
      model('ok-A', 1),
      model('bad-B', 0.5),
      model('none-C', null),
      model('slow-D', 0.97),
    ])
    expect(sorted.map((m) => m.name)).toEqual([
      'bad-B',
      'slow-D',
      'ok-A',
      'none-C',
    ])
  })

  it('keeps input order among models of the same level', () => {
    const sorted = sortModels([model('a', 0.1), model('b', 0.2)])
    expect(sorted.map((m) => m.name)).toEqual(['a', 'b'])
  })

  it('does not mutate the input array', () => {
    const input = [model('a', 1), model('b', 0.1)]
    sortModels(input)
    expect(input.map((m) => m.name)).toEqual(['a', 'b'])
  })
})

describe('problemChips', () => {
  it('lists danger before warning and caps at max', () => {
    const groups = [
      group('g1', [
        model('w1', 0.97),
        model('w2', 0.97),
        model('w3', 0.97),
        model('ok', 1),
      ]),
      group('g2', [model('d1', 0.1), model('d2', 0.2), model('d3', 0.3)]),
    ]
    const chips = problemChips(groups)
    expect(chips).toHaveLength(5)
    expect(chips.map((c) => c.level)).toEqual([
      'danger',
      'danger',
      'danger',
      'warning',
      'warning',
    ])
    expect(chips[0]).toEqual({ model: 'd1', group: 'g2', level: 'danger' })
  })

  it('returns an empty list when nothing is failing or degraded', () => {
    const groups = [group('g', [model('ok', 1), model('none', null)])]
    expect(problemChips(groups)).toEqual([])
  })
})

describe('bannerSummary', () => {
  it('summarises 1 failing + 2 degraded of 10 as danger with 7 healthy', () => {
    const models = [
      model('d', 0.1),
      model('w1', 0.97),
      model('w2', 0.96),
      ...healthy(7),
    ]
    expect(bannerSummary([group('g', models)])).toEqual({
      level: 'danger',
      failing: 1,
      degraded: 2,
      healthy: 7,
      total: 10,
    })
  })

  it('counts neutral models toward total but not healthy', () => {
    const summary = bannerSummary([
      group('g', [model('ok', 1), model('none', null)]),
    ])
    expect(summary).toEqual({
      level: 'success',
      failing: 0,
      degraded: 0,
      healthy: 1,
      total: 2,
    })
  })

  it('is warning with only degraded models', () => {
    expect(bannerSummary([group('g', [model('w', 0.97)])]).level).toBe(
      'warning'
    )
  })

  it('is neutral with no traffic or no groups', () => {
    expect(bannerSummary([group('g', [model('n', null)])]).level).toBe(
      'neutral'
    )
    expect(bannerSummary([]).level).toBe('neutral')
  })
})

describe('bucketTimeFormat', () => {
  it('picks a time format per range', () => {
    expect(bucketTimeFormat('5m')).toBe('HH:mm:ss')
    expect(bucketTimeFormat('1h')).toBe('HH:mm')
    expect(bucketTimeFormat('24h')).toBe('HH:mm')
    expect(bucketTimeFormat('7d')).toBe('MM-DD HH:mm')
    expect(bucketTimeFormat('30d')).toBe('MM-DD HH:mm')
  })
})

describe('formatDuration', () => {
  it.each([
    [null, EMPTY_VALUE],
    [0, EMPTY_VALUE],
    [820, '820 ms'],
    [999.4, '999 ms'],
    [999.5, '1.0 s'],
    [999.9, '1.0 s'],
    [1449, '1.4 s'],
    [12000, '12.0 s'],
  ])('formats %s', (ms, want) => {
    expect(formatDuration(ms, 'en-US')).toBe(want)
  })
})
