package backend

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/tlalocweb/hulation/log"
)

const (
	defaultHealthTimeout  = 30 // seconds
	healthPollInterval    = 500 * time.Millisecond
	tcpDialTimeout        = 2 * time.Second
	httpRequestTimeout    = 2 * time.Second
)

// waitForHealthy polls the backend until it is reachable, or until the timeout expires.
func (m *Manager) waitForHealthy(ctx context.Context, b *BackendConfig) error {
	timeout := time.Duration(b.HealthTimeout) * time.Second
	if timeout == 0 {
		timeout = defaultHealthTimeout * time.Second
	}

	deadline := time.Now().Add(timeout)

	if b.HealthCheckURL != "" {
		return m.waitForHTTPHealthy(ctx, b, deadline)
	}
	return m.waitForTCPReady(ctx, b, deadline)
}

// waitForHTTPHealthy polls an HTTP health check endpoint until it returns 2xx.
func (m *Manager) waitForHTTPHealthy(ctx context.Context, b *BackendConfig, deadline time.Time) error {
	healthURL := b.GetProxyTarget() + b.HealthCheckURL
	httpClient := &http.Client{Timeout: httpRequestTimeout}

	log.Debugf("backend: waiting for %s health check at %s", b.ContainerName, healthURL)

	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		resp, err := httpClient.Get(healthURL)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				log.Infof("backend: %s health check passed (HTTP %d)", b.ContainerName, resp.StatusCode)
				b.SetReady(true)
				return nil
			}
			log.Debugf("backend: %s health check returned HTTP %d, retrying", b.ContainerName, resp.StatusCode)
		} else {
			log.Debugf("backend: %s health check error: %s, retrying", b.ContainerName, err)
		}

		time.Sleep(healthPollInterval)
	}

	return fmt.Errorf("backend %s did not become healthy within %s (url: %s)",
		b.ContainerName, time.Until(deadline)+time.Since(deadline), healthURL)
}

// waitForTCPReady polls a TCP connection to the backend's resolved address until it succeeds.
func (m *Manager) waitForTCPReady(ctx context.Context, b *BackendConfig, deadline time.Time) error {
	addr := b.GetResolvedAddr()
	log.Debugf("backend: waiting for %s TCP readiness at %s", b.ContainerName, addr)

	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		conn, err := net.DialTimeout("tcp", addr, tcpDialTimeout)
		if err == nil {
			conn.Close()
			log.Infof("backend: %s TCP connection succeeded at %s", b.ContainerName, addr)
			b.SetReady(true)
			return nil
		}
		log.Debugf("backend: %s TCP connect failed: %s, retrying", b.ContainerName, err)

		time.Sleep(healthPollInterval)
	}

	return fmt.Errorf("backend %s not reachable at %s within timeout", b.ContainerName, addr)
}
