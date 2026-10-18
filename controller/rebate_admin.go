package controller

// Fork-only file (ikun): invite-rebate exclusion list (root only). Excluded
// users keep advancing their settlement watermark but are never paid out; see
// docs/superpowers/specs/2026-10-01-invite-rebate-design.md §2.3.

import (
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/gin-gonic/gin"
)

// rebateExclusionNoteLimit matches note varchar(255): MySQL utf8mb4 and
// PostgreSQL both count characters, so the limit is measured in runes.
const rebateExclusionNoteLimit = 255

type rebateExclusionRequest struct {
	UserId int    `json:"user_id"`
	Role   string `json:"role"`
	Note   string `json:"note"`
}

func ListRebateExclusions(c *gin.Context) {
	rows := make([]model.RebateExclusion, 0)
	if err := model.DB.Order("id desc").Find(&rows).Error; err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, rows)
}

func AddRebateExclusion(c *gin.Context) {
	var req rebateExclusionRequest
	if err := common.DecodeJson(c.Request.Body, &req); err != nil {
		common.ApiErrorT(c, "Invalid parameters")
		return
	}
	if req.UserId <= 0 || (req.Role != "inviter" && req.Role != "invitee") || utf8.RuneCountInString(req.Note) > rebateExclusionNoteLimit {
		common.ApiErrorT(c, "Invalid parameters")
		return
	}
	var existing int64
	if err := model.DB.Model(&model.RebateExclusion{}).
		Where("user_id = ? AND role = ?", req.UserId, req.Role).Count(&existing).Error; err != nil {
		common.ApiError(c, err)
		return
	}
	if existing > 0 {
		common.ApiErrorT(c, "Already exists")
		return
	}
	row := &model.RebateExclusion{
		UserId:    req.UserId,
		Role:      req.Role,
		Note:      req.Note,
		CreatedAt: common.GetTimestamp(),
	}
	// The unique index on (user_id, role) stays the final guard; a concurrent
	// duplicate fails here as a plain DB error rather than a silent second row.
	if err := model.DB.Create(row).Error; err != nil {
		common.ApiError(c, err)
		return
	}
	recordManageAuditFor(c, req.UserId, "rebate.exclusion_add", map[string]interface{}{
		"target_user_id": req.UserId,
		"role":           req.Role,
	})
	common.ApiSuccess(c, row)
}

func DeleteRebateExclusion(c *gin.Context) {
	id := common.String2Int(c.Param("id"))
	if id <= 0 {
		common.ApiErrorT(c, "Invalid parameters")
		return
	}
	var row model.RebateExclusion
	if err := model.DB.First(&row, id).Error; err != nil {
		common.ApiError(c, err)
		return
	}
	if err := model.DB.Delete(&model.RebateExclusion{}, id).Error; err != nil {
		common.ApiError(c, err)
		return
	}
	recordManageAuditFor(c, row.UserId, "rebate.exclusion_delete", map[string]interface{}{
		"target_user_id": row.UserId,
		"role":           row.Role,
	})
	common.ApiSuccess(c, nil)
}
