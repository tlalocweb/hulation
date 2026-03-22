package backend

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
	"github.com/tlalocweb/hulation/log"
)

// Manager manages Docker containers for backend services.
type Manager struct {
	mu         sync.Mutex
	cli        *client.Client
	inDocker   bool
	selfID     string            // own container ID (if running in Docker)
	networks   map[string]string // serverHost -> networkID
	containers map[string]string // containerName -> containerID
}

// NewManager creates a new backend manager with a Docker client.
func NewManager() (*Manager, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("failed to create Docker client: %w", err)
	}

	// Verify Docker is reachable
	ctx := context.Background()
	_, err = cli.Ping(ctx)
	if err != nil {
		cli.Close()
		return nil, fmt.Errorf("Docker daemon not reachable: %w", err)
	}

	inDocker := IsRunningInDocker()
	selfID := ""
	if inDocker {
		selfID = GetSelfContainerID()
		if selfID != "" {
			log.Infof("backend: running inside Docker, self container ID: %s", selfID)
		} else {
			log.Warnf("backend: detected Docker environment but could not determine own container ID")
		}
	} else {
		log.Infof("backend: running outside Docker, will publish ports to host")
	}

	return &Manager{
		cli:        cli,
		inDocker:   inDocker,
		selfID:     selfID,
		networks:   make(map[string]string),
		containers: make(map[string]string),
	}, nil
}

// Close closes the Docker client.
func (m *Manager) Close() error {
	if m.cli != nil {
		return m.cli.Close()
	}
	return nil
}

// StartBackendsForServer creates a Docker network for the server, pulls images,
// creates and starts all backend containers, then waits for health checks.
func (m *Manager) StartBackendsForServer(ctx context.Context, serverHost string, backends []*BackendConfig) error {
	if len(backends) == 0 {
		return nil
	}

	log.Infof("backend: starting %d backend(s) for server %s", len(backends), serverHost)

	// Create network for this server
	networkID, err := m.createNetwork(ctx, serverHost)
	if err != nil {
		return fmt.Errorf("failed to create network for server %s: %w", serverHost, err)
	}
	m.mu.Lock()
	m.networks[serverHost] = networkID
	m.mu.Unlock()

	netName := networkNameForServer(serverHost)

	// Connect hulation to the network if running inside Docker
	if err := m.connectSelfToNetwork(ctx, networkID); err != nil {
		return err
	}

	// Start each backend
	for _, b := range backends {
		b.SetNetworkName(netName)

		if err := m.pullImage(ctx, b.Image); err != nil {
			return err
		}

		if err := m.createAndStartContainer(ctx, b, networkID, netName); err != nil {
			return err
		}

		// Resolve the address for proxying
		m.resolveAddress(b)

		// Wait for the backend to be healthy
		if err := m.waitForHealthy(ctx, b); err != nil {
			log.Warnf("backend: %s health check failed: %s (will still attempt to proxy)", b.ContainerName, err)
			// Mark as ready anyway — the health check is best-effort
			b.SetReady(true)
		}

		log.Infof("backend: %s started (addr=%s, virtual_path=%s -> container_path=%s)",
			b.ContainerName, b.GetResolvedAddr(), b.VirtualPath, b.ContainerPath)
	}

	return nil
}

// StopBackendsForServer stops and removes all containers for a server,
// then removes the server's Docker network.
func (m *Manager) StopBackendsForServer(ctx context.Context, serverHost string, backends []*BackendConfig) error {
	for _, b := range backends {
		if err := m.stopAndRemoveContainer(ctx, b); err != nil {
			log.Errorf("backend: error stopping container %s: %s", b.ContainerName, err)
		}
	}
	return m.removeNetwork(ctx, serverHost)
}

// StopAll stops all managed containers and removes all networks.
func (m *Manager) StopAll(ctx context.Context) error {
	m.mu.Lock()
	containersCopy := make(map[string]string, len(m.containers))
	for k, v := range m.containers {
		containersCopy[k] = v
	}
	networksCopy := make(map[string]string, len(m.networks))
	for k, v := range m.networks {
		networksCopy[k] = v
	}
	m.mu.Unlock()

	// Stop all containers
	for name, id := range containersCopy {
		log.Infof("backend: stopping container %s (%s)", name, id[:12])
		timeout := 10 // seconds
		err := m.cli.ContainerStop(ctx, id, container.StopOptions{Timeout: &timeout})
		if err != nil {
			log.Errorf("backend: error stopping container %s: %s", name, err)
		}
		err = m.cli.ContainerRemove(ctx, id, container.RemoveOptions{Force: true})
		if err != nil {
			log.Errorf("backend: error removing container %s: %s", name, err)
		}
	}

	// Remove all networks
	for serverHost, networkID := range networksCopy {
		// Disconnect self first
		if m.inDocker && m.selfID != "" {
			_ = m.cli.NetworkDisconnect(ctx, networkID, m.selfID, true)
		}
		err := m.cli.NetworkRemove(ctx, networkID)
		if err != nil {
			log.Errorf("backend: error removing network for %s: %s", serverHost, err)
		} else {
			log.Infof("backend: removed network for server %s", serverHost)
		}
	}

	m.mu.Lock()
	m.containers = make(map[string]string)
	m.networks = make(map[string]string)
	m.mu.Unlock()

	return nil
}

// pullImage pulls a Docker image, logging progress.
func (m *Manager) pullImage(ctx context.Context, imageName string) error {
	log.Infof("backend: pulling image %s", imageName)
	reader, err := m.cli.ImagePull(ctx, imageName, image.PullOptions{})
	if err != nil {
		return fmt.Errorf("failed to pull image %s: %w", imageName, err)
	}
	defer reader.Close()
	// Drain the reader to complete the pull
	_, _ = io.Copy(io.Discard, reader)
	log.Infof("backend: pulled image %s", imageName)
	return nil
}

// createAndStartContainer creates and starts a Docker container for a backend.
func (m *Manager) createAndStartContainer(ctx context.Context, b *BackendConfig, networkID, netName string) error {
	// Check if container already exists
	existing, err := m.cli.ContainerInspect(ctx, b.ContainerName)
	if err == nil {
		// Container exists
		if existing.State.Running {
			log.Infof("backend: container %s already running (id=%s)", b.ContainerName, existing.ID[:12])
			b.SetContainerID(existing.ID)
			m.mu.Lock()
			m.containers[b.ContainerName] = existing.ID
			m.mu.Unlock()
			return nil
		}
		// Exists but not running — remove and recreate
		log.Infof("backend: removing stopped container %s", b.ContainerName)
		_ = m.cli.ContainerRemove(ctx, existing.ID, container.RemoveOptions{Force: true})
	}

	containerCfg := b.ToContainerConfig()
	hostCfg := b.ToHostConfig(!m.inDocker)

	// Networking: attach to the server's network
	networkingCfg := &network.NetworkingConfig{
		EndpointsConfig: map[string]*network.EndpointSettings{
			netName: {
				NetworkID: networkID,
			},
		},
	}

	resp, err := m.cli.ContainerCreate(ctx, containerCfg, hostCfg, networkingCfg, nil, b.ContainerName)
	if err != nil {
		return fmt.Errorf("failed to create container %s: %w", b.ContainerName, err)
	}

	if len(resp.Warnings) > 0 {
		for _, w := range resp.Warnings {
			log.Warnf("backend: container %s warning: %s", b.ContainerName, w)
		}
	}

	b.SetContainerID(resp.ID)
	m.mu.Lock()
	m.containers[b.ContainerName] = resp.ID
	m.mu.Unlock()

	// Start the container
	err = m.cli.ContainerStart(ctx, resp.ID, container.StartOptions{})
	if err != nil {
		return fmt.Errorf("failed to start container %s: %w", b.ContainerName, err)
	}

	log.Infof("backend: started container %s (id=%s)", b.ContainerName, resp.ID[:12])
	return nil
}

// stopAndRemoveContainer stops and removes a single container.
func (m *Manager) stopAndRemoveContainer(ctx context.Context, b *BackendConfig) error {
	id := b.GetContainerID()
	if id == "" {
		return nil
	}

	timeout := 10
	err := m.cli.ContainerStop(ctx, id, container.StopOptions{Timeout: &timeout})
	if err != nil {
		log.Warnf("backend: error stopping container %s: %s", b.ContainerName, err)
	}

	err = m.cli.ContainerRemove(ctx, id, container.RemoveOptions{Force: true})
	if err != nil {
		return fmt.Errorf("failed to remove container %s: %w", b.ContainerName, err)
	}

	m.mu.Lock()
	delete(m.containers, b.ContainerName)
	m.mu.Unlock()

	log.Infof("backend: removed container %s", b.ContainerName)
	b.SetContainerID("")
	b.SetReady(false)
	return nil
}

// resolveAddress determines the address hulation should use to proxy to the backend.
func (m *Manager) resolveAddress(b *BackendConfig) {
	port := b.GetFirstExposedPort()

	if m.inDocker {
		// Inside Docker: address by container name on the shared network
		b.SetResolvedAddr(fmt.Sprintf("%s:%s", b.ContainerName, port))
		return
	}

	// Outside Docker: inspect the container to find the published host port
	info, err := m.cli.ContainerInspect(context.Background(), b.GetContainerID())
	if err != nil {
		log.Warnf("backend: could not inspect container %s, falling back to localhost:%s", b.ContainerName, port)
		b.SetResolvedAddr(fmt.Sprintf("127.0.0.1:%s", port))
		return
	}

	portKey := port + "/tcp"
	if bindings, ok := info.NetworkSettings.Ports[nat.Port(portKey)]; ok && len(bindings) > 0 {
		hostPort := bindings[0].HostPort
		b.SetResolvedAddr(fmt.Sprintf("127.0.0.1:%s", hostPort))
		log.Debugf("backend: %s published on host port %s", b.ContainerName, hostPort)
		return
	}

	// Fallback: try without /tcp suffix
	for natPort, bindings := range info.NetworkSettings.Ports {
		if strings.HasPrefix(string(natPort), port) && len(bindings) > 0 {
			hostPort := bindings[0].HostPort
			b.SetResolvedAddr(fmt.Sprintf("127.0.0.1:%s", hostPort))
			return
		}
	}

	log.Warnf("backend: no published port found for %s, falling back to localhost:%s", b.ContainerName, port)
	b.SetResolvedAddr(fmt.Sprintf("127.0.0.1:%s", port))
}

