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
	"path/filepath"
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

// splitValue parses the text after "key:<gap>" into the value token's quote
// style, the bare (unquoted) value, and the verbatim suffix — the inline comment
// plus any trailing whitespace / "\r". Re-emitting prefix + requote(new, q) +
// suffix therefore changes only the value's bytes, preserving the original
// quoting, trailing spacing, and line ending.
func splitValue(rest string) (q byte, value, suffix string) {
	if rest == "" {
		return 0, "", ""
	}
	switch rest[0] {
	case '"', '\'':
		if i := strings.IndexByte(rest[1:], rest[0]); i >= 0 {
			end := 1 + i + 1 // past the closing quote
			return rest[0], rest[1 : end-1], rest[end:]
		}
		// Unterminated quote — treat as a bare value, no recognized quoting.
		return 0, rest, ""
	default:
		// Unquoted: value runs to an inline comment (" #") if present;
		// whitespace/"\r" before the comment or EOL goes into the suffix.
		body, comment := rest, ""
		if i := strings.Index(rest, " #"); i >= 0 {
			body, comment = rest[:i], rest[i:]
		}
		trimmed := strings.TrimRight(body, " \t\r")
		return 0, trimmed, body[len(trimmed):] + comment
	}
}

// requote re-emits val using the original token's quote style. The values these
// commands write (base64 / base64url) contain only [A-Za-z0-9+/=_-], so the
// single-quote escaping below never actually fires for them — it's there so the
// helper stays correct for any caller.
func requote(val string, q byte) string {
	switch q {
	case '"':
		return `"` + val + `"`
	case '\'':
		return "'" + strings.ReplaceAll(val, "'", "''") + "'"
	default:
		return val // unquoted in the original → leave unquoted
	}
}

// scalarLoc is a resolved target line ready to rewrite.
type scalarLoc struct {
	idx      int    // line index
	curValue string // current (unquoted) value, for the force guard
	prefix   string // everything up to and including the gap after the colon
	quote    byte   // original value quoting: 0 (none), '\'', or '"'
	suffix   string // verbatim tail: inline comment + trailing whitespace / "\r"
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
		q, value, suffix := splitValue(m[4])
		if strings.HasPrefix(value, "|") || strings.HasPrefix(value, ">") ||
			strings.HasPrefix(value, "[") || strings.HasPrefix(value, "{") {
			return scalarLoc{}, fmt.Errorf("%q has a block/flow value this tool won't rewrite", key)
		}
		return scalarLoc{
			idx:      i,
			curValue: value,
			prefix:   m[1] + m[2] + ":" + m[3],
			quote:    q,
			suffix:   suffix,
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
		lines[locs[i].idx] = locs[i].prefix + requote(e.Value, locs[i].quote) + locs[i].suffix
	}

	mode := os.FileMode(0o600)
	if fi, serr := os.Stat(file); serr == nil {
		mode = fi.Mode().Perm()
	}
	backupPath = fmt.Sprintf("%s.bak.%d", file, nowUnix)
	if werr := os.WriteFile(backupPath, data, mode); werr != nil {
		return "", fmt.Errorf("write backup %s: %w", backupPath, werr)
	}

	// Write to a unique temp file in the same dir, then rename atomically.
	// A fixed "<file>.tmp" path would be predictable — it could clobber an
	// existing file or follow a symlink planted by another user (a real risk
	// when this runs as root). CreateTemp makes an O_EXCL 0600 file with a
	// random suffix; we chmod to the target mode and remove it on any failure.
	dir := filepath.Dir(file)
	tmpf, werr := os.CreateTemp(dir, "."+filepath.Base(file)+".tmp-*")
	if werr != nil {
		return "", fmt.Errorf("create temp: %w", werr)
	}
	tmpName := tmpf.Name()
	defer os.Remove(tmpName) // no-op once the rename below succeeds
	if _, werr := tmpf.Write([]byte(strings.Join(lines, "\n"))); werr != nil {
		tmpf.Close()
		return "", fmt.Errorf("write temp: %w", werr)
	}
	if werr := tmpf.Close(); werr != nil {
		return "", fmt.Errorf("close temp: %w", werr)
	}
	if werr := os.Chmod(tmpName, mode); werr != nil {
		return "", fmt.Errorf("chmod temp: %w", werr)
	}
	if rerr := os.Rename(tmpName, file); rerr != nil {
		return "", fmt.Errorf("rename into place: %w", rerr)
	}
	return backupPath, nil
}
