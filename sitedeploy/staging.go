package sitedeploy

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/google/uuid"
	"github.com/tlalocweb/hulation/backend"
	"github.com/tlalocweb/hulation/config"
	"github.com/tlalocweb/hulation/log"
)

// StagingContainer represents a long-lived builder container in staging mode.
type StagingContainer struct {
	mu          sync.Mutex
	ContainerID string
	ServerID    string
	ImageName   string
	HostDir     string         // host-side path (gad.StagingDir)
	ServeDir    string         // container-side path (profile.ServeDir)
	WorkDir     string         // from WORKDIR command in profile.Commands
	BuildCmd    string         // profile.BuildCommand
	conn        io.WriteCloser // stdin connection to hulabuild
	stdout      *bufio.Reader  // stdout from hulabuild
	Running     bool
}

// StagingManager manages long-lived staging containers.
type StagingManager struct {
	mu         sync.Mutex
	cli        *client.Client
	containers map[string]*StagingContainer // serverID -> container
}

// NewStagingManager creates a new StagingManager with the given Docker client.
func NewStagingManager(cli *client.Client) *StagingManager {
	return &StagingManager{
		cli:        cli,
		containers: make(map[string]*StagingContainer),
	}
}

// resolveHostPath translates a container-internal path to the actual host path
// by inspecting hula's own container mounts. This is needed because when hula
// runs inside Docker, paths like /var/hula/sitedeploy are backed by Docker
// volumes whose host location differs from the in-container path. The builder
// container's bind mount must use the host path so both containers share data.
//
// Returns the original path unchanged if not running in Docker or if no
// matching mount is found.
func (sm *StagingManager) resolveHostPath(ctx context.Context, containerPath string) string {
	if !backend.IsRunningInDocker() {
		return containerPath
	}
	selfID := backend.GetSelfContainerID()
	if selfID == "" {
		log.Warnf("sitedeploy: staging: cannot determine own container ID for volume resolution")
		return containerPath
	}

	info, err := sm.cli.ContainerInspect(ctx, selfID)
	if err != nil {
		log.Warnf("sitedeploy: staging: cannot inspect own container: %s", err)
		return containerPath
	}

	// Find the mount whose Destination is a prefix of containerPath
	bestMatch := ""
	bestSource := ""
	for _, m := range info.Mounts {
		dest := m.Destination
		if strings.HasPrefix(containerPath, dest) && len(dest) > len(bestMatch) {
			bestMatch = dest
			bestSource = m.Source
		}
	}
	if bestMatch == "" {
		return containerPath
	}

	// Replace the mount destination prefix with the host source path
	hostPath := bestSource + containerPath[len(bestMatch):]
	log.Debugf("sitedeploy: staging: resolved %s -> %s (via mount %s -> %s)", containerPath, hostPath, bestMatch, bestSource)
	return hostPath
}

// GetStagingContainer returns the staging container for a server, or nil.
func (sm *StagingManager) GetStagingContainer(serverID string) *StagingContainer {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	return sm.containers[serverID]
}

// StartStaging starts a long-lived staging container for the given server.
func (sm *StagingManager) StartStaging(server *config.Server, profile *BuildProfile, imageName string) error {
	gad := server.GitAutoDeploy
	ctx := context.Background()

	// Create host staging directory
	if err := os.MkdirAll(gad.StagingDir, 0o755); err != nil {
		return fmt.Errorf("creating staging dir %s: %w", gad.StagingDir, err)
	}

	// Extract workdir from the commands
	workDir := extractWorkDir(profile.Commands)
	if workDir == "" {
		workDir = "/builder"
	}

	sc := &StagingContainer{
		ServerID:  server.ID,
		ImageName: imageName,
		HostDir:   gad.StagingDir,
		ServeDir:  profile.ServeDir,
		WorkDir:   workDir,
		BuildCmd:  strings.TrimSpace(profile.BuildCommand),
	}

	// Create the builder container with volume mount
	builder := newBuilderContainer(sm.cli)
	if err := builder.ensureImage(ctx, imageName); err != nil {
		return err
	}

	// Build derived image if needed
	if profile.DockerfilePrebuild != "" {
		_, err := builder.buildDerivedImage(ctx, imageName, profile.DockerfilePrebuild)
		if err != nil {
			return fmt.Errorf("building derived image: %w", err)
		}
	}

	// Volume bind: host staging dir -> container serve dir.
	// When hula runs inside Docker, the in-container path (gad.StagingDir) differs
	// from the actual host path backing the Docker volume. We must resolve to the
	// real host path so the builder container's bind mount points to the same data.
	hostStagingDir := sm.resolveHostPath(ctx, gad.StagingDir)
	sc.HostDir = gad.StagingDir // hula serves from its own container path
	binds := []string{hostStagingDir + ":" + profile.ServeDir}
	commandListText := profile.Commands + "\n---\n"

	conn, stdout, err := builder.startContainer(ctx, commandListText, gad.BuildEnv, binds)
	if err != nil {
		return fmt.Errorf("starting staging container: %w", err)
	}
	sc.ContainerID = builder.containerID
	sc.conn = conn
	sc.stdout = stdout

	// Send command list to hulabuild
	if _, err := conn.Write([]byte(commandListText)); err != nil {
		builder.cleanup(ctx)
		conn.Close()
		return fmt.Errorf("writing command list: %w", err)
	}

	// Run protocol loop for initial setup (WORKDIR triggers source transfer).
	// Loop until we see READY (staging mode entered) or an error.
	repoDir := gad.DataDir
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		line := scanner.Text()
		cleanLine := cleanDockerStreamLine(line)
		if cleanLine == "" {
			continue
		}

		cmd, arg, err := ParseProtocolMessage(cleanLine)
		if err != nil {
			continue
		}

		switch cmd {
		case MsgSendTarballTo:
			log.Infof("sitedeploy: staging %s: transferring source to %s", server.ID, arg)
			tarReader, err := createSourceTarball(repoDir)
			if err != nil {
				builder.cleanup(ctx)
				conn.Close()
				return fmt.Errorf("creating source tarball: %w", err)
			}
			if err := builder.copyToContainer(ctx, arg, tarReader); err != nil {
				builder.cleanup(ctx)
				conn.Close()
				return fmt.Errorf("copying source to container: %w", err)
			}
			if _, err := conn.Write([]byte(MsgInboundTarballReady + "\n")); err != nil {
				builder.cleanup(ctx)
				conn.Close()
				return fmt.Errorf("sending INBOUND_TARBALL_READY: %w", err)
			}
			log.Infof("sitedeploy: staging %s: source transferred", server.ID)

		case MsgReady:
			log.Infof("sitedeploy: staging %s: hulabuild entered staging mode", server.ID)
			goto ready

		case MsgBuildLog:
			log.Debugf("sitedeploy: staging %s: %s", server.ID, arg)

		case MsgBuildError:
			builder.cleanup(ctx)
			conn.Close()
			return fmt.Errorf("staging build error: %s", arg)
		}
	}
	// If we get here without seeing READY, something went wrong
	builder.cleanup(ctx)
	conn.Close()
	return fmt.Errorf("hulabuild exited without entering staging mode")

ready:
	// Run initial build if build_command is configured
	if sc.BuildCmd != "" {
		log.Infof("sitedeploy: staging %s: running initial build: %s", server.ID, sc.BuildCmd)
		if _, err := conn.Write([]byte(MsgExecBuild + " " + sc.BuildCmd + "\n")); err != nil {
			builder.cleanup(ctx)
			conn.Close()
			return fmt.Errorf("sending initial EXEC_BUILD: %w", err)
		}

		// Wait for BUILD_DONE or BUILD_ERROR
		for scanner.Scan() {
			line := scanner.Text()
			cleanLine := cleanDockerStreamLine(line)
			if cleanLine == "" {
				continue
			}
			cmd, arg, err := ParseProtocolMessage(cleanLine)
			if err != nil {
				continue
			}
			switch cmd {
			case MsgBuildDone:
				log.Infof("sitedeploy: staging %s: initial build complete", server.ID)
				goto buildDone
			case MsgBuildLog:
				log.Debugf("sitedeploy: staging %s: %s", server.ID, arg)
			case MsgBuildError:
				// Log the error but don't tear down — the container stays alive for retries
				log.Errorf("sitedeploy: staging %s: initial build error: %s", server.ID, arg)
				goto buildDone
			}
		}
	}

buildDone:
	sc.Running = true
	sm.mu.Lock()
	sm.containers[server.ID] = sc
	sm.mu.Unlock()

	log.Infof("sitedeploy: staging container ready for server %s (id=%s, serving from %s)",
		server.ID, sc.ContainerID[:12], sc.HostDir)
	return nil
}

// RebuildStaging sends an EXEC_BUILD command to the staging container and
// waits for it to complete. Returns a BuildState with the build result.
func (sm *StagingManager) RebuildStaging(serverID string) (*BuildState, error) {
	sm.mu.Lock()
	sc := sm.containers[serverID]
	sm.mu.Unlock()

	if sc == nil {
		return nil, fmt.Errorf("no staging container for server %s", serverID)
	}

	sc.mu.Lock()
	defer sc.mu.Unlock()

	if !sc.Running {
		return nil, fmt.Errorf("staging container for server %s is not running", serverID)
	}

	if sc.BuildCmd == "" {
		return nil, fmt.Errorf("no build_command configured for staging server %s", serverID)
	}

	bs := &BuildState{
		BuildID:    uuid.New().String(),
		ServerID:   serverID,
		Status:     BuildRunning,
		StatusText: "running",
		StartedAt:  time.Now(),
		Logs:       []string{},
	}

	// Send EXEC_BUILD
	if _, err := sc.conn.Write([]byte(MsgExecBuild + " " + sc.BuildCmd + "\n")); err != nil {
		bs.fail(fmt.Errorf("sending EXEC_BUILD: %w", err))
		return bs, fmt.Errorf("sending EXEC_BUILD: %w", err)
	}

	bs.addLog(fmt.Sprintf("Executing: %s", sc.BuildCmd))

	// Read response
	scanner := bufio.NewScanner(sc.stdout)
	for scanner.Scan() {
		line := scanner.Text()
		cleanLine := cleanDockerStreamLine(line)
		if cleanLine == "" {
			continue
		}
		cmd, arg, err := ParseProtocolMessage(cleanLine)
		if err != nil {
			continue
		}
		switch cmd {
		case MsgBuildDone:
			bs.complete()
			return bs, nil
		case MsgBuildLog:
			bs.addLog(arg)
			log.Debugf("sitedeploy: staging rebuild %s: %s", serverID, arg)
		case MsgBuildError:
			bs.fail(fmt.Errorf("%s", arg))
			return bs, fmt.Errorf("staging build error: %s", arg)
		}
	}

	bs.fail(fmt.Errorf("hulabuild connection closed during rebuild"))
	return bs, fmt.Errorf("connection closed during rebuild")
}

// StopStaging stops and removes the staging container for a server.
func (sm *StagingManager) StopStaging(serverID string) error {
	sm.mu.Lock()
	sc := sm.containers[serverID]
	delete(sm.containers, serverID)
	sm.mu.Unlock()

	if sc == nil {
		return nil
	}

	sc.mu.Lock()
	defer sc.mu.Unlock()

	ctx := context.Background()

	// Send shutdown command
	if sc.conn != nil {
		sc.conn.Write([]byte(MsgShutdown + "\n"))
		sc.conn.Close()
	}

	sc.Running = false

	// Stop and remove the container
	if sc.ContainerID != "" {
		timeout := 5
		_ = sm.cli.ContainerStop(ctx, sc.ContainerID, container.StopOptions{Timeout: &timeout})
		if err := sm.cli.ContainerRemove(ctx, sc.ContainerID, container.RemoveOptions{Force: true}); err != nil {
			log.Warnf("sitedeploy: failed to remove staging container %s: %s", sc.ContainerID[:12], err)
		} else {
			log.Infof("sitedeploy: removed staging container %s for server %s", sc.ContainerID[:12], serverID)
		}
	}
	return nil
}

// StartupStaging starts staging containers for all servers configured with staging profiles.
func (sm *StagingManager) StartupStaging(servers []*config.Server) {
	for _, s := range servers {
		if s.GitAutoDeploy == nil || s.GitAutoDeploy.NoPullOnStart {
			continue
		}
		if !isStagingProfile(s) {
			continue
		}

		gad := s.GitAutoDeploy
		siteBuildPath := filepath.Join(gad.DataDir, ".hula", "sitebuild.yaml")
		data, err := os.ReadFile(siteBuildPath)
		if err != nil {
			log.Errorf("sitedeploy: staging startup: reading sitebuild.yaml for %s: %s", s.ID, err)
			continue
		}
		siteCfg, err := ParseSiteBuildConfig(data)
		if err != nil {
			log.Errorf("sitedeploy: staging startup: parsing sitebuild.yaml for %s: %s", s.ID, err)
			continue
		}
		profile, err := siteCfg.GetProfile(gad.HulaBuild)
		if err != nil {
			log.Errorf("sitedeploy: staging startup: profile error for %s: %s", s.ID, err)
			continue
		}

		// Validate staging commands
		commands, err := ParseCommandList(profile.Commands)
		if err != nil {
			log.Errorf("sitedeploy: staging startup: parse commands for %s: %s", s.ID, err)
			continue
		}
		if err := ValidateCommandListForStaging(commands); err != nil {
			log.Errorf("sitedeploy: staging startup: validate commands for %s: %s", s.ID, err)
			continue
		}

		imageName := siteCfg.BuilderImageName()
		if err := sm.StartStaging(s, profile, imageName); err != nil {
			log.Errorf("sitedeploy: staging startup failed for %s: %s", s.ID, err)
		}
	}
}

// Close stops all staging containers.
func (sm *StagingManager) Close() {
	sm.mu.Lock()
	serverIDs := make([]string, 0, len(sm.containers))
	for id := range sm.containers {
		serverIDs = append(serverIDs, id)
	}
	sm.mu.Unlock()

	for _, id := range serverIDs {
		sm.StopStaging(id)
	}
}

// extractWorkDir parses the WORKDIR path from a command list text.
func extractWorkDir(commands string) string {
	for _, line := range strings.Split(commands, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(strings.ToUpper(line), "WORKDIR ") {
			parts := strings.SplitN(line, " ", 2)
			if len(parts) > 1 {
				return strings.TrimSpace(parts[1])
			}
		}
	}
	return ""
}

// Package-level global for the StagingManager
var (
	globalStagingManager *StagingManager
	globalSMmu           sync.RWMutex
)

// SetGlobalStagingManager sets the global StagingManager instance.
func SetGlobalStagingManager(sm *StagingManager) {
	globalSMmu.Lock()
	defer globalSMmu.Unlock()
	globalStagingManager = sm
}

// GetStagingManager returns the global StagingManager instance.
func GetStagingManager() *StagingManager {
	globalSMmu.RLock()
	defer globalSMmu.RUnlock()
	return globalStagingManager
}
