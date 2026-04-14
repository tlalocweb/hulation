package handler

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/pquerna/otp/totp"
	"github.com/tlalocweb/hulation/app"
	"github.com/tlalocweb/hulation/log"
	"github.com/tlalocweb/hulation/model"
	"github.com/tlalocweb/hulation/utils"
)

// TotpSetup initiates TOTP enrollment for the admin user.
// Returns the secret, provisioning URI (for QR code), and recovery codes.
func TotpSetup(ctx RequestCtx) error {
	conf := app.GetConfig()
	encKey, err := utils.GetTOTPEncryptionKey(conf.TotpEncryptionKey)
	if err != nil {
		return ctx.Status(http.StatusInternalServerError).SendString("TOTP not configured: " + err.Error())
	}

	perms := ctx.Locals("perms").(*model.UserPermissions)
	username := perms.UserID

	// Check if already enabled
	existing, err := model.GetAdminTotp(model.GetDB(), username)
	if err != nil {
		return ctx.Status(http.StatusInternalServerError).SendString("error checking totp: " + err.Error())
	}
	if existing != nil && existing.TotpEnabled {
		return ctx.Status(http.StatusConflict).SendString("TOTP already enabled. Disable first to re-enroll.")
	}

	issuer := conf.TotpIssuer
	if issuer == "" {
		issuer = "Hulation"
	}

	key, err := totp.Generate(totp.GenerateOpts{
		Issuer:      issuer,
		AccountName: username,
	})
	if err != nil {
		return ctx.Status(http.StatusInternalServerError).SendString("error generating TOTP: " + err.Error())
	}

	// Encrypt the secret
	encrypted, err := utils.EncryptTOTPSecret(key.Secret(), encKey)
	if err != nil {
		return ctx.Status(http.StatusInternalServerError).SendString("error encrypting secret: " + err.Error())
	}

	// Generate recovery codes
	codes, err := utils.GenerateRecoveryCodes(8)
	if err != nil {
		return ctx.Status(http.StatusInternalServerError).SendString("error generating recovery codes: " + err.Error())
	}
	hashedCodes, err := utils.HashRecoveryCodes(codes)
	if err != nil {
		return ctx.Status(http.StatusInternalServerError).SendString("error hashing recovery codes: " + err.Error())
	}

	// Save as pending
	rec := &model.AdminTotpRecord{
		Username:                username,
		TotpEnabled:             false,
		TotpPendingSetup:        true,
		TotpSecretEncrypted:     encrypted,
		TotpRecoveryCodesHashed: hashedCodes,
	}
	if err := model.UpsertAdminTotp(model.GetDB(), rec); err != nil {
		return ctx.Status(http.StatusInternalServerError).SendString("error saving totp: " + err.Error())
	}

	return ctx.SendJSON(map[string]interface{}{
		"secret":         key.Secret(),
		"url":            key.URL(),
		"recovery_codes": codes,
	})
}

// TotpVerifySetup completes TOTP enrollment by verifying the first code.
func TotpVerifySetup(ctx RequestCtx) error {
	conf := app.GetConfig()
	encKey, err := utils.GetTOTPEncryptionKey(conf.TotpEncryptionKey)
	if err != nil {
		return ctx.Status(http.StatusInternalServerError).SendString("TOTP not configured: " + err.Error())
	}

	var input struct {
		Code string `json:"code"`
	}
	if err := ctx.BodyParser(&input); err != nil {
		return ctx.Status(http.StatusBadRequest).SendString("bad request: " + err.Error())
	}
	if input.Code == "" {
		return ctx.Status(http.StatusBadRequest).SendString("code required")
	}

	perms := ctx.Locals("perms").(*model.UserPermissions)
	username := perms.UserID

	rec, err := model.GetAdminTotp(model.GetDB(), username)
	if err != nil || rec == nil {
		return ctx.Status(http.StatusBadRequest).SendString("no pending TOTP setup")
	}
	if !rec.TotpPendingSetup {
		return ctx.Status(http.StatusBadRequest).SendString("no pending TOTP setup")
	}

	secret, err := utils.DecryptTOTPSecret(rec.TotpSecretEncrypted, encKey)
	if err != nil {
		return ctx.Status(http.StatusInternalServerError).SendString("error decrypting secret: " + err.Error())
	}

	if !totp.Validate(input.Code, secret) {
		return ctx.Status(http.StatusUnauthorized).SendString("invalid code")
	}

	rec.TotpEnabled = true
	rec.TotpPendingSetup = false
	rec.TotpEnabledAt = time.Now()
	if err := model.UpsertAdminTotp(model.GetDB(), rec); err != nil {
		return ctx.Status(http.StatusInternalServerError).SendString("error saving: " + err.Error())
	}

	log.Infof("TOTP enabled for admin user %s", username)
	return ctx.SendJSON(map[string]string{"status": "totp_enabled"})
}

// TotpValidate validates a TOTP code (or recovery code) using a totp_pending token.
// On success, returns a full JWT.
func TotpValidate(ctx RequestCtx) error {
	conf := app.GetConfig()

	var input struct {
		Token          string `json:"totp_token"`
		Code           string `json:"code"`
		IsRecoveryCode bool   `json:"is_recovery_code"`
	}
	if err := ctx.BodyParser(&input); err != nil {
		return ctx.Status(http.StatusBadRequest).SendString("bad request: " + err.Error())
	}
	if input.Token == "" || input.Code == "" {
		return ctx.Status(http.StatusBadRequest).SendString("totp_token and code required")
	}

	// Verify the totp_pending token
	ok, perms, err := model.VerifyJWTClaims(model.GetDB(), input.Token)
	if err != nil || !ok {
		return ctx.Status(http.StatusUnauthorized).SendString("invalid token")
	}
	if !perms.HasCap("totp_pending") {
		return ctx.Status(http.StatusUnauthorized).SendString("not a totp pending token")
	}

	username := perms.UserID
	rec, err := model.GetAdminTotp(model.GetDB(), username)
	if err != nil || rec == nil || !rec.TotpEnabled {
		return ctx.Status(http.StatusBadRequest).SendString("TOTP not enabled")
	}

	if input.IsRecoveryCode {
		matched, err := model.VerifyRecoveryCode(model.GetDB(), rec, input.Code)
		if err != nil {
			log.Errorf("TOTP recovery code error: %s", err)
		}
		if !matched {
			return ctx.Status(http.StatusUnauthorized).SendString("invalid recovery code")
		}
		log.Warnf("TOTP recovery code used by %s (%d remaining)", username, len(rec.TotpRecoveryCodesHashed))
	} else {
		encKey, err := utils.GetTOTPEncryptionKey(conf.TotpEncryptionKey)
		if err != nil {
			return ctx.Status(http.StatusInternalServerError).SendString("TOTP not configured")
		}
		secret, err := utils.DecryptTOTPSecret(rec.TotpSecretEncrypted, encKey)
		if err != nil {
			return ctx.Status(http.StatusInternalServerError).SendString("error decrypting secret")
		}
		if !totp.Validate(input.Code, secret) {
			return ctx.Status(http.StatusUnauthorized).SendString("invalid TOTP code")
		}
	}

	// Issue full JWT
	isAdmin := (username == conf.Admin.Username)
	jwt, err := model.NewJWTClaimsCommit(model.GetDB(), username, &model.LoginOpts{
		IsAdmin: isAdmin,
	})
	if err != nil {
		return ctx.Status(http.StatusInternalServerError).SendString("error creating JWT: " + err.Error())
	}

	return ctx.SendJSON(map[string]string{"jwt": jwt})
}

// TotpDisable disables TOTP for the admin user. Requires current TOTP code.
func TotpDisable(ctx RequestCtx) error {
	conf := app.GetConfig()
	encKey, err := utils.GetTOTPEncryptionKey(conf.TotpEncryptionKey)
	if err != nil {
		return ctx.Status(http.StatusInternalServerError).SendString("TOTP not configured")
	}

	var input struct {
		Code string `json:"code"`
	}
	if err := ctx.BodyParser(&input); err != nil {
		return ctx.Status(http.StatusBadRequest).SendString("bad request: " + err.Error())
	}
	if input.Code == "" {
		return ctx.Status(http.StatusBadRequest).SendString("code required")
	}

	perms := ctx.Locals("perms").(*model.UserPermissions)
	username := perms.UserID

	rec, err := model.GetAdminTotp(model.GetDB(), username)
	if err != nil || rec == nil || !rec.TotpEnabled {
		return ctx.Status(http.StatusBadRequest).SendString("TOTP not enabled")
	}

	secret, err := utils.DecryptTOTPSecret(rec.TotpSecretEncrypted, encKey)
	if err != nil {
		return ctx.Status(http.StatusInternalServerError).SendString("error decrypting secret")
	}

	if !totp.Validate(input.Code, secret) {
		return ctx.Status(http.StatusUnauthorized).SendString("invalid code")
	}

	rec.TotpEnabled = false
	rec.TotpPendingSetup = false
	rec.TotpSecretEncrypted = ""
	rec.TotpRecoveryCodesHashed = nil
	if err := model.UpsertAdminTotp(model.GetDB(), rec); err != nil {
		return ctx.Status(http.StatusInternalServerError).SendString("error saving: " + err.Error())
	}

	log.Infof("TOTP disabled for admin user %s", username)
	return ctx.SendJSON(map[string]string{"status": "totp_disabled"})
}

// TotpStatus returns whether TOTP is enabled and how many recovery codes remain.
func TotpStatus(ctx RequestCtx) error {
	perms := ctx.Locals("perms").(*model.UserPermissions)
	username := perms.UserID

	rec, err := model.GetAdminTotp(model.GetDB(), username)
	if err != nil {
		return ctx.Status(http.StatusInternalServerError).SendString("error: " + err.Error())
	}

	enabled := false
	pendingSetup := false
	recoveryCodes := 0
	if rec != nil {
		enabled = rec.TotpEnabled
		pendingSetup = rec.TotpPendingSetup
		recoveryCodes = len(rec.TotpRecoveryCodesHashed)
	}

	return ctx.SendJSON(map[string]interface{}{
		"totp_enabled":        enabled,
		"totp_pending_setup":  pendingSetup,
		"recovery_codes_left": recoveryCodes,
		"totp_required":       app.GetConfig().Admin.TotpRequired,
	})
}

// CheckTotpRequired checks if the admin user needs TOTP validation.
// Returns true if TOTP is enabled for this user (or required by config and not yet set up).
func CheckTotpRequired(username string) bool {
	rec, err := model.GetAdminTotp(model.GetDB(), username)
	if err != nil {
		return false
	}
	if rec != nil && rec.TotpEnabled {
		return true
	}
	// If config requires TOTP but user hasn't set it up, still require the TOTP flow
	// so they can enroll
	if app.GetConfig().Admin.TotpRequired {
		return true
	}
	return false
}

// LoginResponseForTotp is used by Login handler when TOTP is required.
func LoginResponseForTotp(ctx RequestCtx, username string) error {
	totpToken, err := model.NewTotpPendingToken(model.GetDB(), username)
	if err != nil {
		log.Errorf("error creating totp pending token: %s", err)
		return ctx.Status(http.StatusInternalServerError).SendString(fmt.Sprintf("error creating totp token: %s", err.Error()))
	}

	// Check if user needs to set up TOTP first
	rec, _ := model.GetAdminTotp(model.GetDB(), username)
	needsSetup := (rec == nil || !rec.TotpEnabled)

	return ctx.SendJSON(map[string]interface{}{
		"totp_required":   true,
		"totp_token":      totpToken,
		"totp_needs_setup": needsSetup,
	})
}

// Helper used by the OPA input creation to detect totp_pending tokens
func IsTotpPendingCap(caps []string) bool {
	for _, c := range caps {
		if strings.TrimSpace(c) == "totp_pending" {
			return true
		}
	}
	return false
}
