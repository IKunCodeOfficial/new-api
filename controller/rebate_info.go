package controller

// Fork-only file (ikun): public read-only endpoint exposing the invite-rebate
// switch, rate and activity window to the wallet card and the admin settings
// block. Settlement runs in an external Python job; this endpoint never answers
// enabled=true for a configuration that job would refuse.

import (
	"net/http"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/gin-gonic/gin"
)

const ikunRebateCacheTTL = 60 * time.Second

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
		options := make(map[string]string, 3)
		var optionRows []model.Option
		optionKeys := []string{model.IkunRebateEnabledOptionKey, model.IkunRebateRateOptionKey, "LogConsumeEnabled"}
		if err := model.DB.Where(map[string]any{"key": optionKeys}).Find(&optionRows).Error; err != nil {
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

		// The rate uses the same strict form the save path enforces (spec §3.3);
		// an absent key and an unparseable rate are equally unusable.
		rate, rateUsable := model.ParseIkunRebateRate(options[model.IkunRebateRateOptionKey])
		start, timezone, windowUsable := rebateMetaIfUsable(meta)

		// The settler refuses to run unless consume logging is on: the row is
		// absent (default on) or exactly "true".
		logConsume, logConsumeSet := options["LogConsumeEnabled"]
		consumeLogged := !logConsumeSet || logConsume == "true"

		answer := gin.H{"enabled": false}
		if windowUsable {
			enabled := rateUsable && consumeLogged && options[model.IkunRebateEnabledOptionKey] == "true"
			answer = gin.H{
				"enabled":        enabled,
				"activity_start": start,
				"timezone":       timezone,
			}
			// The endpoint is public. The admin page reads the stored rate from
			// /api/option/, so a planned or stopped rate is not advertised.
			if enabled {
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
