package badactor

import (
	_ "embed"
	"fmt"
	"os"
	"regexp"
	"sync"

	"gopkg.in/yaml.v2"
)

//go:embed default_signatures.yaml
var defaultSignaturesYAML []byte

// SignatureType is the kind of request field a signature matches against.
type SignatureType string

const (
	SigTypeURL         SignatureType = "url"
	SigTypeUserAgent   SignatureType = "user_agent"
	SigTypeQueryString SignatureType = "query_string"
)

// SignatureEntry is a single signature definition from YAML.
type SignatureEntry struct {
	Name    string        `yaml:"name"`
	Type    SignatureType `yaml:"type"`
	Pattern string        `yaml:"pattern"`
	Score   int           `yaml:"score"`
	Reason  string        `yaml:"reason"`
}

// CategoryDef groups signatures under a category.
type CategoryDef struct {
	Description string           `yaml:"description"`
	Signatures  []SignatureEntry `yaml:"signatures"`
}

// SignaturesFile is the top-level YAML structure.
type SignaturesFile struct {
	Version    int                    `yaml:"version"`
	Categories map[string]CategoryDef `yaml:"categories"`
}

// CompiledSignature is a signature with a precompiled regex.
type CompiledSignature struct {
	Name     string
	Type     SignatureType
	Regex    *regexp.Regexp
	Score    int
	Reason   string
	Category string
	// validatedPaths caches per-path check results for URL-type signatures.
	// Key: URL path, Value: true if known valid (skip scoring), false if not.
	validatedPaths sync.Map
}

// CompiledSignatures holds all compiled signatures grouped by type for efficient matching.
type CompiledSignatures struct {
	URL         []*CompiledSignature
	UserAgent   []*CompiledSignature
	QueryString []*CompiledSignature
	All         []*CompiledSignature
}

// LoadSignatures loads the default embedded signatures, optionally merging with a custom file.
func LoadSignatures(customFile string) (*CompiledSignatures, error) {
	var base SignaturesFile
	if err := yaml.Unmarshal(defaultSignaturesYAML, &base); err != nil {
		return nil, fmt.Errorf("parsing default signatures: %w", err)
	}

	// Merge custom file if provided
	if customFile != "" {
		data, err := os.ReadFile(customFile)
		if err != nil {
			return nil, fmt.Errorf("reading custom signatures file %s: %w", customFile, err)
		}
		var custom SignaturesFile
		if err := yaml.Unmarshal(data, &custom); err != nil {
			return nil, fmt.Errorf("parsing custom signatures %s: %w", customFile, err)
		}
		// Merge: custom categories override base categories by name
		for k, v := range custom.Categories {
			base.Categories[k] = v
		}
	}

	return compileSignatures(&base)
}

func compileSignatures(sf *SignaturesFile) (*CompiledSignatures, error) {
	cs := &CompiledSignatures{}
	for catName, cat := range sf.Categories {
		for _, sig := range cat.Signatures {
			re, err := regexp.Compile(sig.Pattern)
			if err != nil {
				return nil, fmt.Errorf("compiling signature %s/%s pattern %q: %w", catName, sig.Name, sig.Pattern, err)
			}
			compiled := &CompiledSignature{
				Name:     sig.Name,
				Type:     sig.Type,
				Regex:    re,
				Score:    sig.Score,
				Reason:   sig.Reason,
				Category: catName,
			}
			cs.All = append(cs.All, compiled)
			switch sig.Type {
			case SigTypeURL:
				cs.URL = append(cs.URL, compiled)
			case SigTypeUserAgent:
				cs.UserAgent = append(cs.UserAgent, compiled)
			case SigTypeQueryString:
				cs.QueryString = append(cs.QueryString, compiled)
			}
		}
	}
	return cs, nil
}

// MatchResult holds details about a signature match.
type MatchResult struct {
	Score    int
	Reason   string
	SigName  string
	Category string
}

// MatchRequest checks a request against all signatures.
// validPathCheck is called for URL-type matches to see if the path is valid (should be skipped).
// Returns all matches (there can be multiple).
func (cs *CompiledSignatures) MatchRequest(urlPath, userAgent, queryString string, validPathCheck func(sig *CompiledSignature, path string) bool) []MatchResult {
	var results []MatchResult

	for _, sig := range cs.URL {
		if sig.Regex.MatchString(urlPath) {
			if validPathCheck != nil && validPathCheck(sig, urlPath) {
				continue // valid path, skip
			}
			results = append(results, MatchResult{Score: sig.Score, Reason: sig.Reason, SigName: sig.Name, Category: sig.Category})
		}
	}
	for _, sig := range cs.UserAgent {
		if sig.Regex.MatchString(userAgent) {
			results = append(results, MatchResult{Score: sig.Score, Reason: sig.Reason, SigName: sig.Name, Category: sig.Category})
		}
	}
	if queryString != "" {
		for _, sig := range cs.QueryString {
			if sig.Regex.MatchString(queryString) {
				results = append(results, MatchResult{Score: sig.Score, Reason: sig.Reason, SigName: sig.Name, Category: sig.Category})
			}
		}
	}

	return results
}
