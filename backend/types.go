package backend

import (
	"fmt"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/go-connections/nat"
)

// BackendConfig represents a Docker container backend service that hulation
// reverse-proxies to. Config fields are compose-compatible where possible.
type BackendConfig struct {
	// Hulation-specific fields
	VirtualPath   string `yaml:"virtual_path"`            // URL path hulation serves, e.g. "/api"
	ContainerPath string `yaml:"container_path,omitempty"` // Path on the container, e.g. "/api/v2"
	HealthCheckURL string `yaml:"health_check,omitempty"`  // Optional HTTP health endpoint to poll
	HealthTimeout int    `yaml:"health_timeout,omitempty"` // Seconds to wait for healthy (default 30)

	// Compose-compatible fields
	ContainerName string            `yaml:"container_name"`
	Image         string            `yaml:"image"`
	Expose        []string          `yaml:"expose,omitempty"`
	Restart       string            `yaml:"restart,omitempty"`
	Environment   []string          `yaml:"environment,omitempty"`
	Command       stringOrSlice     `yaml:"command,omitempty"`
	Volumes       []string          `yaml:"volumes,omitempty"`
	Ports         []string          `yaml:"ports,omitempty"`
	Labels        map[string]string `yaml:"labels,omitempty"`
	WorkingDir    string            `yaml:"working_dir,omitempty"`
	Entrypoint    stringOrSlice     `yaml:"entrypoint,omitempty"`
	User          string            `yaml:"user,omitempty"`
	NetworkMode   string            `yaml:"network_mode,omitempty"`
	ExtraHosts    []string          `yaml:"extra_hosts,omitempty"`
	DNS           []string          `yaml:"dns,omitempty"`
	CapAdd        []string          `yaml:"cap_add,omitempty"`
	CapDrop       []string          `yaml:"cap_drop,omitempty"`
	Privileged    bool              `yaml:"privileged,omitempty"`
	ReadOnly      bool              `yaml:"read_only,omitempty"`
	Tmpfs         []string          `yaml:"tmpfs,omitempty"`

	// Runtime fields (not from YAML)
	resolvedAddr string
	networkName  string
	containerID  string
	ready        bool
}

// stringOrSlice handles docker-compose's flexible command/entrypoint syntax:
// either a single string (shell form) or a list of strings (exec form).
type stringOrSlice []string

func (s *stringOrSlice) UnmarshalYAML(unmarshal func(interface{}) error) error {
	// Try string first
	var str string
	if err := unmarshal(&str); err == nil {
		*s = strings.Fields(str)
		return nil
	}
	// Try slice
	var slice []string
	if err := unmarshal(&slice); err != nil {
		return fmt.Errorf("command/entrypoint must be a string or list of strings: %w", err)
	}
	*s = slice
	return nil
}

// GetFirstExposedPort returns the first port from the expose list, or "80" as default.
func (b *BackendConfig) GetFirstExposedPort() string {
	if len(b.Expose) > 0 {
		// Strip any protocol suffix (e.g. "8002/tcp" -> "8002")
		port := strings.Split(b.Expose[0], "/")[0]
		return port
	}
	return "80"
}

// GetProxyTarget returns the resolved HTTP URL for this backend.
func (b *BackendConfig) GetProxyTarget() string {
	return "http://" + b.resolvedAddr
}

// GetResolvedAddr returns the resolved address (host:port) for this backend.
func (b *BackendConfig) GetResolvedAddr() string {
	return b.resolvedAddr
}

// SetResolvedAddr sets the address used to reach this backend.
func (b *BackendConfig) SetResolvedAddr(addr string) {
	b.resolvedAddr = addr
}

// GetContainerID returns the Docker container ID.
func (b *BackendConfig) GetContainerID() string {
	return b.containerID
}

// SetContainerID sets the Docker container ID.
func (b *BackendConfig) SetContainerID(id string) {
	b.containerID = id
}

// IsReady returns whether the backend has passed health checks.
func (b *BackendConfig) IsReady() bool {
	return b.ready
}

// SetReady marks the backend as ready or not.
func (b *BackendConfig) SetReady(ready bool) {
	b.ready = ready
}

// GetNetworkName returns the Docker network name for this backend.
func (b *BackendConfig) GetNetworkName() string {
	return b.networkName
}

// SetNetworkName sets the Docker network name.
func (b *BackendConfig) SetNetworkName(name string) {
	b.networkName = name
}

// ToContainerConfig converts the backend config to Docker API container config.
func (b *BackendConfig) ToContainerConfig() *container.Config {
	cfg := &container.Config{
		Image:  b.Image,
		Labels: b.Labels,
	}

	if len(b.Command) > 0 {
		cfg.Cmd = []string(b.Command)
	}
	if len(b.Entrypoint) > 0 {
		cfg.Entrypoint = []string(b.Entrypoint)
	}
	if b.WorkingDir != "" {
		cfg.WorkingDir = b.WorkingDir
	}
	if b.User != "" {
		cfg.User = b.User
	}

	// Environment
	cfg.Env = b.Environment

	// Exposed ports
	exposed := nat.PortSet{}
	for _, p := range b.Expose {
		port := nat.Port(p)
		if !strings.Contains(p, "/") {
			port = nat.Port(p + "/tcp")
		}
		exposed[port] = struct{}{}
	}
	if len(exposed) > 0 {
		cfg.ExposedPorts = exposed
	}

	return cfg
}

// ToHostConfig converts the backend config to Docker API host config.
func (b *BackendConfig) ToHostConfig(publishPorts bool) *container.HostConfig {
	hc := &container.HostConfig{
		Privileged: b.Privileged,
		ReadonlyRootfs: b.ReadOnly,
		CapAdd:     b.CapAdd,
		CapDrop:    b.CapDrop,
		DNS:        b.DNS,
		ExtraHosts: b.ExtraHosts,
	}

	// Restart policy
	switch b.Restart {
	case "always":
		hc.RestartPolicy = container.RestartPolicy{Name: container.RestartPolicyAlways}
	case "unless-stopped":
		hc.RestartPolicy = container.RestartPolicy{Name: container.RestartPolicyUnlessStopped}
	case "on-failure":
		hc.RestartPolicy = container.RestartPolicy{Name: container.RestartPolicyOnFailure}
	case "no", "":
		hc.RestartPolicy = container.RestartPolicy{Name: container.RestartPolicyDisabled}
	}

	// Binds (volumes in short syntax)
	if len(b.Volumes) > 0 {
		hc.Binds = b.Volumes
	}

	// Tmpfs
	if len(b.Tmpfs) > 0 {
		hc.Tmpfs = make(map[string]string)
		for _, t := range b.Tmpfs {
			parts := strings.SplitN(t, ":", 2)
			if len(parts) == 2 {
				hc.Tmpfs[parts[0]] = parts[1]
			} else {
				hc.Tmpfs[parts[0]] = ""
			}
		}
	}

	// Port bindings (only when publishing ports, i.e. outside Docker)
	if publishPorts && len(b.Expose) > 0 {
		portBindings := nat.PortMap{}
		for _, p := range b.Expose {
			port := p
			if !strings.Contains(port, "/") {
				port = port + "/tcp"
			}
			portBindings[nat.Port(port)] = []nat.PortBinding{
				{HostIP: "127.0.0.1", HostPort: ""}, // empty = random host port
			}
		}
		hc.PortBindings = portBindings
	}

	// Network mode
	if b.NetworkMode != "" {
		hc.NetworkMode = container.NetworkMode(b.NetworkMode)
	}

	return hc
}

// Validate checks that required fields are present.
func (b *BackendConfig) Validate(serverHost string) error {
	if b.ContainerName == "" {
		return fmt.Errorf("server[%s]: backend missing container_name", serverHost)
	}
	if b.Image == "" {
		return fmt.Errorf("server[%s]: backend %s missing image", serverHost, b.ContainerName)
	}
	if b.VirtualPath == "" {
		return fmt.Errorf("server[%s]: backend %s missing virtual_path", serverHost, b.ContainerName)
	}
	if !strings.HasPrefix(b.VirtualPath, "/") {
		return fmt.Errorf("server[%s]: backend %s virtual_path must start with /", serverHost, b.ContainerName)
	}
	if b.ContainerPath != "" && !strings.HasPrefix(b.ContainerPath, "/") {
		return fmt.Errorf("server[%s]: backend %s container_path must start with /", serverHost, b.ContainerName)
	}
	return nil
}
