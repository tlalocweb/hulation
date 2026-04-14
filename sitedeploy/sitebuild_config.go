package sitedeploy

import (
	"fmt"

	"gopkg.in/yaml.v2"
)

const (
	DefaultBuilderImage = "default"
	DefaultBuildProfile = "production"
)

// SiteBuildConfig represents the .hula/sitebuild.yaml file in a site repository.
type SiteBuildConfig struct {
	BuilderImage string                  `yaml:"builder_image,omitempty"`
	Hugo         *HugoVersionConfig      `yaml:"hugo,omitempty"`
	Configs      map[string]*BuildProfile `yaml:"configs"`
}

// HugoVersionConfig specifies hugo version requirements.
type HugoVersionConfig struct {
	// AtLeast specifies the minimum required version (e.g., "0.159.1")
	AtLeast string `yaml:"at_least,omitempty"`
	// Version specifies an exact version (e.g., "0.159.1")
	Version string `yaml:"version,omitempty"`
}

// BuildProfile defines a named build configuration (e.g., "production", "staging").
type BuildProfile struct {
	// Hugo overrides the top-level hugo version config for this profile
	Hugo *HugoVersionConfig `yaml:"hugo,omitempty"`
	// DockerfilePrebuild contains Dockerfile commands to extend the builder image.
	// A derived image is built from these commands before running the build.
	DockerfilePrebuild string `yaml:"dockerfile_prebuild,omitempty"`
	// Commands is the COMMANDLIST (WORKDIR, HUGO, CP, FINALIZE, etc.)
	Commands string `yaml:"commands"`
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
	return &cfg, nil
}

// GetProfile returns the build profile for the given name.
// If no configs are defined, it returns a default production profile.
func (c *SiteBuildConfig) GetProfile(name string) (*BuildProfile, error) {
	if len(c.Configs) == 0 {
		// No configs defined, return sane default
		return &BuildProfile{
			Commands: "WORKDIR /builder\nHUGO --minify\nCP -r public/* site/\nFINALIZE /site\n",
		}, nil
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

// ResolveHugoConfig returns the effective hugo version config, with profile-level overrides.
func (c *SiteBuildConfig) ResolveHugoConfig(profile *BuildProfile) *HugoVersionConfig {
	if profile.Hugo != nil {
		return profile.Hugo
	}
	return c.Hugo
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
