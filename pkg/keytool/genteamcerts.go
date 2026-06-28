package keytool

import (
	"encoding/base64"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/tlalocweb/hulation/pkg/team/pki"
)

// TeamCertsResult summarises a completed genteamcerts ceremony.
type TeamCertsResult struct {
	TeamID       string
	Validity     time.Duration
	Nodes        []string
	BootstrapB64 string
	OutDir       string
}

// GenTeamCerts runs the offline team-PKI ceremony (HA_PLAN3 §3.1): a Team CA +
// per-node mTLS bundles + a bootstrap token, written under outDir. teamID is
// generated when empty; validityStr is a Go duration; outDir defaults to
// ./team-bundles. It never contacts a running hula — pure operator scaffolding.
//
// Moved out of hulactl so the `hula genteamcerts` subcommand can call it; the
// cert primitives live in pkg/team/pki.
func GenTeamCerts(nodeIDs []string, teamID, validityStr, outDir string) (TeamCertsResult, error) {
	if len(nodeIDs) == 0 {
		return TeamCertsResult{}, fmt.Errorf("at least one node id is required (--nodes)")
	}
	if teamID == "" {
		teamID = uuid.NewString()
	}
	validity, err := time.ParseDuration(validityStr)
	if err != nil {
		return TeamCertsResult{}, fmt.Errorf("invalid --validity %q: %w", validityStr, err)
	}
	if outDir == "" {
		outDir = "./team-bundles"
	}

	ca, err := pki.GenerateCA(teamID, validity)
	if err != nil {
		return TeamCertsResult{}, fmt.Errorf("generate CA: %w", err)
	}
	nodes := make([]*pki.NodeCert, 0, len(nodeIDs))
	for _, id := range nodeIDs {
		n, err := pki.GenerateNodeCert(ca, teamID, id, "", validity)
		if err != nil {
			return TeamCertsResult{}, fmt.Errorf("generate node cert %s: %w", id, err)
		}
		nodes = append(nodes, n)
	}
	tok, err := pki.GenerateBootstrapToken()
	if err != nil {
		return TeamCertsResult{}, fmt.Errorf("generate bootstrap token: %w", err)
	}
	tokB64 := base64.StdEncoding.EncodeToString(tok)
	if err := pki.WriteBundle(outDir, teamID, ca, nodes, []byte(tokB64)); err != nil {
		return TeamCertsResult{}, fmt.Errorf("write bundle: %w", err)
	}

	return TeamCertsResult{
		TeamID:       teamID,
		Validity:     validity,
		Nodes:        nodeIDs,
		BootstrapB64: tokB64,
		OutDir:       outDir,
	}, nil
}
