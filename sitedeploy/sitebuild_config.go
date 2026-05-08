package sitedeploy

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cbroglie/mustache"
	"github.com/tlalocweb/hulation/log"
	"gopkg.in/yaml.v2"
)

// ErrNoBuilderDetected is returned by GetProfile when no
// sitebuild.yaml configs are defined and the repository has no
// recognisable generator marker file (mkdocs.yml, hugo.toml,
// astro.config.*, gatsby-config.*).
var ErrNoBuilderDetected = errors.New(
	"no build profile defined and no generator marker file detected " +
		"(looked for mkdocs.yml, astro.config.*, gatsby-config.*, hugo.toml)")

const (
	DefaultBuilderImage = "default"
	DefaultBuildProfile = "production"
)

// SiteBuildConfig represents the .hula/sitebuild.yaml file in a site repository.
type SiteBuildConfig struct {
	Defs         map[string]string        `yaml:"defs,omitempty"`
	BuilderImage string                   `yaml:"builder_image,omitempty"`
	Hugo         *HugoVersionConfig       `yaml:"hugo,omitempty"`
	MkDocs       *MkDocsVersionConfig     `yaml:"mkdocs,omitempty"`
	Configs      map[string]*BuildProfile `yaml:"configs"`
}

// HugoVersionConfig specifies hugo version requirements.
type HugoVersionConfig struct {
	// AtLeast specifies the minimum required version (e.g., "0.159.1")
	AtLeast string `yaml:"at_least,omitempty"`
	// Version specifies an exact version (e.g., "0.159.1")
	Version string `yaml:"version,omitempty"`
}

// MkDocsVersionConfig specifies mkdocs and mkdocs-material version
// requirements plus arbitrary extra pip packages. Non-nil values
// drive a derived-image prebuild: the requested versions are
// pip-installed FROM the base builder image, content-hash-cached the
// same way operator-supplied DockerfilePrebuild blocks are.
type MkDocsVersionConfig struct {
	// Version pins mkdocs to an exact release (e.g., "1.6.1"). If set,
	// AtLeast is ignored.
	Version string `yaml:"version,omitempty"`
	// AtLeast installs `mkdocs>=AtLeast` when Version is empty
	// (e.g., "1.5.0"). pip resolves the upper bound.
	AtLeast string `yaml:"at_least,omitempty"`
	// Material pins mkdocs-material to an exact release (e.g.,
	// "9.5.49"). Empty leaves the baked-in version in place.
	Material string `yaml:"material,omitempty"`
	// ExtraPackages is an arbitrary list of pip packages to install
	// alongside mkdocs (e.g., "pymdown-extensions==10.11.2",
	// "mkdocs-mermaid2-plugin", "mike==2.1.3"). Each entry is passed
	// verbatim to `pip install` — full pip syntax (`==`, `>=`, extras,
	// VCS URLs) is supported.
	//
	// SECURITY: ExtraPackages runs in the operator's trust domain.
	// Anyone who can land a sitebuild.yaml change can install
	// arbitrary code in the build container — same authority `RUN` and
	// `dockerfile_prebuild` already grant. Treat changes to this field
	// with the same review discipline as changes to a Dockerfile.
	ExtraPackages []string `yaml:"extra_packages,omitempty"`
}

// IsZero reports whether the config requests no overrides — used to
// short-circuit synth-prebuild generation for the common case.
func (m *MkDocsVersionConfig) IsZero() bool {
	if m == nil {
		return true
	}
	return m.Version == "" && m.AtLeast == "" && m.Material == "" && len(m.ExtraPackages) == 0
}

// BuildProfile defines a named build configuration (e.g., "production", "staging").
type BuildProfile struct {
	// Hugo overrides the top-level hugo version config for this profile
	Hugo *HugoVersionConfig `yaml:"hugo,omitempty"`
	// MkDocs overrides the top-level mkdocs version config for this profile
	MkDocs *MkDocsVersionConfig `yaml:"mkdocs,omitempty"`
	// DockerfilePrebuild contains Dockerfile commands to extend the builder image.
	// A derived image is built from these commands before running the build.
	DockerfilePrebuild string `yaml:"dockerfile_prebuild,omitempty"`
	// Commands is the COMMANDLIST (WORKDIR, HUGO, CP, FINALIZE, etc.)
	Commands string `yaml:"commands"`
	// ServeDir is the absolute path inside the container to mount as a volume
	// for staging mode. When set, the profile is treated as a staging profile.
	ServeDir string `yaml:"servedir,omitempty"`
	// BuildCommand is the command to re-run when a staging rebuild is triggered.
	BuildCommand string `yaml:"build_command,omitempty"`
}

// IsStaging returns true if this profile is a staging profile (has a servedir configured).
func (p *BuildProfile) IsStaging() bool {
	return p.ServeDir != ""
}

// ParseSiteBuildConfig parses a sitebuild.yaml file.
func ParseSiteBuildConfig(data []byte) (*SiteBuildConfig, error) {
	var cfg SiteBuildConfig
	if err := yaml.UnmarshalStrict(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse sitebuild.yaml: %w", err)
	}
	if cfg.BuilderImage == "" {
		cfg.BuilderImage = DefaultBuilderImage
	}
	if err := cfg.ApplyDefs(); err != nil {
		return nil, fmt.Errorf("applying defs: %w", err)
	}
	return &cfg, nil
}

// GetProfile returns the build profile for the given name.
//
// When the SiteBuildConfig has explicit configs (operator-defined),
// looks up `name` in those.
//
// When configs is empty (no sitebuild.yaml, or one without a configs
// section), auto-detects the generator from marker files in repoDir
// and returns a default profile sized to that generator. The shape
// (production vs staging) is chosen from `name` — `name == "staging"`
// returns a staging-shaped profile (servedir + build_command, no
// FINALIZE); anything else returns a production-shaped profile.
//
// Auto-detection precedence: mkdocs > astro > gatsby > hugo. When
// multiple generator markers are present (e.g. a Hugo→MkDocs
// migration that left config.toml around), the higher-precedence one
// wins and the others are reported in a Warnf log so the misfire is
// diagnosable.
//
// If detection finds no marker, returns ErrNoBuilderDetected.
//
// repoDir may be empty when called from contexts that don't have one
// (e.g. legacy callers); in that case a missing configs section is
// treated as a hard error.
func (c *SiteBuildConfig) GetProfile(name, repoDir string) (*BuildProfile, error) {
	if len(c.Configs) == 0 {
		det := DetectGenerator(repoDir)
		if det.Generator == "" {
			return nil, ErrNoBuilderDetected
		}
		if len(det.Ignored) > 0 {
			log.Warnf("sitedeploy: auto-detected %s from %s; ignored %v — set sitebuild.yaml configs to override",
				det.Generator, det.Marker, det.Ignored)
		} else {
			log.Infof("sitedeploy: auto-detected %s from %s", det.Generator, det.Marker)
		}
		staging := strings.EqualFold(name, "staging")
		profile := defaultProfileFor(det.Generator, staging)
		if profile == nil {
			return nil, fmt.Errorf("no default profile available for generator %q", det.Generator)
		}
		return profile, nil
	}
	profile, ok := c.Configs[name]
	if !ok {
		return nil, fmt.Errorf("build profile %q not found in sitebuild.yaml (available: %s)", name, availableProfiles(c.Configs))
	}
	if profile.Commands == "" {
		return nil, fmt.Errorf("build profile %q has empty commands", name)
	}
	return profile, nil
}

// GeneratorDetection records the result of inspecting a repo for a
// site-generator marker file. When multiple markers are present
// (e.g. during a Hugo→MkDocs migration), Generator/Marker name the
// winner and Ignored lists the rest so callers can surface the
// conflict in logs.
type GeneratorDetection struct {
	Generator string   // "mkdocs", "hugo", "astro", "gatsby", or "" if none found
	Marker    string   // the file we matched, e.g. "mkdocs.yml"
	Ignored   []string // other markers we saw and overruled
}

// DetectGenerator inspects repoDir for known generator marker files
// in priority order: mkdocs > astro > gatsby > hugo. The first
// matching marker within the highest-priority generator wins. Any
// other markers found (within the winning generator's alternates
// *or* in lower-priority generators) are reported in Ignored.
//
// Empty repoDir or unreadable directory returns an empty Generator
// (callers treat that as "no generator detected").
func DetectGenerator(repoDir string) GeneratorDetection {
	if repoDir == "" {
		return GeneratorDetection{}
	}

	// Order = precedence. Earlier wins.
	candidates := []struct {
		gen     string
		markers []string
	}{
		{"mkdocs", []string{"mkdocs.yml", "mkdocs.yaml"}},
		{"astro", []string{"astro.config.mjs", "astro.config.ts", "astro.config.js"}},
		{"gatsby", []string{"gatsby-config.js", "gatsby-config.ts"}},
		{"hugo", []string{"hugo.toml", "hugo.yaml", "hugo.yml", "config.toml", "config.yaml", "config.yml"}},
	}

	var det GeneratorDetection
	for _, c := range candidates {
		for _, m := range c.markers {
			if _, err := os.Stat(filepath.Join(repoDir, m)); err != nil {
				continue
			}
			if det.Generator == "" {
				det.Generator = c.gen
				det.Marker = m
			} else {
				det.Ignored = append(det.Ignored, m)
			}
		}
	}
	return det
}

// defaultProfileFor returns the canonical default BuildProfile for a
// detected generator. Each profile follows the same shape:
//
//   - Production: WORKDIR /builder, run the generator, FINALIZE its
//     default output directory.
//   - Staging: WORKDIR /builder, ServeDir + BuildCommand pointing at
//     the same default output directory; no FINALIZE (staging
//     profiles forbid it — see ValidateCommandListForStaging).
//
// Returns nil for unknown generators.
func defaultProfileFor(gen string, staging bool) *BuildProfile {
	switch gen {
	case "mkdocs":
		// mkdocs writes to ./site/ relative to mkdocs.yml by
		// default, which would collide with hulabuild's <workdir>/site
		// source layout. Force --site-dir to a sibling directory.
		const siteDir = "_hula_out"
		if staging {
			return &BuildProfile{
				ServeDir:     "/builder/site/" + siteDir,
				BuildCommand: "MKDOCS build --site-dir " + siteDir,
				Commands: "WORKDIR /builder\n" +
					"MKDOCS build --site-dir " + siteDir + "\n",
			}
		}
		return &BuildProfile{
			Commands: "WORKDIR /builder\n" +
				"MKDOCS build --strict --site-dir " + siteDir + "\n" +
				"FINALIZE /builder/site/" + siteDir + "\n",
		}
	case "hugo":
		if staging {
			return &BuildProfile{
				ServeDir:     "/builder/site/public",
				BuildCommand: "HUGO",
				Commands: "WORKDIR /builder\n" +
					"HUGO\n",
			}
		}
		return &BuildProfile{
			Commands: "WORKDIR /builder\n" +
				"HUGO --minify\n" +
				"FINALIZE /builder/site/public\n",
		}
	case "astro":
		if staging {
			return &BuildProfile{
				ServeDir:     "/builder/site/dist",
				BuildCommand: "ASTRO build",
				Commands: "WORKDIR /builder\n" +
					"ASTRO build\n",
			}
		}
		return &BuildProfile{
			Commands: "WORKDIR /builder\n" +
				"ASTRO build\n" +
				"FINALIZE /builder/site/dist\n",
		}
	case "gatsby":
		if staging {
			return &BuildProfile{
				ServeDir:     "/builder/site/public",
				BuildCommand: "GATSBY build",
				Commands: "WORKDIR /builder\n" +
					"GATSBY build\n",
			}
		}
		return &BuildProfile{
			Commands: "WORKDIR /builder\n" +
				"GATSBY build\n" +
				"FINALIZE /builder/site/public\n",
		}
	}
	return nil
}

// ResolveHugoConfig returns the effective hugo version config, with profile-level overrides.
func (c *SiteBuildConfig) ResolveHugoConfig(profile *BuildProfile) *HugoVersionConfig {
	if profile.Hugo != nil {
		return profile.Hugo
	}
	return c.Hugo
}

// ResolveMkDocsConfig returns the effective mkdocs version config,
// with profile-level overrides taking precedence over top-level.
func (c *SiteBuildConfig) ResolveMkDocsConfig(profile *BuildProfile) *MkDocsVersionConfig {
	if profile.MkDocs != nil {
		return profile.MkDocs
	}
	return c.MkDocs
}

// synthMkDocsPrebuild returns a Dockerfile fragment that pip-installs
// the requested mkdocs / mkdocs-material / extra packages over the
// base builder image. Empty when the config requests no overrides.
//
// The output is a single `RUN pip3 install` line so the derived image
// has one extra layer. Versions go through pip's standard specifier
// syntax — `version` becomes `==X`, `at_least` becomes `>=X`. We
// don't pass --break-system-packages explicitly; both builder images
// set ENV PIP_BREAK_SYSTEM_PACKAGES=1 (see builder-images/*/Dockerfile)
// so the synth works uniformly across alpine and ubuntu bases.
func synthMkDocsPrebuild(cfg *MkDocsVersionConfig) string {
	if cfg.IsZero() {
		return ""
	}
	var pkgs []string
	switch {
	case cfg.Version != "":
		pkgs = append(pkgs, "mkdocs=="+cfg.Version)
	case cfg.AtLeast != "":
		pkgs = append(pkgs, "mkdocs>="+cfg.AtLeast)
	}
	if cfg.Material != "" {
		pkgs = append(pkgs, "mkdocs-material=="+cfg.Material)
	}
	pkgs = append(pkgs, cfg.ExtraPackages...)
	if len(pkgs) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("# synthesized from sitebuild.yaml mkdocs:\n")
	b.WriteString("RUN pip3 install --no-cache-dir \\\n")
	for i, p := range pkgs {
		b.WriteString("    ")
		b.WriteString(p)
		if i < len(pkgs)-1 {
			b.WriteString(" \\")
		}
		b.WriteString("\n")
	}
	return b.String()
}

// EffectivePrebuild returns the Dockerfile prebuild fragment that
// should be passed to buildDerivedImage for this profile, combining:
//
//  1. Synthesized mkdocs install lines (when MkDocsVersionConfig is set)
//  2. The operator's explicit DockerfilePrebuild block (when set)
//
// In that order — synth first so operators can write follow-up RUN
// lines that depend on the pinned versions. Returns empty string when
// no derivation is needed.
func (c *SiteBuildConfig) EffectivePrebuild(profile *BuildProfile) string {
	var parts []string
	if mkdocs := c.ResolveMkDocsConfig(profile); !mkdocs.IsZero() {
		if synth := synthMkDocsPrebuild(mkdocs); synth != "" {
			parts = append(parts, synth)
		}
	}
	if profile.DockerfilePrebuild != "" {
		parts = append(parts, profile.DockerfilePrebuild)
	}
	return strings.Join(parts, "\n")
}

// BuilderImageName returns the full Docker image name for the builder.
func (c *SiteBuildConfig) BuilderImageName() string {
	name := c.BuilderImage
	if name == "" || name == "default" {
		name = "alpine-default"
	}
	return "hula-builder-" + name
}

func availableProfiles(configs map[string]*BuildProfile) string {
	names := make([]string, 0, len(configs))
	for name := range configs {
		names = append(names, name)
	}
	return fmt.Sprintf("%v", names)
}

// ApplyDefs substitutes defs variables into all profile string fields using mustache.
func (c *SiteBuildConfig) ApplyDefs() error {
	if len(c.Defs) == 0 {
		return nil
	}
	for name, profile := range c.Configs {
		var err error
		if profile.Commands != "" {
			profile.Commands, err = renderMustache(profile.Commands, c.Defs)
			if err != nil {
				return fmt.Errorf("profile %s commands: %w", name, err)
			}
		}
		if profile.ServeDir != "" {
			profile.ServeDir, err = renderMustache(profile.ServeDir, c.Defs)
			if err != nil {
				return fmt.Errorf("profile %s servedir: %w", name, err)
			}
		}
		if profile.BuildCommand != "" {
			profile.BuildCommand, err = renderMustache(profile.BuildCommand, c.Defs)
			if err != nil {
				return fmt.Errorf("profile %s build_command: %w", name, err)
			}
		}
		if profile.DockerfilePrebuild != "" {
			profile.DockerfilePrebuild, err = renderMustache(profile.DockerfilePrebuild, c.Defs)
			if err != nil {
				return fmt.Errorf("profile %s dockerfile_prebuild: %w", name, err)
			}
		}
	}
	return nil
}

func renderMustache(template string, vars map[string]string) (string, error) {
	tmpl, err := mustache.ParseString(template)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := tmpl.FRender(&buf, vars); err != nil {
		return "", err
	}
	return buf.String(), nil
}
