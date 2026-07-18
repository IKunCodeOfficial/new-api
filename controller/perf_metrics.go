package controller

import (
	"net/http"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	perfmetrics "github.com/QuantumNous/new-api/pkg/perf_metrics"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/ratio_setting"

	"github.com/gin-gonic/gin"
)

func GetPerfMetricsSummary(c *gin.Context) {
	hours := 24
	if rawHours := c.Query("hours"); rawHours != "" {
		if parsed, err := strconv.Atoi(rawHours); err == nil {
			hours = parsed
		}
	}

	usableGroups := getUsablePerfMetricGroups(c)
	activeGroups := make([]string, 0, len(usableGroups))
	for group := range usableGroups {
		activeGroups = append(activeGroups, group)
	}
	result, err := perfmetrics.QuerySummaryAll(hours, activeGroups)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data":    result,
	})
}

func GetPerfMetrics(c *gin.Context) {
	modelName := c.Query("model")
	if modelName == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": "model is required",
		})
		return
	}

	hours := 24
	if rawHours := c.Query("hours"); rawHours != "" {
		if parsed, err := strconv.Atoi(rawHours); err == nil {
			hours = parsed
		}
	}

	result, err := perfmetrics.Query(perfmetrics.QueryParams{
		Model: modelName,
		Group: c.Query("group"),
		Hours: hours,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": err.Error(),
		})
		return
	}

	result.Groups = filterPerfMetricGroupsByUsableGroups(result.Groups, getUsablePerfMetricGroups(c))

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data":    result,
	})
}

func getUsablePerfMetricGroups(c *gin.Context) map[string]struct{} {
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

	activeRatios := ratio_setting.GetGroupRatioCopy()
	usableGroups := service.GetUserUsableGroupsForRole(userGroup, role)
	groups := make(map[string]struct{}, len(usableGroups))
	for group := range usableGroups {
		if _, ok := activeRatios[group]; ok || group == "auto" {
			groups[group] = struct{}{}
		}
	}
	return groups
}

func filterPerfMetricGroupsByUsableGroups(groups []perfmetrics.GroupResult, usableGroups map[string]struct{}) []perfmetrics.GroupResult {
	filtered := make([]perfmetrics.GroupResult, 0, len(groups))
	for _, group := range groups {
		if _, ok := usableGroups[group.Group]; ok {
			filtered = append(filtered, group)
		}
	}
	return filtered
}
