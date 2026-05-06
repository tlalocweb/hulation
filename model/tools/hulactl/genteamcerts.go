package main

import (
	"encoding/base64"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/tlalocweb/hulation/pkg/team/pki"
)

// runGenTeamCerts is the offline ceremony described in HA_PLAN3 §3.1.
// We never talk to a running hula — this is operator scaffolding that
// lives entirely on their workstation.
func runGenTeamCerts(cfg *HulactlConfig) {
	if strings.TrimSpace(cfg.TeamCertsNodes) == "" {
		fmt.Fprintf(os.Stderr, "Error: --nodes is required (comma-separated node ids)\n")
		fmt.Fprintf(os.Stderr, "Usage: %s\n", CMD_GENTEAMCERTS_USAGE)
		os.Exit(1)
	}

	nodeIDs := splitCSV(cfg.TeamCertsNodes)
	if len(nodeIDs) == 0 {
		fmt.Fprintf(os.Stderr, "Error: --nodes parsed to an empty list\n")
		os.Exit(1)
	}

	teamID := strings.TrimSpace(cfg.TeamCertsTeamID)
	if teamID == "" {
		teamID = uuid.NewString()
	}

	validity, err := time.ParseDuration(cfg.TeamCertsValidity)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: --validity %q is not a valid Go duration: %v\n", cfg.TeamCertsValidity, err)
		os.Exit(1)
	}

	out := cfg.TeamCertsOut
	if out == "" {
		out = "./team-bundles"
	}

	ca, err := pki.GenerateCA(teamID, validity)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error generating CA: %v\n", err)
		os.Exit(1)
	}

	nodes := make([]*pki.NodeCert, 0, len(nodeIDs))
	for _, id := range nodeIDs {
		n, err := pki.GenerateNodeCert(ca, teamID, id, "", validity)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error generating node cert for %s: %v\n", id, err)
			os.Exit(1)
		}
		nodes = append(nodes, n)
	}

	tok, err := pki.GenerateBootstrapToken()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error generating bootstrap token: %v\n", err)
		os.Exit(1)
	}
	tokB64 := []byte(base64.StdEncoding.EncodeToString(tok))

	if err := pki.WriteBundle(out, teamID, ca, nodes, tokB64); err != nil {
		fmt.Fprintf(os.Stderr, "Error writing bundle: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Team CA + per-node bundle written to %s\n\n", out)
	fmt.Printf("  team_id:        %s\n", teamID)
	fmt.Printf("  validity:       %s\n", validity)
	fmt.Printf("  nodes:          %s\n", strings.Join(nodeIDs, ", "))
	fmt.Printf("  bootstrap_token (base64): %s\n\n", string(tokB64))
	fmt.Printf("Operator next steps:\n")
	fmt.Printf("  1. Move %s/ca.key into your secrets vault. NEVER deploy it to a node.\n", out)
	fmt.Printf("  2. Distribute %s/<node-id>/ to the matching node (cert.pem, key.pem, ca.pem).\n", out)
	fmt.Printf("  3. Set HULA_TEAM_BOOTSTRAP_TOKEN=%s on every node before first boot.\n", string(tokB64))
	fmt.Printf("  4. Configure team.team_id, team.node_id, team.pki.{ca_cert,node_cert,node_key} on each node.\n")
}

func splitCSV(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
