package server

// CSV export + rate limiting for the analytics surface.
//
// Both features bolt on at the HTTP middleware layer rather than at
// gRPC. CSV needs to inspect the grpc-gateway JSON response and
// reshape it; rate limiting needs per-caller tracking keyed on the
// Phase-0 authware claims we already populate in request context.

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/tlalocweb/hulation/log"
	"github.com/tlalocweb/hulation/pkg/server/authware"
	"golang.org/x/time/rate"
)

// analyticsHTTPMiddleware wraps every /api/v1/analytics/* request. The
// middleware does three things in order:
//
//  1. Rate-limits per authware.Claims.Username (10 qps burst, 30/min
//     sustained). Over-quota callers get 429 with Retry-After.
//  2. If the request carries ?format=csv, captures the gateway's JSON
//     response, translates into CSV with a header row plus one row
//     per leaf array, and rewrites the response.
//  3. Everything else passes through unchanged.
//
// Attach via srv.AttachHTTPMiddleware before or after the admin
// authware middleware — order is independent because this middleware
// skips non-analytics paths.
func analyticsHTTPMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/v1/analytics/") {
			next.ServeHTTP(w, r)
			return
		}
		// Rate limit first — reject before doing any work.
		if !analyticsLimiterAllow(r.Context()) {
			w.Header().Set("Retry-After", "2")
			http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
			return
		}
		if r.URL.Query().Get("format") != "csv" {
			next.ServeHTTP(w, r)
			return
		}
		// Capture the response, convert JSON → CSV, rewrite.
		rec := &csvRecorder{ResponseWriter: w, buf: &bytes.Buffer{}}
		next.ServeHTTP(rec, r)
		if rec.status == 0 {
			rec.status = http.StatusOK
		}
		if rec.status != http.StatusOK {
			// Passthrough on error responses — the admin should see the
			// JSON error body, not a mangled CSV.
			w.WriteHeader(rec.status)
			_, _ = w.Write(rec.buf.Bytes())
			return
		}
		csvBytes, err := jsonToCSV(rec.buf.Bytes())
		if err != nil {
			log.Warnf("analytics CSV encode failed: %s; falling back to JSON", err.Error())
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(rec.buf.Bytes())
			return
		}
		filename := csvFilename(r.URL.Path)
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", "attachment; filename=\""+filename+"\"")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(csvBytes)
	})
}

// csvRecorder buffers the upstream response so we can transform it.
type csvRecorder struct {
	http.ResponseWriter
	buf    *bytes.Buffer
	status int
}

func (r *csvRecorder) WriteHeader(code int) { r.status = code }
func (r *csvRecorder) Write(p []byte) (int, error) {
	return r.buf.Write(p)
}

// jsonToCSV translates a grpc-gateway JSON response into CSV. It looks
// for the first array value in the top-level object (or any nested
// object for Devices, which has three parallel arrays) and emits one
// CSV row per element. Non-array scalar fields become a single-row
// "key,value" CSV (used by Summary, which has no arrays).
func jsonToCSV(raw []byte) ([]byte, error) {
	var top map[string]any
	if err := json.Unmarshal(raw, &top); err != nil {
		return nil, fmt.Errorf("parse json: %w", err)
	}
	// Look for the first array — for most reports it's "rows",
	// "buckets", or "visitors". Devices is handled below.
	var arrayKey string
	for _, k := range []string{"rows", "buckets", "visitors", "recent", "top_pages", "top_sources"} {
		if arr, ok := top[k].([]any); ok && len(arr) >= 0 {
			arrayKey = k
			_ = arr
			break
		}
	}
	if arrayKey == "" {
		// Devices: three arrays. Concatenate with a "kind" column.
		if dc, ok := top["device_category"].([]any); ok {
			br, _ := top["browser"].([]any)
			os, _ := top["os"].([]any)
			return devicesCSV(dc, br, os)
		}
		// Scalar response (Summary). Emit key-value CSV.
		return scalarCSV(top)
	}
	arr, _ := top[arrayKey].([]any)
	return arrayCSV(arr)
}

func arrayCSV(arr []any) ([]byte, error) {
	if len(arr) == 0 {
		return []byte{}, nil
	}
	first, ok := arr[0].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("array element not an object")
	}
	headers := sortedKeys(first)
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	_ = w.Write(headers)
	for _, row := range arr {
		obj, _ := row.(map[string]any)
		vals := make([]string, 0, len(headers))
		for _, h := range headers {
			vals = append(vals, fmt.Sprintf("%v", obj[h]))
		}
		_ = w.Write(vals)
	}
	w.Flush()
	return buf.Bytes(), w.Error()
}

func devicesCSV(dc, br, os []any) ([]byte, error) {
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	_ = w.Write([]string{"kind", "key", "visitors", "pageviews"})
	for _, kind := range []struct {
		name string
		arr  []any
	}{
		{"device_category", dc},
		{"browser", br},
		{"os", os},
	} {
		for _, row := range kind.arr {
			obj, _ := row.(map[string]any)
			_ = w.Write([]string{
				kind.name,
				fmt.Sprintf("%v", obj["key"]),
				fmt.Sprintf("%v", obj["visitors"]),
				fmt.Sprintf("%v", obj["pageviews"]),
			})
		}
	}
	w.Flush()
	return buf.Bytes(), w.Error()
}

func scalarCSV(top map[string]any) ([]byte, error) {
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	_ = w.Write([]string{"key", "value"})
	for _, k := range sortedKeys(top) {
		_ = w.Write([]string{k, fmt.Sprintf("%v", top[k])})
	}
	w.Flush()
	return buf.Bytes(), w.Error()
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func csvFilename(path string) string {
	base := strings.TrimPrefix(path, "/api/v1/analytics/")
	base = strings.ReplaceAll(base, "/", "_")
	if base == "" {
		base = "analytics"
	}
	return fmt.Sprintf("%s-%s.csv", base, time.Now().UTC().Format("20060102"))
}

// --- rate limiting ---------------------------------------------------

var (
	analyticsLimitersMu sync.Mutex
	analyticsLimiters   = make(map[string]*rate.Limiter)
	// Burst 10 qps, sustained 30/min = 0.5 qps. Use `rate.Limit` of
	// 30/60 = 0.5 tokens per second with a burst of 10 so a dashboard
	// load (several concurrent RPCs) doesn't immediately trip.
	analyticsLimit = rate.Limit(0.5)
	analyticsBurst = 10
)

func analyticsLimiterAllow(ctx context.Context) bool {
	claims, _ := ctx.Value(authware.ClaimsKey).(*authware.Claims)
	key := "anon"
	if claims != nil && claims.Username != "" {
		key = claims.Username
	}
	analyticsLimitersMu.Lock()
	l, ok := analyticsLimiters[key]
	if !ok {
		l = rate.NewLimiter(analyticsLimit, analyticsBurst)
		analyticsLimiters[key] = l
	}
	analyticsLimitersMu.Unlock()
	return l.Allow()
}
