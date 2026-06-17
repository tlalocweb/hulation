package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveEnrollCode(t *testing.T) {
	cases := []struct {
		name    string
		flag    string
		argz    []string
		want    string
		wantErr bool
	}{
		{"positional", "", []string{"relay-enroll", "https://r", "HULA-ENROLL-1"}, "HULA-ENROLL-1", false},
		{"flag when no positional", "HULA-FLAG", []string{"relay-enroll", "https://r"}, "HULA-FLAG", false},
		{"flag preferred over positional", "HULA-FLAG", []string{"relay-enroll", "https://r", "HULA-POS"}, "HULA-FLAG", false},
		{"missing", "", []string{"relay-enroll", "https://r"}, "", true},
		{"misplaced flag in code slot", "", []string{"relay-enroll", "https://r", "--code"}, "", true},
		{"whitespace trimmed", "", []string{"relay-enroll", "https://r", "  HULA-WS  "}, "HULA-WS", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := resolveEnrollCode(c.flag, c.argz)
			if c.wantErr {
				if err == nil {
					t.Fatalf("expected error, got code %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}

// writePushRelayBlock must not widen the config file's permissions — it now
// holds the signing seed, and ModifyYamlFile rewrites at 0644.
func TestWritePushRelayBlockPreservesMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("port: 443\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := writePushRelayBlock(path, "https://relay.example.com", "inst_abc", "c2VlZHNlZWRzZWVk"); err != nil {
		t.Fatalf("writePushRelayBlock: %v", err)
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Fatalf("file mode widened to %o, want 0600", got)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	for _, want := range []string{
		"push_relay:", "base_url", "https://relay.example.com",
		"installation_id", "inst_abc", "signing_key_b64", "c2VlZHNlZWRzZWVk",
		"port: 443", // pre-existing content must survive
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("written config missing %q; got:\n%s", want, got)
		}
	}
}
