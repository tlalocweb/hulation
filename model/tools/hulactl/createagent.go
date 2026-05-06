package main

// hulactl create-agent — Phase 1, offline. Generates a one-shot Agent
// CA + leaf cert and prints an agent yaml to stdout. The Phase-2
// version will hit `POST /api/v1/agent/create` on a running hula and
// register the agent ID in the FSM-backed registry; this offline form
// is here so the Rust side can be exercised end-to-end before that
// server-side work lands.
//
// Two invocation forms:
//
//	hulactl create-agent --allow-build=gravhl --allow-staging-build=gravhl,OPT --expires-in=1y
//	hulactl create-agent -c agent-template.yaml
//
// See HULAAGENT_PLAN.md for the full schema and design rationale.

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/tlalocweb/hulation/pkg/agent/pki"
)

// agentYaml is the on-disk schema produced by create-agent and
// consumed by hula-agent. Keep in lock-step with hulaagent/src/config.rs.
type agentYaml struct {
	Agent agentBlock           `yaml:"agent"`
	Sites map[string]siteAllow `yaml:"sites"`
}

type agentBlock struct {
	ID       string     `yaml:"id"`
	HulaHost string     `yaml:"hula_host"`
	MTLS     mtlsBundle `yaml:"mTLS"`
}

type mtlsBundle struct {
	CA   string `yaml:"ca"`
	Cert string `yaml:"cert"`
	Key  string `yaml:"key"`
}

type siteAllow struct {
	Allow map[string]string `yaml:"allow"`
}

// agentTemplate is the on-disk schema accepted via `-c`.
type agentTemplate struct {
	Config struct {
		ExpiresIn string `yaml:"expires-in"`
	} `yaml:"config"`
	Sites map[string]siteAllow `yaml:"sites"`
}

// runCreateAgent dispatches the create-agent command. Args layout:
//
//	hulactl [-c <template>] create-agent --allow-build=site,opts ... --expires-in=DUR --hula-host=HOST
//
// Both forms produce yaml on stdout; on error we exit non-zero with
// a message on stderr.
func runCreateAgent(cfg *HulactlConfig, argz []string) {
	allowFlags, expiresIn, hulaHost, err := parseCreateAgentArgs(cfg, argz)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(2)
	}

	// Merge template (if any) with flag-form allow rules. Flag-form
	// wins on conflicts so an operator can layer overrides on a base
	// template.
	sites := map[string]siteAllow{}
	if cfg.AgentTemplatePath != "" {
		tmpl, err := loadAgentTemplate(cfg.AgentTemplatePath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: load template %s: %v\n", cfg.AgentTemplatePath, err)
			os.Exit(2)
		}
		if expiresIn == 0 && tmpl.Config.ExpiresIn != "" {
			d, err := parseHumanDuration(tmpl.Config.ExpiresIn)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: bad expires-in %q in template: %v\n", tmpl.Config.ExpiresIn, err)
				os.Exit(2)
			}
			expiresIn = d
		}
		for site, allow := range tmpl.Sites {
			sites[site] = allow
		}
	}
	for site, verbAllow := range allowFlags {
		s := sites[site]
		if s.Allow == nil {
			s.Allow = map[string]string{}
		}
		for verb, opts := range verbAllow {
			s.Allow[verb] = opts
		}
		sites[site] = s
	}
	if len(sites) == 0 {
		fmt.Fprintf(os.Stderr, "Error: at least one --allow-* flag or template site is required\n")
		os.Exit(2)
	}
	if expiresIn == 0 {
		expiresIn = pki.DefaultValidity
	}

	// Generate identity. Phase 1: one-off CA per invocation. Phase 2:
	// pull the persistent Agent CA from the running hula.
	id, err := newAgentID()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: agent id: %v\n", err)
		os.Exit(1)
	}
	ca, err := pki.NewAgentCA(expiresIn + 24*time.Hour) // CA outlives leaf by 1d so it's never the bottleneck
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: agent ca: %v\n", err)
		os.Exit(1)
	}
	leaf, err := pki.GenerateAgentCert(ca, id, expiresIn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: agent cert: %v\n", err)
		os.Exit(1)
	}

	out := agentYaml{
		Agent: agentBlock{
			ID:       id,
			HulaHost: hulaHost,
			MTLS: mtlsBundle{
				CA:   string(ca.CertPEM),
				Cert: string(leaf.CertPEM),
				Key:  string(leaf.KeyPEM),
			},
		},
		Sites: sites,
	}

	enc := yaml.NewEncoder(os.Stdout)
	enc.SetIndent(2)
	if err := enc.Encode(out); err != nil {
		fmt.Fprintf(os.Stderr, "Error: encoding yaml: %v\n", err)
		os.Exit(1)
	}
	enc.Close()
}

// parseCreateAgentArgs scans argz for --allow-<verb>=<site>[,<opts>]
// pairs and one-off flags (--expires-in, --hula-host, -c). Built-in
// flag.Parse can't handle --allow-* dynamically because the verbs are
// open-ended.
//
// Returns: allow[site][verb]=opts, expires-in duration (0 if unset),
// hula host (empty if unset).
func parseCreateAgentArgs(cfg *HulactlConfig, argz []string) (map[string]map[string]string, time.Duration, string, error) {
	allow := map[string]map[string]string{}
	var expiresIn time.Duration
	var hulaHost string

	for _, a := range argz[1:] { // skip the verb itself
		switch {
		case strings.HasPrefix(a, "--allow-"):
			rest := strings.TrimPrefix(a, "--allow-")
			eq := strings.Index(rest, "=")
			if eq <= 0 {
				return nil, 0, "", fmt.Errorf("malformed --allow flag %q (want --allow-<verb>=<site>[,<opts>])", a)
			}
			verb := rest[:eq]
			payload := rest[eq+1:]
			parts := strings.SplitN(payload, ",", 2)
			site := parts[0]
			var opts string
			if len(parts) == 2 {
				opts = parts[1]
			}
			if site == "" {
				return nil, 0, "", fmt.Errorf("--allow-%s missing site", verb)
			}
			if allow[site] == nil {
				allow[site] = map[string]string{}
			}
			allow[site][verb] = opts
		case strings.HasPrefix(a, "--expires-in="):
			d, err := parseHumanDuration(strings.TrimPrefix(a, "--expires-in="))
			if err != nil {
				return nil, 0, "", fmt.Errorf("--expires-in: %w", err)
			}
			expiresIn = d
		case strings.HasPrefix(a, "--hula-host="):
			hulaHost = strings.TrimPrefix(a, "--hula-host=")
		default:
			// Ignore other args — the caller's HulactlConfig flags
			// (e.g. -c) are parsed earlier by the global flag pass.
		}
	}

	if hulaHost == "" {
		// Falls back to the resolved server URL for the default profile.
		hulaHost = cfg.Host
	}
	return allow, expiresIn, hulaHost, nil
}

// loadAgentTemplate parses a -c template file. We tolerate empty
// allow strings (the design defaults them to "" anyway).
func loadAgentTemplate(path string) (*agentTemplate, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var t agentTemplate
	if err := yaml.Unmarshal(b, &t); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	return &t, nil
}

// parseHumanDuration accepts "1yr", "30d", "12h", or a Go duration.
// Wraps time.ParseDuration with the year/day extensions because
// 8760h is awkward to type.
func parseHumanDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty duration")
	}
	// Strip "yr" / "y" / "d" suffixes and convert to hours since
	// Go's time.ParseDuration doesn't know either unit.
	switch {
	case strings.HasSuffix(s, "yr"):
		n, err := atoi(strings.TrimSuffix(s, "yr"))
		if err != nil {
			return 0, err
		}
		return time.Duration(n) * 365 * 24 * time.Hour, nil
	case strings.HasSuffix(s, "y"):
		n, err := atoi(strings.TrimSuffix(s, "y"))
		if err != nil {
			return 0, err
		}
		return time.Duration(n) * 365 * 24 * time.Hour, nil
	case strings.HasSuffix(s, "d"):
		n, err := atoi(strings.TrimSuffix(s, "d"))
		if err != nil {
			return 0, err
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	return time.ParseDuration(s)
}

func atoi(s string) (int, error) {
	var n int
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("not an integer: %q", s)
		}
		n = n*10 + int(r-'0')
	}
	return n, nil
}

// newAgentID generates a 16-byte URL-safe random ID. Wide enough that
// guessing/collision are non-issues; short enough to fit comfortably
// in a CN.
func newAgentID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// sortedSites returns site keys in stable order for golden-file
// friendly yaml output. Used by tests that compare emitted yaml byte
// for byte.
func sortedSites(m map[string]siteAllow) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// suppress unused-import lint when sortedSites isn't called yet.
var _ = sortedSites
