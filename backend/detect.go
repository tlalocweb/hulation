package backend

import (
	"os"
	"strings"

	"github.com/tlalocweb/hulation/log"
)

// IsRunningInDocker detects whether hulation is running inside a Docker container.
func IsRunningInDocker() bool {
	// Explicit override via environment variable
	if val := os.Getenv("HULATION_IN_DOCKER"); val != "" {
		switch strings.ToLower(val) {
		case "true", "1", "yes":
			return true
		case "false", "0", "no":
			return false
		}
	}
	// Standard Docker indicator file
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}
	return false
}

// GetSelfContainerID attempts to discover the container ID of the current process
// when running inside Docker. Returns empty string if not in Docker or cannot determine.
func GetSelfContainerID() string {
	// Docker typically sets hostname to the short container ID
	hostname, err := os.Hostname()
	if err != nil {
		log.Debugf("backend: could not get hostname for self-detection: %s", err)
		return ""
	}

	// Docker container IDs are 12-char hex strings (short form)
	if len(hostname) == 12 && isHex(hostname) {
		return hostname
	}

	// Fallback: read from cgroup (works on cgroup v1)
	data, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		parts := strings.SplitN(line, ":", 3)
		if len(parts) == 3 {
			// Look for docker container ID in the cgroup path
			path := parts[2]
			if idx := strings.LastIndex(path, "/docker/"); idx != -1 {
				id := path[idx+len("/docker/"):]
				if len(id) >= 12 {
					return id[:12]
				}
			}
			if idx := strings.LastIndex(path, "/docker-"); idx != -1 {
				// containerd format: /docker-<id>.scope
				id := path[idx+len("/docker-"):]
				id = strings.TrimSuffix(id, ".scope")
				if len(id) >= 12 {
					return id[:12]
				}
			}
		}
	}

	return ""
}

func isHex(s string) bool {
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}
