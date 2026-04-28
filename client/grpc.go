package client

// gRPC client plumbing. Phase 0 stage 0.8 lands the dial logic and
// per-service stub cache; the individual gRPC method calls live in
// client/grpc_<service>.go.
//
// The existing HTTP methods on Client (forms.go, lander.go, etc.)
// continue to work against the unified server's /api/* legacy
// bridge. Callers can opt into the gRPC path by invoking DialGRPC()
// first, then calling any Grpc*-prefixed method.

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/url"
	"strconv"

	authspec "github.com/tlalocweb/hulation/pkg/apispec/v1/auth"
	badactorspec "github.com/tlalocweb/hulation/pkg/apispec/v1/badactor"
	formsspec "github.com/tlalocweb/hulation/pkg/apispec/v1/forms"
	landersspec "github.com/tlalocweb/hulation/pkg/apispec/v1/landers"
	sitespec "github.com/tlalocweb/hulation/pkg/apispec/v1/site"
	stagingspec "github.com/tlalocweb/hulation/pkg/apispec/v1/staging"
	statusspec "github.com/tlalocweb/hulation/pkg/apispec/v1/status"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

// grpcState holds the live gRPC connection and the generated
// service-stub clients. Lazily populated by DialGRPC.
type grpcState struct {
	conn     *grpc.ClientConn
	status   statusspec.StatusServiceClient
	auth     authspec.AuthServiceClient
	forms    formsspec.FormsServiceClient
	landers  landersspec.LandersServiceClient
	site     sitespec.SiteServiceClient
	staging  stagingspec.StagingServiceClient
	badactor badactorspec.BadActorServiceClient
}

// DialGRPC opens a gRPC connection to the configured apiUrl and caches
// stub clients for every hulation service. Safe to call multiple times
// — subsequent calls reuse the existing connection.
//
// TLS policy:
//   - https:// URL (default hula deployment) → use native TLS. Hostname
//     from the URL is the SNI name.
//   - http://  URL (local/test) → plaintext insecure transport.
//   - To skip server-cert verification set InsecureSkipTLSVerify=true
//     on the Client before calling.
func (c *Client) DialGRPC() error {
	if c.grpc != nil && c.grpc.conn != nil {
		return nil
	}
	u, err := url.Parse(c.apiUrl)
	if err != nil {
		return fmt.Errorf("parse apiUrl %q: %w", c.apiUrl, err)
	}
	host := u.Host
	if host == "" {
		// apiUrl was given as host:port without scheme — use it directly.
		host = c.apiUrl
	}
	// Ensure a port is present; gRPC Dial requires host:port.
	if _, _, splitErr := splitHostPort(host); splitErr != nil {
		// Default to 443 for https / otherwise 80.
		if u.Scheme == "https" || u.Scheme == "" {
			host = host + ":443"
		} else {
			host = host + ":80"
		}
	}

	var opts []grpc.DialOption
	switch u.Scheme {
	case "http":
		opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	default: // https or unset → TLS
		tlsCfg := &tls.Config{
			ServerName:         hostOnly(host),
			InsecureSkipVerify: c.InsecureSkipTLSVerify,
		}
		opts = append(opts, grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)))
	}

	conn, err := grpc.NewClient(host, opts...)
	if err != nil {
		return fmt.Errorf("grpc dial %s: %w", host, err)
	}
	c.grpc = &grpcState{
		conn:     conn,
		status:   statusspec.NewStatusServiceClient(conn),
		auth:     authspec.NewAuthServiceClient(conn),
		forms:    formsspec.NewFormsServiceClient(conn),
		landers:  landersspec.NewLandersServiceClient(conn),
		site:     sitespec.NewSiteServiceClient(conn),
		staging:  stagingspec.NewStagingServiceClient(conn),
		badactor: badactorspec.NewBadActorServiceClient(conn),
	}
	return nil
}

// CloseGRPC closes the gRPC connection (if open). Safe to call
// regardless of state.
func (c *Client) CloseGRPC() error {
	if c.grpc == nil || c.grpc.conn == nil {
		return nil
	}
	err := c.grpc.conn.Close()
	c.grpc = nil
	return err
}

// authCtx returns a context carrying the caller's bearer token as gRPC
// metadata. Used by every Grpc*-prefixed method that needs authn.
func (c *Client) authCtx(ctx context.Context) context.Context {
	if c.token == "" {
		return ctx
	}
	return metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+c.token)
}

// hostOnly strips the port from a host:port string.
func hostOnly(hp string) string {
	for i := len(hp) - 1; i >= 0; i-- {
		if hp[i] == ':' {
			return hp[:i]
		}
	}
	return hp
}

// splitHostPort reports whether a string is in host:port form by
// attempting to parse the port.
func splitHostPort(hp string) (host string, port int, err error) {
	for i := len(hp) - 1; i >= 0; i-- {
		if hp[i] == ':' {
			host = hp[:i]
			p, perr := strconv.Atoi(hp[i+1:])
			if perr != nil {
				return "", 0, perr
			}
			return host, p, nil
		}
	}
	return "", 0, fmt.Errorf("no port in %q", hp)
}
