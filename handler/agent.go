package handler

// Phase-2 hulaagent server-side endpoint. Today this is just
// CreateAgent — Phase 6 adds list/revoke alongside.
//
// The persistent Agent CA is bootstrapped once at server boot by
// server/agent_boot.go and stashed via SetAgentCA. Handlers below
// pull it from there; tests inject a freshly-generated CA the same
// way.

import (
	"context"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	hulation "github.com/tlalocweb/hulation/app"
	agentpki "github.com/tlalocweb/hulation/pkg/agent/pki"
	"github.com/tlalocweb/hulation/pkg/agent/registry"
	"github.com/tlalocweb/hulation/pkg/store/storage"
)

// Package-global Agent CA. Set by server/agent_boot.SetAgentCA after
// LoadOrCreateCA finishes; read inside CreateAgent. Avoids plumbing
// the CA through every hula RPC for a single handler that needs it.
var (
	agentCAMu sync.RWMutex
	agentCA   *agentpki.CA
)

// SetAgentCA installs the process-wide Agent CA. Idempotent for the
// same instance.
func SetAgentCA(ca *agentpki.CA) {
	agentCAMu.Lock()
	agentCA = ca
	agentCAMu.Unlock()
}

// GetAgentCA returns the process-wide Agent CA or nil if boot
// hasn't installed one yet.
func GetAgentCA() *agentpki.CA {
	agentCAMu.RLock()
	defer agentCAMu.RUnlock()
	return agentCA
}

// CreateAgentRequest is the JSON body posted to /api/agent/create
// by hulactl. ExpiresInSeconds carries the operator-picked validity;
// 0 means "use server default" (1y).
type CreateAgentRequest struct {
	ExpiresInSeconds int64                              `json:"expires_in_seconds,omitempty"`
	Sites            map[string]CreateAgentSiteRequest  `json:"sites"`
	HulaHost         string                             `json:"hula_host,omitempty"`
}

// CreateAgentSiteRequest carries the per-site allow-map.
type CreateAgentSiteRequest struct {
	Allow map[string]string `json:"allow"`
}

// CreateAgentResponse carries the rendered yaml + the issued ID.
// The yaml is a plain string so hulactl writes it to stdout
// verbatim — the operator typically pipes it to a file.
type CreateAgentResponse struct {
	AgentID string `json:"agent_id"`
	Yaml    string `json:"yaml"`
}

// agentYamlSchema mirrors the on-disk schema documented in
// HULAAGENT_PLAN.md and the offline-mode emitter in
// model/tools/hulactl/createagent.go.
type agentYamlSchema struct {
	Agent agentYamlBlock           `yaml:"agent"`
	Sites map[string]agentYamlSite `yaml:"sites"`
}

type agentYamlBlock struct {
	ID       string        `yaml:"id"`
	HulaHost string        `yaml:"hula_host"`
	MTLS     agentYamlMTLS `yaml:"mTLS"`
}

type agentYamlMTLS struct {
	CA   string `yaml:"ca"`
	Cert string `yaml:"cert"`
	Key  string `yaml:"key"`
}

type agentYamlSite struct {
	Allow map[string]string `yaml:"allow"`
}

// CreateAgent handles POST /api/agent/create. Admin-only.
//
// Pipeline:
//  1. Decode + validate the request body.
//  2. Mint a fresh agent ID.
//  3. Sign a leaf cert under the persistent Agent CA.
//  4. Write the registry record (canonical + fingerprint index).
//  5. Render the agent yaml and return it in the response.
//
// On post-cert-mint failure we DO NOT roll back the cert (the key
// is gone with the response anyway), but we DO surface the failure
// so the operator knows the agent isn't usable.
func CreateAgent(ctx RequestCtx) error {
	var req CreateAgentRequest
	body := ctx.Body()
	if len(body) == 0 {
		return ctx.Status(http.StatusBadRequest).SendString("request body is required")
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return ctx.Status(http.StatusBadRequest).SendString("bad request: " + err.Error())
	}
	if len(req.Sites) == 0 {
		return ctx.Status(http.StatusBadRequest).SendString("at least one site with allow rules is required")
	}

	ca := GetAgentCA()
	if ca == nil {
		return ctx.Status(http.StatusServiceUnavailable).SendString("agent CA not initialized — server boot incomplete")
	}
	store := storage.Global()
	if store == nil {
		return ctx.Status(http.StatusServiceUnavailable).SendString("storage not initialized")
	}

	validity := time.Duration(req.ExpiresInSeconds) * time.Second
	if validity <= 0 {
		validity = agentpki.DefaultValidity
	}

	id, err := newAgentID()
	if err != nil {
		return ctx.Status(http.StatusInternalServerError).SendString("agent id: " + err.Error())
	}
	leaf, err := agentpki.GenerateAgentCert(ca, id, validity)
	if err != nil {
		return ctx.Status(http.StatusInternalServerError).SendString("agent cert: " + err.Error())
	}

	// Permissions: deep-copy the request's allow maps so subsequent
	// mutations of the request struct can't affect the persisted
	// record. (The request struct is short-lived so this is mostly
	// belt-and-braces, but registry.Record gets serialised to JSON
	// and shared aliases would be a future-bug magnet.)
	now := time.Now().UTC()
	perms := make(map[string]map[string]string, len(req.Sites))
	for site, s := range req.Sites {
		clone := make(map[string]string, len(s.Allow))
		for verb, opts := range s.Allow {
			clone[verb] = opts
		}
		perms[site] = clone
	}

	// Re-parse the freshly-generated leaf so the registry's
	// fingerprint matches what the mTLS verification middleware
	// will compute on incoming connections.
	leafCert, err := parseCertPEM(leaf.CertPEM)
	if err != nil {
		return ctx.Status(http.StatusInternalServerError).SendString("parse leaf cert: " + err.Error())
	}
	rec := &registry.Record{
		ID:          id,
		Permissions: perms,
		CertSHA256:  registry.FingerprintFromCert(leafCert),
		CreatedAt:   now,
		ExpiresAt:   now.Add(validity),
	}
	storeCtx, cancel := context.WithTimeout(ctx.Context(), 5*time.Second)
	defer cancel()
	if err := registry.Put(storeCtx, store, rec); err != nil {
		return ctx.Status(http.StatusInternalServerError).SendString("registry put: " + err.Error())
	}

	hulaHost := req.HulaHost
	if hulaHost == "" {
		hulaHost = defaultAgentHulaHost()
	}

	out := agentYamlSchema{
		Agent: agentYamlBlock{
			ID:       id,
			HulaHost: hulaHost,
			MTLS: agentYamlMTLS{
				CA:   string(ca.CertPEM),
				Cert: string(leaf.CertPEM),
				Key:  string(leaf.KeyPEM),
			},
		},
		Sites: make(map[string]agentYamlSite, len(perms)),
	}
	for site, allow := range perms {
		out.Sites[site] = agentYamlSite{Allow: allow}
	}
	yamlBytes, err := marshalAgentYaml(&out)
	if err != nil {
		return ctx.Status(http.StatusInternalServerError).SendString("marshal yaml: " + err.Error())
	}

	return ctx.SendJSON(CreateAgentResponse{
		AgentID: id,
		Yaml:    string(yamlBytes),
	})
}

// marshalAgentYaml emits stable-key-ordered yaml so two invocations
// with the same logical input produce byte-identical output. Helps
// e2e golden-file checks and operator diffs.
func marshalAgentYaml(out *agentYamlSchema) ([]byte, error) {
	// yaml.v3 doesn't sort map keys; build a *yaml.Node tree by
	// hand so the top-level (`agent:` then `sites:`) AND the
	// per-site map keys are in deterministic order.
	siteNames := make([]string, 0, len(out.Sites))
	for k := range out.Sites {
		siteNames = append(siteNames, k)
	}
	sort.Strings(siteNames)

	var sb strings.Builder
	enc := yaml.NewEncoder(&sb)
	enc.SetIndent(2)

	root := &yaml.Node{Kind: yaml.MappingNode}

	agentNode := &yaml.Node{}
	if err := agentNode.Encode(out.Agent); err != nil {
		return nil, fmt.Errorf("encode agent: %w", err)
	}
	root.Content = append(root.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Value: "agent"},
		agentNode,
	)

	sitesNode := &yaml.Node{Kind: yaml.MappingNode}
	for _, name := range siteNames {
		siteNode := &yaml.Node{}
		if err := siteNode.Encode(out.Sites[name]); err != nil {
			return nil, fmt.Errorf("encode site %s: %w", name, err)
		}
		sitesNode.Content = append(sitesNode.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Value: name},
			siteNode,
		)
	}
	root.Content = append(root.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Value: "sites"},
		sitesNode,
	)

	if err := enc.Encode(root); err != nil {
		return nil, fmt.Errorf("encode yaml: %w", err)
	}
	enc.Close()
	return []byte(sb.String()), nil
}

// parseCertPEM decodes a PEM-encoded certificate to its x509 form.
// Used to compute the registry fingerprint from freshly-generated
// PEM bytes.
func parseCertPEM(p []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(p)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("expected CERTIFICATE pem block")
	}
	return x509.ParseCertificate(block.Bytes)
}

// defaultAgentHulaHost falls back to the first configured server's
// public host. Returns "" when no server is configured, in which
// case the operator can override via request.HulaHost.
func defaultAgentHulaHost() string {
	cfg := hulation.GetConfig()
	if cfg == nil {
		return ""
	}
	for _, s := range cfg.Servers {
		if s.Host != "" {
			return s.Host + ":443"
		}
	}
	return ""
}

// newAgentID mints a 16-byte URL-safe random ID. Same shape as the
// offline-mode hulactl create-agent so an e2e harness flipping
// between the two paths sees consistent IDs.
func newAgentID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
