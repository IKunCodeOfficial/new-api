# System Banner Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A site-wide, dismissible top banner (zh + en, two colour levels), configured by the root admin and shown on every page including to logged-out visitors.

**Architecture:** Four new option keys under a `system_banner` config struct plus a backend-maintained `published_at`; `model.UpdateOption` routes banner keys through one function that validates, bumps `published_at`, and writes in a single transaction. `/api/status` carries the banner when enabled. The frontend mounts one `SystemBanner` in `__root.tsx`, stores the dismissed `published_at` in localStorage, and publishes its height as `--system-banner-height`, which every layout offset adds.

**Tech Stack:** Go 1.26, Gin, GORM, testify; React 19, TanStack Query/Router, Tailwind 4, React Hook Form + Zod, Vitest + Testing Library, Bun.

**Spec:** `docs/superpowers/specs/2026-11-02-system-banner-design.md`

## Global Constraints

- Read repo-root `AGENTS.md` and `web/AGENTS.md` before starting; every rule there applies.
- No database schema change. Banner values live as rows in the existing `options` table.
- Option keys, exactly: `system_banner.enabled`, `system_banner.level`, `system_banner.text_zh`, `system_banner.text_en`, `system_banner.published_at`.
- `level` ∈ {`normal`, `important`}; default `normal`. `enabled` default `false`. `published_at` default `0`, Unix seconds.
- Text: trimmed before storing, max 500 runes per language.
- Language rule: `(i18n.resolvedLanguage || i18n.language).startsWith('zh')` → `text_zh`, else `text_en`. Interface codes are `zhCN` / `zhTW`.
- Plain text only. Never `dangerouslySetInnerHTML`, `RichContent` or `HtmlContent` for banner text.
- Backend JSON through `common.*` wrappers; console API messages are English source strings via `common.NewMessage` and get added to all seven `web/src/i18n/locales/*.json`.
- Go tests use `require` / `assert`; at most one new Go test file for this feature.
- Frontend tests live in the module's `__tests__/` directory; run `bun run test <file>`, `bun check -b`, and `bun run lint` on touched files before each commit.
- Do not touch the existing `Notice`, announcements, bell popover, or dashboard panel behaviour.

## Review Focus

1. Admin enables the banner twice within the same second (save, disable, re-enable fast) → each publish must get a strictly larger `published_at`, otherwise a user's dismissal hides the new one. Test: Task 1 `same second publish still increments`.
2. Admin disables the banner while clearing its text in the same form save → the save must succeed (disable is written first). Test: Task 4 `disabling saves enabled before the texts`.
3. Visitor with a stale localStorage `status` snapshot that has no `system_banner` field, or a malformed one → no banner, no crash. Test: Task 2 `renders nothing when status has no banner`.
4. Dismissed banner, then the admin edits only the colour level → still hidden. Test: Task 1 `changing level does not bump`, and Task 2 `same published_at stays hidden after rerender`.
5. Banner dismissed or absent → `--system-banner-height` is `0px` so no layout keeps a phantom gap. Test: Task 2 `dismiss sets banner height to 0px`.

---

### Task 1: Backend settings, validation, publish timestamp, status field

**Files:**
- Create: `setting/system_setting/system_banner.go`
- Create: `model/option_system_banner.go`
- Modify: `model/option.go` (`UpdateOption` dispatch near line 283; `validateOptionValue` near line 236)
- Modify: `controller/misc.go` (`GetStatus`, after the `cs.FAQEnabled` block near line 141)
- Test: `model/option_system_banner_test.go`

**Interfaces:**
- Produces (Go): `system_setting.SystemBannerSettings{Enabled bool; Level string; TextZh string; TextEn string; PublishedAt int64}` with JSON tags `enabled`, `level`, `text_zh`, `text_en`, `published_at`, registered as `config.GlobalConfig.Register("system_banner", ...)`; `system_setting.GetSystemBannerSettings() *SystemBannerSettings`; constants `SystemBannerLevelNormal = "normal"`, `SystemBannerLevelImportant = "important"`, `SystemBannerTextMaxRunes = 500`.
- Produces (Go): `model.IsSystemBannerOption(key string) bool` (true for the four editable keys).
- Produces (HTTP): `/api/status` field `system_banner: {"level": string, "text_zh": string, "text_en": string, "published_at": number}`, present only when enabled.
- Produces (errors, exact English source text used as i18n keys in Task 4):
  - `"System banner level must be normal or important"`
  - `"System banner text must be at most 500 characters"`
  - `"System banner needs both Chinese and English text while enabled"`
  - `"System banner publish time is maintained automatically"`
  - `"System banner enabled must be true or false"`
  - `"System banner settings must be saved one at a time"`

- [ ] **Step 1: Write the failing tests** in `model/option_system_banner_test.go`, reusing `useFrontendOptionMigrationDB` and `requireOptionValue` from `model/frontend_option_migration_test.go`. Each test resets `common.OptionMap` and the registered `SystemBannerSettings` to defaults in setup and restores them in `t.Cleanup`. Freeze time through a package-level `var systemBannerNow = time.Now` in the model file, overridden in tests.

  - `TestSystemBannerRejectsInvalidValues` (table): `level=urgent`; `text_zh` of 501 runes (`strings.Repeat("维", 501)`); `enabled=yes`; `published_at=123` via `UpdateOption`; `published_at=123` via `UpdateOptionsBulk`; `text_zh` via `UpdateOptionsBulk`; `enabled=true` while `text_en` is empty; with banner enabled and both texts set, `text_zh="   "`. Each asserts `Error` and that no DB row changed.
  - `TestSystemBannerPublishedAtRules` (table of sequences, each step `UpdateOption(key, value)`, then assert `published_at` row value and `GetSystemBannerSettings().PublishedAt`):
    - `enable bumps`: set zh, en (now=1000) → `published_at` stays `0`; enable (now=1000) → `1000`.
    - `re-enable with same text bumps`: from above, disable (now=1100) → `1000`; enable (now=1200) → `1200`.
    - `text change while enabled bumps`: enabled at 1000; `text_en` new value at 1300 → `1300`.
    - `same text while enabled does not bump`: enabled at 1000; save identical `text_zh` at 1300 → `1000`.
    - `changing level does not bump`: enabled at 1000; `level=important` at 1300 → `1000`.
    - `text change while disabled does not bump`: disabled; `text_zh` changed at 1300 → previous value.
    - `same second publish still increments`: enable at 1000 → `1000`; disable at 1000; enable at 1000 → `1001`.
  - `TestSystemBannerTrimsText`: `text_zh="  维护  "` stores `"维护"`.

- [ ] **Step 2: Run tests to verify they fail**

  Run: `go test ./model -run SystemBanner -count=1`
  Expected: FAIL (undefined `GetSystemBannerSettings` / `systemBannerNow`).

- [ ] **Step 3: Add `setting/system_setting/system_banner.go`** following `discord.go`: struct, defaults (`Level: "normal"`), `init()` registration, getter, the three constants.

- [ ] **Step 4: Add `model/option_system_banner.go`** with `IsSystemBannerOption` and `updateSystemBannerOption(key, value string) error`, guarded by a package `sync.Mutex`. Algorithm:

  ```
  lock
  cur := snapshot of enabled/level/text_zh/text_en/published_at from GetSystemBannerSettings()
  value = TrimSpace(value) for text keys
  validate key/value (errors above); next := cur with key applied
  if next.Enabled && (next.TextZh == "" || next.TextEn == "") → error
  bump := (!cur.Enabled && next.Enabled) || (next.Enabled && isTextKey && value != cur text)
  writes := {key: value}
  if bump: writes["system_banner.published_at"] = max(systemBannerNow().Unix(), cur.PublishedAt+1)
  DB.Transaction: FirstOrCreate + Save each write (same shape as UpdateOptionsBulk)
  then updateOptionMap(k, v) for each write
  ```

  In `UpdateOption`, dispatch `IsSystemBannerOption(key)` to it before `validateOptionValue`. In `validateOptionValue`, reject `system_banner.published_at` with the read-only error. In `UpdateOptionsBulk`, return `"System banner settings must be saved one at a time"` when `values` contains any `IsSystemBannerOption` key: the bump rules live only in the single-key path, and no caller bulk-saves banner keys today.

- [ ] **Step 5: Run tests to verify they pass**

  Run: `go test ./model -run SystemBanner -count=1`
  Expected: PASS

- [ ] **Step 6: Add the `/api/status` field** in `GetStatus`: when `GetSystemBannerSettings().Enabled`, set `data["system_banner"] = gin.H{"level": ..., "text_zh": ..., "text_en": ..., "published_at": ...}`.

- [ ] **Step 7: Build, vet, format, full model/controller tests**

  Run: `gofmt -l setting model controller && go build ./... && go test ./model ./controller ./setting/... -count=1`
  Expected: no gofmt output, build OK, tests PASS.

- [ ] **Step 8: Commit**

  ```bash
  git add setting/system_setting/system_banner.go model/option_system_banner.go model/option_system_banner_test.go model/option.go controller/misc.go
  git commit -m "feat(banner): store the system banner and publish it in /api/status"
  ```

---

### Task 2: Banner component, dismissal, marquee, root mount

**Files:**
- Create: `web/src/components/system-banner/system-banner.tsx`
- Create: `web/src/components/system-banner/lib.ts`
- Test: `web/src/components/system-banner/__tests__/language.test.ts`
- Test: `web/src/components/system-banner/__tests__/dismissal.test.tsx`
- Modify: `web/src/features/auth/types.ts` (`SystemStatus.data`)
- Modify: `web/src/routes/__root.tsx` (render `<SystemBanner />` before `<Outlet />`)
- Modify: `web/src/styles/theme.css` (`:root` gets `--system-banner-height: 0px;` next to `--app-header-height`)
- Modify: `web/src/styles/index.css` (`marquee` keyframes + `.animate-marquee`; add `.animate-marquee` to the `prefers-reduced-motion` block)
- Modify: all seven `web/src/i18n/locales/*.json` (new UI strings)

**Interfaces:**
- Consumes: `/api/status` `system_banner` from Task 1, via `useStatus()` from `@/hooks/use-status` (`status?.data?.system_banner`).
- Produces (TS, in `lib.ts`):
  - `export type SystemBannerLevel = 'normal' | 'important'`
  - `export type SystemBannerStatus = { level: SystemBannerLevel; text_zh: string; text_en: string; published_at: number }`
  - `export function pickSystemBannerText(banner: SystemBannerStatus, language: string | undefined): string`
  - `export const SYSTEM_BANNER_DISMISSED_KEY = 'system-banner-dismissed'`
- Produces: `SystemStatus.data.system_banner?: SystemBannerStatus`; `export function SystemBanner()`; CSS variable `--system-banner-height` on `document.documentElement` (measured px when shown, `0px` otherwise). Task 3 consumes the variable.
- UI strings (i18n keys): `'System notice'` (region `aria-label`), `'Close notice'` (button `aria-label`).

- [ ] **Step 1: Write the failing tests**

  `language.test.ts`: `pickSystemBannerText` returns `text_zh` for `zhCN`, `zhTW`; `text_en` for `en`, `fr`, `ru`, `ja`, `vi`; `text_en` for `undefined` and for `'xx'`.

  `dismissal.test.tsx` (render `SystemBanner` inside a `QueryClientProvider` whose `['status']` cache is seeded with `setQueryData`; stub `ResizeObserver` and give elements fixed `getBoundingClientRect` / `scrollWidth` / `clientWidth`; clear localStorage and the CSS variable in `afterEach`):
  - `renders nothing when status has no banner` — seeded status without `system_banner`; `queryByRole('region')` is null; `--system-banner-height` is `0px`.
  - `shows banner with accessible region and close button` — region named `System notice`, button named `Close notice`, text visible.
  - `important level is visually distinct from normal` — assert via a `data-level` attribute on the region equals the level.
  - `dismiss hides banner and stores published_at` — click close → region gone, `localStorage[SYSTEM_BANNER_DISMISSED_KEY] === '1000'`.
  - `dismiss sets banner height to 0px`.
  - `same published_at stays hidden after rerender` — dismissed at 1000, re-seed with level changed and same `published_at` → still hidden.
  - `new published_at shows again` — re-seed with `published_at: 1001` → region visible.
  - `short text does not scroll, overflowing text scrolls` — `scrollWidth <= clientWidth` → text element lacks `animate-marquee`; `scrollWidth > clientWidth` → has it. (Class name is the CSS contract with `index.css`, not a styling snapshot.)

- [ ] **Step 2: Run tests to verify they fail**

  Run: `cd web && bun run test src/components/system-banner`
  Expected: FAIL (module not found).

- [ ] **Step 3: Implement `lib.ts`, the type addition, and `system-banner.tsx`.**
  - Before writing markup, run the `web/AGENTS.md` §3.3 search (`components/ui/alert.tsx`, `components/ui/button.tsx`, icons used by `notification-popover.tsx`). Use the project `Button` (icon size, ghost) for close; record in the commit/PR body why `Alert` is or is not used.
  - Root element: `fixed inset-x-0 top-0` with a z-index above `PublicHeader`'s `z-50`; `role='region'`, `aria-label={t('System notice')}`, `data-level`.
  - Colours: normal = amber-50/amber-950 text with amber-300 bottom border (`dark:` variants readable); important = red-600 bg, white text. Contrast ≥ 4.5:1.
  - Dismissed when `Number(localStorage[SYSTEM_BANNER_DISMISSED_KEY]) === banner.published_at`; read in a `useState` initialiser and written on close.
  - Height: a `ResizeObserver` on the root writes `${height}px` to `--system-banner-height`; an effect cleanup and the not-rendered path write `0px`.
  - Overflow: a `ResizeObserver` on the track compares text `scrollWidth` to track `clientWidth`; overflowing text gets `animate-marquee` and `--marquee-duration` = `(textWidth + trackWidth) / 60` seconds (60 px/s); non-overflowing text is centred.

- [ ] **Step 4: Add CSS.** In `index.css`, next to `scroll-up`:

  ```css
  @keyframes marquee { from { transform: translateX(0); } to { transform: translateX(-100%); } }
  .animate-marquee { display: inline-block; padding-left: 100%; animation: marquee var(--marquee-duration, 20s) linear infinite; will-change: transform; }
  .marquee-track:hover .animate-marquee { animation-play-state: paused; }
  ```

  In the reduced-motion block add `.animate-marquee { animation: none !important; padding-left: 0; white-space: normal; }` so long text wraps (the banner height observer then grows the offset).

- [ ] **Step 5: Mount in `__root.tsx`** before `<Outlet />`; add `'System notice'` and `'Close notice'` to all seven locale files (zh: `系统通知` / `关闭通知`; zh-TW: `系統通知` / `關閉通知`; others translated).

- [ ] **Step 6: Run tests, typecheck, lint**

  Run: `cd web && bun run test src/components/system-banner && bun check -b && bun run lint`
  Expected: PASS, no type errors, no lint errors in touched files.

- [ ] **Step 7: Commit**

  ```bash
  git add web/src/components/system-banner web/src/features/auth/types.ts web/src/routes/__root.tsx web/src/styles/theme.css web/src/styles/index.css web/src/i18n/locales
  git commit -m "feat(web): show the dismissible system banner on every page"
  ```

---

### Task 3: Layout offsets for the banner

**Files (modify; line numbers are from the spec survey — re-check each and `rg "100svh|100vh|100dvh|h-screen|min-h-screen|top-0|top-20|pt-20" web/src` for any missed fixed/sticky offset):**
- `web/src/components/layout/components/header.tsx:28`
- `web/src/components/layout/components/authenticated-layout.tsx:51-53` (and the outer wrapper)
- `web/src/components/ui/sidebar.tsx:161, 253`
- `web/src/components/layout/components/public-header.tsx:202, 362`
- `web/src/components/layout/components/public-layout.tsx` (`<main>` `pt-20`)
- `web/src/features/home/components/sections/hero.tsx:83`
- `web/src/features/home/index.tsx:64, 81`
- `web/src/features/pricing/index.tsx:154, 179, 224`
- `web/src/features/rankings/index.tsx:76`
- `web/src/features/model-status/index.tsx:130`
- `web/src/features/about/index.tsx:157`
- `web/src/features/auth/auth-layout.tsx`
- `web/src/features/setup/setup-wizard.tsx:286`

**Interfaces:**
- Consumes: `--system-banner-height` (Task 2), default `0px` from `theme.css`.

- [ ] **Step 1: Apply the offsets.** Rule for every site:
  - fixed/sticky `top-X` → `top-[calc(X+var(--system-banner-height))]`;
  - padding-top that clears a fixed header → add the variable the same way;
  - viewport heights (`100svh`, `100vh`, `100dvh`, `h-screen`, `min-h-svh`) → subtract the variable.
  - Do not add the offset where an ancestor already applies it (e.g. if `public-layout` `<main>` gets the offset, inner page padding that is relative to `<main>` stays as is). Note each decision in the commit body.

- [ ] **Step 2: Verify `0px` changes nothing** — with no banner, the computed classes resolve to the old values.

  Run: `cd web && bun check -b && bun run lint && bun run test`
  Expected: all pass (existing layout tests unchanged).

- [ ] **Step 3: Visual check in the running app** (use the `run` skill; backend + `bun run dev`), banner enabled with short and 400-char texts, at 1440px and 390px width: home, pricing (sticky filter column), rankings, model status, about, sign-in, console dashboard with sidebar expanded and collapsed, console mobile sidebar drawer, public mobile drawer. Expected for each: nothing hidden under the banner, no second scrollbar in the console, sidebar starts below header. Then dismiss: everything returns to the pre-feature layout. Repeat once in dark mode and once with reduced motion (long text wraps, layout follows the taller banner).

- [ ] **Step 4: Commit**

  ```bash
  git add web/src
  git commit -m "fix(web): make room for the system banner in every layout"
  ```

---

### Task 4: Admin settings section

**Files:**
- Create: `web/src/features/system-settings/maintenance/system-banner-section.tsx`
- Test: `web/src/features/system-settings/maintenance/__tests__/system-banner-section.test.tsx`
- Modify: `web/src/features/system-settings/site/section-registry.tsx` (new entry `id: 'system-banner'`, `titleKey: 'System banner'`, right after `notice`)
- Modify: `web/src/features/system-settings/types.ts` (`SiteSettings` gets `'system_banner.enabled': boolean`, `'system_banner.level': string`, `'system_banner.text_zh': string`, `'system_banner.text_en': string`)
- Modify: `web/src/features/system-settings/site/index.tsx` (defaults `false`, `'normal'`, `''`, `''`)
- Modify: `web/src/features/system-settings/hooks/use-update-option.ts` (`STATUS_RELATED_KEYS` gets the four keys)
- Modify: all seven `web/src/i18n/locales/*.json`

**Interfaces:**
- Consumes: `useUpdateOption().mutateAsync({ key, value })`; backend error messages from Task 1.
- Produces: `export function SystemBannerSection(props: { defaultValues: { enabled: boolean; level: SystemBannerLevel; text_zh: string; text_en: string } })`. `SystemBannerLevel` from `@/components/system-banner/lib`.
- UI strings: `'System banner'`, `'Show banner'`, `'Level'`, `'Normal'`, `'Important'`, `'Chinese text'`, `'English text'`, `'Required while the banner is shown'`, plus the six backend messages from Task 1.

- [ ] **Step 1: Write the failing tests** (mock `useUpdateOption` at the module boundary and record call order):
  - `enabling saves texts and level before enabled` — defaults disabled; fill both texts, toggle on, save → calls in order `text_zh`, `text_en` (only changed fields), then `enabled=true` last.
  - `disabling saves enabled before the texts` — defaults enabled with texts; toggle off and clear `text_zh`, save → first call `enabled=false`, then `text_zh=''`.
  - `enabled with empty English text shows a field error and saves nothing`.
  - `text over 500 characters shows a field error` and the counter shows `501/500`.
  - `unchanged form saves nothing`.
  - Fields reachable by label (`Show banner`, `Level`, `Chinese text`, `English text`).

- [ ] **Step 2: Run tests to verify they fail**

  Run: `cd web && bun run test src/features/system-settings/maintenance/__tests__/system-banner-section.test.tsx`
  Expected: FAIL (module not found).

- [ ] **Step 3: Implement the section**, modelled on `notice-section.tsx` (`SettingsSection`, `SettingsForm`, `SettingsPageFormActions`, project `Switch`, `Select`/`RadioGroup` for level, `Textarea`). Zod schema: texts trimmed, `max(500)`, and a `superRefine` requiring both texts when `enabled`. On submit, compute changed keys; if `enabled` turns on, save it last; if it turns off, save it first; stop on the first failed mutation. Never send `system_banner.published_at`.

- [ ] **Step 4: Register the section, types, defaults, `STATUS_RELATED_KEYS`, locale strings.** Run `bun run i18n:sync` and fill zh, zh-TW, fr, ru, ja, vi.

- [ ] **Step 5: Run tests, typecheck, lint**

  Run: `cd web && bun run test src/features/system-settings && bun check -b && bun run lint`
  Expected: PASS, no errors.

- [ ] **Step 6: End-to-end check in the running app** as root: enable with both texts → banner appears for a logged-out private window after reload; dismiss; disable and re-enable with identical text → appears again; edit only level → stays dismissed; edit English text while enabled → appears again; disable while clearing text → save succeeds and banner disappears; try enabling with empty Chinese text → field error, no request. Check `curl -s localhost:3000/api/status | jq .data.system_banner` matches each state.

- [ ] **Step 7: Commit**

  ```bash
  git add web/src/features/system-settings web/src/i18n/locales
  git commit -m "feat(web): let admins publish the system banner"
  ```
