package auth

// DB-backed tests for the OPAQUE-login TOTP gate. Skipped cleanly when
// ClickHouse isn't reachable (mirrors the model/server harnesses).

import (
	"sync"
	"testing"

	"github.com/tlalocweb/hulation/app"
	"github.com/tlalocweb/hulation/config"
	"github.com/tlalocweb/hulation/model"
)

var (
	totpDBOnce sync.Once
	totpDBErr  error
)

func setupTotpTestDB(t *testing.T) {
	t.Helper()
	totpDBOnce.Do(func() {
		if _, cerr := app.LoadConfigWithFile("testdata/opaque-totp-test.yaml"); cerr != nil {
			totpDBErr = cerr
			return
		}
		_, _, _, totpDBErr = model.SetupAppDB(config.GetConfig())
	})
	if totpDBErr != nil || model.GetDB() == nil {
		t.Skipf("ClickHouse not available for OPAQUE-TOTP tests: %v", totpDBErr)
	}
}

// A user with no TOTP record and non-admin provider does not require TOTP.
func TestTotpRequiredForLogin_NoRecord_Internal_False(t *testing.T) {
	setupTotpTestDB(t)
	if totpRequiredForLogin("no-totp-user@example.com", providerInternal) {
		t.Fatal("internal user without a TOTP record must not require TOTP")
	}
}

// The admin requires TOTP when config mandates it, even before enrollment
// (so they're forced through setup).
func TestTotpRequiredForLogin_AdminConfigRequired_True(t *testing.T) {
	setupTotpTestDB(t)
	cfg := config.GetConfig()
	if cfg == nil || cfg.Admin == nil {
		t.Skip("no admin config")
	}
	prev := cfg.Admin.TotpRequired
	cfg.Admin.TotpRequired = true
	defer func() { cfg.Admin.TotpRequired = prev }()

	if !totpRequiredForLogin(cfg.Admin.Username, providerAdmin) {
		t.Fatal("admin must require TOTP when config.admin.totp_required is set")
	}
	// The same flag must NOT force TOTP on an internal user (it's an
	// admin-scoped setting).
	if totpRequiredForLogin("someone-else@example.com", providerInternal) {
		t.Fatal("admin.totp_required must not apply to internal users")
	}
}

// A user with an enabled TOTP record requires TOTP regardless of provider.
func TestTotpRequiredForLogin_EnabledRecord_True(t *testing.T) {
	setupTotpTestDB(t)
	const u = "totp-enabled-user@example.com"
	if err := model.UpsertAdminTotp(model.GetDB(), &model.AdminTotpRecord{
		Username:    u,
		TotpEnabled: true,
	}); err != nil {
		t.Fatalf("seed TOTP record: %v", err)
	}
	if !totpRequiredForLogin(u, providerInternal) {
		t.Fatal("a user with an enabled TOTP record must require TOTP")
	}
}
