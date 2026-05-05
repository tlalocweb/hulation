package sitedeploy

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/client"
	"github.com/google/uuid"
	"github.com/tlalocweb/hulation/config"
	"github.com/tlalocweb/hulation/log"
)

// BuildStatus represents the state of a build.
type BuildStatus int

const (
	BuildPending BuildStatus = iota
	BuildCloning
	BuildPreparingImage
	BuildStartingContainer
	BuildTransferringSource
	BuildRunning
	BuildExtractingResult
	BuildDeploying
	BuildComplete
	BuildFailed
)

var buildStatusText = map[BuildStatus]string{
	BuildPending:            "pending",
	BuildCloning:            "cloning",
	BuildPreparingImage:     "preparing_image",
	BuildStartingContainer:  "starting_container",
	BuildTransferringSource: "transferring_source",
	BuildRunning:            "running",
	BuildExtractingResult:   "extracting_result",
	BuildDeploying:          "deploying",
	BuildComplete:           "complete",
	BuildFailed:             "failed",
}

func (s BuildStatus) String() string {
	if t, ok := buildStatusText[s]; ok {
		return t
	}
	return "unknown"
}

// BuildState tracks the state of a single build.
type BuildState struct {
	BuildID    string      `json:"build_id"`
	ServerID   string      `json:"server_id"`
	Status     BuildStatus `json:"status"`
	StatusText string      `json:"status_text"`
	StartedAt  time.Time   `json:"started_at"`
	EndedAt    *time.Time  `json:"ended_at,omitempty"`
	Logs       []string    `json:"logs"`
	Error      string      `json:"error,omitempty"`
	mu         sync.Mutex
}

func (bs *BuildState) setStatus(status BuildStatus) {
	bs.mu.Lock()
	defer bs.mu.Unlock()
	bs.Status = status
	bs.StatusText = status.String()
}

func (bs *BuildState) addLog(msg string) {
	bs.mu.Lock()
	defer bs.mu.Unlock()
	bs.Logs = append(bs.Logs, msg)
}

func (bs *BuildState) fail(err error) {
	bs.mu.Lock()
	defer bs.mu.Unlock()
	bs.Status = BuildFailed
	bs.StatusText = "failed"
	bs.Error = err.Error()
	now := time.Now()
	bs.EndedAt = &now
}

func (bs *BuildState) complete() {
	bs.mu.Lock()
	defer bs.mu.Unlock()
	bs.Status = BuildComplete
	bs.StatusText = "complete"
	now := time.Now()
	bs.EndedAt = &now
}

// BuildStateSnapshot is a copy of BuildState safe for JSON marshaling (no mutex).
type BuildStateSnapshot struct {
	BuildID    string      `json:"build_id"`
	ServerID   string      `json:"server_id"`
	Status     BuildStatus `json:"status"`
	StatusText string      `json:"status_text"`
	StartedAt  time.Time   `json:"started_at"`
	EndedAt    *time.Time  `json:"ended_at,omitempty"`
	Logs       []string    `json:"logs"`
	Error      string      `json:"error,omitempty"`
}

// Snapshot returns a copy of the build state safe for JSON marshaling.
func (bs *BuildState) Snapshot() BuildStateSnapshot {
	bs.mu.Lock()
	defer bs.mu.Unlock()
	logs := make([]string, len(bs.Logs))
	copy(logs, bs.Logs)
	return BuildStateSnapshot{
		BuildID:    bs.BuildID,
		ServerID:   bs.ServerID,
		Status:     bs.Status,
		StatusText: bs.StatusText,
		StartedAt:  bs.StartedAt,
		EndedAt:    bs.EndedAt,
		Logs:       logs,
		Error:      bs.Error,
	}
}

// BuildManager orchestrates site builds.
type BuildManager struct {
	mu           sync.Mutex
	cli          *client.Client
	serverLocks  map[string]*sync.Mutex // serverID -> lock
	builds       map[string]*BuildState // buildID -> state
	serverBuilds map[string][]string    // serverID -> [buildID, ...]
	maxHistory   int                    // max builds to retain per server
}

const defaultMaxHistory = 10

// NewBuildManager creates a new BuildManager with a Docker client.
func NewBuildManager() (*BuildManager, error) {
	// Check that git is available (needed for clone/pull)
	if _, err := exec.LookPath("git"); err != nil {
		return nil, fmt.Errorf("git not found in PATH: site deployment requires git to be installed")
	}

	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("failed to create Docker client for site deploy: %w (if running in Docker, ensure /var/run/docker.sock is mounted)", err)
	}

	ctx := context.Background()
	if _, err := cli.Ping(ctx); err != nil {
		cli.Close()
		return nil, fmt.Errorf("Docker daemon not reachable for site deploy: %w (if running in Docker, mount -v /var/run/docker.sock:/var/run/docker.sock)", err)
	}

	// Sweep any builder containers left over from a previous hula
	// run that didn't get to clean up (crash, SIGKILL, or the prior
	// cancelled-context bug in BuilderContainer.cleanup). Safe to
	// run unconditionally — no builds are active yet at this point.
	sweepOrphanBuilders(ctx, cli)

	return &BuildManager{
		cli:          cli,
		serverLocks:  make(map[string]*sync.Mutex),
		builds:       make(map[string]*BuildState),
		serverBuilds: make(map[string][]string),
		maxHistory:   defaultMaxHistory,
	}, nil
}

// Close closes the Docker client.
func (bm *BuildManager) Close() error {
	if bm.cli != nil {
		return bm.cli.Close()
	}
	return nil
}

// DockerClient returns the underlying Docker client for sharing with other managers.
func (bm *BuildManager) DockerClient() *client.Client {
	return bm.cli
}

// GetBuild returns the build state for a given build ID.
func (bm *BuildManager) GetBuild(buildID string) *BuildState {
	bm.mu.Lock()
	defer bm.mu.Unlock()
	return bm.builds[buildID]
}

// GetBuildsForServer returns all build states for a server, newest first.
func (bm *BuildManager) GetBuildsForServer(serverID string) []BuildStateSnapshot {
	bm.mu.Lock()
	defer bm.mu.Unlock()
	ids := bm.serverBuilds[serverID]
	result := make([]BuildStateSnapshot, 0, len(ids))
	for i := len(ids) - 1; i >= 0; i-- {
		if bs, ok := bm.builds[ids[i]]; ok {
			result = append(result, bs.Snapshot())
		}
	}
	return result
}

// IsBuilding returns true if a build is currently in progress for the server.
func (bm *BuildManager) IsBuilding(serverID string) bool {
	bm.mu.Lock()
	defer bm.mu.Unlock()
	ids := bm.serverBuilds[serverID]
	for _, id := range ids {
		if bs, ok := bm.builds[id]; ok {
			bs.mu.Lock()
			status := bs.Status
			bs.mu.Unlock()
			if status != BuildComplete && status != BuildFailed {
				return true
			}
		}
	}
	return false
}

// TriggerBuild starts a build asynchronously. Returns the build ID.
// Returns an error if a build is already in progress for this server.
func (bm *BuildManager) TriggerBuild(server *config.Server, args []string) (string, error) {
	if server.GitAutoDeploy == nil {
		return "", fmt.Errorf("server %s has no git autodeploy config", server.ID)
	}

	if bm.IsBuilding(server.ID) {
		return "", fmt.Errorf("build already in progress for server %s", server.ID)
	}

	buildID := uuid.New().String()
	bs := &BuildState{
		BuildID:    buildID,
		ServerID:   server.ID,
		Status:     BuildPending,
		StatusText: "pending",
		StartedAt:  time.Now(),
		Logs:       []string{},
	}

	bm.mu.Lock()
	bm.builds[buildID] = bs
	bm.serverBuilds[server.ID] = append(bm.serverBuilds[server.ID], buildID)
	// Trim history
	if len(bm.serverBuilds[server.ID]) > bm.maxHistory {
		oldID := bm.serverBuilds[server.ID][0]
		bm.serverBuilds[server.ID] = bm.serverBuilds[server.ID][1:]
		delete(bm.builds, oldID)
	}
	// Get or create per-server lock
	serverLock, ok := bm.serverLocks[server.ID]
	if !ok {
		serverLock = &sync.Mutex{}
		bm.serverLocks[server.ID] = serverLock
	}
	bm.mu.Unlock()

	// Run the build in a goroutine
	go func() {
		serverLock.Lock()
		defer serverLock.Unlock()
		bm.executeBuild(server, bs, args)
	}()

	return buildID, nil
}

// executeBuild runs the full build workflow synchronously.
func (bm *BuildManager) executeBuild(server *config.Server, bs *BuildState, args []string) {
	ctx := context.Background()
	gad := server.GitAutoDeploy

	// Step 1: Clone or pull the repository
	bs.setStatus(BuildCloning)
	bs.addLog("Cloning/pulling repository...")

	repoDir, err := CloneOrPull(gad)
	if err != nil {
		bs.fail(fmt.Errorf("git clone/pull: %w", err))
		log.Errorf("sitedeploy: build %s failed at clone: %s", bs.BuildID, err)
		return
	}
	bs.addLog(fmt.Sprintf("Repository ready at %s", repoDir))

	// Step 2: Read and parse .hula/sitebuild.yaml
	siteBuildPath := filepath.Join(repoDir, ".hula", "sitebuild.yaml")
	data, err := os.ReadFile(siteBuildPath)
	if err != nil {
		bs.fail(fmt.Errorf("reading sitebuild.yaml: %w", err))
		log.Errorf("sitedeploy: build %s failed reading sitebuild.yaml: %s", bs.BuildID, err)
		return
	}

	siteCfg, err := ParseSiteBuildConfig(data)
	if err != nil {
		bs.fail(err)
		log.Errorf("sitedeploy: build %s failed parsing sitebuild.yaml: %s", bs.BuildID, err)
		return
	}

	// Step 3: Resolve the build profile
	profileName := gad.HulaBuild
	profile, err := siteCfg.GetProfile(profileName)
	if err != nil {
		bs.fail(err)
		log.Errorf("sitedeploy: build %s failed resolving profile %s: %s", bs.BuildID, profileName, err)
		return
	}
	bs.addLog(fmt.Sprintf("Using build profile: %s", profileName))

	// Step 4: Parse and validate command list
	commands, err := ParseCommandList(profile.Commands)
	if err != nil {
		bs.fail(fmt.Errorf("parsing command list: %w", err))
		return
	}
	if err := ValidateCommandList(commands); err != nil {
		bs.fail(fmt.Errorf("validating command list: %w", err))
		return
	}

	// Step 5: Prepare builder image
	bs.setStatus(BuildPreparingImage)
	builder := newBuilderContainer(bm.cli)
	defer builder.cleanup(ctx)

	imageName := siteCfg.BuilderImageName()
	if err := builder.ensureImage(ctx, imageName); err != nil {
		bs.fail(err)
		log.Errorf("sitedeploy: build %s: %s", bs.BuildID, err)
		return
	}

	// Build derived image if prebuild commands exist
	if profile.DockerfilePrebuild != "" {
		bs.addLog("Building derived image for prebuild commands...")
		_, err := builder.buildDerivedImage(ctx, imageName, profile.DockerfilePrebuild)
		if err != nil {
			bs.fail(fmt.Errorf("building derived image: %w", err))
			return
		}
		bs.addLog("Derived image ready")
	}

	// Step 6: Start builder container
	bs.setStatus(BuildStartingContainer)
	bs.addLog("Starting builder container...")

	// Send command list to hulabuild via stdin
	commandListText := profile.Commands + "\n---\n"
	conn, stdout, err := builder.startContainer(ctx, commandListText, gad.BuildEnv, nil)
	if err != nil {
		bs.fail(err)
		return
	}
	defer conn.Close()

	// Write the command list to stdin
	if _, err := conn.Write([]byte(commandListText)); err != nil {
		bs.fail(fmt.Errorf("writing command list to container: %w", err))
		return
	}

	bs.setStatus(BuildRunning)

	// Step 7: Protocol loop - read from stdout, respond on stdin
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		line := scanner.Text()
		// Docker multiplexed streams have an 8-byte header; handle raw lines too
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
			bs.setStatus(BuildTransferringSource)
			bs.addLog(fmt.Sprintf("Transferring site source to container at %s ...", arg))

			// Create tarball of repo source
			tarReader, err := createSourceTarball(repoDir)
			if err != nil {
				bs.fail(fmt.Errorf("creating source tarball: %w", err))
				return
			}

			// Docker cp into the container
			if err := builder.copyToContainer(ctx, arg, tarReader); err != nil {
				bs.fail(fmt.Errorf("copying source to container: %w", err))
				return
			}

			// Tell hulabuild the tarball is ready
			bs.setStatus(BuildRunning)
			if _, err := conn.Write([]byte(MsgInboundTarballReady + "\n")); err != nil {
				bs.fail(fmt.Errorf("sending INBOUND_TARBALL_READY: %w", err))
				return
			}
			bs.addLog("Site source transferred successfully")

		case MsgOutboundTarballReady:
			bs.setStatus(BuildExtractingResult)
			bs.addLog(fmt.Sprintf("Extracting built site from %s ...", arg))

			// Docker cp from the container
			reader, err := builder.copyFromContainer(ctx, arg)
			if err != nil {
				bs.fail(fmt.Errorf("copying built site from container: %w", err))
				return
			}

			// Deploy to server's Root directory
			bs.setStatus(BuildDeploying)
			if err := deploySite(reader, server.Root); err != nil {
				reader.Close()
				bs.fail(fmt.Errorf("deploying site: %w", err))
				return
			}
			reader.Close()
			bs.addLog("Site deployed successfully")

		case MsgBuildLog:
			bs.addLog(arg)
			log.Debugf("sitedeploy: [build %s] %s", bs.BuildID[:8], arg)

		case MsgBuildError:
			bs.fail(fmt.Errorf("build error: %s", arg))
			log.Errorf("sitedeploy: build %s error: %s", bs.BuildID, arg)
			return
		}
	}

	if err := scanner.Err(); err != nil {
		bs.fail(fmt.Errorf("reading builder output: %w", err))
		return
	}

	// Record the commit hash we just built
	currentHead := getGitHead(repoDir)
	if currentHead != "" {
		markerFile := filepath.Join(filepath.Dir(gad.DeployDir), ".last-build-commit")
		os.MkdirAll(filepath.Dir(markerFile), 0o755)
		os.WriteFile(markerFile, []byte(currentHead), 0o644)
	}

	bs.complete()
	log.Infof("sitedeploy: build %s completed successfully for server %s", bs.BuildID, server.ID)
}

// deploySite extracts the built site tarball to the server's root directory.
// Uses atomic deployment: writes to a temp dir, then swaps.
func deploySite(tarReader io.ReadCloser, rootDir string) error {
	if rootDir == "" {
		return fmt.Errorf("server root directory not configured")
	}

	// Create temp directory alongside root
	parentDir := filepath.Dir(rootDir)
	tmpDir, err := os.MkdirTemp(parentDir, ".hula-deploy-")
	if err != nil {
		return fmt.Errorf("creating temp deploy dir: %w", err)
	}

	// Extract to temp dir
	if err := extractSiteTarball(tarReader, tmpDir); err != nil {
		os.RemoveAll(tmpDir)
		return fmt.Errorf("extracting site tarball: %w", err)
	}

	// Atomic swap: rename old root, rename temp to root, remove old
	oldDir := rootDir + ".old"
	os.RemoveAll(oldDir) // clean up any previous .old

	// If root exists, rename it
	if _, err := os.Stat(rootDir); err == nil {
		if err := os.Rename(rootDir, oldDir); err != nil {
			os.RemoveAll(tmpDir)
			return fmt.Errorf("moving old root: %w", err)
		}
	}

	// Move new site into place
	if err := os.Rename(tmpDir, rootDir); err != nil {
		// Rollback: restore old root
		if _, statErr := os.Stat(oldDir); statErr == nil {
			os.Rename(oldDir, rootDir)
		}
		return fmt.Errorf("moving new site to root: %w", err)
	}

	// Clean up old root
	os.RemoveAll(oldDir)

	log.Infof("sitedeploy: site deployed to %s", rootDir)
	return nil
}

// cleanDockerStreamLine handles Docker's multiplexed stream format.
// Docker streams have an 8-byte header before each frame.
// If the line starts with non-printable chars, skip the header bytes.
func cleanDockerStreamLine(line string) string {
	// Docker multiplexed streams: first byte is stream type (1=stdout, 2=stderr),
	// bytes 4-7 are big-endian frame size. The payload follows.
	// If we get raw text (no header), just return it.
	if len(line) == 0 {
		return ""
	}
	// If starts with a printable ASCII char, it's raw text
	if line[0] >= 32 && line[0] < 127 {
		return line
	}
	// Otherwise, try to skip the 8-byte Docker stream header
	if len(line) > 8 {
		return line[8:]
	}
	return ""
}

// siteNeedsBuild returns true if the deploy directory is missing or empty.
func siteNeedsBuild(deployDir string) bool {
	entries, err := os.ReadDir(deployDir)
	if err != nil {
		return true // doesn't exist or can't read
	}
	return len(entries) == 0
}

// warnOnProfileMismatch logs a one-line warn when the operator-chosen
// HulaBuild profile name disagrees with the repo's sitebuild.yaml.
// E.g. operator picked `hula_build: staging` but the matching profile
// has no `servedir`, or the operator picked production but the
// profile is staging-shaped. Static root is fixed at config-load time
// from gad.HulaBuild and isn't changed here — this is informational.
func warnOnProfileMismatch(s *config.Server, repoDir string) {
	gad := s.GitAutoDeploy
	if gad == nil {
		return
	}
	siteBuildPath := filepath.Join(repoDir, ".hula", "sitebuild.yaml")
	data, err := os.ReadFile(siteBuildPath)
	if err != nil {
		log.Debugf("sitedeploy: no sitebuild.yaml for %s: %s", s.ID, err)
		return
	}
	siteCfg, err := ParseSiteBuildConfig(data)
	if err != nil {
		log.Errorf("sitedeploy: parse sitebuild.yaml for %s: %s", s.ID, err)
		return
	}
	profile, err := siteCfg.GetProfile(gad.HulaBuild)
	if err != nil {
		log.Errorf("sitedeploy: profile %q not found in sitebuild.yaml for %s: %s", gad.HulaBuild, s.ID, err)
		return
	}
	operatorWantsStaging := strings.EqualFold(gad.HulaBuild, "staging")
	repoSaysStaging := profile.IsStaging()
	switch {
	case operatorWantsStaging && !repoSaysStaging:
		log.Warnf("sitedeploy: server %s — hula_build=%q implies staging but profile lacks servedir; static root remains %s",
			s.ID, gad.HulaBuild, s.Root)
	case !operatorWantsStaging && repoSaysStaging:
		log.Warnf("sitedeploy: server %s — hula_build=%q has servedir (staging-shaped) but operator picked production root %s",
			s.ID, gad.HulaBuild, s.Root)
	case operatorWantsStaging:
		log.Infof("sitedeploy: server %s — staging mode, serving from %s", s.ID, s.Root)
	}
}

// isStagingProfile returns true when the operator picked a staging
// build profile (i.e. `hula_build: staging` in YAML). Source of truth
// is the operator's intent — we don't read sitebuild.yaml here
// because that would require a clone we haven't necessarily done yet.
// warnOnProfileMismatch (called from StartupBuildAll after the clone
// succeeds) catches the case where the operator's intent disagrees
// with the repo's actual profile shape.
func isStagingProfile(s *config.Server) bool {
	gad := s.GitAutoDeploy
	if gad == nil {
		return false
	}
	return strings.EqualFold(gad.HulaBuild, "staging")
}

// StartupBuildAll processes all servers with root_git_autodeploy sequentially.
// For each server (unless no_pull_on_start is set), it clones/pulls the repo
// and builds the site if it hasn't been built yet or if updates are available.
// Servers are processed one at a time to avoid overloading the system.
func (bm *BuildManager) StartupBuildAll(servers []*config.Server) {
	for _, s := range servers {
		if s.GitAutoDeploy == nil || s.GitAutoDeploy.NoPullOnStart {
			continue
		}

		// Skip staging servers — they are handled by StagingManager
		if isStagingProfile(s) {
			log.Infof("sitedeploy: skipping production build for staging server %s", s.ID)
			continue
		}

		needsBuild := siteNeedsBuild(s.Root)
		gad := s.GitAutoDeploy

		if !needsBuild {
			// Site exists — check if repo has updates by doing a pull
			log.Infof("sitedeploy: startup check for %s — site exists at %s, checking for updates", s.ID, s.Root)
		} else {
			log.Infof("sitedeploy: startup build for %s — site not yet built at %s", s.ID, s.Root)
		}

		// Clone or pull the repo
		repoDir, err := CloneOrPull(gad)
		if err != nil {
			log.Errorf("sitedeploy: startup clone/pull failed for %s: %s", s.ID, err)
			continue
		}

		// Sanity check: the operator's HulaBuild value should match
		// the profile's nature in sitebuild.yaml. Doesn't change
		// behaviour, just surfaces config drift.
		warnOnProfileMismatch(s, repoDir)

		if !needsBuild {
			// Repo was pulled. Check if HEAD changed since last build.
			// We store the last-built commit in a marker file.
			markerFile := filepath.Join(filepath.Dir(gad.DeployDir), ".last-build-commit")
			currentHead := getGitHead(repoDir)
			if currentHead != "" {
				lastBuilt, _ := os.ReadFile(markerFile)
				if string(lastBuilt) == currentHead {
					log.Infof("sitedeploy: %s is up to date (commit %s), skipping build", s.ID, currentHead[:min(len(currentHead), 8)])
					continue
				}
			}
			log.Infof("sitedeploy: %s has updates, rebuilding", s.ID)
		}

		// Run a synchronous build
		buildID := uuid.New().String()
		bs := &BuildState{
			BuildID:    buildID,
			ServerID:   s.ID,
			Status:     BuildPending,
			StatusText: "pending",
			StartedAt:  time.Now(),
			Logs:       []string{"startup build"},
		}

		bm.mu.Lock()
		bm.builds[buildID] = bs
		bm.serverBuilds[s.ID] = append(bm.serverBuilds[s.ID], buildID)
		bm.mu.Unlock()

		bm.executeBuild(s, bs, nil)

		if bs.Status == BuildComplete {
			log.Infof("sitedeploy: startup build complete for %s", s.ID)
		} else {
			log.Errorf("sitedeploy: startup build failed for %s: %s", s.ID, bs.Error)
		}
	}
}

// getGitHead returns the current HEAD commit hash from the repo, or "".
func getGitHead(repoDir string) string {
	output, err := runGitOutput(repoDir, "rev-parse", "HEAD")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(output)
}

// Package-level global for the BuildManager
var (
	globalBuildManager *BuildManager
	globalBMmu         sync.RWMutex
)

// SetGlobalBuildManager sets the global BuildManager instance.
func SetGlobalBuildManager(bm *BuildManager) {
	globalBMmu.Lock()
	defer globalBMmu.Unlock()
	globalBuildManager = bm
}

// GetBuildManager returns the global BuildManager instance.
func GetBuildManager() *BuildManager {
	globalBMmu.RLock()
	defer globalBMmu.RUnlock()
	return globalBuildManager
}
