package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
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

// Fork-only (ikun): OAuth/OIDC/custom-OAuth sign-ups create the account through
// InsertWithTx. The inviter must be stored on the row, or the external settler
// (which pays users.inviter_id) never sees the invitee even though the inviter
// already received the invite bonus.
func TestInsertStoresInviterForEveryRegistrationPath(t *testing.T) {
	require.NoError(t, DB.Session(&gorm.Session{AllowGlobalUpdate: true}).Unscoped().Delete(&User{}).Error)
	t.Cleanup(func() {
		require.NoError(t, DB.Session(&gorm.Session{AllowGlobalUpdate: true}).Unscoped().Delete(&User{}).Error)
	})
	inviter := &User{Username: "aff-inviter", Password: "12345678", Status: common.UserStatusEnabled}
	require.NoError(t, inviter.Insert(0))

	oauthUser := &User{Username: "aff-oauth", Status: common.UserStatusEnabled}
	require.NoError(t, DB.Transaction(func(tx *gorm.DB) error { return oauthUser.InsertWithTx(tx, inviter.Id) }))
	passwordUser := &User{Username: "aff-password", Password: "12345678", Status: common.UserStatusEnabled}
	require.NoError(t, passwordUser.Insert(inviter.Id))
	plainUser := &User{Username: "aff-plain", Password: "12345678", Status: common.UserStatusEnabled}
	require.NoError(t, plainUser.Insert(0))

	for username, want := range map[string]int{"aff-oauth": inviter.Id, "aff-password": inviter.Id, "aff-plain": 0} {
		var stored User
		require.NoError(t, DB.Where("username = ?", username).First(&stored).Error)
		assert.Equal(t, want, stored.InviterId, username)
	}
}

// Legacy accounts get their affiliate code on first request. Only aff_code may
// be written: a whole-row update replays the row as it was read and undoes a
// ban that lands while the code is being allocated. A second call must return
// the same code.
func TestEnsureAffCodeWritesOnlyTheCode(t *testing.T) {
	require.NoError(t, DB.Session(&gorm.Session{AllowGlobalUpdate: true}).Unscoped().Delete(&User{}).Error)
	t.Cleanup(func() {
		require.NoError(t, DB.Session(&gorm.Session{AllowGlobalUpdate: true}).Unscoped().Delete(&User{}).Error)
	})
	legacy := &User{Username: "aff-legacy", Password: "12345678", Status: common.UserStatusEnabled, Role: common.RoleCommonUser}
	require.NoError(t, DB.Create(legacy).Error)
	require.NoError(t, DB.Model(&User{}).Where("id = ?", legacy.Id).Update("aff_code", "").Error)

	original := randomAffCode
	t.Cleanup(func() { randomAffCode = original })
	randomAffCode = func() string {
		// An administrator bans the account after it was read, before the code is written.
		require.NoError(t, DB.Model(&User{}).Where("id = ?", legacy.Id).Update("status", common.UserStatusDisabled).Error)
		return "lg01"
	}

	code, err := EnsureAffCode(legacy.Id)
	require.NoError(t, err)
	assert.Equal(t, "lg01", code)
	randomAffCode = func() string { return "lg02" }
	again, err := EnsureAffCode(legacy.Id)
	require.NoError(t, err)
	assert.Equal(t, "lg01", again, "an existing code is returned, never replaced")

	var stored User
	require.NoError(t, DB.First(&stored, legacy.Id).Error)
	assert.Equal(t, "lg01", stored.AffCode)
	assert.Equal(t, common.UserStatusDisabled, stored.Status, "allocating a code must not rewrite other columns")

	// A concurrent first request writes its code after this one read the
	// empty column: the code already handed out must stay, not be replaced.
	racing := &User{Username: "aff-racing", Password: "12345678", Status: common.UserStatusEnabled, Role: common.RoleCommonUser}
	require.NoError(t, DB.Create(racing).Error)
	require.NoError(t, DB.Model(&User{}).Where("id = ?", racing.Id).Update("aff_code", "").Error)
	randomAffCode = func() string {
		require.NoError(t, DB.Model(&User{}).Where("id = ?", racing.Id).Update("aff_code", "rc01").Error)
		return "rc02"
	}
	code, err = EnsureAffCode(racing.Id)
	require.NoError(t, err)
	assert.Equal(t, "rc01", code, "the code another request already handed out is returned")
	var storedRacing User
	require.NoError(t, DB.First(&storedRacing, racing.Id).Error)
	assert.Equal(t, "rc01", storedRacing.AffCode)
}

// The invite bonus lands in aff_quota next to paid rebates. A disabled
// inviter's code still binds the invitee (the settler forfeits that share),
// but the account must not keep accruing bonuses, and a bonus is only logged
// when it was credited.
func TestInvitationBonusSkipsDisabledInviters(t *testing.T) {
	require.NoError(t, DB.Session(&gorm.Session{AllowGlobalUpdate: true}).Unscoped().Delete(&User{}).Error)
	require.NoError(t, LOG_DB.Exec("DELETE FROM logs").Error)
	t.Cleanup(func() {
		require.NoError(t, DB.Session(&gorm.Session{AllowGlobalUpdate: true}).Unscoped().Delete(&User{}).Error)
		require.NoError(t, LOG_DB.Exec("DELETE FROM logs").Error)
	})
	payment := operation_setting.GetPaymentSetting()
	prevConfirmed, prevVersion, prevInviter := payment.ComplianceConfirmed, payment.ComplianceTermsVersion, common.QuotaForInviter
	payment.ComplianceConfirmed, payment.ComplianceTermsVersion = true, operation_setting.CurrentComplianceTermsVersion
	common.QuotaForInviter = 1
	t.Cleanup(func() {
		payment.ComplianceConfirmed, payment.ComplianceTermsVersion, common.QuotaForInviter = prevConfirmed, prevVersion, prevInviter
	})

	active := &User{Username: "aff-active", Password: "12345678", Status: common.UserStatusEnabled}
	require.NoError(t, active.Insert(0))
	banned := &User{Username: "aff-banned", Password: "12345678", Status: common.UserStatusEnabled}
	require.NoError(t, banned.Insert(0))
	require.NoError(t, DB.Model(&User{}).Where("id = ?", banned.Id).Update("status", common.UserStatusDisabled).Error)

	for _, inviter := range []*User{active, banned} {
		invitee := &User{Username: "aff-of-" + inviter.Username, Password: "12345678", Status: common.UserStatusEnabled}
		require.NoError(t, invitee.Insert(inviter.Id))
	}

	for inviter, want := range map[*User]int{active: 1, banned: 0} {
		var stored User
		require.NoError(t, DB.First(&stored, inviter.Id).Error)
		assert.Equal(t, want, stored.AffCount, inviter.Username)
		assert.Equal(t, want, stored.AffQuota, inviter.Username)
		var logs int64
		require.NoError(t, LOG_DB.Model(&Log{}).Where("user_id = ? AND type = ?", inviter.Id, LogTypeSystem).Count(&logs).Error)
		assert.Equal(t, int64(want), logs, inviter.Username)
		var bound int64
		require.NoError(t, DB.Model(&User{}).Where("inviter_id = ?", inviter.Id).Count(&bound).Error)
		assert.Equal(t, int64(1), bound, "the invitee is still linked to %s", inviter.Username)
	}
}

// Invite links of a hard-deleted account keep circulating. Its code must never
// be reissued, or new sign-ups through those links would bind (and earn
// rebates) for an unrelated new user.
func TestHardDeletedAffCodeIsNeverReissued(t *testing.T) {
	require.NoError(t, DB.Session(&gorm.Session{AllowGlobalUpdate: true}).Unscoped().Delete(&User{}).Error)
	require.NoError(t, DB.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&RetiredAffCode{}).Error)
	t.Cleanup(func() {
		require.NoError(t, DB.Session(&gorm.Session{AllowGlobalUpdate: true}).Unscoped().Delete(&User{}).Error)
		require.NoError(t, DB.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&RetiredAffCode{}).Error)
	})
	original := randomAffCode
	t.Cleanup(func() { randomAffCode = original })

	randomAffCode = func() string { return "dead" }
	gone := &User{Username: "aff-hard-deleted", Password: "12345678", Status: common.UserStatusEnabled}
	require.NoError(t, gone.Insert(0))
	_, err := HardDeleteUserById(gone.Id)
	require.NoError(t, err)

	calls := 0
	randomAffCode = func() string {
		calls++
		if calls == 1 {
			return "dead"
		}
		return "liv3"
	}
	code, err := NewAffCode(DB)
	require.NoError(t, err)
	assert.Equal(t, "liv3", code)
	id, _ := GetUserIdByAffCode("dead")
	assert.Zero(t, id, "the retired code resolves to nobody")

	// Legacy accounts without a code (NULL) can still be hard-deleted.
	legacy := &User{Username: "aff-null-code", Password: "12345678", Status: common.UserStatusEnabled}
	require.NoError(t, legacy.Insert(0))
	require.NoError(t, DB.Model(&User{}).Where("id = ?", legacy.Id).Update("aff_code", gorm.Expr("NULL")).Error)
	_, err = HardDeleteUserById(legacy.Id)
	require.NoError(t, err)
	var left int64
	require.NoError(t, DB.Unscoped().Model(&User{}).Where("id = ?", legacy.Id).Count(&left).Error)
	assert.Zero(t, left)
}

// Third-party identities stay taken after their account is soft-deleted, so the
// same identity cannot sign up again and pick a new inviter.
func TestDeletedAccountKeepsItsOidcIdentity(t *testing.T) {
	require.NoError(t, DB.Session(&gorm.Session{AllowGlobalUpdate: true}).Unscoped().Delete(&User{}).Error)
	t.Cleanup(func() {
		require.NoError(t, DB.Session(&gorm.Session{AllowGlobalUpdate: true}).Unscoped().Delete(&User{}).Error)
	})
	user := &User{Username: "aff-oidc", Status: common.UserStatusEnabled, OidcId: "oidc-sub-1"}
	require.NoError(t, user.Insert(0))
	require.True(t, IsOidcIdAlreadyTaken("oidc-sub-1"))
	_, err := DeleteUserById(user.Id)
	require.NoError(t, err)
	assert.True(t, IsOidcIdAlreadyTaken("oidc-sub-1"), "a soft-deleted account still holds its OIDC identity")
}
