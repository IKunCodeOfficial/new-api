package modelstatus

import (
	"cmp"
	"slices"
	"strings"

	"github.com/QuantumNous/new-api/model"
)

// Viewer is what one caller may see: usable group key → description, and the
// group ratios as they apply to the caller's own user group.
type Viewer struct {
	Groups map[string]string
	Ratios map[string]float64
}

// Response is the wire shape of GET /api/model_status. It never carries
// request counts; everything weighted by volume is computed here.
type Response struct {
	Range         string      `json:"range"`
	BucketSeconds int64       `json:"bucket_seconds"`
	WindowStart   int64       `json:"window_start"`
	UpdatedAt     int64       `json:"updated_at"`
	HasDetail     bool        `json:"has_detail"`
	Groups        []GroupView `json:"groups"`
}

type GroupView struct {
	Key          string      `json:"key"`
	Desc         string      `json:"desc"`
	Ratio        float64     `json:"ratio"`
	Availability *float64    `json:"availability"`
	Models       []ModelView `json:"models"`
}

type ModelView struct {
	Name         string        `json:"name"`
	VendorIcon   string        `json:"vendor_icon"`
	Availability *float64      `json:"availability"`
	Current      *float64      `json:"current"`
	TtftAvgMs    int64         `json:"ttft_avg_ms"`
	LatencyAvgMs int64         `json:"latency_avg_ms"`
	Tps          float64       `json:"tps"`
	Buckets      []*BucketView `json:"buckets"`
	Detail       *DetailView   `json:"detail,omitempty"`
}

type BucketView struct {
	Rate   float64 `json:"rate"`
	TtftMs int64   `json:"ttft_ms"`
}

type DetailView struct {
	TtftP50    int64              `json:"ttft_p50"`
	TtftP95    int64              `json:"ttft_p95"`
	TtftP99    int64              `json:"ttft_p99"`
	LatencyP50 int64              `json:"latency_p50"`
	LatencyP95 int64              `json:"latency_p95"`
	LatencyP99 int64              `json:"latency_p99"`
	FailShare  map[string]float64 `json:"fail_share,omitempty"`
}

// BuildResponse filters the shared snapshot down to what v may see. Groups are
// v's groups that sell at least one model, default first then alphabetical;
// models come from pricing, so sold models without traffic are listed and
// stale models that are no longer sold are not. snap is never modified.
func BuildResponse(snap *Snapshot, pricing []model.Pricing, vendors []model.PricingVendor, v Viewer) Response {
	spec := snap.Range
	resp := Response{
		Range:         spec.Key,
		BucketSeconds: spec.BucketSeconds,
		WindowStart:   snap.WindowStart,
		UpdatedAt:     snap.UpdatedAt,
		HasDetail:     spec.Detail,
		Groups:        []GroupView{},
	}

	vendorIcons := make(map[int]string, len(vendors))
	for _, vendor := range vendors {
		vendorIcons[vendor.ID] = vendor.Icon
	}

	groupKeys := make([]string, 0, len(v.Groups))
	for g := range v.Groups {
		if g != "" && g != "auto" {
			groupKeys = append(groupKeys, g)
		}
	}
	slices.SortFunc(groupKeys, func(a, b string) int {
		if a == b {
			return 0
		}
		if a == "default" {
			return -1
		}
		if b == "default" {
			return 1
		}
		return strings.Compare(a, b)
	})

	// The current status covers the most recent max(3, ceil(N*8%)) buckets.
	currentBuckets := min(max(3, (spec.Buckets*8+99)/100), spec.Buckets)

	for _, g := range groupKeys {
		type rankedModel struct {
			view ModelView
			req  int64
		}
		var ranked []rankedModel
		var groupReq, groupOk int64
		for _, item := range pricing {
			if !slices.Contains(item.EnableGroup, g) && !slices.Contains(item.EnableGroup, "all") {
				continue
			}
			icon := item.Icon
			if icon == "" {
				icon = vendorIcons[item.VendorID]
			}
			view := ModelView{Name: item.ModelName, VendorIcon: icon, Buckets: make([]*BucketView, spec.Buckets)}
			var total Counters
			series := snap.Pairs[PairKey{Group: g, Model: item.ModelName}]
			if series != nil {
				total = series.Total
				var recent Counters
				for i, bucket := range series.Buckets {
					if i >= len(series.Buckets)-currentBuckets {
						recent.Merge(bucket)
					}
					if bucket.Req == 0 {
						continue
					}
					var ttft int64
					if bucket.TtftN > 0 {
						ttft = bucket.TtftSum / bucket.TtftN
					}
					view.Buckets[i] = &BucketView{Rate: float64(bucket.Ok) / float64(bucket.Req), TtftMs: ttft}
				}
				view.Current = successRate(recent)
			}
			view.Availability = successRate(total)
			if total.Req > 0 {
				view.LatencyAvgMs = total.LatSum / total.Req
			}
			if total.TtftN > 0 {
				view.TtftAvgMs = total.TtftSum / total.TtftN
			}
			if total.GenMs > 0 {
				view.Tps = float64(total.Out) * 1000 / float64(total.GenMs)
			}
			if spec.Detail {
				view.Detail = &DetailView{
					TtftP50:    Percentile(total.TtftHist, 0.50),
					TtftP95:    Percentile(total.TtftHist, 0.95),
					TtftP99:    Percentile(total.TtftHist, 0.99),
					LatencyP50: Percentile(total.LatHist, 0.50),
					LatencyP95: Percentile(total.LatHist, 0.95),
					LatencyP99: Percentile(total.LatHist, 0.99),
				}
				var failures int64
				for _, n := range total.Fail {
					failures += n
				}
				if failures > 0 {
					view.Detail.FailShare = make(map[string]float64, len(total.Fail))
					for i, n := range total.Fail {
						view.Detail.FailShare[FailureKind(i+1).Key()] = float64(n) / float64(failures)
					}
				}
			}
			groupReq += total.Req
			groupOk += total.Ok
			ranked = append(ranked, rankedModel{view: view, req: total.Req})
		}
		if len(ranked) == 0 {
			continue
		}

		slices.SortFunc(ranked, func(a, b rankedModel) int {
			return cmp.Or(cmp.Compare(b.req, a.req), strings.Compare(a.view.Name, b.view.Name))
		})
		models := make([]ModelView, len(ranked))
		for i, r := range ranked {
			models[i] = r.view
		}
		resp.Groups = append(resp.Groups, GroupView{
			Key:          g,
			Desc:         v.Groups[g],
			Ratio:        v.Ratios[g],
			Availability: successRate(Counters{Req: groupReq, Ok: groupOk}),
			Models:       models,
		})
	}
	return resp
}

// successRate is Ok/Req as a 0–1 fraction, or nil when there were no requests.
func successRate(c Counters) *float64 {
	if c.Req == 0 {
		return nil
	}
	rate := float64(c.Ok) / float64(c.Req)
	return &rate
}
