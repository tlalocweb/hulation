package keytool

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sampleConfig = `# top comment
admin:
    username: admin

jwt_key: "CHANGE_ME_TO_A_RANDOM_STRING"   # inline note
jwt_expiration: "72h"
port: 443

# a blank line and this comment must survive
totp_encryption_key: "REPLACE_WITH_OUTPUT_OF_hulactl_totp-key"

opaque:
    # nested comment
    oprf_seed: "old-seed"
    ake_secret: "old-ake"
servers:
    - host: www.example.com
`

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSetScalars_OnlyChangesValue_PreservesEverythingElse(t *testing.T) {
	p := writeTemp(t, sampleConfig)
	bak, err := SetScalarsInPlace(p, []ScalarEdit{{KeyPath: []string{"jwt_key"}, Value: "NEWJWT"}}, false, 12345)
	if err != nil {
		t.Fatalf("SetScalarsInPlace: %v", err)
	}
	got, _ := os.ReadFile(p)
	want := strings.Replace(sampleConfig,
		`jwt_key: "CHANGE_ME_TO_A_RANDOM_STRING"   # inline note`,
		`jwt_key: "NEWJWT"   # inline note`, 1)
	if string(got) != want {
		t.Fatalf("file changed beyond the one value.\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
	// backup is the pristine original.
	bakData, err := os.ReadFile(bak)
	if err != nil {
		t.Fatalf("backup not written: %v", err)
	}
	if string(bakData) != sampleConfig {
		t.Fatal("backup is not the original content")
	}
	if !strings.HasSuffix(bak, ".bak.12345") {
		t.Fatalf("backup name = %s, want suffix .bak.12345", bak)
	}
}

func TestSetScalars_NestedOpaque_Both(t *testing.T) {
	p := writeTemp(t, sampleConfig)
	if _, err := SetScalarsInPlace(p, []ScalarEdit{
		{KeyPath: []string{"opaque", "oprf_seed"}, Value: "S"},
		{KeyPath: []string{"opaque", "ake_secret"}, Value: "A"},
	}, true, 1); err != nil {
		t.Fatalf("nested set: %v", err)
	}
	got, _ := os.ReadFile(p)
	s := string(got)
	if !strings.Contains(s, `    oprf_seed: "S"`) || !strings.Contains(s, `    ake_secret: "A"`) {
		t.Fatalf("nested values not set:\n%s", s)
	}
	// nested comment + indentation preserved
	if !strings.Contains(s, "    # nested comment") {
		t.Fatal("nested comment lost")
	}
}

func TestSetScalars_RefuseRealValueWithoutForce(t *testing.T) {
	p := writeTemp(t, sampleConfig)
	// opaque.oprf_seed currently "old-seed" — a real value.
	_, err := SetScalarsInPlace(p, []ScalarEdit{{KeyPath: []string{"opaque", "oprf_seed"}, Value: "x"}}, false, 1)
	if !errors.Is(err, ErrValueExists) {
		t.Fatalf("want ErrValueExists, got %v", err)
	}
	// file must be untouched (no write, no partial edit).
	got, _ := os.ReadFile(p)
	if string(got) != sampleConfig {
		t.Fatal("file was modified despite refusal")
	}
}

func TestSetScalars_AtomicGuard_NoPartialWrite(t *testing.T) {
	p := writeTemp(t, sampleConfig)
	// First edit targets a placeholder (ok), second targets a real value (refused).
	// Because all edits are guarded before any write, neither should apply.
	_, err := SetScalarsInPlace(p, []ScalarEdit{
		{KeyPath: []string{"totp_encryption_key"}, Value: "OK"},
		{KeyPath: []string{"opaque", "ake_secret"}, Value: "NO"},
	}, false, 1)
	if !errors.Is(err, ErrValueExists) {
		t.Fatalf("want ErrValueExists, got %v", err)
	}
	got, _ := os.ReadFile(p)
	if string(got) != sampleConfig {
		t.Fatal("partial write: totp value changed even though the batch was refused")
	}
}

func TestSetScalars_KeyNotFound(t *testing.T) {
	p := writeTemp(t, sampleConfig)
	_, err := SetScalarsInPlace(p, []ScalarEdit{{KeyPath: []string{"noise_static_key"}, Value: "x"}}, true, 1)
	if !errors.Is(err, ErrKeyNotFound) {
		t.Fatalf("want ErrKeyNotFound, got %v", err)
	}
}

func TestSetScalars_PreservesFileMode(t *testing.T) {
	p := writeTemp(t, sampleConfig) // written 0o600
	if _, err := SetScalarsInPlace(p, []ScalarEdit{{KeyPath: []string{"jwt_key"}, Value: "X"}}, false, 1); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(p)
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode widened to %o, want 0600", fi.Mode().Perm())
	}
}

// The edited line must keep its original quote style (single / double / none)
// and any trailing whitespace — only the value's bytes change.
func TestSetScalars_PreservesQuoteStyleAndTrailingWS(t *testing.T) {
	src := "a: 'CHANGE_ME'   # single\n" +
		"b: CHANGE_ME\n" +
		"c: \"CHANGE_ME\"\n" +
		"d: CHANGE_ME  \n" // trailing whitespace, no comment
	p := writeTemp(t, src)
	for _, k := range []string{"a", "b", "c", "d"} {
		if _, err := SetScalarsInPlace(p, []ScalarEdit{{KeyPath: []string{k}, Value: "NEW_" + k}}, true, 1); err != nil {
			t.Fatalf("set %s: %v", k, err)
		}
	}
	got, _ := os.ReadFile(p)
	want := "a: 'NEW_a'   # single\n" +
		"b: NEW_b\n" +
		"c: \"NEW_c\"\n" +
		"d: NEW_d  \n"
	if string(got) != want {
		t.Fatalf("quote style / trailing whitespace not preserved.\n got: %q\nwant: %q", got, want)
	}
}

func TestPlaceholderDetection(t *testing.T) {
	for _, v := range []string{"", "   ", "CHANGE_ME_x", "replace_me", "REPLACE_WITH_y"} {
		if !looksLikePlaceholder(v) {
			t.Errorf("%q should be a placeholder", v)
		}
	}
	for _, v := range []string{"realsecret", "abc123=="} {
		if looksLikePlaceholder(v) {
			t.Errorf("%q should NOT be a placeholder", v)
		}
	}
}
