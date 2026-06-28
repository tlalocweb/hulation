// Package keytool implements `hula`'s key/secret generation + config-update
// subcommands (totp-key-update, jwt-key-update, …). The YAML updater here is
// deliberately a surgical line edit, NOT a yaml.Marshal round-trip: it changes
// only the one scalar value's bytes and leaves every comment, blank line,
// indentation, key order, and quote style elsewhere byte-for-byte identical.
// (A yaml.Node round-trip preserves comments but drops blank lines and can
// reindent, which would violate "only change the value needed".)
package keytool

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

// ErrValueExists means the target field already holds a real (non-placeholder)
// value and force was not set. ErrKeyNotFound means the key path is absent.
var (
	ErrValueExists = fmt.Errorf("field already has a value (use --force to overwrite)")
	ErrKeyNotFound = fmt.Errorf("field not found in config")
)

// ScalarEdit is one keyPath→value change. KeyPath is the nested key sequence,
// e.g. []string{"jwt_key"} or []string{"opaque", "oprf_seed"}. Depth 1 (a
// top-level key) and depth 2 (one level of nesting) are supported — enough for
// every field these commands target.
type ScalarEdit struct {
	KeyPath []string
	Value   string
}

// keyLineRe splits "  key: rest" into indent, key, gap-after-colon, rest.
var keyLineRe = regexp.MustCompile(`^(\s*)([A-Za-z0-9_.\-]+):(\s*)(.*)$`)

// looksLikePlaceholder reports whether v is empty or a scaffolding placeholder
// that's safe to overwrite without --force.
func looksLikePlaceholder(v string) bool {
	if strings.TrimSpace(v) == "" {
		return true
	}
	u := strings.ToUpper(v)
	for _, m := range []string{"CHANGE_ME", "REPLACE_ME", "REPLACE_WITH"} {
		if strings.Contains(u, m) {
			return true
		}
	}
	return false
}

func indentOf(line string) int {
	return len(line) - len(strings.TrimLeft(line, " \t"))
}

// splitValueComment separates the value token from an optional trailing inline
// comment. Quoted values end at the closing quote (everything after is comment,
// preserved verbatim); unquoted values end at the first " #" sequence.
func splitValueComment(rest string) (value, comment string) {
	if rest == "" {
		return "", ""
	}
	switch rest[0] {
	case '"', '\'':
		q := rest[0]
		if i := strings.IndexByte(rest[1:], q); i >= 0 {
			end := 1 + i + 1 // past the closing quote
			return rest[:end], rest[end:]
		}
		// Unterminated quote — treat the whole remainder as the value.
		return rest, ""
	default:
		if i := strings.Index(rest, " #"); i >= 0 {
			return rest[:i], rest[i:]
		}
		return rest, ""
	}
}

func unquote(v string) string {
	v = strings.TrimSpace(v)
	if len(v) >= 2 && (v[0] == '"' && v[len(v)-1] == '"' || v[0] == '\'' && v[len(v)-1] == '\'') {
		return v[1 : len(v)-1]
	}
	return v
}

// quote wraps v in double quotes. The values these commands write (base64 /
// base64url) contain only [A-Za-z0-9+/=_-], none of which need escaping.
func quote(v string) string { return `"` + v + `"` }

// scalarLoc is a resolved target line ready to rewrite.
type scalarLoc struct {
	idx      int    // line index
	curValue string // current (unquoted) value, for the force guard
	prefix   string // everything up to and including the gap after the colon
	comment  string // trailing inline comment (with leading spaces), preserved
}

// findScalar locates keyPath within lines and returns where/how to rewrite it.
func findScalar(lines []string, keyPath []string) (scalarLoc, error) {
	switch len(keyPath) {
	case 1:
		return findKeyInRange(lines, 0, len(lines), keyPath[0], false)
	case 2:
		pIdx := -1
		for i, ln := range lines {
			t := strings.TrimSpace(ln)
			if t == "" || strings.HasPrefix(t, "#") || indentOf(ln) != 0 {
				continue
			}
			if m := keyLineRe.FindStringSubmatch(ln); m != nil && m[2] == keyPath[0] {
				pIdx = i
				break
			}
		}
		if pIdx < 0 {
			return scalarLoc{}, fmt.Errorf("section %q: %w", keyPath[0], ErrKeyNotFound)
		}
		// The parent block runs until the next column-0 (non-blank, non-comment) line.
		end := len(lines)
		for i := pIdx + 1; i < len(lines); i++ {
			t := strings.TrimSpace(lines[i])
			if t == "" || strings.HasPrefix(t, "#") {
				continue
			}
			if indentOf(lines[i]) == 0 {
				end = i
				break
			}
		}
		return findKeyInRange(lines, pIdx+1, end, keyPath[1], true)
	default:
		return scalarLoc{}, fmt.Errorf("unsupported key depth %d", len(keyPath))
	}
}

func findKeyInRange(lines []string, start, end int, key string, requireIndent bool) (scalarLoc, error) {
	for i := start; i < end; i++ {
		ln := lines[i]
		t := strings.TrimSpace(ln)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		m := keyLineRe.FindStringSubmatch(ln)
		if m == nil || m[2] != key {
			continue
		}
		indent := m[1]
		if requireIndent && indent == "" {
			continue
		}
		if !requireIndent && indent != "" {
			continue
		}
		value, comment := splitValueComment(m[4])
		if v := strings.TrimSpace(value); strings.HasPrefix(v, "|") || strings.HasPrefix(v, ">") ||
			strings.HasPrefix(v, "[") || strings.HasPrefix(v, "{") {
			return scalarLoc{}, fmt.Errorf("%q has a block/flow value this tool won't rewrite", key)
		}
		return scalarLoc{
			idx:      i,
			curValue: unquote(value),
			prefix:   m[1] + m[2] + ":" + m[3],
			comment:  comment,
		}, nil
	}
	return scalarLoc{}, fmt.Errorf("%q: %w", key, ErrKeyNotFound)
}

// SetScalarsInPlace rewrites each edit's value in file, changing only those
// values' bytes. All edits are located and guarded BEFORE any write, so a
// refused edit (real value + !force) leaves the file untouched. On success it
// writes <file>.bak.<nowUnix> first, writes atomically (temp+rename), and
// preserves the original file mode. nowUnix names the backup (pass time.Now().Unix()).
func SetScalarsInPlace(file string, edits []ScalarEdit, force bool, nowUnix int64) (backupPath string, err error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return "", err
	}
	lines := strings.Split(string(data), "\n")

	locs := make([]scalarLoc, len(edits))
	for i, e := range edits {
		loc, ferr := findScalar(lines, e.KeyPath)
		if ferr != nil {
			return "", fmt.Errorf("%s: %w", strings.Join(e.KeyPath, "."), ferr)
		}
		if !force && !looksLikePlaceholder(loc.curValue) {
			return "", fmt.Errorf("%s: %w", strings.Join(e.KeyPath, "."), ErrValueExists)
		}
		locs[i] = loc
	}

	for i, e := range edits {
		lines[locs[i].idx] = locs[i].prefix + quote(e.Value) + locs[i].comment
	}

	mode := os.FileMode(0o600)
	if fi, serr := os.Stat(file); serr == nil {
		mode = fi.Mode().Perm()
	}
	backupPath = fmt.Sprintf("%s.bak.%d", file, nowUnix)
	if werr := os.WriteFile(backupPath, data, mode); werr != nil {
		return "", fmt.Errorf("write backup %s: %w", backupPath, werr)
	}
	tmp := file + ".tmp"
	if werr := os.WriteFile(tmp, []byte(strings.Join(lines, "\n")), mode); werr != nil {
		return "", fmt.Errorf("write temp: %w", werr)
	}
	if rerr := os.Rename(tmp, file); rerr != nil {
		return "", fmt.Errorf("rename into place: %w", rerr)
	}
	_ = os.Chmod(file, mode)
	return backupPath, nil
}
