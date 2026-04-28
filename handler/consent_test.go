package handler

import (
	"testing"

	"github.com/tlalocweb/hulation/config"
)

func TestResolveConsent(t *testing.T) {
	cases := []struct {
		name        string
		mode        string
		body        *ConsentState
		gpc         string
		wantA       bool
		wantM       bool
		wantBlock   bool
		wantSource  string
	}{
		// --- mode=off (default) ---
		{
			name: "off: no signal → analytics+marketing both true",
			mode: "off", body: nil, gpc: "",
			wantA: true, wantM: true, wantBlock: false, wantSource: "default_off",
		},
		{
			name: "off: GPC=1 → marketing false, analytics still true (legitimate-interest)",
			mode: "off", body: nil, gpc: "1",
			wantA: true, wantM: false, wantBlock: false, wantSource: "gpc_header",
		},
		{
			name: "off: CMP {analytics:true, marketing:false} → wins over default",
			mode: "off", body: &ConsentState{Analytics: true, Marketing: false}, gpc: "",
			wantA: true, wantM: false, wantBlock: false, wantSource: "cmp_payload",
		},
		{
			name: "off: CMP overrides GPC (CMP-explicit beats header)",
			mode: "off", body: &ConsentState{Analytics: true, Marketing: true}, gpc: "1",
			wantA: true, wantM: true, wantBlock: false, wantSource: "cmp_payload",
		},
		// --- mode=opt_in ---
		{
			name: "opt_in: no signal → block + everything false",
			mode: "opt_in", body: nil, gpc: "",
			wantA: false, wantM: false, wantBlock: true, wantSource: "default_optin",
		},
		{
			name: "opt_in: GPC alone is not affirmative → still blocked",
			mode: "opt_in", body: nil, gpc: "1",
			wantA: false, wantM: false, wantBlock: true, wantSource: "gpc_header",
		},
		{
			name: "opt_in: CMP analytics:true unblocks; marketing as supplied",
			mode: "opt_in", body: &ConsentState{Analytics: true, Marketing: false}, gpc: "",
			wantA: true, wantM: false, wantBlock: false, wantSource: "cmp_payload",
		},
		{
			name: "opt_in: CMP analytics:false stays blocked even if marketing:true",
			mode: "opt_in", body: &ConsentState{Analytics: false, Marketing: true}, gpc: "",
			wantA: false, wantM: true, wantBlock: true, wantSource: "cmp_payload",
		},
		// --- mode=opt_out ---
		{
			name: "opt_out: no signal → both true, never block",
			mode: "opt_out", body: nil, gpc: "",
			wantA: true, wantM: true, wantBlock: false, wantSource: "default_optout",
		},
		{
			name: "opt_out: GPC=1 → marketing=false, analytics still true, no block",
			mode: "opt_out", body: nil, gpc: "1",
			wantA: true, wantM: false, wantBlock: false, wantSource: "gpc_header",
		},
		// --- empty mode falls back to "off" ---
		{
			name: "empty mode treated as off",
			mode: "", body: nil, gpc: "",
			wantA: true, wantM: true, wantBlock: false, wantSource: "default_off",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := resolveConsent(&config.Server{ConsentMode: tc.mode}, tc.body, tc.gpc)
			if d.State.Analytics != tc.wantA {
				t.Errorf("analytics: got %v want %v", d.State.Analytics, tc.wantA)
			}
			if d.State.Marketing != tc.wantM {
				t.Errorf("marketing: got %v want %v", d.State.Marketing, tc.wantM)
			}
			if d.Block != tc.wantBlock {
				t.Errorf("block: got %v want %v", d.Block, tc.wantBlock)
			}
			if d.State.Source != tc.wantSource {
				t.Errorf("source: got %q want %q", d.State.Source, tc.wantSource)
			}
		})
	}
}

func TestResolveConsent_GPCWhitespace(t *testing.T) {
	// Whitespace around "1" should still be honored.
	d := resolveConsent(&config.Server{ConsentMode: "off"}, nil, " 1 ")
	if d.State.Marketing {
		t.Errorf("expected marketing=false on Sec-GPC: ' 1 '")
	}
	if d.State.Source != "gpc_header" {
		t.Errorf("source: got %q want gpc_header", d.State.Source)
	}
}
