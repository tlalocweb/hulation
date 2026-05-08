package sitedeploy

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// touch creates an empty file at path, MkdirAll'ing parents.
func touch(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	f.Close()
}

func TestDetectGenerator_EmptyRepoDir(t *testing.T) {
	d := DetectGenerator("")
	if d.Generator != "" || d.Marker != "" || len(d.Ignored) != 0 {
		t.Fatalf("expected empty detection for empty repoDir, got %+v", d)
	}
}

func TestDetectGenerator_NoMarkers(t *testing.T) {
	dir := t.TempDir()
	d := DetectGenerator(dir)
	if d.Generator != "" {
		t.Fatalf("expected no generator detected in empty dir, got %+v", d)
	}
}

func TestDetectGenerator_PerGenerator(t *testing.T) {
	cases := []struct {
		name       string
		marker     string
		wantGen    string
		wantMarker string
	}{
		{"mkdocs.yml", "mkdocs.yml", "mkdocs", "mkdocs.yml"},
		{"mkdocs.yaml", "mkdocs.yaml", "mkdocs", "mkdocs.yaml"},
		{"astro.mjs", "astro.config.mjs", "astro", "astro.config.mjs"},
		{"astro.ts", "astro.config.ts", "astro", "astro.config.ts"},
		{"astro.js", "astro.config.js", "astro", "astro.config.js"},
		{"gatsby.js", "gatsby-config.js", "gatsby", "gatsby-config.js"},
		{"gatsby.ts", "gatsby-config.ts", "gatsby", "gatsby-config.ts"},
		{"hugo.toml", "hugo.toml", "hugo", "hugo.toml"},
		{"hugo.yaml", "hugo.yaml", "hugo", "hugo.yaml"},
		{"config.toml", "config.toml", "hugo", "config.toml"},
		{"config.yml", "config.yml", "hugo", "config.yml"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			touch(t, filepath.Join(dir, tc.marker))
			d := DetectGenerator(dir)
			if d.Generator != tc.wantGen {
				t.Errorf("generator: got %q, want %q", d.Generator, tc.wantGen)
			}
			if d.Marker != tc.wantMarker {
				t.Errorf("marker: got %q, want %q", d.Marker, tc.wantMarker)
			}
			if len(d.Ignored) != 0 {
				t.Errorf("ignored: got %v, want []", d.Ignored)
			}
		})
	}
}

func TestDetectGenerator_PrecedenceMkdocsOverHugo(t *testing.T) {
	dir := t.TempDir()
	touch(t, filepath.Join(dir, "mkdocs.yml"))
	touch(t, filepath.Join(dir, "hugo.toml"))
	touch(t, filepath.Join(dir, "config.toml"))

	d := DetectGenerator(dir)
	if d.Generator != "mkdocs" {
		t.Fatalf("expected mkdocs to win precedence over hugo, got %q", d.Generator)
	}
	if d.Marker != "mkdocs.yml" {
		t.Errorf("marker: got %q, want mkdocs.yml", d.Marker)
	}
	want := []string{"config.toml", "hugo.toml"}
	got := append([]string(nil), d.Ignored...)
	sort.Strings(got)
	if !equalSlices(got, want) {
		t.Errorf("ignored: got %v, want %v", got, want)
	}
}

func TestDetectGenerator_PrefersYmlOverYaml(t *testing.T) {
	dir := t.TempDir()
	touch(t, filepath.Join(dir, "mkdocs.yml"))
	touch(t, filepath.Join(dir, "mkdocs.yaml"))

	d := DetectGenerator(dir)
	if d.Marker != "mkdocs.yml" {
		t.Errorf("expected mkdocs.yml to win over mkdocs.yaml, got %q", d.Marker)
	}
	if len(d.Ignored) != 1 || d.Ignored[0] != "mkdocs.yaml" {
		t.Errorf("ignored: got %v, want [mkdocs.yaml]", d.Ignored)
	}
}

func TestDetectGenerator_FullPrecedenceOrder(t *testing.T) {
	// All four generators present. mkdocs wins; the rest go into Ignored.
	dir := t.TempDir()
	touch(t, filepath.Join(dir, "mkdocs.yml"))
	touch(t, filepath.Join(dir, "astro.config.mjs"))
	touch(t, filepath.Join(dir, "gatsby-config.js"))
	touch(t, filepath.Join(dir, "hugo.toml"))

	d := DetectGenerator(dir)
	if d.Generator != "mkdocs" {
		t.Fatalf("expected mkdocs to win, got %q", d.Generator)
	}
	want := []string{"astro.config.mjs", "gatsby-config.js", "hugo.toml"}
	got := append([]string(nil), d.Ignored...)
	sort.Strings(got)
	if !equalSlices(got, want) {
		t.Errorf("ignored: got %v, want %v", got, want)
	}
}

func TestDefaultProfileFor_Production(t *testing.T) {
	cases := []struct {
		gen          string
		wantContains []string
	}{
		{"mkdocs", []string{"MKDOCS build --strict --site-dir _hula_out", "FINALIZE /builder/site/_hula_out"}},
		{"hugo", []string{"HUGO --minify", "FINALIZE /builder/site/public"}},
		{"astro", []string{"ASTRO build", "FINALIZE /builder/site/dist"}},
		{"gatsby", []string{"GATSBY build", "FINALIZE /builder/site/public"}},
	}
	for _, tc := range cases {
		t.Run(tc.gen, func(t *testing.T) {
			p := defaultProfileFor(tc.gen, false)
			if p == nil {
				t.Fatalf("nil profile for %q", tc.gen)
			}
			if p.IsStaging() {
				t.Errorf("production profile reports IsStaging=true")
			}
			for _, sub := range tc.wantContains {
				if !strings.Contains(p.Commands, sub) {
					t.Errorf("Commands missing %q:\n%s", sub, p.Commands)
				}
			}
		})
	}
}

func TestDefaultProfileFor_Staging(t *testing.T) {
	cases := []struct {
		gen          string
		wantServeDir string
		wantBuildCmd string
	}{
		{"mkdocs", "/builder/site/_hula_out", "MKDOCS build --site-dir _hula_out"},
		{"hugo", "/builder/site/public", "HUGO"},
		{"astro", "/builder/site/dist", "ASTRO build"},
		{"gatsby", "/builder/site/public", "GATSBY build"},
	}
	for _, tc := range cases {
		t.Run(tc.gen, func(t *testing.T) {
			p := defaultProfileFor(tc.gen, true)
			if p == nil {
				t.Fatalf("nil profile for %q", tc.gen)
			}
			if !p.IsStaging() {
				t.Errorf("staging profile reports IsStaging=false")
			}
			if p.ServeDir != tc.wantServeDir {
				t.Errorf("ServeDir: got %q, want %q", p.ServeDir, tc.wantServeDir)
			}
			if p.BuildCommand != tc.wantBuildCmd {
				t.Errorf("BuildCommand: got %q, want %q", p.BuildCommand, tc.wantBuildCmd)
			}
			// Staging profiles must not FINALIZE — see ValidateCommandListForStaging.
			if strings.Contains(p.Commands, "FINALIZE") {
				t.Errorf("staging profile must not contain FINALIZE:\n%s", p.Commands)
			}
		})
	}
}

func TestDefaultProfileFor_UnknownGenerator(t *testing.T) {
	if p := defaultProfileFor("jekyll", false); p != nil {
		t.Errorf("expected nil profile for unknown generator, got %+v", p)
	}
}

func TestGetProfile_AutoDetectMkdocs(t *testing.T) {
	dir := t.TempDir()
	touch(t, filepath.Join(dir, "mkdocs.yml"))

	cfg := &SiteBuildConfig{}
	p, err := cfg.GetProfile("production", dir)
	if err != nil {
		t.Fatalf("GetProfile: %v", err)
	}
	if !strings.Contains(p.Commands, "MKDOCS") {
		t.Errorf("expected mkdocs default profile, got:\n%s", p.Commands)
	}
}

func TestGetProfile_AutoDetectStagingShape(t *testing.T) {
	dir := t.TempDir()
	touch(t, filepath.Join(dir, "mkdocs.yml"))

	cfg := &SiteBuildConfig{}
	p, err := cfg.GetProfile("staging", dir)
	if err != nil {
		t.Fatalf("GetProfile staging: %v", err)
	}
	if !p.IsStaging() {
		t.Errorf("expected staging-shaped profile, got Commands:\n%s", p.Commands)
	}
}

func TestGetProfile_NoMarker_ReturnsErrNoBuilderDetected(t *testing.T) {
	dir := t.TempDir()
	cfg := &SiteBuildConfig{}
	_, err := cfg.GetProfile("production", dir)
	if !errors.Is(err, ErrNoBuilderDetected) {
		t.Fatalf("expected ErrNoBuilderDetected, got %v", err)
	}
}

func TestGetProfile_EmptyRepoDir_ReturnsErrNoBuilderDetected(t *testing.T) {
	cfg := &SiteBuildConfig{}
	_, err := cfg.GetProfile("production", "")
	if !errors.Is(err, ErrNoBuilderDetected) {
		t.Fatalf("expected ErrNoBuilderDetected, got %v", err)
	}
}

func TestGetProfile_ExplicitConfigsBeatAutoDetection(t *testing.T) {
	dir := t.TempDir()
	touch(t, filepath.Join(dir, "mkdocs.yml")) // would auto-detect mkdocs
	cfg := &SiteBuildConfig{
		Configs: map[string]*BuildProfile{
			"production": {Commands: "WORKDIR /builder\nRUN echo custom\nFINALIZE /builder/site\n"},
		},
	}
	p, err := cfg.GetProfile("production", dir)
	if err != nil {
		t.Fatalf("GetProfile: %v", err)
	}
	if strings.Contains(p.Commands, "MKDOCS") {
		t.Errorf("auto-detection ran despite explicit configs; got:\n%s", p.Commands)
	}
	if !strings.Contains(p.Commands, "RUN echo custom") {
		t.Errorf("expected operator-defined commands, got:\n%s", p.Commands)
	}
}

func TestGetProfile_ExplicitConfigs_UnknownProfile(t *testing.T) {
	cfg := &SiteBuildConfig{
		Configs: map[string]*BuildProfile{
			"production": {Commands: "WORKDIR /b\nFINALIZE /b/site\n"},
		},
	}
	_, err := cfg.GetProfile("nonexistent", "")
	if err == nil {
		t.Fatal("expected error for missing profile")
	}
	if errors.Is(err, ErrNoBuilderDetected) {
		t.Errorf("explicit configs should not return ErrNoBuilderDetected, got: %v", err)
	}
}

func TestMkDocsVersionConfig_IsZero(t *testing.T) {
	cases := []struct {
		name string
		cfg  *MkDocsVersionConfig
		want bool
	}{
		{"nil", nil, true},
		{"empty struct", &MkDocsVersionConfig{}, true},
		{"version set", &MkDocsVersionConfig{Version: "1.6.1"}, false},
		{"at_least set", &MkDocsVersionConfig{AtLeast: "1.5.0"}, false},
		{"material set", &MkDocsVersionConfig{Material: "9.5.49"}, false},
		{"extras set", &MkDocsVersionConfig{ExtraPackages: []string{"pymdown-extensions"}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cfg.IsZero(); got != tc.want {
				t.Errorf("IsZero: got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSynthMkDocsPrebuild(t *testing.T) {
	cases := []struct {
		name         string
		cfg          *MkDocsVersionConfig
		wantEmpty    bool
		wantContains []string
		wantMissing  []string
	}{
		{
			name:      "zero -> empty",
			cfg:       &MkDocsVersionConfig{},
			wantEmpty: true,
		},
		{
			name:         "version only",
			cfg:          &MkDocsVersionConfig{Version: "1.6.1"},
			wantContains: []string{"mkdocs==1.6.1", "pip3 install"},
			wantMissing:  []string{"mkdocs-material"},
		},
		{
			name:         "version + material",
			cfg:          &MkDocsVersionConfig{Version: "1.6.1", Material: "9.5.49"},
			wantContains: []string{"mkdocs==1.6.1", "mkdocs-material==9.5.49"},
		},
		{
			name:         "at_least when version is empty",
			cfg:          &MkDocsVersionConfig{AtLeast: "1.5.0"},
			wantContains: []string{"mkdocs>=1.5.0"},
			wantMissing:  []string{"mkdocs==", "mkdocs-material"},
		},
		{
			name:         "version wins over at_least",
			cfg:          &MkDocsVersionConfig{Version: "1.6.1", AtLeast: "1.5.0"},
			wantContains: []string{"mkdocs==1.6.1"},
			wantMissing:  []string{"mkdocs>="},
		},
		{
			name:         "extras only",
			cfg:          &MkDocsVersionConfig{ExtraPackages: []string{"pymdown-extensions==10.11.2", "mike==2.1.3"}},
			wantContains: []string{"pymdown-extensions==10.11.2", "mike==2.1.3"},
			wantMissing:  []string{"mkdocs==", "mkdocs>=", "mkdocs-material"},
		},
		{
			name: "all fields combined",
			cfg: &MkDocsVersionConfig{
				Version:       "1.6.1",
				Material:      "9.5.49",
				ExtraPackages: []string{"pymdown-extensions==10.11.2"},
			},
			wantContains: []string{"mkdocs==1.6.1", "mkdocs-material==9.5.49", "pymdown-extensions==10.11.2"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := synthMkDocsPrebuild(tc.cfg)
			if tc.wantEmpty {
				if got != "" {
					t.Errorf("expected empty, got:\n%s", got)
				}
				return
			}
			for _, sub := range tc.wantContains {
				if !strings.Contains(got, sub) {
					t.Errorf("output missing %q:\n%s", sub, got)
				}
			}
			for _, sub := range tc.wantMissing {
				if strings.Contains(got, sub) {
					t.Errorf("output should not contain %q:\n%s", sub, got)
				}
			}
		})
	}
}

func TestSynthMkDocsPrebuild_DeterministicOrdering(t *testing.T) {
	cfg := &MkDocsVersionConfig{
		Version:       "1.6.1",
		Material:      "9.5.49",
		ExtraPackages: []string{"a", "b", "c"},
	}
	a := synthMkDocsPrebuild(cfg)
	b := synthMkDocsPrebuild(cfg)
	if a != b {
		t.Errorf("synth not deterministic across calls — derived-image cache will miss\nfirst:\n%s\nsecond:\n%s", a, b)
	}
}

func TestResolveMkDocsConfig(t *testing.T) {
	top := &MkDocsVersionConfig{Version: "1.6.0"}
	override := &MkDocsVersionConfig{Version: "1.6.1"}

	cases := []struct {
		name     string
		topLevel *MkDocsVersionConfig
		profile  *MkDocsVersionConfig
		want     *MkDocsVersionConfig
	}{
		{"both nil", nil, nil, nil},
		{"top only", top, nil, top},
		{"profile only", nil, override, override},
		{"profile overrides top", top, override, override},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &SiteBuildConfig{MkDocs: tc.topLevel}
			p := &BuildProfile{MkDocs: tc.profile}
			got := c.ResolveMkDocsConfig(p)
			if got != tc.want {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestEffectivePrebuild(t *testing.T) {
	cases := []struct {
		name         string
		siteCfg      *SiteBuildConfig
		profile      *BuildProfile
		wantEmpty    bool
		wantContains []string
	}{
		{
			name:      "no mkdocs, no operator prebuild -> empty",
			siteCfg:   &SiteBuildConfig{},
			profile:   &BuildProfile{},
			wantEmpty: true,
		},
		{
			name:    "operator prebuild only",
			siteCfg: &SiteBuildConfig{},
			profile: &BuildProfile{
				DockerfilePrebuild: "RUN apk add foo",
			},
			wantContains: []string{"RUN apk add foo"},
		},
		{
			name: "mkdocs synth only",
			siteCfg: &SiteBuildConfig{
				MkDocs: &MkDocsVersionConfig{Version: "1.6.1"},
			},
			profile:      &BuildProfile{},
			wantContains: []string{"mkdocs==1.6.1", "pip3 install"},
		},
		{
			name: "synth before operator prebuild",
			siteCfg: &SiteBuildConfig{
				MkDocs: &MkDocsVersionConfig{Version: "1.6.1"},
			},
			profile: &BuildProfile{
				DockerfilePrebuild: "RUN apk add foo",
			},
			wantContains: []string{"mkdocs==1.6.1", "RUN apk add foo"},
		},
		{
			name:    "profile-level mkdocs overrides top-level",
			siteCfg: &SiteBuildConfig{MkDocs: &MkDocsVersionConfig{Version: "1.6.0"}},
			profile: &BuildProfile{
				MkDocs: &MkDocsVersionConfig{Version: "1.6.1"},
			},
			wantContains: []string{"mkdocs==1.6.1"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.siteCfg.EffectivePrebuild(tc.profile)
			if tc.wantEmpty {
				if got != "" {
					t.Errorf("expected empty, got:\n%s", got)
				}
				return
			}
			for _, sub := range tc.wantContains {
				if !strings.Contains(got, sub) {
					t.Errorf("missing %q:\n%s", sub, got)
				}
			}
			// When both pieces are present, synth must precede operator prebuild.
			if !tc.siteCfg.ResolveMkDocsConfig(tc.profile).IsZero() && tc.profile.DockerfilePrebuild != "" {
				synthIdx := strings.Index(got, "pip3 install")
				operatorIdx := strings.Index(got, tc.profile.DockerfilePrebuild)
				if synthIdx < 0 || operatorIdx < 0 || synthIdx > operatorIdx {
					t.Errorf("synth must precede operator prebuild; got:\n%s", got)
				}
			}
		})
	}
}

func TestEffectivePrebuild_YAMLRoundTrip(t *testing.T) {
	const src = `
mkdocs:
  version: "1.6.1"
  material: "9.5.49"
  extra_packages:
    - pymdown-extensions==10.11.2

configs:
  production:
    commands: |
      WORKDIR /builder
      MKDOCS build --site-dir _hula_out
      FINALIZE /builder/site/_hula_out
`
	cfg, err := ParseSiteBuildConfig([]byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	profile, err := cfg.GetProfile("production", "")
	if err != nil {
		t.Fatalf("GetProfile: %v", err)
	}
	out := cfg.EffectivePrebuild(profile)
	for _, want := range []string{"mkdocs==1.6.1", "mkdocs-material==9.5.49", "pymdown-extensions==10.11.2"} {
		if !strings.Contains(out, want) {
			t.Errorf("synth missing %q:\n%s", want, out)
		}
	}
}

func equalSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
