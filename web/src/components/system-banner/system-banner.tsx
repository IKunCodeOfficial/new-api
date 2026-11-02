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
import { Cancel01Icon } from '@hugeicons/core-free-icons'
import { HugeiconsIcon } from '@hugeicons/react'
import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { useStatus } from '@/hooks/use-status'
import { cn } from '@/lib/utils'

import {
  parseSystemBanner,
  pickSystemBannerText,
  SYSTEM_BANNER_DISMISSED_KEY,
} from './lib'

const BANNER_HEIGHT_VAR = '--system-banner-height'
const MARQUEE_SPEED_PX_PER_SECOND = 60

function setBannerHeight(height: number) {
  document.documentElement.style.setProperty(BANNER_HEIGHT_VAR, `${height}px`)
}

const REDUCED_MOTION_QUERY = '(prefers-reduced-motion: reduce)'

function readDismissedAt(): number {
  try {
    return Number(window.localStorage.getItem(SYSTEM_BANNER_DISMISSED_KEY))
  } catch {
    return 0
  }
}

/**
 * Site-wide notice bar. Publishes its height as `--system-banner-height`
 * (0px when hidden) so fixed layouts can offset themselves.
 */
export function SystemBanner() {
  const { t, i18n } = useTranslation()
  const { status } = useStatus()
  const banner = parseSystemBanner(status?.data?.system_banner)
  const [dismissedAt, setDismissedAt] = useState(readDismissedAt)
  const [marqueeDuration, setMarqueeDuration] = useState<number | null>(null)
  const [reducedMotion, setReducedMotion] = useState(false)
  const rootRef = useRef<HTMLDivElement>(null)
  const trackRef = useRef<HTMLDivElement>(null)
  const textRef = useRef<HTMLSpanElement>(null)

  const visible = banner !== null && banner.published_at !== dismissedAt
  const text = banner
    ? pickSystemBannerText(banner, i18n.resolvedLanguage || i18n.language)
    : ''

  useEffect(() => {
    const query = window.matchMedia?.(REDUCED_MOTION_QUERY)
    if (!query) return
    const sync = () => setReducedMotion(query.matches)
    sync()
    query.addEventListener('change', sync)
    return () => query.removeEventListener('change', sync)
  }, [])

  useEffect(() => {
    const root = rootRef.current
    if (!visible || !root) {
      setBannerHeight(0)
      return
    }
    const measure = () => setBannerHeight(root.getBoundingClientRect().height)
    measure()
    const observer = new ResizeObserver(measure)
    observer.observe(root)
    return () => {
      observer.disconnect()
      setBannerHeight(0)
    }
  }, [visible])

  useEffect(() => {
    const track = trackRef.current
    const textEl = textRef.current
    if (!visible || !track || !textEl) return
    // Reduced motion wraps the text instead of scrolling it. Wrapping changes
    // the measured width, so the decision must not depend on layout here.
    if (reducedMotion) {
      setMarqueeDuration(null)
      return
    }
    const measure = () => {
      const textWidth = textEl.scrollWidth
      const trackWidth = track.clientWidth
      setMarqueeDuration(
        textWidth > trackWidth
          ? (textWidth + trackWidth) / MARQUEE_SPEED_PX_PER_SECOND
          : null
      )
    }
    measure()
    const observer = new ResizeObserver(measure)
    observer.observe(track)
    return () => observer.disconnect()
  }, [visible, text, reducedMotion])

  if (!banner || !visible) return null

  const dismiss = () => {
    try {
      window.localStorage.setItem(
        SYSTEM_BANNER_DISMISSED_KEY,
        String(banner.published_at)
      )
    } catch {
      // Storage unavailable: the banner is only hidden for this page view.
    }
    setDismissedAt(banner.published_at)
  }

  return (
    <div
      ref={rootRef}
      role='region'
      aria-label={t('System notice')}
      data-level={banner.level}
      className={cn(
        'fixed inset-x-0 top-0 z-[60] flex items-center gap-2 px-3 py-1.5 text-sm',
        banner.level === 'important'
          ? 'bg-red-600 text-white'
          : 'border-b border-amber-300 bg-amber-50 text-amber-950 dark:border-amber-800 dark:bg-amber-950 dark:text-amber-50'
      )}
    >
      <div
        ref={trackRef}
        data-slot='system-banner-track'
        className={cn(
          'marquee-track min-w-0 flex-1 overflow-hidden text-center',
          reducedMotion ? 'whitespace-normal' : 'whitespace-nowrap'
        )}
      >
        <span
          className={cn(marqueeDuration !== null && 'animate-marquee')}
          style={
            marqueeDuration === null
              ? undefined
              : ({
                  '--marquee-duration': `${marqueeDuration}s`,
                } as React.CSSProperties)
          }
        >
          <span ref={textRef} className='inline-block'>
            {text}
          </span>
        </span>
      </div>
      <Button
        type='button'
        variant='ghost'
        size='icon-xs'
        aria-label={t('Close notice')}
        className='shrink-0 text-current hover:bg-black/10 hover:text-current dark:hover:bg-white/10'
        onClick={dismiss}
      >
        <HugeiconsIcon icon={Cancel01Icon} strokeWidth={2} aria-hidden />
      </Button>
    </div>
  )
}
