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
)

const affCodeAttempts = 16

var ErrAffCodeExhausted = errors.New("failed to allocate a unique affiliate code")

// randomAffCode is a variable so tests can force collisions deterministically.
var randomAffCode = func() string { return common.GetRandomString(4) }

// NewAffCode returns an affiliate code no user holds. Soft-deleted users are
// included because their row still occupies the unique index. A concurrent
// registration drawing the same code in the same instant is still caught by
// that index; this only removes the collisions with existing rows.
func NewAffCode(tx *gorm.DB) (string, error) {
	for range affCodeAttempts {
		code := randomAffCode()
		var taken int64
		if err := tx.Model(&User{}).Unscoped().Where("aff_code = ?", code).Count(&taken).Error; err != nil {
			return "", err
		}
		if taken == 0 {
			return code, nil
		}
	}
	return "", ErrAffCodeExhausted
}
