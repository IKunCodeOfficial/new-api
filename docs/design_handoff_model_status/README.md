# Handoff: 模型状态页（Model Status）

## Overview
A new public page at `/status` (sibling of `/pricing` 模型广场). It shows the **availability and performance of each model in each group**, computed from **real user request logs (not probes)**. Users can switch the time range: last 5 minutes, 1 hour, 24 hours, 7 days or 30 days.

Target repo: `IKunCodeOfficial/new-api`. Frontend: `web/default` (React 19 + TS, TanStack Router/Query, Tailwind v4, Radix/shadcn, lucide-react, i18next). Backend: Go (gin + gorm).

## About the Design Files
`Model Status.dc.html` is a **design reference built in HTML**, a prototype of the intended look and behavior. It is not production code to copy. Rebuild it in `web/default` using the existing patterns: Tailwind classes, `cn()`, shadcn components, `useTranslation()` for every string, and React Query for data. All data in the prototype is **mock** (seeded random generator in the `<script data-dc-script>` block). Replace it with a real backend API.

## Fidelity
**High-fidelity.** Colors, typography, spacing and interactions are final. The page shell reuses existing components, so reuse them instead of rebuilding:
- `PublicLayout` / `PublicHeader` (`web/default/src/components/layout/components/`)
- The hero block and background glow are copied from `features/pricing/index.tsx`
- `SegmentedControl` is copied from `features/pricing/components/pricing-toolbar.tsx`; extract it into a shared component
- Status dot + label follows `components/status-badge.tsx` (`success` / `warning` / `danger` / `neutral` variants)
- Group header row style follows `features/dashboard/components/overview/uptime-panel.tsx`

## Screens / Views

### Page structure (top → bottom)
Container: `relative mx-auto w-full max-w-7xl px-6 pt-20 pb-12` (prototype uses 1280px max). Background glow: same 3 radial gradients as the pricing page, `h-[600px] opacity-20 dark:opacity-10`, masked to fade out at the bottom.

1. **Hero** (centered, `max-w-3xl`, `pt-8 mb-8`)
   - Eyebrow: `Model Status`, `text-xs font-medium tracking-widest uppercase text-muted-foreground`
   - H1: `模型状态`, `text-[clamp(2rem,5.5vw,3.5rem)] leading-[1.15] font-bold tracking-tight`
   - Sub: `基于全站真实用户请求统计各分组、各模型的可用性与性能`, `text-base text-muted-foreground/80 mt-4`

2. **Status banner card** (`rounded-xl border bg-card/70 overflow-hidden mb-4`)
   - Row `px-5 py-4 flex justify-between flex-wrap gap-3`
   - Left: pulsing live dot (10px; ping animation `scale 1→2.6, opacity .7→0, 1.8s`) in the overall status color, then:
     - Title `text-[15px] font-semibold` in the status text color:
       - `所有模型运行正常` (success)
       - `N 个模型当前异常，M 个降级` (danger)
       - `M 个模型当前降级` (warning)
     - Sub `text-xs text-muted-foreground`: `{healthy} / {total} 个分组模型正常 · 更新于 HH:mm:ss · {每 30 秒}自动刷新`
   - Right: up to 5 chips for problem models (failing first): `h-6 px-2 rounded-md border bg-background text-xs`, holding a 6px dot, the model name (mono, semibold) and the group (muted)
   - Note: the user removed the stat-tiles row from the design. Do not implement it.

3. **Toolbar** (`sticky top-[72px] z-20 rounded-xl border p-3 bg-background/85 backdrop-blur-lg mb-4`)
   - Left: SegmentedControl **time range** `5 分钟 | 1 小时 | 24 小时 | 7 天 | 30 天`; SegmentedControl **group** `全部分组 | default | vip | …` (from usable groups; reuse `EXCLUDED_GROUPS`)
   - Right: search input (`h-8 w-[220px] rounded-md border pl-[30px] text-[13px]`, lucide `Search` 14px, placeholder `搜索模型名称`); SegmentedControl **view** `[Rows3] 列表 | [Grid2X2] 矩阵`; refresh icon button `size-8 rounded-md border` (lucide `RotateCw`, spins while fetching)
   - SegmentedControl spec: container `bg-muted/60 h-8 rounded-md border p-0.5`; button `rounded-[5px] px-3 text-xs font-medium`; active `bg-foreground text-background shadow-sm`; inactive `text-muted-foreground hover:text-foreground`

4. **List view** (default): one card per group, `space-y-4`
   - Card `rounded-xl border bg-card/60 overflow-hidden`; inner wrapper `overflow-x-auto` with `min-w-[960px]`
   - **Group header** `px-5 py-3 bg-muted/30 border-b border-border/60`
     - Left (baseline aligned, gap 10px): group key (mono 15px bold), desc (13px muted), `倍率 {ratio}x · {n} 个模型` (12px muted/70)
     - Right: issue summary with dot (`全部正常` / `N 个异常 · M 个降级` / `M 个降级`, 12px medium, status color); group availability (mono 15px semibold, status color)
   - **Column header** row (11px medium muted/70, `py-2 px-5 border-b border-border/40`): `模型 | {N 前} ……… 现在 | 可用率 | 首字时延 | 平均耗时 | (chevron)`
   - Grid template for header and rows: `236px minmax(260px,1fr) 84px 92px 92px 28px`, gap 16px
   - **Model row** `py-2.5 px-5 border-b border-border/40 cursor-pointer hover:bg-muted/50`. If current status is failing, row bg is `bad 5%` tint.
     - Model cell: 32px icon box (`rounded-lg bg-muted/70`, 18px vendor icon via `getLobeIcon`) + name (mono 13px bold, truncate) + status line below (6px dot + `当前正常` / `当前降级` / `当前异常` / `无请求`, 12px medium, status text color)
     - Timeline: flex of N bars, `h-7 gap-px`, each `flex-1 min-w-0 rounded-[2px]`, color by that bucket's success rate; grey when the bucket has no requests; `hover:opacity-65`
     - 可用率: mono 14px semibold; foreground color when ≥99%, otherwise status text color
     - 首字时延: mono 13px; 平均耗时: mono 13px muted
     - Chevron (lucide `ChevronDown` 14px) rotates 180° when expanded
   - **Expanded row** (one open at a time): `grid grid-cols-3 gap-3 pt-1 pb-4 pl-[62px] pr-5`, each panel `rounded-lg border bg-background px-3.5 py-3`
     - `时延分位`: 3×3 grid, rows P50/P95/P99, columns 首字 / 总耗时 (mono semibold 12px)
     - `失败原因`: 6px stacked bar (5xx = danger, 429 = warning, 超时 = teal `oklch(0.6 0.118 184.704)`, 其他 = slate-400) + 2-col legend showing **percentages only**
     - `吞吐`: `输出速度  {n} tok/s`
   - Sort within a group: current severity first (异常 > 降级 > 正常 > 无请求), then by request volume (descending)
   - Empty search result per group: `没有匹配的模型` (13px muted, centered, `py-7`)
   - **Request counts are not shown anywhere on the page** (user decision). Use them only for weighting and sorting.

5. **Matrix view**: one card, `min-w-[860px]`. Grid `236px repeat(groupCount, minmax(0,1fr))`, gap 8px
   - Header row (`bg-muted/30`): `模型 / 分组`, then per group: key (mono 13px bold) + `{desc} · {ratio}x` (11px muted)
   - Each row: icon (28px box) + model name; one cell per group, `h-12 rounded-lg px-3 border flex justify-between`: dot + availability (mono 14px semibold) on the left, TTFT (mono 11px muted, nowrap) on the right
     - If the current status is slowed or failing: bg `{status} 9%`, border `{status} 35%`
     - If the model is not in that group: transparent cell, text `— / 未开放`
     - Native `title`: `{model} @ {group} · 当前{label}`

6. **Footer legend** (12px muted, `mt-5`): swatches `正常 ≥ 99%`, `降级 95% – 99%`, `异常 < 95%`, `无请求`. Right side: `状态标签取最近时段数据 · 首字时延仅统计流式请求`

### Bar tooltip
Fixed position, centered above the hovered bar (`translate(-50%,-100%)`, 8px gap). Style: `rounded-md border bg-card px-2.5 py-2 text-xs shadow`, min-width 168px.
- Title: time span, e.g. `14:00 – 14:30`
- Rows: `成功率` (status color) and `首字时延`

Time format: 5m range uses `HH:mm:ss`; 1h and 24h use `HH:mm`; 7d and 30d use `MM-DD HH:mm`. Use the shadcn `Tooltip` if it performs well with many bars; otherwise use a single shared floating tooltip like the prototype.

## Interactions & Behavior
- Time range, group, view and search should live in URL search params (zod `validateSearch`, same as `routes/pricing/index.tsx`): `?range=1h&group=all&view=list&q=`
- Changing the range collapses any expanded row
- Auto-refresh (`refetchInterval`): 5m every 10s; 1h every 30s; 24h every 5min; 7d every 30min; 30d every 60min
- Header: reuse `PublicHeader`, which already shrinks into a floating pill on scroll. Add a `模型状态` nav link (see "Nav link" below).
- Respect `prefers-reduced-motion`: turn off the ping and the spin.
- Dark mode: driven by theme tokens. Monochrome vendor icons (openai, moonshot) need to invert in dark mode; `getLobeIcon` already handles this.

## Status rules
- Bucket and range availability = `success / total` (request-weighted)
- `正常` ≥ 99%; `降级` 95% to under 99%; `异常` < 95%; `无请求` when total = 0
- **Current status** (badge, banner, sort) = availability over the most recent `max(3, ceil(N × 8%))` buckets
- **Range availability** (the % column) = the whole selected range

## Buckets per range
| range | buckets | bucket size |
|---|---|---|
| 5m | 30 | 10 s |
| 1h | 60 | 1 min |
| 24h | 48 | 30 min |
| 7d | 42 | 4 h |
| 30d | 60 | 12 h |

## Backend (suggested)
The data source is the `logs` table (`model/log.go`):
- Successful calls are `type = 2` (consume)
- Failures are `type = 5` (`LogTypeError`)
- Useful fields: `use_time` (seconds), `is_stream`, `group`, `model_name`, `created_at`, and `other` (JSON)

Steps:
1. Add first-token latency (ms) to `other` when recording logs (e.g. `other.frt`), if it isn't already there.
2. Record the error category in error logs: upstream status code, 429, or timeout.
3. Add a new public endpoint `GET /api/model_status?range=1h`. It aggregates by `(group, model_name, bucket)` and returns per bucket `{ts, total, success, ttft_avg}`. Per (group, model) it returns:
   - `{total, success, ttft_avg, latency_avg, ttft_p50/p95/p99, latency_p50/p95/p99, err: {5xx, 429, timeout, other}, tps}`
4. Cache the result, e.g. in Redis or memory with a TTL roughly equal to the refresh interval. For 7d and 30d, pre-aggregate into an hourly rollup table instead of scanning raw logs.
5. Only expose groups the viewer can use: reuse the `usable_group` logic from `/api/pricing`. Hide raw request counts from the response unless the user is an admin.
6. Make it toggleable with an option such as `ModelStatusEnabled`. Note that `/api/pricing` is public; decide whether the status page also requires auth.

## Nav link
`hooks/use-top-nav-links.ts` builds the header links from `HeaderNavModules`. Add a `status` module (`{ enabled, requireAuth }`) that adds a link with `t('Model Status')` → `/status`. Also add it to the admin header-nav settings.

## State
- URL: `range`, `group`, `view`, `q`
- Local: `expandedKey` (`group|model`), tooltip state
- Server: `useQuery(['model-status', range], …)`, with `refetchInterval` set per range

## Design Tokens
Use the existing `web/default/src/styles/theme.css`; no new tokens. Status colors are Tailwind defaults:

| Role | Light | Dark |
|---|---|---|
| Success dot/bar | emerald-500 | emerald-500 |
| Success text | emerald-700 | emerald-400 |
| Warning dot/bar | amber-500 | amber-500 |
| Warning text | amber-700 | amber-400 |
| Danger dot/bar | rose-500 | rose-500 |
| Danger text | rose-700 | rose-400 |
| No requests (dot) | slate-400 | slate-400 |
| Empty bar | `oklch(0.93 0.008 255)` | `oklch(0.33 0.016 265)` |

- Radii: `rounded-xl` for cards, `rounded-lg` for panels/icon boxes, `rounded-md` for controls
- Font: Public Sans (already loaded); numbers use `font-mono tabular-nums`

## Assets
- Vendor icons: `getLobeIcon(vendor_icon)` from `@/lib/lobe-icon` (the prototype loads the `@lobehub/icons-static-svg` CDN)
- UI icons: lucide-react `Search`, `Rows3`, `Grid2X2`, `RotateCw`, `ChevronDown`, plus the existing header icons
- Logo: the existing `public/logo.png`

## Files
- `Model Status.dc.html`: the interactive prototype. Open it in a browser. The `<script data-dc-script>` block holds the mock data generator and the status logic (`status()`, `build()`).
- `logo.png`: referenced by the prototype header.
