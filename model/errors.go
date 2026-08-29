package model

import "errors"

// Common errors
var (
	ErrDatabase = errors.New("database error")
)

// User auth errors
var (
	ErrInvalidCredentials   = errors.New("invalid credentials")
	ErrUserEmptyCredentials = errors.New("empty credentials")
	ErrEmailAlreadyTaken    = errors.New("email already taken")
	ErrEmailNotFound        = errors.New("email not found")
	ErrEmailAmbiguous       = errors.New("email matches multiple users")
)

// Token auth errors
var (
	ErrTokenNotProvided = errors.New("token not provided")
	ErrTokenInvalid     = errors.New("token invalid")
)

// Redemption errors
var (
	// ErrRedeemFailed covers internal failures (database errors, quota
	// overflow) where the caller should retry later.
	ErrRedeemFailed       = errors.New("redeem.failed")
	ErrRedeemCodeInvalid  = errors.New("redeem.code_invalid")
	ErrRedeemCodeUsed     = errors.New("redeem.code_used")
	ErrRedeemCodeExpired  = errors.New("redeem.code_expired")
	ErrRedeemCodeDisabled = errors.New("redeem.code_disabled")
	ErrRedeemCodeNotGiven = errors.New("redeem.code_not_given")
)

// 2FA errors
var ErrTwoFANotEnabled = errors.New("2fa not enabled")
var ErrTwoFAAlreadyEnabled = errors.New("2fa already enabled")
