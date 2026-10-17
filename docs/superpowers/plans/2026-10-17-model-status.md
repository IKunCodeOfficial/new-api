# Model Status Page Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Public `/status` page + `GET /api/model_status` showing per-group, per-model availability/latency from real traffic, with zero schema changes and negligible hot-path cost.

**Architecture:** A new package `pkg/model_status` (Go name `modelstatus`) hooks into the existing `perfmetrics.Record` path and keeps 10-second slots for the last ~66 minutes (in-process ring when Redis is off; one Redis hash per 10 s slot appended to the existing `recordRedis` pipeline when Redis is on). Short ranges (5m/1h) read those slots; long ranges (24h/7d/30d) read completed hours from the existing `perf_metrics` table plus the current partial hour from the slots. One unfiltered snapshot per range is cached in-process; each request filters it by the viewer's groups and attaches viewer-specific ratios. Frontend is a new feature `web/src/features/model-status` rebuilt from the prototype.

**Tech Stack:** Go (gin, gorm, go-redis v8, testify, miniredis), React 19 + TS, TanStack Router/Query, Tailwind v4, Base UI/shadcn, i18next, vitest.

**Spec:** `docs/superpowers/specs/2026-10-17-model-status-design.md` (read it fully). Visual reference: `docs/design_handoff_model_status/README.md` + `Model Status.dc.html`.

## Global Constraints

- No table created, altered or migrated. No reads of `logs`.
- No extra network round trip per relay request; new Redis commands go into the existing `TxPipeline` in `recordRedis`.
- Response never contains request counts (admins included); failure reasons only as shares.
- Viewer sees only groups from `getUsablePerfMetricGroups(c)`; admin sees all; ratios overridden with `ratio_setting.GetGroupGroupRatio(userGroup, g)`; `auto` and `""` groups never shown.
- Status thresholds (frontend): ≥ 0.99 success, ≥ 0.95 warning, < 0.95 danger, no requests → neutral. Rates are fractions 0–1 on the wire.
- Current-status bucket count: `max(3, ceil(N * 0.08))` most recent buckets.
- Ranges: `5m` 30×10s · `1h` 60×60s · `24h` 24×3600s · `7d` 42×14400s · `30d` 60×43200s. Detail (percentiles + failure shares) only for 5m/1h.
- Cache TTL = refetch interval: 5m 10s · 1h 30s · 24h 300s · 7d 1800s · 30d 3600s.
- Upstream files get only small hook lines: `pkg/perf_metrics/types.go`, `pkg/perf_metrics/metrics.go`, `router/api-router.go`, `web/src/lib/nav-modules.ts`, `web/src/hooks/use-top-nav-links.ts`, `web/src/features/system-settings/maintenance/{config.ts,header-navigation-section.tsx}`, locale JSONs, route tree.
- Go: `any`, `common.Marshal/Unmarshal`, gofmt, testify `require`/`assert`. At most ONE new Go test file (`pkg/model_status/model_status_test.go`); controller tests extend `controller/perf_metrics_test.go`.
- Frontend: `bun`; all copy via `t('English key')`; numbers via `@/lib/format`; any Intl locale via `toIntlLocale`; reuse `PublicLayout`, `status-badge` color maps, `getLobeIcon`, `ui/toggle-group`, `ui/input`.
- Every new source file carries the same AGPL header block as neighbouring files.

## Review Focus

1. Brand-new deployment / quiet model with zero traffic in the window → every bar grey, status "No requests", availability `null` rendered as `—`, no NaN/Infinity anywhere (Task 1, Task 3, Task 6 tests).
2. Model names or group names containing `:` or unusual characters → Redis field encoding must not mis-split (`\x00` separator) (Task 2 test).
3. Redis enabled but temporarily failing → `/api/model_status` still answers (empty short-range data, long range from DB), never 500s on Redis errors (Task 3 test).
4. Clock crossing a bucket boundary between snapshot build and request → bucket array length is always exactly N and `window_start + N*bucket_seconds` > now (Task 3 test).
5. Logged-in user whose group can't see `vip` requests `?group=vip` in URL → page shows nothing for vip and falls back to `all`; backend never returns it (Task 4 controller test, Task 7 behaviour).

---

### Task 1: Core types, histogram, failure classification

**Files:**
- Create: `pkg/model_status/types.go`, `pkg/model_status/histogram.go`, `pkg/model_status/failure.go`
- Test: `pkg/model_status/model_status_test.go` (new — the only new Go test file)

**Interfaces:**
- Produces:
  - `type FailureKind uint8` with consts `FailureNone, Failure5xx, Failure429, FailureTimeout, FailureOther`; `func (k FailureKind) Key() string` → `"5xx" | "429" | "timeout" | "other"`.
  - `type Sample struct { Model, Group string; Success bool; LatencyMs, TtftMs int64; HasTtft bool; OutputTokens, GenerationMs int64; Failure FailureKind }`
  - `const HistBins = 20`; `var histBoundsMs = [HistBins-1]int64{100,200,300,500,750,1000,1500,2000,3000,5000,7500,10000,15000,20000,30000,45000,60000,90000,120000}`
  - `func histIndex(ms int64) int` (first bound ≥ ms; else last bin)
  - `type Counters struct { Req, Ok, LatSum, TtftSum, TtftN, Out, GenMs int64; LatHist, TtftHist [HistBins]int64; Fail [4]int64 }` (Fail indexed by `kind-1`)
  - `func (c *Counters) AddSample(s Sample)`, `func (c *Counters) Merge(o Counters)`
  - `func Percentile(hist [HistBins]int64, p float64) int64` — upper bound of bin holding rank `ceil(p*total)`; last bin returns `120000`; empty → 0.
  - `func ClassifyFailure(status int, code, errorType string, timedOut bool) FailureKind`
  - `func FailureKindOf(info *relaycommon.RelayInfo, apiErr *types.NewAPIError) FailureKind` (relaykit `types`)

- [ ] **Step 1: Write failing tests** in `model_status_test.go`:
  - `TestHistIndexAndPercentile`: `histIndex(0)==0`, `histIndex(100)==0`, `histIndex(101)==1`, `histIndex(999999)==19`; hist with 90 samples at 250ms and 10 at 4000ms → `Percentile(h,0.5)==300`, `Percentile(h,0.95)==5000`, `Percentile(h,0.99)==5000`; empty hist → 0.
  - `TestClassifyFailure` table: `(429,"","",false)→Failure429`; `(0,"rate_limit_exceeded","",false)→Failure429`; `(0,"","insufficient_quota",false)→Failure429`; `(504,"","",false)→FailureTimeout`; `(524,…)→FailureTimeout`; `(0,"","",true)→FailureTimeout`; `(502,"","",false)→Failure5xx`; `(0,"","overloaded_error",false)→Failure5xx`; `(401,"invalid_api_key","",false)→FailureOther`; `(0,"","",false)→FailureOther`. Timeout beats 429 beats 5xx.
  - `TestCountersAddSample`: failed sample with `Failure: FailureNone` lands in `Fail[FailureOther-1]`; success sample never touches `Fail`; `HasTtft=false` leaves `TtftN==0` and `TtftHist` empty; `OutputTokens>0 && GenerationMs>0` required for `Out/GenMs` (mirror `atomicBucket.add` in `pkg/perf_metrics/types.go:111`).
- [ ] **Step 2:** `go test ./pkg/model_status/ -run 'TestHist|TestClassify|TestCounters' -v` → FAIL (undefined).
- [ ] **Step 3: Implement.** `ClassifyFailure` order: timeout (`timedOut`, status 408/504/524, code contains `timeout`) → 429 (status 429; code/type in `rate_limit_exceeded, rate_limit_error, insufficient_quota, quota_exceeded, resource_exhausted`) → 5xx (status ≥ 500; code/type in `overloaded_error, server_error, internal_error, service_unavailable`) → other. Lowercase code/type first. `FailureKindOf` extracts the same inputs `ClassifyRelayOutcome` uses (`pkg/perf_metrics/outcome.go:23-40`): if `info.StreamStatus.OutcomeSnapshot().Response == relaycommon.ResponseOutcomeFailed` use `ErrorStatus/ErrorCode/ErrorType`; else if `apiErr != nil` walk `errors.As(apiErr.Unwrap(), &inner)` to the root and use `StatusCode`, `GetErrorCode()`, `ToOpenAIError().Type`; `timedOut` = `StreamStatus != nil && (errors.Is(EndError, context.DeadlineExceeded) || snapshot.EndReason == StreamEndReasonTimeout)`.
- [ ] **Step 4:** rerun → PASS.
- [ ] **Step 5:** `gofmt -l pkg/model_status` empty; commit `feat(model-status): add sample counters, histogram and failure kinds`.

### Task 2: Slot collector (memory ring + Redis) and perfmetrics hook

**Files:**
- Create: `pkg/model_status/collector.go`, `pkg/model_status/module.go`
- Modify: `pkg/perf_metrics/types.go` (`Sample` gains `Failure modelstatus.FailureKind`), `pkg/perf_metrics/metrics.go` (`RecordRelayResult` sets `Failure`; `Record` calls `modelstatus.RecordLocal`; `recordRedis` calls `modelstatus.AppendRedis`)
- Test: `pkg/model_status/model_status_test.go`

**Interfaces:**
- Consumes: Task 1 `Sample`, `Counters`.
- Produces:
  - `const SlotSeconds int64 = 10`, `const slotCount = 396` (66 min)
  - `type PairKey struct{ Group, Model string }`
  - `func RecordLocal(s Sample, now time.Time)` — no-op when `common.RedisEnabled` or module disabled.
  - `func AppendRedis(ctx context.Context, pipe redis.Pipeliner, s Sample, now time.Time)` — no-op when module disabled.
  - `func ReadSlots(ctx context.Context, fromTs, toTs int64) (map[PairKey]map[int64]Counters, error)` — keyed by slot start ts, `[fromTs, toTs)`, reads Redis when enabled else memory.
  - `func ModuleEnabled() bool` — `status` header-nav module enabled flag.
  - `func startJanitor()` invoked lazily via `sync.Once` from `RecordLocal`.

- [ ] **Step 1: Write failing tests:**
  - `TestLocalSlotsRecordAndRead` (`common.RedisEnabled=false`): record 3 samples at `t0`, 1 at `t0+10s`, 1 at `t0-70min`; `ReadSlots(t0-60s, t0+20s)` returns two slots for the pair with `Req` 3 and 1; the 70-min-old sample is absent (overwritten/expired slot, not resurrected).
  - `TestRedisSlotsRoundTrip` with `miniredis`: set `common.RedisEnabled=true`, `common.RDB=redis.NewClient(&redis.Options{Addr: mr.Addr()})`; append samples for model `"a:b"` group `"g:1"` through a `TxPipeline` + `Exec`; `ReadSlots` returns exactly that pair with matching counters including one `LatHist` bin and `Fail[Failure429-1]==1`; key TTL > 0.
  - `TestModuleEnabledParsing` table on `common.OptionMap["HeaderNavModules"]`: `""`→true, `{"status":false}`→false, `{"status":{"enabled":false}}`→false, `{"status":{"enabled":true,"requireAuth":true}}`→true, `{"status":"false"}`→false, invalid JSON→true. (Call the uncached parse `parseStatusModuleEnabled(raw string) bool`.)
- [ ] **Step 2:** run → FAIL.
- [ ] **Step 3: Implement.**
  - Memory: `sync.Map[PairKey]*pairRing`; `pairRing{ mu sync.Mutex; lastWrite atomic.Int64; slots [slotCount]struct{ ts int64; c Counters } }`; index `(ts/SlotSeconds)%slotCount`; on write, if `slot.ts != slotTs` reset it. Janitor every 10 min deletes rings whose `lastWrite` is older than 70 min.
  - Redis key `model_status:10s:{slotTs}`; field `group + "\x00" + model + "\x00" + name`; names `req ok lat ttft ttft_n out gen lh{i} th{i} f{kind}`; only non-zero increments; one `Expire(key, 70*time.Minute)` per sample. Read: pipeline `HGetAll` for every slot key in range in one `Exec`; ignore `redis.Nil`; return error on other failures.
  - `ModuleEnabled`: cache result in an `atomic` value refreshed at most every 10 s from `common.OptionMap["HeaderNavModules"]` under `common.OptionMapRWMutex.RLock`; parse like `middleware.parseHeaderNavAccess` (bool / string / float64 / map with `enabled`), default true. Cannot import `middleware` (import cycle via perf_metrics) — say so in a one-line comment.
  - Hook lines in `pkg/perf_metrics/metrics.go`: in `RecordRelayResult` set `Failure: modelstatus.FailureKindOf(info, apiErr)` only when `outcome == OutcomeFailure`; in `Record` after the `Enabled` guard and `Group` default, build a `modelstatus.Sample` from `sample` and call `modelstatus.RecordLocal(ms, now)`; pass the same `ms` into `recordRedis` (add a parameter) and call `modelstatus.AppendRedis(ctx, pipe, ms, now)` before `pipe.Exec`.
- [ ] **Step 4:** `go test ./pkg/model_status/ ./pkg/perf_metrics/ -v` → PASS (existing perf_metrics tests still green).
- [ ] **Step 5:** commit `feat(model-status): collect 10s slots alongside perf metrics`.

### Task 3: Range snapshots, long-range DB read, cache

**Files:**
- Create: `pkg/model_status/snapshot.go`, `model/model_status.go`
- Test: `pkg/model_status/model_status_test.go`

**Interfaces:**
- Consumes: Task 2 `ReadSlots`, `PairKey`; Task 1 `Counters`.
- Produces:
  - `type RangeSpec struct{ Key string; Buckets int; BucketSeconds int64; TTL time.Duration; Detail bool }`; `func ParseRange(key string) (RangeSpec, bool)` with the five Global Constraints rows.
  - `type Series struct{ Buckets []Counters; Total Counters }`
  - `type Snapshot struct{ Range RangeSpec; WindowStart, UpdatedAt int64; Pairs map[PairKey]*Series }`
  - `func BuildSnapshot(ctx context.Context, spec RangeSpec, now time.Time) (*Snapshot, error)`
  - `func GetSnapshot(ctx context.Context, spec RangeSpec) (*Snapshot, error)` — cached, one build at a time per range (per-range `sync.Mutex` + double-checked expiry). Snapshot is treated as immutable by callers.
  - `model.GetPerfMetricRows(startTs, endTs int64) ([]model.PerfMetric, error)` — `bucket_ts >= start AND bucket_ts < end`, selecting only `model_name, group, bucket_ts, request_count, success_count, total_latency_ms, ttft_sum_ms, ttft_count, output_tokens, generation_ms` (use `commonGroupCol` for `group`).

- [ ] **Step 1: Write failing tests:**
  - `TestBuildSnapshotWindowShape` table over all five ranges with fixed `now`: `len(Buckets)==spec.Buckets`, `WindowStart%spec.BucketSeconds==0`, `WindowStart + Buckets*BucketSeconds > now.Unix()` and `≤ now + BucketSeconds`.
  - `TestBuildSnapshotShortRange`: local slots (Redis off) at `now-25s` and `now-5s` for 5m → land in buckets 27 and 29 (or as computed from alignment), `Total.Req` equals sum, detail histograms populated.
  - `TestBuildSnapshotLongRangeUsesDBForCompletedHours` (SQLite via `model.InitDB` like `pkg/perf_metrics/metrics_test.go:168`): seed `perf_metrics` rows at `curHour-1h` (req 10, ok 9) and `curHour` (req 99 — must be IGNORED: current hour comes from slots), plus one local slot at `now-30s` (req 2, ok 2); 24h snapshot last bucket `Req==2`, second-to-last `Req==10`; `Total.Req==12`; `LatHist` all zero for long ranges (no detail).
  - `TestBuildSnapshotRedisErrorDegrades`: Redis enabled with a closed miniredis → `BuildSnapshot` for 24h returns DB data and no error; for 5m returns empty pairs and no error (log via `common.SysError`).
  - `TestGetSnapshotCaches`: two calls within TTL return the same pointer; after manually expiring the cache entry a new pointer.
- [ ] **Step 2:** run → FAIL.
- [ ] **Step 3: Implement.** Bucket alignment: `end = now - now%BucketSeconds + BucketSeconds`; `WindowStart = end - Buckets*BucketSeconds`. Short ranges: `ReadSlots(WindowStart, end)` and place each slot at `(slotTs-WindowStart)/BucketSeconds`. Long ranges: `curHour = now - now%3600`; DB rows `[WindowStart, curHour)` mapped into buckets by `bucket_ts` (convert row columns into `Counters` without hist/fail; rows with `perf_metrics_setting` bucket < 1h still map correctly by timestamp), plus `ReadSlots(curHour, now+SlotSeconds)` with hist/fail cleared, added to the last bucket. Skip pairs whose group is `""` or `auto`.
- [ ] **Step 4:** run → PASS. Then run the long-range read against MySQL and PostgreSQL too (`SQL_DSN=... go test ./pkg/model_status/ -run LongRange -v`), recording engine versions for the final handoff.
- [ ] **Step 5:** commit `feat(model-status): build cached range snapshots`.

### Task 4: Response view, controller, route

**Files:**
- Create: `pkg/model_status/view.go`, `controller/model_status.go`
- Modify: `router/api-router.go` (one line next to `/rankings`, line ~45)
- Test: `pkg/model_status/model_status_test.go`, extend `controller/perf_metrics_test.go`

**Interfaces:**
- Consumes: Task 3 `Snapshot`, `ParseRange`, `GetSnapshot`.
- Produces:
  - `type Viewer struct{ Groups map[string]string /*key→desc*/; Ratios map[string]float64 }`
  - `func BuildResponse(snap *Snapshot, pricing []model.Pricing, vendors []model.PricingVendor, v Viewer) Response`
  - JSON shape exactly as spec §5.2: `Response{range, bucket_seconds, window_start, updated_at, has_detail, groups[]}`, `GroupView{key, desc, ratio, availability *float64, models[]}`, `ModelView{name, vendor_icon, availability *float64, current *float64, ttft_avg_ms, latency_avg_ms, tps, buckets []*BucketView, detail *DetailView}`, `BucketView{rate float64, ttft_ms int64}`, `DetailView{ttft_p50, ttft_p95, ttft_p99, latency_p50, latency_p95, latency_p99, fail_share map[string]float64}`.
  - `controller.GetModelStatus(c *gin.Context)`; `controller.modelStatusViewer(c *gin.Context) modelstatus.Viewer`.
  - Route: `apiRouter.GET("/model_status", middleware.HeaderNavModuleAuth("status"), controller.GetModelStatus)`.

- [ ] **Step 1: Write failing tests:**
  - `TestBuildResponseVisibilityAndNoCounts`: pricing items `m1{EnableGroup:[default,vip]}`, `m2{EnableGroup:[all]}`, `m3{EnableGroup:[vip]}`; snapshot has data for `(vip,m3)` and `(default,m1)` and a stale `(default,gone)` not in pricing; viewer `{Groups:{default:"Public"}, Ratios:{default:0.8}}` → exactly one group `default` with ratio 0.8, models `m1, m2` only (m2 shows `availability:null`, all buckets null); marshalled JSON contains none of the substrings `"req"`, `"total"`, `"count"`, `"success"` as keys.
  - `TestBuildResponseRatesAndOrdering`: 1h snapshot, `m1` 100 req / 99 ok, `m2` 300 req / 300 ok in same group → `models[0].name=="m2"` (volume desc); `m1.availability==0.99`; group availability `== 399/400`; `current` uses last `max(3, ceil(60*0.08))==5` buckets; `has_detail==true` and `detail.fail_share` sums to 1 when failures exist, `nil` map when none; 24h snapshot → `detail==nil`, `has_detail==false`.
  - `TestBuildResponseVendorIcon`: `Pricing.Icon` wins; else vendor icon by `VendorID`; else `""`.
  - In `controller/perf_metrics_test.go`, `TestModelStatusViewerScopesGroupsAndRatios`: usable groups `{"default":"Public","vip":"VIP"}`, special usable groups so user group `vip` sees `vip`; ratios `{"default":1,"vip":2,"internal":3}`, group-group ratio `{"vip":{"default":0.5}}`; guest → groups `{default}`, ratio default 1; vip user → includes `vip`, default ratio 0.5; admin → `default, vip, internal`. Plus `TestGetModelStatusRejectsBadRange` → 400 for `range=2h`.
- [ ] **Step 2:** run → FAIL.
- [ ] **Step 3: Implement.** Groups listed = viewer groups minus `""`/`auto` that have ≥1 model, ordered `default` first then alphabetical. Models per group from pricing `EnableGroup` (`all` → every viewer group). `Tps = Out*1000/GenMs` (0 when `GenMs==0`), averages via integer division with zero guards. Never mutate `snap`. `modelStatusViewer`: groups from `getUsablePerfMetricGroups(c)`, desc from `setting.GetUsableGroupDescription`, ratios from `ratio_setting.GetGroupRatioCopy()` overridden by `ratio_setting.GetGroupGroupRatio(userGroup, g)` (user group via `model.GetUserGroup(c.GetInt("id"), false)` when logged in). `GetModelStatus`: `ParseRange(c.DefaultQuery("range","1h"))` else 400 `{success:false}`; `GetSnapshot` error → 500; else `{success:true, data}` using `model.GetPricing()` and `model.GetVendors()`.
- [ ] **Step 4:** `go test ./pkg/model_status/ ./controller/ -run 'ModelStatus|BuildResponse|PerfMetrics' -v` → PASS; `go build ./...` OK.
- [ ] **Step 5:** commit `feat(model-status): add /api/model_status endpoint`.

### Task 5: Header-nav `status` module (frontend plumbing + route guard)

**Files:**
- Modify: `web/src/lib/nav-modules.ts`, `web/src/hooks/use-top-nav-links.ts`, `web/src/features/system-settings/maintenance/config.ts`, `web/src/features/system-settings/maintenance/header-navigation-section.tsx`
- Create: `web/src/routes/status/index.tsx` (renders a placeholder `ModelStatus` from Task 7's export path; create `web/src/features/model-status/index.tsx` exporting a stub component in this task)
- Test: extend the existing nav-modules tests if present (`rg -l parseHeaderNavModules web/src --glob '*test*'`); otherwise create `web/src/features/model-status/__tests__/status.test.ts` here (Tasks 6–7 extend it) — the feature gets only this one frontend test file.

**Interfaces:**
- Produces: `HeaderNavModule = 'rankings' | 'pricing' | 'status'`; `HeaderNavModules.status: ModuleAccess` default `{ enabled: true, requireAuth: false }`; nav link `{ title: t('Model Status'), href: '/status', requiresAuth }` placed right after Rankings; admin toggle with `t('Model Status')`, description `t('Public model status page based on live request data.')`, require-auth title `t('Require login to view model status')`, description `t('Visitors must authenticate before accessing the model status page.')`.
- Route `/status/` with `validateSearch` zod schema: `range: z.enum(['5m','1h','24h','7d','30d']).optional().catch(undefined)`, `group: z.string().optional()`, `view: z.enum(['list','matrix']).optional().catch(undefined)`, `q: z.string().optional()`; `beforeLoad` identical to `web/src/routes/rankings/index.tsx` with module `'status'`.

- [ ] **Step 1:** Write failing test: `parseHeaderNavModulesFromStatus({HeaderNavModules: '{"status":{"enabled":false,"requireAuth":false}}'})!.status.enabled === false`, and missing key → `{enabled:true, requireAuth:false}`.
- [ ] **Step 2:** `cd web && bun run test -- model-status` → FAIL.
- [ ] **Step 3:** Implement, mirroring every `rankings` branch.
- [ ] **Step 4:** test PASS; `bun run typecheck` (or `bunx tsc -b`) clean; route tree regenerated by the dev/build plugin.
- [ ] **Step 5:** commit `feat(model-status): add status header nav module and route`.

### Task 6: Data layer and status logic

**Files:**
- Create: `web/src/features/model-status/types.ts`, `api.ts`, `lib/status.ts`, `constants.ts`
- Test: extend `web/src/features/model-status/__tests__/status.test.ts`

**Interfaces:**
- Produces:
  - `types.ts`: TS mirrors of Task 4 JSON (`ModelStatusResponse`, `ModelStatusGroup`, `ModelStatusModel`, `ModelStatusBucket | null`, `ModelStatusDetail`), `StatusRange = '5m'|'1h'|'24h'|'7d'|'30d'`.
  - `api.ts`: `getModelStatus(range: StatusRange): Promise<ModelStatusResponse>` via `api.get('/api/model_status', { params: { range } })` (pattern of `features/rankings/api.ts`); `useModelStatus(range)` = `useQuery({ queryKey: ['model-status', range], refetchInterval: REFRESH_MS[range] })`.
  - `constants.ts`: `REFRESH_MS = { '5m': 10_000, '1h': 30_000, '24h': 300_000, '7d': 1_800_000, '30d': 3_600_000 }`, `RANGES` list with labels `t('5 min')`, `t('1 hour')`, `t('24 hours')`, `t('7 days')`, `t('30 days')`.
  - `lib/status.ts`: `type StatusLevel = 'success'|'warning'|'danger'|'neutral'`; `statusOf(rate: number | null | undefined): StatusLevel`; `severityRank(level): number` (danger 0, warning 1, success 2, neutral 3); `sortModels(models): ModelStatusModel[]` (stable by severity of `current`, keeps backend volume order); `problemChips(groups, max = 5)` → failing first then degraded, `{ model, group, level }`; `bucketTimeFormat(range)` → `'HH:mm:ss'` for 5m, `'HH:mm'` for 1h/24h, `'MM-DD HH:mm'` for 7d/30d; `bannerSummary(groups)` → `{ level, failing, degraded, healthy, total }`.
- [ ] **Step 1:** Write failing tests: `statusOf(0.99)==='success'`, `statusOf(0.9899)==='warning'`, `statusOf(0.95)==='warning'`, `statusOf(0.9499)==='danger'`, `statusOf(null)==='neutral'`; `sortModels` on `[ok-A(vol order 0), bad-B(1), none-C(2), slow-D(3)]` → `B, D, A, C`; `problemChips` caps at 5 and lists danger before warning; `bannerSummary` with 1 danger + 2 warning out of 10 → `{level:'danger', failing:1, degraded:2, healthy:7, total:10}` (neutral counts toward `total` but not `healthy`).
- [ ] **Step 2:** `bun run test -- model-status` → FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** PASS.
- [ ] **Step 5:** commit `feat(model-status): add status data layer`.

### Task 7: Page UI

**Files:**
- Create: `web/src/components/segmented-control.tsx`; `web/src/features/model-status/index.tsx` (replace stub) and `components/{status-banner,status-toolbar,group-card,model-row,model-row-detail,status-matrix,bucket-tooltip,status-legend}.tsx`
- Modify: `web/src/i18n/locales/*.json` via `bun run i18n:sync` (then fill zh / zh-TW from the prototype's Chinese copy; other locales get English source or a reasonable translation)
- Test: extend `web/src/features/model-status/__tests__/status.test.ts` with one render test

**Interfaces:**
- Consumes: Task 6 hooks/helpers, Task 5 route search params.
- Produces: `SegmentedControl<T extends string>({ value, onValueChange, options: { value: T; label: ReactNode }[], ariaLabel })` built on `@/components/ui/toggle-group` (single, non-deselectable) with spec styling: container `bg-muted/60 h-8 rounded-md border p-0.5`, item `rounded-[5px] px-3 text-xs font-medium`, active `bg-foreground text-background shadow-sm`, inactive `text-muted-foreground hover:text-foreground`.

- [ ] **Step 1:** Write failing render test: with `useModelStatus` mocked to a 1h payload holding one danger model, `<ModelStatus />` shows the banner text for "1 model failing", a model row for it first, and NO element containing a request count; clicking the row reveals `P95`. Clicking a 24h payload row reveals throughput but not `P95`.
- [ ] **Step 2:** run → FAIL.
- [ ] **Step 3:** Build per prototype/README sections 1–6 with these deltas: README paths `web/default/...` mean `web/src/...`; group SegmentedControl options = `t('All groups')` + response group keys (if URL `group` is not among them, treat as `all`); search filters by model name case-insensitively; changing range clears `expandedKey`; one shared fixed-position tooltip (not per-bar `Tooltip`); `prefers-reduced-motion` disables ping and spin (`motion-safe:` variants); expanded row for `has_detail=false` shows only the throughput panel; availability `null` renders `—`; durations via a small formatter in `lib/status.ts` (`ms < 1000 → "820 ms"`, else `"1.4 s"`) using `@/lib/format` for numbers; banner time via `toIntlLocale(i18n.language)`.
- [ ] **Step 4:** `bun run test` PASS, `bun run lint`, `bun run build` clean. Then local run (memory `local-run-recipe`): send a few streaming and failing requests, open `/status` in light + dark at 5m / 1h / 24h, list + matrix, narrow viewport scrolls horizontally inside cards only.
- [ ] **Step 5:** commit `feat(model-status): add /status page`.

### Task 8: Whole-feature verification

- [ ] **Step 1:** `go vet ./pkg/model_status/ ./controller/ ./pkg/perf_metrics/` and `go test ./pkg/model_status/ ./pkg/perf_metrics/ ./controller/ ./middleware/` → PASS.
- [ ] **Step 2:** three-DB run of Task 3's long-range test (SQLite, MySQL ≥5.7.8, PostgreSQL ≥9.6); record versions + commands.
- [ ] **Step 3:** Manual: disable `status` in admin header nav → `/api/model_status` 403, nav link gone, `/status` redirects home; re-enable; set require-auth → guest redirected to sign-in.
- [ ] **Step 4:** Hot-path check: `go test ./pkg/perf_metrics/ -bench . -run ^$` if a benchmark exists; otherwise confirm by reading `recordRedis` that only one `Exec` occurs per sample.
- [ ] **Step 5:** Final summary listing upstream files touched, DB versions, and the SegmentedControl reuse justification (toggle-group composed, no existing segmented component).
