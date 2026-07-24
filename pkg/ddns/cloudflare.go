package ddns

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// cloudflareAPIBase is the production Cloudflare v4 API root. Tests override it
// via WithAPIBase to point at an httptest server.
const cloudflareAPIBase = "https://api.cloudflare.com/client/v4"

// CloudflareProvider upserts A/AAAA records via the Cloudflare v4 API using
// bearer-token auth (same scheme as config.CloudflareOriginCAConfig). Zone ids
// left empty on a Record are auto-resolved from the record name (longest-suffix
// match against the token's visible zones) and memoised.
type CloudflareProvider struct {
	httpClient *http.Client
	apiBase    string

	mu        sync.Mutex
	zoneCache map[string]string // key: apiToken + "\x00" + recordName → zoneID
}

// CFOption configures a CloudflareProvider.
type CFOption func(*CloudflareProvider)

// WithAPIBase overrides the Cloudflare API root (for tests).
func WithAPIBase(base string) CFOption {
	return func(p *CloudflareProvider) { p.apiBase = strings.TrimRight(base, "/") }
}

// WithHTTPClient overrides the HTTP client (for tests / custom timeouts).
func WithHTTPClient(c *http.Client) CFOption {
	return func(p *CloudflareProvider) { p.httpClient = c }
}

// NewCloudflareProvider builds a provider with a 15s-timeout client by default.
func NewCloudflareProvider(opts ...CFOption) *CloudflareProvider {
	p := &CloudflareProvider{
		httpClient: &http.Client{Timeout: 15 * time.Second},
		apiBase:    cloudflareAPIBase,
		zoneCache:  make(map[string]string),
	}
	for _, o := range opts {
		o(p)
	}
	if p.zoneCache == nil {
		p.zoneCache = make(map[string]string)
	}
	return p
}

// --- Cloudflare API envelope (mirrors config/cloudflare_origin_ca.go) ---

type cfAPIResponse struct {
	Success bool            `json:"success"`
	Errors  []cfAPIError    `json:"errors"`
	Result  json.RawMessage `json:"result"`
}

type cfAPIError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e cfAPIError) String() string { return fmt.Sprintf("[%d] %s", e.Code, e.Message) }

type cfZone struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type cfDNSRecord struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	Proxied bool   `json:"proxied"`
	TTL     int    `json:"ttl"`
}

type cfDNSRecordPayload struct {
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	Proxied bool   `json:"proxied"`
	TTL     int    `json:"ttl"`
}

// EnsureRecord implements Provider.
func (p *CloudflareProvider) EnsureRecord(ctx context.Context, rec Record) error {
	if rec.APIToken == "" {
		return fmt.Errorf("cloudflare: no api token for %s", rec.Name)
	}
	if rec.Type != "A" && rec.Type != "AAAA" {
		return fmt.Errorf("cloudflare: unsupported record type %q", rec.Type)
	}

	zoneID := rec.ZoneID
	if zoneID == "" {
		z, err := p.resolveZone(ctx, rec.APIToken, rec.Name)
		if err != nil {
			return err
		}
		zoneID = z
	}

	existing, err := p.getRecord(ctx, rec.APIToken, zoneID, rec.Type, rec.Name)
	if err != nil {
		return err
	}
	if existing == nil {
		return p.createRecord(ctx, rec.APIToken, zoneID, rec)
	}
	// No-op when nothing changed — this keeps a steady IP from generating churn.
	if existing.Content == rec.Content && existing.Proxied == rec.Proxied && existing.TTL == rec.TTL {
		return nil
	}
	return p.updateRecord(ctx, rec.APIToken, zoneID, existing.ID, rec)
}

// resolveZone finds the zone whose name is the longest suffix of recordName,
// memoising the result per (token, recordName).
func (p *CloudflareProvider) resolveZone(ctx context.Context, token, recordName string) (string, error) {
	cacheKey := token + "\x00" + recordName
	p.mu.Lock()
	if z, ok := p.zoneCache[cacheKey]; ok {
		p.mu.Unlock()
		return z, nil
	}
	p.mu.Unlock()

	var zones []cfZone
	if err := p.do(ctx, token, http.MethodGet, "/zones?per_page=50", nil, &zones); err != nil {
		return "", fmt.Errorf("cloudflare: list zones for %s: %w", recordName, err)
	}
	best, ok := pickZone(recordName, zones)
	if !ok {
		return "", fmt.Errorf("cloudflare: no zone matches %s (token sees %d zones)", recordName, len(zones))
	}
	p.mu.Lock()
	p.zoneCache[cacheKey] = best.ID
	p.mu.Unlock()
	return best.ID, nil
}

// pickZone returns the zone whose Name is the longest suffix of recordName.
func pickZone(recordName string, zones []cfZone) (cfZone, bool) {
	var best cfZone
	found := false
	for _, z := range zones {
		if z.Name == "" {
			continue
		}
		if recordName == z.Name || strings.HasSuffix(recordName, "."+z.Name) {
			if !found || len(z.Name) > len(best.Name) {
				best = z
				found = true
			}
		}
	}
	return best, found
}

// getRecord returns the existing record matching type+name, or nil if none.
func (p *CloudflareProvider) getRecord(ctx context.Context, token, zoneID, typ, name string) (*cfDNSRecord, error) {
	path := fmt.Sprintf("/zones/%s/dns_records?type=%s&name=%s", url.PathEscape(zoneID), url.QueryEscape(typ), url.QueryEscape(name))
	var recs []cfDNSRecord
	if err := p.do(ctx, token, http.MethodGet, path, nil, &recs); err != nil {
		return nil, fmt.Errorf("cloudflare: get %s %s: %w", typ, name, err)
	}
	if len(recs) == 0 {
		return nil, nil
	}
	r := recs[0]
	return &r, nil
}

func (p *CloudflareProvider) createRecord(ctx context.Context, token, zoneID string, rec Record) error {
	path := fmt.Sprintf("/zones/%s/dns_records", url.PathEscape(zoneID))
	payload := cfDNSRecordPayload{Type: rec.Type, Name: rec.Name, Content: rec.Content, Proxied: rec.Proxied, TTL: rec.TTL}
	if err := p.do(ctx, token, http.MethodPost, path, payload, nil); err != nil {
		return fmt.Errorf("cloudflare: create %s %s: %w", rec.Type, rec.Name, err)
	}
	return nil
}

func (p *CloudflareProvider) updateRecord(ctx context.Context, token, zoneID, recordID string, rec Record) error {
	path := fmt.Sprintf("/zones/%s/dns_records/%s", url.PathEscape(zoneID), url.PathEscape(recordID))
	payload := cfDNSRecordPayload{Type: rec.Type, Name: rec.Name, Content: rec.Content, Proxied: rec.Proxied, TTL: rec.TTL}
	if err := p.do(ctx, token, http.MethodPatch, path, payload, nil); err != nil {
		return fmt.Errorf("cloudflare: update %s %s: %w", rec.Type, rec.Name, err)
	}
	return nil
}

// do performs a Cloudflare API call with bearer auth and unmarshals the
// envelope's Result into out (when non-nil).
func (p *CloudflareProvider) do(ctx context.Context, token, method, path string, body, out interface{}) error {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal request: %w", err)
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, p.apiBase+path, reader)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("api request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}

	var apiResp cfAPIResponse
	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		snip := respBody
		if len(snip) > 200 {
			snip = snip[:200]
		}
		return fmt.Errorf("parse response (status %d): %w (body: %s)", resp.StatusCode, err, string(snip))
	}
	if !apiResp.Success {
		msgs := make([]string, len(apiResp.Errors))
		for i, e := range apiResp.Errors {
			msgs[i] = e.String()
		}
		return fmt.Errorf("api errors (status %d): %s", resp.StatusCode, strings.Join(msgs, "; "))
	}
	if out != nil && len(apiResp.Result) > 0 {
		if err := json.Unmarshal(apiResp.Result, out); err != nil {
			return fmt.Errorf("parse result: %w", err)
		}
	}
	return nil
}
