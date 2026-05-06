package unified

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"strings"

	"google.golang.org/grpc"
)

// InternalServicePathPrefix is the gRPC route prefix that identifies
// "internal" service calls — those that ride on the unified HTTPS
// listener but are gated on a Team-CA-signed client cert. Every
// proto under pkg/apispec/v1/internalapi/ uses package
// hulation.v1.internalapi which produces this URL prefix.
const InternalServicePathPrefix = "/hulation.v1.internalapi."

// EnableInternalChannel turns on the second gRPC server that hosts
// internal team-only services (Membership, RaftTransport,
// StorageProxy, Relay, Gossip, ChatLookup). Once called, the unified
// listener:
//
//   - Requires a Team-CA-signed client cert when the ClientHello
//     SNI matches *.team.internal (HA_PLAN3 §4.1). Other SNIs keep
//     their existing handshake semantics.
//   - Presents `leaf` as the server cert on internal handshakes so
//     the joining peer can verify us against its copy of ca.
//   - Routes incoming HTTP/2 + application/grpc traffic on paths
//     starting with InternalServicePathPrefix to the internal
//     gRPC server.
//
// teamCAPool MUST contain only Team CA certs and nothing else; we
// use it as both ClientCAs (verifying peers) and the trust anchor
// for our own server-side handshake on internal SNIs.
//
// This is idempotent in the sense that a second call with the same
// args produces the same wire behaviour, but the embedded
// internalGRPC server is kept across calls — re-registering all
// services on a fresh server would surprise existing callers.
func (s *Server) EnableInternalChannel(teamCAPool *x509.CertPool, leaf *tls.Certificate, opts ...grpc.ServerOption) error {
	if s == nil {
		return fmt.Errorf("nil server")
	}
	if teamCAPool == nil {
		return fmt.Errorf("teamCAPool is required")
	}
	if leaf == nil {
		return fmt.Errorf("leaf cert is required")
	}
	if s.internalGRPC != nil {
		return fmt.Errorf("internal channel already enabled")
	}

	s.teamCAPool = teamCAPool
	s.teamLeaf = leaf
	s.internalGRPC = grpc.NewServer(opts...)

	if s.httpServer != nil && s.httpServer.TLSConfig != nil {
		base := s.httpServer.TLSConfig
		base.GetConfigForClient = s.getConfigForClient
	}
	s.logger.Infof("Internal mTLS gRPC channel enabled (SNI suffix: %s)", IsInternalSNISuffix)
	return nil
}

// IsInternalSNISuffix is the DNS suffix the unified listener treats
// as "internal traffic". Every Team-CA-signed cert carries a SAN
// ending with this suffix (see pkg/team/pki).
const IsInternalSNISuffix = ".team.internal"

// GetInternalGRPCServer returns the internal gRPC server for service
// registration. nil if EnableInternalChannel has not been called.
func (s *Server) GetInternalGRPCServer() *grpc.Server {
	return s.internalGRPC
}

// IsInternalSNI reports whether the ClientHello's ServerName looks
// like internal team traffic.
func IsInternalSNI(serverName string) bool {
	return strings.HasSuffix(strings.ToLower(serverName), IsInternalSNISuffix)
}

// getConfigForClient is the per-handshake TLS config switch.
// Internal SNI gets RequireAndVerifyClientCert + ClientCAs. Public
// SNI inherits the base config (which currently sets
// VerifyClientCertIfGiven for legacy izcragent compatibility).
func (s *Server) getConfigForClient(hello *tls.ClientHelloInfo) (*tls.Config, error) {
	if !IsInternalSNI(hello.ServerName) {
		return nil, nil // base config wins
	}

	clone := s.httpServer.TLSConfig.Clone()
	clone.ClientCAs = s.teamCAPool
	clone.ClientAuth = tls.RequireAndVerifyClientCert
	clone.GetCertificate = func(_ *tls.ClientHelloInfo) (*tls.Certificate, error) {
		return s.teamLeaf, nil
	}
	clone.GetConfigForClient = nil
	return clone, nil
}

// dispatchInternalGRPC returns true if the request was routed to
// the internal gRPC server. Called from grpcHandlerOrPassFunc when
// the request looks like gRPC; we route on path prefix because by
// the time we get here the TLS handshake has already vetted the
// client cert when applicable.
func (s *Server) dispatchInternalGRPC(rPath string) bool {
	return s.internalGRPC != nil && strings.HasPrefix(rPath, InternalServicePathPrefix)
}
