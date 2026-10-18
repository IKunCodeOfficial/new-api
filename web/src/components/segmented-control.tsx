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
import type { ReactNode } from 'react'

import { ToggleGroup, ToggleGroupItem } from '@/components/ui/toggle-group'
import { cn } from '@/lib/utils'

export type SegmentedControlOption<T extends string> = {
  value: T
  label: ReactNode
  title?: string
}

type SegmentedControlProps<T extends string> = {
  value: T
  onValueChange: (value: T) => void
  options: SegmentedControlOption<T>[]
  ariaLabel: string
  className?: string
  // Lets segments flow onto further rows when they don't fit the width.
  wrap?: boolean
}

// Single-select toggle group that cannot be deselected: pressing the active
// segment keeps it selected instead of clearing the value.
export function SegmentedControl<T extends string>(
  props: SegmentedControlProps<T>
) {
  return (
    <ToggleGroup
      value={[props.value]}
      onValueChange={(values) => {
        const next = props.options.find((o) => o.value === values[0])
        if (next) props.onValueChange(next.value)
      }}
      spacing={1}
      aria-label={props.ariaLabel}
      className={cn(
        'bg-muted/60 h-8 gap-0 rounded-md border p-0.5',
        props.wrap && 'h-auto max-w-full flex-wrap',
        props.className
      )}
    >
      {props.options.map((option) => (
        <ToggleGroupItem
          key={option.value}
          value={option.value}
          title={option.title}
          className={cn(
            'text-muted-foreground h-full min-w-0 rounded-[5px] px-3 text-xs font-medium',
            'hover:text-foreground hover:bg-transparent',
            'aria-pressed:bg-foreground aria-pressed:text-background aria-pressed:shadow-sm',
            props.wrap && 'h-7'
          )}
        >
          {option.label}
        </ToggleGroupItem>
      ))}
    </ToggleGroup>
  )
}
