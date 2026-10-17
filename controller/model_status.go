package controller

import (
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	modelstatus "github.com/QuantumNous/new-api/pkg/model_status"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/ratio_setting"

	"github.com/gin-gonic/gin"
)

func GetModelStatus(c *gin.Context) {
	spec, ok := modelstatus.ParseRange(c.DefaultQuery("range", "1h"))
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": "invalid range",
		})
		return
	}
	snap, err := modelstatus.GetSnapshot(c.Request.Context(), spec)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": err.Error(),
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data":    modelstatus.BuildResponse(snap, model.GetPricing(), model.GetVendors(), modelStatusViewer(c)),
	})
}

// modelStatusViewer limits the status page to the caller's usable groups (all
// groups for admins) and applies the caller's group-specific ratios, as
// GetPricing does. Ratios depend on the caller, so they are never cached.
func modelStatusViewer(c *gin.Context) modelstatus.Viewer {
	usableGroups := getUsablePerfMetricGroups(c)
	userGroup := ""
	role := common.RoleGuestUser
	if userID := c.GetInt("id"); userID != 0 {
		if group, err := model.GetUserGroup(userID, false); err == nil {
			userGroup = group
		}
		if model.IsAdmin(userID) {
			role = common.RoleAdminUser
		}
	}

	// Descriptions match /api/pricing's usable_group; visibility stays the
	// key set of usableGroups.
	descriptions := service.GetUserUsableGroupsForRole(userGroup, role)
	groupRatios := ratio_setting.GetGroupRatioCopy()
	viewer := modelstatus.Viewer{
		Groups: make(map[string]string, len(usableGroups)),
		Ratios: make(map[string]float64, len(usableGroups)),
	}
	for group := range usableGroups {
		desc, ok := descriptions[group]
		if !ok {
			desc = group
		}
		viewer.Groups[group] = desc
		ratio := groupRatios[group]
		if override, ok := ratio_setting.GetGroupGroupRatio(userGroup, group); ok {
			ratio = override
		}
		viewer.Ratios[group] = ratio
	}
	return viewer
}
