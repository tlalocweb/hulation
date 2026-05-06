package main

// hulactl team-* commands. All except team-init talk to a hula
// node's internal mTLS gRPC channel (HA_PLAN3 §3.2 / §3.3 / §12).
// The PKI bundle ships from `genteamcerts` — operator points
// --pki-dir at the per-node directory containing ca.pem, cert.pem,
// and key.pem.

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	internalspec "github.com/tlalocweb/hulation/pkg/apispec/v1/internalapi"
	"github.com/tlalocweb/hulation/pkg/team/pki"
)

const (
	teamRPCDialTimeout = 10 * time.Second
	teamRPCCallTimeout = 30 * time.Second
)

// runTeamInit prints a fresh team_id + bootstrap_token. Doesn't
// touch the network — it's a pure ceremony before the seed node
// boots.
func runTeamInit() {
	tok, err := pki.GenerateBootstrapToken()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	teamID := uuid.NewString()
	fmt.Printf("team_id:         %s\n", teamID)
	fmt.Printf("bootstrap_token: %s\n", base64.StdEncoding.EncodeToString(tok))
	fmt.Printf("\nNext steps:\n")
	fmt.Printf("  1. Set HULA_TEAM_BOOTSTRAP_TOKEN to the value above on every team node.\n")
	fmt.Printf("  2. Configure team.team_id: %s in each node's hula config.\n", teamID)
	fmt.Printf("  3. On the seed node only: team.bootstrap: first-of-team\n")
	fmt.Printf("  4. Generate per-node certs with: hulactl genteamcerts --team-id %s --nodes <id1>,<id2>,...\n", teamID)
}

// runTeamJoin dials the leader's MembershipService.Join. Run on the
// node that wants to join (hula must already be running locally so
// the joiner's Raft can pick up the new configuration immediately).
func runTeamJoin(cfg *HulactlConfig, argz []string) {
	if len(argz) < 2 {
		fmt.Fprintf(os.Stderr, "Error: leader-addr is required\nUsage: %s\n", CMD_TEAM_JOIN_USAGE)
		os.Exit(1)
	}
	leaderAddr := argz[1]
	if cfg.TeamToken == "" {
		fmt.Fprintf(os.Stderr, "Error: --token is required\nUsage: %s\n", CMD_TEAM_JOIN_USAGE)
		os.Exit(1)
	}

	// Validate base64 shape but ship the encoded form on the wire —
	// proto string fields require valid UTF-8 and a 32-byte random
	// secret almost certainly is not. The server decodes inside the
	// Join RPC.
	if _, err := base64.StdEncoding.DecodeString(cfg.TeamToken); err != nil {
		fmt.Fprintf(os.Stderr, "Error: --token is not valid base64: %v\n", err)
		os.Exit(1)
	}

	cli, conn, err := dialPeer(leaderAddr, cfg.TeamPKIDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()

	nodeID := cfg.TeamNodeID
	if nodeID == "" {
		if h, err := os.Hostname(); err == nil {
			nodeID = h
		}
	}
	teamID := teamIDFromBundle(cfg.TeamPKIDir)

	ctx, cancel := context.WithTimeout(context.Background(), teamRPCCallTimeout)
	defer cancel()

	resp, err := cli.Join(ctx, &internalspec.JoinRequest{
		TeamId:         teamID,
		NodeId:         nodeID,
		RaftAddr:       advertisedRaftAddr(cfg, leaderAddr),
		BootstrapToken: cfg.TeamToken,
		NodeHostname:   cfg.TeamNodeHostname,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Join failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Joined team. leader=%s addr=%s last_index=%d\n",
		resp.GetLeaderId(), resp.GetLeaderAddr(), resp.GetLastIndex())
}

func runTeamLeave(cfg *HulactlConfig, argz []string) {
	if len(argz) < 2 {
		fmt.Fprintf(os.Stderr, "Error: leader-addr is required\nUsage: %s\n", CMD_TEAM_LEAVE_USAGE)
		os.Exit(1)
	}
	leaderAddr := argz[1]

	nodeID := ""
	if len(argz) >= 3 {
		nodeID = argz[2]
	}
	if nodeID == "" {
		if h, err := os.Hostname(); err == nil {
			nodeID = h
		}
	}

	cli, conn, err := dialPeer(leaderAddr, cfg.TeamPKIDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), teamRPCCallTimeout)
	defer cancel()

	if _, err := cli.Leave(ctx, &internalspec.LeaveRequest{
		TeamId: teamIDFromBundle(cfg.TeamPKIDir),
		NodeId: nodeID,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "Leave failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Left team: %s\n", nodeID)
}

func runTeamStatus(cfg *HulactlConfig, argz []string) {
	if len(argz) < 2 {
		fmt.Fprintf(os.Stderr, "Error: node-addr is required\nUsage: %s\n", CMD_TEAM_STATUS_USAGE)
		os.Exit(1)
	}
	nodeAddr := argz[1]

	cli, conn, err := dialPeer(nodeAddr, cfg.TeamPKIDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), teamRPCCallTimeout)
	defer cancel()

	resp, err := cli.Status(ctx, &internalspec.StatusRequest{})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Status failed: %v\n", err)
		os.Exit(1)
	}

	if cfg.TeamCertsTeamID != "" {
		got := teamIDFromBundle(cfg.TeamPKIDir)
		if got != "" && got != cfg.TeamCertsTeamID {
			fmt.Fprintf(os.Stderr, "Error: --team-id %s does not match the cert bundle's team_id %s — wrong PKI\n",
				cfg.TeamCertsTeamID, got)
			os.Exit(1)
		}
	}

	fmt.Printf("leader:        %s (%s)\n", resp.GetLeaderId(), resp.GetLeaderAddr())
	fmt.Printf("last_index:    %d\n", resp.GetLastIndex())
	fmt.Printf("applied_index: %d\n", resp.GetAppliedIndex())
	fmt.Printf("has_quorum:    %t\n", resp.GetHasQuorum())
	fmt.Printf("\nmembers:\n")
	fmt.Printf("  %-20s %-30s %-10s %-3s %s\n", "NODE", "ADDR", "SUFFRAGE", "CH", "ROLE")
	for _, m := range resp.GetMembers() {
		role := "follower"
		if m.GetIsLeader() {
			role = "LEADER"
		}
		ch := "-"
		if m.GetChConnected() {
			ch = "yes"
		}
		fmt.Printf("  %-20s %-30s %-10s %-3s %s\n",
			m.GetNodeId(), m.GetRaftAddr(), m.GetSuffrage(), ch, role)
	}
}

// runTeamRotateToken: Stage-3 follow-up. The token rotation can't go
// through MembershipService directly (no Rotate RPC by design — the
// surface is intentionally narrow). For now this is a TODO that
// surfaces a clear error so the operator knows where to look.
func runTeamRotateToken(cfg *HulactlConfig, argz []string) {
	fmt.Fprintf(os.Stderr,
		"team-rotate-bootstrap-token requires the admin gRPC surface, which lands in 3.4b.\n"+
			"For now: stop the leader, run `hulactl --bolt <data.db> set-bootstrap-token`, restart.\n")
	os.Exit(2)
	_ = cfg
	_ = argz
}

// dialPeer opens an mTLS gRPC connection against a hula node's
// unified listener. The PKI bundle dir must contain ca.pem,
// cert.pem, and key.pem written by `hulactl genteamcerts`.
//
// Uses pki.PeerDialTLSConfig so the SNI fires the listener's
// internal-channel gate AND the chain verification works across
// every node-id (per-node SAN uniqueness would otherwise reject
// legitimate peers).
func dialPeer(addr, pkiDir string) (internalspec.MembershipServiceClient, *grpc.ClientConn, error) {
	if pkiDir == "" {
		return nil, nil, fmt.Errorf("--pki-dir is required (use the dir genteamcerts produced for this node)")
	}
	caPath := filepath.Join(pkiDir, "ca.pem")
	certPath := filepath.Join(pkiDir, "cert.pem")
	keyPath := filepath.Join(pkiDir, "key.pem")
	bundle, err := pki.LoadBundle(caPath, certPath, keyPath)
	if err != nil {
		return nil, nil, fmt.Errorf("load pki bundle: %w", err)
	}
	tlsCfg, err := pki.PeerDialTLSConfig(bundle)
	if err != nil {
		return nil, nil, fmt.Errorf("peer dial tls: %w", err)
	}

	dialCtx, cancel := context.WithTimeout(context.Background(), teamRPCDialTimeout)
	defer cancel()

	conn, err := grpc.DialContext(dialCtx, addr,
		grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)),
		grpc.WithBlock(),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("dial %s: %w", addr, err)
	}
	return internalspec.NewMembershipServiceClient(conn), conn, nil
}

// teamIDFromBundle reads <pki-dir>/../team-id (the file genteamcerts
// drops next to the per-node bundle dir). Empty string when the
// file is absent — Join still succeeds because the leader matches
// against its own configured team_id.
func teamIDFromBundle(pkiDir string) string {
	if pkiDir == "" {
		return ""
	}
	candidates := []string{
		filepath.Join(pkiDir, "team-id"),
		filepath.Join(filepath.Dir(pkiDir), "team-id"),
	}
	for _, c := range candidates {
		if b, err := os.ReadFile(c); err == nil {
			return string(b)
		}
	}
	return ""
}

// advertisedRaftAddr returns the raft_addr that we advertise to
// the leader. Today this is the same address peers will dial back
// when they reach us; surfaced via team.advertise_addr in config
// or — when unset — falls back to the leader-supplied loopback
// shape which is a clear bug surface.
//
// For Stage 3.4 we let the operator pass it via config; later
// stages will consult the live hula's /readyz to discover its own
// advertised address.
func advertisedRaftAddr(cfg *HulactlConfig, leaderAddr string) string {
	_ = leaderAddr // reserved
	if cfg.TeamNodeHostname != "" {
		return cfg.TeamNodeHostname + ":443"
	}
	if h, err := os.Hostname(); err == nil {
		return h + ":443"
	}
	return ""
}
