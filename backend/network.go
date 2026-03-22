package backend

import (
	"context"
	"fmt"
	"strings"

	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/network"
	"github.com/tlalocweb/hulation/log"
)

const networkPrefix = "hula_"

// networkNameForServer creates a deterministic Docker network name from a server host.
// Example: "example.com" -> "hula_example_com"
func networkNameForServer(serverHost string) string {
	sanitized := strings.ReplaceAll(serverHost, ".", "_")
	sanitized = strings.ReplaceAll(sanitized, ":", "_")
	sanitized = strings.ReplaceAll(sanitized, "-", "_")
	return networkPrefix + sanitized
}

// createNetwork creates a Docker bridge network for a server's backends.
// If the network already exists, it returns its ID.
func (m *Manager) createNetwork(ctx context.Context, serverHost string) (string, error) {
	name := networkNameForServer(serverHost)

	// Check if network already exists
	networks, err := m.cli.NetworkList(ctx, network.ListOptions{
		Filters: filters.NewArgs(filters.Arg("name", name)),
	})
	if err != nil {
		return "", fmt.Errorf("failed to list networks: %w", err)
	}
	for _, n := range networks {
		if n.Name == name {
			log.Infof("backend: reusing existing network %s (id=%s)", name, n.ID[:12])
			return n.ID, nil
		}
	}

	// Create a new bridge network
	resp, err := m.cli.NetworkCreate(ctx, name, network.CreateOptions{
		Driver: "bridge",
		Labels: map[string]string{
			"managed-by":  "hulation",
			"server-host": serverHost,
		},
	})
	if err != nil {
		return "", fmt.Errorf("failed to create network %s: %w", name, err)
	}
	log.Infof("backend: created network %s (id=%s) for server %s", name, resp.ID[:12], serverHost)
	return resp.ID, nil
}

// removeNetwork removes the Docker network for a server.
func (m *Manager) removeNetwork(ctx context.Context, serverHost string) error {
	name := networkNameForServer(serverHost)

	m.mu.Lock()
	networkID, exists := m.networks[serverHost]
	if exists {
		delete(m.networks, serverHost)
	}
	m.mu.Unlock()

	if !exists {
		return nil
	}

	// Disconnect self first if we're in Docker
	if m.inDocker && m.selfID != "" {
		_ = m.cli.NetworkDisconnect(ctx, networkID, m.selfID, true)
	}

	err := m.cli.NetworkRemove(ctx, networkID)
	if err != nil {
		return fmt.Errorf("failed to remove network %s: %w", name, err)
	}
	log.Infof("backend: removed network %s for server %s", name, serverHost)
	return nil
}

// connectSelfToNetwork connects the hulation container to a backend network.
// This is only needed when hulation is running inside Docker.
func (m *Manager) connectSelfToNetwork(ctx context.Context, networkID string) error {
	if !m.inDocker || m.selfID == "" {
		return nil
	}

	err := m.cli.NetworkConnect(ctx, networkID, m.selfID, nil)
	if err != nil {
		// Ignore "already connected" errors
		if strings.Contains(err.Error(), "already exists") {
			log.Debugf("backend: hulation container already connected to network %s", networkID[:12])
			return nil
		}
		return fmt.Errorf("failed to connect hulation to network %s: %w", networkID[:12], err)
	}
	log.Infof("backend: connected hulation container %s to network %s", m.selfID[:12], networkID[:12])
	return nil
}
