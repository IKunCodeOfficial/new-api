package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// Fork-only (ikun): the 4-character affiliate code collides once the users
// table is large (57k users on the production copy made registration fail
// with a duplicate-key error). Allocation must skip taken codes, including
// those held by soft-deleted accounts, and the registration path must use it.
func TestNewAffCodeSkipsTakenCodes(t *testing.T) {
	require.NoError(t, DB.Session(&gorm.Session{AllowGlobalUpdate: true}).Unscoped().Delete(&User{}).Error)
	t.Cleanup(func() {
		require.NoError(t, DB.Session(&gorm.Session{AllowGlobalUpdate: true}).Unscoped().Delete(&User{}).Error)
	})
	require.NoError(t, DB.Create(&[]User{
		{Username: "aff-live", Password: "12345678", AffCode: "live", Status: common.UserStatusEnabled},
		{Username: "aff-gone", Password: "12345678", AffCode: "gone", Status: common.UserStatusEnabled},
	}).Error)
	require.NoError(t, DB.Where("username = ?", "aff-gone").Delete(&User{}).Error)

	original := randomAffCode
	t.Cleanup(func() { randomAffCode = original })
	sequence := func(codes ...string) func() string {
		i := 0
		return func() string {
			code := codes[min(i, len(codes)-1)]
			i++
			return code
		}
	}

	randomAffCode = sequence("live", "gone", "fre3")
	code, err := NewAffCode(DB)
	require.NoError(t, err)
	assert.Equal(t, "fre3", code, "codes held by live and soft-deleted users are skipped")

	randomAffCode = sequence("live")
	_, err = NewAffCode(DB)
	assert.ErrorIs(t, err, ErrAffCodeExhausted)

	randomAffCode = sequence("live", "new1")
	user := &User{Username: "aff-new", Password: "12345678", Status: common.UserStatusEnabled}
	require.NoError(t, user.Insert(0))
	var stored User
	require.NoError(t, DB.Where("username = ?", "aff-new").First(&stored).Error)
	assert.Equal(t, "new1", stored.AffCode, "registration allocates a free code instead of failing on the unique index")
}
