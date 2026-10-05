package controller

// Fork-only file (ikun): public read-only endpoint exposing the invite-rebate
// switch, rate and activity window to the wallet card and the admin settings
// block. Settlement runs in an external Python job; this endpoint never answers
// enabled=true for a configuration that job would refuse.

import (
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/gin-gonic/gin"
)

const (
	ikunRebateEnabledKey = "IkunRebateEnabled"
	ikunRebateRateKey    = "IkunRebateRate"
	ikunRebateCacheTTL   = 60 * time.Second
)

// rebateRateIfUsable validates the rate the admin settings block wrote (spec
// §3.3). The switch is not part of the answer: the rate is still reported to
// the admin page while the activity is off, so that re-enabling it does not
// silently start from a different rate. An absent key and an unparseable rate
// are equally unusable.
func rebateRateIfUsable(values map[string]string) (float64, bool) {
	rate, err := strconv.ParseFloat(values[ikunRebateRateKey], 64)
	if err != nil || math.IsNaN(rate) || rate <= 0 || rate >= 1 {
		return 0, false
	}
	return rate, true
}

// rebateMetaIfUsable validates the activity window the settler wrote at init.
// "Local" is rejected because Go resolves it while Python's ZoneInfo does not.
func rebateMetaIfUsable(values map[string]string) (string, string, bool) {
	start, timezone := values["activity_start"], values["timezone"]
	if _, err := time.Parse(time.DateOnly, start); err != nil {
		return "", "", false
	}
	if timezone == "" || timezone == "Local" {
		return "", "", false
	}
	if _, err := time.LoadLocation(timezone); err != nil {
		return "", "", false
	}
	return start, timezone, true
}

var ikunRebateCache struct {
	sync.Mutex
	fetchedAt time.Time
	payload   gin.H
}

func resetRebateInfoCache() {
	ikunRebateCache.Lock()
	ikunRebateCache.fetchedAt = time.Time{}
	ikunRebateCache.Unlock()
}

// GetRebateInfo answers the rebate settings for the wallet card; the admin
// settings page reads activity_start and timezone from the same payload to
// decide whether the settlement job has been initialised.
//
// The window is reported whenever rebate_meta is readable, even while the
// switch is off. Only a missing table or a missing/invalid window row falls
// back to {"enabled": false} alone, so the admin page loses the window rather
// than the endpoint failing.
//
// The answer is cached for a minute: it is polled on every wallet page load.
func GetRebateInfo(c *gin.Context) {
	ikunRebateCache.Lock()
	if time.Since(ikunRebateCache.fetchedAt) > ikunRebateCacheTTL {
		// Read the option rows back from the database rather than the in-memory
		// OptionMap: the option sync loop never drops deleted rows, so a deleted
		// row would keep advertising a stopped activity until restart.
		options := make(map[string]string, 2)
		var optionRows []model.Option
		if err := model.DB.Where(map[string]any{"key": []string{ikunRebateEnabledKey, ikunRebateRateKey}}).Find(&optionRows).Error; err != nil {
			// Fails closed: the map stays empty, so a transient read error can
			// neither advertise a rate nor turn the switch on.
			common.SysError("rebate_info: read rebate options: " + err.Error())
		} else {
			for _, row := range optionRows {
				options[row.Key] = row.Value
			}
		}

		// The settler owns rebate_meta and creates it from `init`, so it may not
		// exist yet; the Go side never migrates it. A read error fails closed the
		// same way an empty window does.
		meta := make(map[string]string, 2)
		if model.DB.Migrator().HasTable("rebate_meta") {
			var metaRows []struct {
				MetaKey   string `gorm:"column:meta_key"`
				MetaValue string `gorm:"column:meta_value"`
			}
			if err := model.DB.Table("rebate_meta").Find(&metaRows).Error; err != nil {
				common.SysError("rebate_info: read rebate_meta: " + err.Error())
			} else {
				for _, row := range metaRows {
					meta[row.MetaKey] = row.MetaValue
				}
			}
		}

		rate, rateUsable := rebateRateIfUsable(options)
		start, timezone, windowUsable := rebateMetaIfUsable(meta)

		answer := gin.H{"enabled": false}
		if windowUsable {
			answer = gin.H{
				"enabled":        rateUsable && options[ikunRebateEnabledKey] == "true",
				"activity_start": start,
				"timezone":       timezone,
			}
			if rateUsable {
				answer["rate"] = rate
			}
		}
		ikunRebateCache.payload = answer
		ikunRebateCache.fetchedAt = time.Now()
	}
	payload := ikunRebateCache.payload
	ikunRebateCache.Unlock()

	c.JSON(http.StatusOK, payload)
}
