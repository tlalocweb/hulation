package internal

// Internal auth provider — formerly the SHA256+Argon2id legacy
// password path. As of the OPAQUE migration this provider performs
// no password verification of its own; all password-based
// authentication goes through the OPAQUE PAKE endpoints
// (/api/v1/auth/opaque/{register,login}/{init,finish}), which
// already handle both `admin` and `internal` user records. The
// provider stays registered under the "internal" provider type so
// existing config shapes don't break, but it's effectively a no-op
// shell — only LoginOIDC and ValidateToken remain on the interface,
// neither of which it implements.

import (
	"context"
	"fmt"
	"strings"

	"github.com/tlalocweb/hulation/pkg/server/authware/tokens"
	apiobjects "github.com/tlalocweb/hulation/pkg/apiobjects/v1"
	authspec "github.com/tlalocweb/hulation/pkg/apispec/v1/auth"
	baseprovider "github.com/tlalocweb/hulation/pkg/server/authware/provider/base"
)

// IzcrProvider implements local password authentication for internal users
type IzcrProvider struct {
	baseprovider.BaseProvider
	jwtFac *tokens.JWTFactory
}

func NewIzcrProvider(cfg *baseprovider.AuthProviderConfig) (*IzcrProvider, error) {
	if cfg == nil {
		return nil, fmt.Errorf("nil provider config")
	}
	if strings.TrimSpace(cfg.Name) == "" {
		cfg.Name = "Izcr"
	}
	return &IzcrProvider{
		BaseProvider: baseprovider.BaseProvider{Config: cfg},
	}, nil
}

func (p *IzcrProvider) Initialize(ctx context.Context) error {
	v := ctx.Value(baseprovider.CtxKeyJWTFactory)
	if fac, ok := v.(*tokens.JWTFactory); ok && fac != nil {
		p.jwtFac = fac
		return nil
	}
	return fmt.Errorf("jwt factory not provided")
}

func (p *IzcrProvider) Shutdown(ctx context.Context) error { return nil }
func (p *IzcrProvider) IsHealthy(ctx context.Context) bool { return true }

// LoginOIDC is not supported for local provider
func (p *IzcrProvider) LoginOIDC(ctx context.Context, req *authspec.LoginOIDCRequest) (resp *authspec.LoginOIDCResponse, err error) {
	return nil, fmt.Errorf("not implemented")
}

// ValidateToken: tokens for this provider are JWTs issued by the core JWTFactory and validated upstream
func (p *IzcrProvider) ValidateToken(token string) (user *apiobjects.User, valid bool, err error) {
	return nil, false, nil
}
