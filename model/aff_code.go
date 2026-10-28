package model

// Fork-only file (ikun): affiliate code allocation with a uniqueness check.
//
// Affiliate codes are 4 alphanumeric characters (14.7M combinations). The
// production copy holds 57k users, so a blind random pick collides on about
// 0.4% of registrations and the unique index turned those into a raw
// "Duplicate entry ... users.idx_users_aff_code" error shown to the person
// registering. Codes are kept at 4 characters because they are embedded in
// invite links already in circulation.

import (
	"errors"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const affCodeAttempts = 16

var ErrAffCodeExhausted = errors.New("failed to allocate a unique affiliate code")

// RetiredAffCode keeps the affiliate code of a hard-deleted account out of
// circulation. Invite links already shared still carry it, and reissuing it
// would bind future sign-ups, and their rebates, to an unrelated new user.
type RetiredAffCode struct {
	Code      string `json:"code" gorm:"type:varchar(32);primaryKey"`
	RetiredAt int64  `json:"retired_at" gorm:"type:bigint"`
}

func (RetiredAffCode) TableName() string { return "retired_aff_codes" }

// retireAffCodeWithTx records the user's code before the row is hard-deleted.
func retireAffCodeWithTx(tx *gorm.DB, userId int) error {
	// Legacy rows may hold NULL, which cannot be scanned into a string.
	var codes []string
	if err := tx.Unscoped().Model(&User{}).Where("id = ? AND aff_code IS NOT NULL AND aff_code <> ''", userId).
		Pluck("aff_code", &codes).Error; err != nil {
		return err
	}
	if len(codes) == 0 {
		return nil
	}
	return tx.Clauses(clause.OnConflict{DoNothing: true}).
		Create(&RetiredAffCode{Code: codes[0], RetiredAt: common.GetTimestamp()}).Error
}

// randomAffCode is a variable so tests can force collisions deterministically.
var randomAffCode = func() string { return common.GetRandomString(4) }

// NewAffCode returns an affiliate code no user holds. Soft-deleted users are
// included because their row still occupies the unique index, and codes of
// hard-deleted users are never reissued. A concurrent registration drawing the
// same code in the same instant is still caught by that index; this only
// removes the collisions with existing rows.
func NewAffCode(tx *gorm.DB) (string, error) {
	for range affCodeAttempts {
		code := randomAffCode()
		var taken, retired int64
		if err := tx.Model(&User{}).Unscoped().Where("aff_code = ?", code).Count(&taken).Error; err != nil {
			return "", err
		}
		if taken > 0 {
			continue
		}
		if err := tx.Model(&RetiredAffCode{}).Where("code = ?", code).Count(&retired).Error; err != nil {
			return "", err
		}
		if retired == 0 {
			return code, nil
		}
	}
	return "", ErrAffCodeExhausted
}

// EnsureAffCode returns the user's affiliate code, allocating one for legacy
// accounts that never had it. Only aff_code is written, and only while it is
// still empty: a concurrent first request cannot hand out a code that a later
// write replaces, and no other column is rewritten from a stale read (an
// administrator's ban landing in between used to be reverted).
func EnsureAffCode(userId int) (string, error) {
	var current User
	if err := DB.Select("id", "aff_code").First(&current, userId).Error; err != nil {
		return "", err
	}
	if current.AffCode != "" {
		return current.AffCode, nil
	}
	code, err := NewAffCode(DB)
	if err != nil {
		return "", err
	}
	if err := DB.Model(&User{}).Where("id = ? AND (aff_code IS NULL OR aff_code = '')", userId).
		Update("aff_code", code).Error; err != nil {
		return "", err
	}
	if err := DB.Select("id", "aff_code").First(&current, userId).Error; err != nil {
		return "", err
	}
	return current.AffCode, nil
}
