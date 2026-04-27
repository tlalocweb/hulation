// Package status implements the StatusService gRPC API. It's the
// smallest service Hula ships and serves as a smoke test for the
// unified server wiring.
package status

import (
	"context"
	"time"

	"github.com/tlalocweb/hulation/config"
	statusspec "github.com/tlalocweb/hulation/pkg/apispec/v1/status"
	"github.com/tlalocweb/hulation/pkg/server/authware"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Server implements statusspec.StatusServiceServer.
type Server struct {
	statusspec.UnimplementedStatusServiceServer
	startedAt time.Time
}

// New returns a StatusService implementation.
func New() *Server {
	return &Server{startedAt: time.Now()}
}

// Status returns basic liveness info. Any authenticated token is accepted;
// no specific permission required (enforced by authware middleware — no
// `(izuma.auth.permission)` annotation on this RPC).
func (s *Server) Status(ctx context.Context, req *statusspec.StatusRequest) (*statusspec.StatusResponse, error) {
	return &statusspec.StatusResponse{
		Ok:            true,
		Version:       config.Version,
		UptimeSeconds: int64(time.Since(s.startedAt).Seconds()),
	}, nil
}

// AuthOk reports the caller's identity. 401 if the token is invalid,
// enforced upstream by authware. When we get here the context carries
// Claims.
func (s *Server) AuthOk(ctx context.Context, req *statusspec.AuthOkRequest) (*statusspec.AuthOkResponse, error) {
	claims, ok := ctx.Value(authware.ClaimsKey).(*authware.Claims)
	if !ok || claims == nil {
		return nil, status.Error(codes.Unauthenticated, "no claims in context")
	}
	return &statusspec.AuthOkResponse{
		Ok:       true,
		UserId:   claims.Subject,
		Username: claims.Username,
	}, nil
}
