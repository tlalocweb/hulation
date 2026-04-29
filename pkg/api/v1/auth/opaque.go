package auth

// OPAQUE register + login RPC handlers. Wires the AuthService
// proto endpoints to pkg/auth/opaque (OPAQUE wire library) and
// pkg/store/bolt (record persistence).
//
// Design:
//   * RegisterInit / RegisterFinish — admin can self-register
//     anytime; internal users register via the invite-token flow
//     (TODO stage 7).
//   * LoginInit / LoginFinish — issues a JWT on success. For admin,
//     populates `admintoken`; for internal users, `token`. Matches
//     the legacy LoginAdmin / LoginWithSecret response shapes so
//     downstream code paths don't fork.
//   * legacy_available — when a username has no OPAQUE record but
//     the legacy hash is still good (admin only — see `cfg.Admin.Hash`),
//     return legacy_available=true on LoginInit so the client can
//     fall back to LoginAdmin during the deprecation window.

import (
	"context"
	"encoding/base64"
	"fmt"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/tlalocweb/hulation/config"
	"github.com/tlalocweb/hulation/model"
	"github.com/tlalocweb/hulation/pkg/auth/opaque"
	authspec "github.com/tlalocweb/hulation/pkg/apispec/v1/auth"
	"github.com/tlalocweb/hulation/pkg/server/authware"
	hulabolt "github.com/tlalocweb/hulation/pkg/store/bolt"
	"github.com/tlalocweb/hulation/pkg/store/storage"
)

const (
	providerAdmin    = "admin"
	providerInternal = "internal"
)

// b64dec accepts either base64url (raw or padded) — matches what
// serenity-kit emits + bytemare consumes (OPAQUE_PLAN §18.3a).
func b64dec(s string) ([]byte, error) {
	if s == "" {
		return nil, fmt.Errorf("empty")
	}
	if b, err := base64.RawURLEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	if b, err := base64.URLEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	if b, err := base64.RawStdEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	if b, err := base64.StdEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	return nil, fmt.Errorf("not valid base64")
}

// b64enc — bytes to raw base64url. The shared encoding with
// serenity-kit's wire format.
func b64enc(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// validProvider returns nil if p is a recognised provider for OPAQUE.
func validProvider(p string) error {
	if p == providerAdmin || p == providerInternal {
		return nil
	}
	return status.Errorf(codes.InvalidArgument, "unknown provider %q", p)
}

// canRegister returns nil when the caller is allowed to register
// (or rotate) the password for (provider, username).
//
// Rules:
//   * provider="admin" AND username == config.Admin.Username AND
//     no existing OPAQUE record → allow without auth (bootstrap).
//   * any other case → require admin JWT on the request context.
//
// The "bootstrap window" closes the moment a record exists; from
// then on, password rotation requires admin auth.
func canRegister(ctx context.Context, provider, username string) error {
	existing, err := hulabolt.GetOpaqueRecord(ctx, storage.Global(), provider, username)
	if err != nil {
		return status.Errorf(codes.Internal, "check existing record: %v", err)
	}
	if existing == nil && provider == providerAdmin {
		cfg := config.GetConfig()
		if cfg != nil && cfg.Admin != nil && cfg.Admin.Username == username {
			return nil
		}
	}
	if !callerIsAdmin(ctx) {
		return status.Error(codes.PermissionDenied,
			"OPAQUE register requires admin authentication "+
				"(or the bootstrap path: provider=admin, "+
				"matching config.Admin.Username, no existing record)")
	}
	return nil
}

// callerIsAdmin checks the authware Claims on the request context.
func callerIsAdmin(ctx context.Context) bool {
	c, ok := ctx.Value(authware.ClaimsKey).(*authware.Claims)
	if !ok || c == nil {
		return false
	}
	for _, r := range c.Roles {
		if r == "admin" || r == "superadmin" {
			return true
		}
	}
	if c.Username == "admin" {
		return true
	}
	return false
}

// OpaqueRegisterInit — first half of registration. See canRegister
// for the auth gate.
func (s *Server) OpaqueRegisterInit(ctx context.Context, req *authspec.OpaqueRegisterInitRequest) (*authspec.OpaqueRegisterInitResponse, error) {
	if req == nil || req.GetUsername() == "" {
		return nil, status.Error(codes.InvalidArgument, "username required")
	}
	if err := validProvider(req.GetProvider()); err != nil {
		return nil, err
	}
	if err := canRegister(ctx, req.GetProvider(), req.GetUsername()); err != nil {
		return nil, err
	}
	srv := opaque.Global()
	if srv == nil {
		return nil, status.Error(codes.FailedPrecondition, "OPAQUE server not initialized")
	}
	m1, err := b64dec(req.GetM1B64())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "decode m1_b64: %v", err)
	}
	credID := opaque.CredentialID(req.GetProvider(), req.GetUsername())
	m2, err := srv.RegisterInit(credID, m1)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "opaque register init: %v", err)
	}
	return &authspec.OpaqueRegisterInitResponse{M2B64: b64enc(m2)}, nil
}

// OpaqueRegisterFinish — persists the resulting record under the
// (provider, username) key. Idempotent: replays/updates rotate.
//
// Auth gate: same canRegister rules as OpaqueRegisterInit.
func (s *Server) OpaqueRegisterFinish(ctx context.Context, req *authspec.OpaqueRegisterFinishRequest) (*authspec.OpaqueRegisterFinishResponse, error) {
	if req == nil || req.GetUsername() == "" {
		return nil, status.Error(codes.InvalidArgument, "username required")
	}
	if err := validProvider(req.GetProvider()); err != nil {
		return nil, err
	}
	if err := canRegister(ctx, req.GetProvider(), req.GetUsername()); err != nil {
		return nil, err
	}
	srv := opaque.Global()
	if srv == nil {
		return nil, status.Error(codes.FailedPrecondition, "OPAQUE server not initialized")
	}
	m3, err := b64dec(req.GetM3B64())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "decode m3_b64: %v", err)
	}
	envelope, err := srv.RegisterFinish(m3)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "opaque register finish: %v", err)
	}
	if _, err := hulabolt.PutOpaqueRecord(ctx, storage.Global(), hulabolt.StoredOpaqueRecord{
		Username: req.GetUsername(),
		Provider: req.GetProvider(),
		Envelope: envelope,
	}); err != nil {
		return nil, status.Errorf(codes.Internal, "persist opaque record: %v", err)
	}
	aLog.Infof("OPAQUE: registered %s|%s (envelope=%dB)", req.GetProvider(), req.GetUsername(), len(envelope))
	return &authspec.OpaqueRegisterFinishResponse{Ok: true}, nil
}

// OpaqueLoginInit — looks up the record + drives the AKE init step.
//
// Returns NotFound when the user has no OPAQUE record. There is no
// legacy fallback — operators bootstrap an admin password via the
// `set-admin-password.sh` script (which uses the noauth bootstrap
// path of OpaqueRegisterInit/Finish for admin) before any login
// will succeed.
func (s *Server) OpaqueLoginInit(ctx context.Context, req *authspec.OpaqueLoginInitRequest) (*authspec.OpaqueLoginInitResponse, error) {
	if req == nil || req.GetUsername() == "" {
		return nil, status.Error(codes.InvalidArgument, "username required")
	}
	if err := validProvider(req.GetProvider()); err != nil {
		return nil, err
	}
	srv := opaque.Global()
	if srv == nil {
		return nil, status.Error(codes.FailedPrecondition, "OPAQUE server not initialized")
	}
	rec, err := hulabolt.GetOpaqueRecord(ctx, storage.Global(), req.GetProvider(), req.GetUsername())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "load opaque record: %v", err)
	}
	if rec == nil {
		return nil, status.Error(codes.NotFound,
			"no OPAQUE record for this user — run set-admin-password "+
				"(or the deploy bootstrap script) first")
	}
	ke1, err := b64dec(req.GetKe1B64())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "decode ke1_b64: %v", err)
	}
	credID := opaque.CredentialID(req.GetProvider(), req.GetUsername())
	out, err := srv.LoginInit(req.GetProvider(), req.GetUsername(), credID, ke1, rec.Envelope)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "opaque login init: %v", err)
	}
	return &authspec.OpaqueLoginInitResponse{
		Ke2B64:    b64enc(out.KE2),
		SessionId: out.SessionID,
	}, nil
}

// OpaqueLoginFinish — verifies KE3, issues a JWT.
func (s *Server) OpaqueLoginFinish(ctx context.Context, req *authspec.OpaqueLoginFinishRequest) (*authspec.OpaqueLoginFinishResponse, error) {
	if req == nil || req.GetSessionId() == "" {
		return nil, status.Error(codes.InvalidArgument, "session_id required")
	}
	srv := opaque.Global()
	if srv == nil {
		return nil, status.Error(codes.FailedPrecondition, "OPAQUE server not initialized")
	}
	ke3, err := b64dec(req.GetKe3B64())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "decode ke3_b64: %v", err)
	}
	finish, err := srv.LoginFinish(req.GetSessionId(), ke3)
	if err != nil {
		// Both ErrInvalidLogin and ErrSessionNotFound surface as
		// "invalid credentials" client-side — don't leak the
		// distinction (password-vs-session) to the wire.
		return &authspec.OpaqueLoginFinishResponse{Error: "invalid credentials"}, nil
	}

	// Best-effort: bump LastSuccessLogin on the record.
	_ = hulabolt.MarkOpaqueLoginSuccess(ctx, storage.Global(), finish.Provider, finish.Username)

	// Issue JWT — same path as LoginAdmin / LoginWithSecret today.
	isAdmin := finish.Provider == providerAdmin
	jwt, err := model.NewJWTClaimsCommit(model.GetDB(), finish.Username, &model.LoginOpts{
		IsAdmin: isAdmin,
	})
	if err != nil {
		aLog.Errorf("OPAQUE LoginFinish: JWT issue: %v", err)
		return nil, status.Errorf(codes.Internal, "issue jwt: %v", err)
	}
	resp := &authspec.OpaqueLoginFinishResponse{}
	if isAdmin {
		resp.Admintoken = jwt
	} else {
		resp.Token = jwt
	}
	aLog.Infof("OPAQUE: login OK %s|%s", finish.Provider, finish.Username)
	return resp, nil
}
