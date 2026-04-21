package filter

import (
	"path/filepath"
	"regexp"
	"strings"
)

// Filter holds a pattern for matching strings. It supports shell-style globs and regex patterns.
type Filter struct {
	pattern string
	isRegex bool
	regex   *regexp.Regexp
}

// NewFilter creates a new Filter based on the given pattern.
// If the pattern starts with "regex:", the remainder is treated as a regex and compiled.
// Otherwise, it is treated as a shell-style glob.
func NewFilter(pattern string) (*Filter, error) {
	f := &Filter{pattern: pattern}
	if strings.HasPrefix(pattern, "regex:") {
		reStr := pattern[len("regex:"):]
		r, err := regexp.Compile(reStr)
		if err != nil {
			return nil, err
		}
		f.isRegex = true
		f.regex = r
		return f, nil
	}
	// Validate glob pattern
	if _, err := filepath.Match(pattern, ""); err != nil {
		return nil, err
	}
	return f, nil
}

// Match reports whether the input string s matches the Filter's pattern.
func (f *Filter) Match(s string) bool {
	if f.isRegex {
		return f.regex.MatchString(s)
	}
	matched, err := filepath.Match(f.pattern, s)
	if err != nil {
		return false
	}
	return matched
}

// FilterStrings returns all inputs that match the Filter's pattern.
func (f *Filter) FilterStrings(inputs []string) []string {
	var result []string
	for _, s := range inputs {
		if f.Match(s) {
			result = append(result, s)
		}
	}
	return result
}
