package client

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-webdav"
	"github.com/fsnotify/fsnotify"
)

// buildDebounceDelay is how long we wait after the last file sync before
// triggering an auto-build. Additional syncs during this window reset the timer.
const buildDebounceDelay = 2 * time.Second

// StagingMountOptions configures a staging mount session.
type StagingMountOptions struct {
	ServerID    string
	LocalDir    string                            // absolute path to local mount point
	Dangerous   bool                              // allow executables and security-sensitive files
	AutoBuild   bool                              // trigger a staging build after files are synced
	Output      func(string, ...any) (int, error) // user-facing output (e.g., fmt.Printf)
	ConfirmFunc func() bool                       // ask user to confirm (yes/no)
}

// StagingMounter syncs a local directory with a remote staging folder via WebDAV.
type StagingMounter struct {
	opts         StagingMountOptions
	client       *Client        // hula API client, used for triggering auto-builds
	davClient    *webdav.Client
	davURLPrefix string // URL path prefix of the WebDAV endpoint (e.g., "/api/staging/foo/dav")
	watcher      *fsnotify.Watcher
	ctx          context.Context
	cancel       context.CancelFunc

	// Auto-build state (protected by buildMu)
	buildMu      sync.Mutex
	buildRunning bool        // a build is currently executing
	buildPending bool        // files have changed; a build is needed
	buildTimer   *time.Timer // debounce timer; fires triggerBuild
}

// bearerHTTPClient wraps an http.Client to inject a Bearer token on every request.
// Implements webdav.HTTPClient.
type bearerHTTPClient struct {
	inner *http.Client
	token string
}

func (b *bearerHTTPClient) Do(req *http.Request) (*http.Response, error) {
	req.Header.Set("Authorization", "Bearer "+b.token)
	return b.inner.Do(req)
}

// NewStagingMounter creates a new StagingMounter.
func NewStagingMounter(ctx context.Context, c *Client, opts StagingMountOptions) (*StagingMounter, error) {
	davURLPrefix := "/api/staging/" + opts.ServerID + "/dav"
	davEndpoint := c.GetAPIUrl() + davURLPrefix

	authHTTP := &bearerHTTPClient{
		inner: c.GetHTTPClient(),
		token: c.GetToken(),
	}

	davClient, err := webdav.NewClient(authHTTP, davEndpoint)
	if err != nil {
		return nil, fmt.Errorf("creating WebDAV client: %w", err)
	}

	innerCtx, cancel := context.WithCancel(ctx)

	return &StagingMounter{
		opts:         opts,
		client:       c,
		davClient:    davClient,
		davURLPrefix: davURLPrefix,
		ctx:          innerCtx,
		cancel:       cancel,
	}, nil
}

// remotePath converts a relative path (with or without leading slash) to the
// form the WebDAV client expects: a relative path that will be joined with
// the endpoint URL path. An empty path means the WebDAV root.
func (m *StagingMounter) remotePath(rel string) string {
	rel = strings.TrimPrefix(rel, "/")
	if rel == "" {
		return "."
	}
	return rel
}

// stripDAVPrefix strips the WebDAV endpoint URL prefix from paths returned
// by the server (e.g., PROPFIND responses).
func (m *StagingMounter) stripDAVPrefix(p string) string {
	p = strings.TrimPrefix(p, m.davURLPrefix)
	p = strings.TrimPrefix(p, "/")
	return p
}

// Close shuts down the mounter and releases resources.
func (m *StagingMounter) Close() {
	m.cancel()
	if m.watcher != nil {
		m.watcher.Close()
	}
	m.buildMu.Lock()
	if m.buildTimer != nil {
		m.buildTimer.Stop()
	}
	m.buildMu.Unlock()
}

// ---------- Auto-build ----------

// scheduleBuild marks a build as pending and resets the build debounce timer.
// Called from processPending when AutoBuild is enabled and files were synced.
func (m *StagingMounter) scheduleBuild() {
	if !m.opts.AutoBuild {
		return
	}
	m.buildMu.Lock()
	defer m.buildMu.Unlock()
	m.buildPending = true
	if m.buildTimer == nil {
		m.buildTimer = time.AfterFunc(buildDebounceDelay, m.triggerBuild)
	} else {
		m.buildTimer.Reset(buildDebounceDelay)
	}
}

// triggerBuild fires when the build-debounce timer elapses. Starts a build if
// one isn't already running. If a build is running, the buildPending flag
// ensures another build is scheduled after it completes.
func (m *StagingMounter) triggerBuild() {
	m.buildMu.Lock()
	if m.buildRunning {
		// A build is already in flight; leave buildPending so we re-trigger after
		m.buildMu.Unlock()
		return
	}
	if !m.buildPending {
		m.buildMu.Unlock()
		return
	}
	m.buildRunning = true
	m.buildPending = false
	m.buildMu.Unlock()

	m.opts.Output("  Auto-build: triggering build...\n")
	go func() {
		_, result, err := m.client.StagingBuild(m.opts.ServerID)

		// Special case: server returned 409 — a build is already running server-side.
		// Mark as still pending and re-arm the debounce timer.
		if cerr, ok := err.(*ClientError); ok && cerr.StatusCode == http.StatusConflict {
			m.opts.Output("  Auto-build: server busy, will retry\n")
			m.buildMu.Lock()
			m.buildRunning = false
			m.buildPending = true
			m.buildMu.Unlock()
			m.scheduleBuild()
			return
		}

		m.buildMu.Lock()
		m.buildRunning = false
		pending := m.buildPending
		m.buildMu.Unlock()

		if err != nil {
			m.opts.Output("  Auto-build: failed: %s\n", err)
		} else if result != nil && result.Error != "" {
			m.opts.Output("  Auto-build: error: %s\n", result.Error)
		} else {
			m.opts.Output("  Auto-build: complete\n")
		}

		// If files changed while the build was running, schedule another
		if pending {
			m.scheduleBuild()
		}
	}()
}

// ---------- Security filter ----------

// forbiddenPatterns are glob patterns for security-sensitive files.
var forbiddenPatterns = []string{
	".ssh", ".ssh/*",
	".env", ".env.*",
	"*.key", "*.pem", "*.p12", "*.pfx", "*.jks", "*.keystore",
	"id_rsa*", "id_ed25519*", "id_dsa*", "id_ecdsa*",
	".gnupg", ".gnupg/*",
	".aws", ".aws/*",
	"passwd", "shadow", "htpasswd", ".htpasswd",
	".netrc", ".pgpass",
	"credentials.json", "service-account*.json",
}

// isForbidden checks whether a file should be skipped for security reasons.
// Returns (true, reason) if the file should be skipped.
func isForbidden(relPath string, info os.FileInfo, dangerous bool) (bool, string) {
	if dangerous {
		return false, ""
	}

	// Check executable permission bits
	if !info.IsDir() && info.Mode()&0111 != 0 {
		return true, "executable file"
	}

	// Check against blocked patterns
	base := filepath.Base(relPath)
	for _, pattern := range forbiddenPatterns {
		if matched, _ := filepath.Match(pattern, base); matched {
			return true, "security-sensitive file"
		}
		if matched, _ := filepath.Match(pattern, relPath); matched {
			return true, "security-sensitive file"
		}
	}

	// Check if any path component is a forbidden directory
	parts := strings.Split(filepath.ToSlash(relPath), "/")
	for _, part := range parts {
		for _, pattern := range forbiddenPatterns {
			if matched, _ := filepath.Match(pattern, part); matched {
				return true, "inside security-sensitive directory"
			}
		}
	}

	return false, ""
}

// isTempFile returns true for editor temp files and other noise.
func isTempFile(p string) bool {
	base := filepath.Base(p)
	rel := filepath.ToSlash(p)

	if strings.HasSuffix(base, ".swp") || strings.HasSuffix(base, ".swo") {
		return true
	}
	if strings.HasSuffix(base, "~") {
		return true
	}
	if strings.HasPrefix(base, ".#") {
		return true
	}
	if strings.Contains(base, "__jb_tmp__") || strings.Contains(base, "__jb_old__") {
		return true
	}
	if base == ".DS_Store" {
		return true
	}
	if strings.Contains(rel, ".git/") || strings.HasPrefix(rel, ".git") {
		return true
	}
	if strings.Contains(rel, "node_modules/") {
		return true
	}
	return false
}

// ---------- Initial sync ----------

// InitialSync performs the initial synchronization between local and remote directories.
func (m *StagingMounter) InitialSync() error {
	localDir := m.opts.LocalDir

	// Create local directory if it doesn't exist
	info, err := os.Stat(localDir)
	if os.IsNotExist(err) {
		m.opts.Output("Creating directory %s\n", localDir)
		if err := os.MkdirAll(localDir, 0o755); err != nil {
			return fmt.Errorf("creating local directory: %w", err)
		}
		return m.syncFromRemote()
	}
	if err != nil {
		return fmt.Errorf("stat local directory: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", localDir)
	}

	// Check if local directory is empty
	entries, err := os.ReadDir(localDir)
	if err != nil {
		return fmt.Errorf("reading local directory: %w", err)
	}
	if len(entries) == 0 {
		return m.syncFromRemote()
	}

	// Local directory has files — do sanity check and bidirectional sync
	return m.syncBidirectional()
}

// syncFromRemote downloads all remote files to the (empty) local directory.
func (m *StagingMounter) syncFromRemote() error {
	m.opts.Output("Downloading files from remote staging...\n")

	remoteFiles, err := m.davClient.ReadDir(m.ctx, m.remotePath(""), true)
	if err != nil {
		return fmt.Errorf("listing remote files: %w", err)
	}

	// Filter to only files (ReadDir with recursive=true returns dirs too)
	var files []webdav.FileInfo
	var dirs []webdav.FileInfo
	for _, fi := range remoteFiles {
		relPath := m.stripDAVPrefix(fi.Path)
		if relPath == "" {
			continue // skip the root itself
		}
		if fi.IsDir {
			dirs = append(dirs, fi)
		} else {
			files = append(files, fi)
		}
	}

	// Create directories first
	for _, d := range dirs {
		relPath := m.stripDAVPrefix(d.Path)
		localPath := filepath.Join(m.opts.LocalDir, filepath.FromSlash(relPath))
		os.MkdirAll(localPath, 0o755)
	}

	// Download files
	skipped := 0
	for i, fi := range files {
		relPath := m.stripDAVPrefix(fi.Path)
		if isTempFile(relPath) {
			continue
		}

		localPath := filepath.Join(m.opts.LocalDir, filepath.FromSlash(relPath))

		// We can't check executable bits on remote files, so just check path patterns
		if skip, reason := isForbiddenByPath(relPath, m.opts.Dangerous); skip {
			m.opts.Output("  Skipping: %s (%s - use --dangerous to override)\n", relPath, reason)
			skipped++
			continue
		}

		if err := m.downloadFile(relPath, localPath, fi.ModTime); err != nil {
			m.opts.Output("  Error downloading %s: %s\n", relPath, err)
			continue
		}
		m.opts.Output("  [%d/%d] Downloaded: %s (%s)\n", i+1-skipped, len(files)-skipped, relPath, formatSize(fi.Size))
	}

	m.opts.Output("Initial sync complete: %d files synced, %d skipped\n", len(files)-skipped, skipped)
	return nil
}

// syncBidirectional syncs local and remote with local taking precedence.
func (m *StagingMounter) syncBidirectional() error {
	// Build local file map
	localFiles := make(map[string]os.FileInfo)
	err := filepath.Walk(m.opts.LocalDir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // skip errors
		}
		relPath, _ := filepath.Rel(m.opts.LocalDir, p)
		if relPath == "." || isTempFile(relPath) {
			return nil
		}
		localFiles[filepath.ToSlash(relPath)] = info
		return nil
	})
	if err != nil {
		return fmt.Errorf("walking local directory: %w", err)
	}

	// Build remote file map
	remoteList, err := m.davClient.ReadDir(m.ctx, m.remotePath(""), true)
	if err != nil {
		return fmt.Errorf("listing remote files: %w", err)
	}
	remoteFiles := make(map[string]webdav.FileInfo)
	for _, fi := range remoteList {
		relPath := m.stripDAVPrefix(fi.Path)
		if relPath == "" || isTempFile(relPath) {
			continue
		}
		remoteFiles[relPath] = fi
	}

	// Sanity check — compute overlap
	commonCount := 0
	for k := range localFiles {
		if _, ok := remoteFiles[k]; ok {
			commonCount++
		}
	}
	unionCount := len(localFiles) + len(remoteFiles) - commonCount
	localOnly := len(localFiles) - commonCount
	remoteOnly := len(remoteFiles) - commonCount

	if unionCount > 0 && len(remoteFiles) > 0 && float64(commonCount)/float64(unionCount) < 0.3 {
		m.opts.Output("WARNING: Local and remote directories have very different contents.\n")
		m.opts.Output("  Local only: %d files, Remote only: %d files, Common: %d files\n", localOnly, remoteOnly, commonCount)
		m.opts.Output("  This may not be the correct mount point.\n")
		if !m.opts.ConfirmFunc() {
			return fmt.Errorf("aborted by user")
		}
	}

	m.opts.Output("Syncing (local takes precedence)...\n")
	uploaded, downloaded, skipped := 0, 0, 0

	// Files in both — local takes precedence
	for relPath, localInfo := range localFiles {
		if localInfo.IsDir() {
			continue
		}
		if skip, reason := isForbidden(relPath, localInfo, m.opts.Dangerous); skip {
			m.opts.Output("  Skipping: %s (%s - use --dangerous to override)\n", relPath, reason)
			skipped++
			continue
		}

		localPath := filepath.Join(m.opts.LocalDir, filepath.FromSlash(relPath))

		if remoteInfo, ok := remoteFiles[relPath]; ok {
			// Both exist — upload if local differs
			if localInfo.Size() != remoteInfo.Size || localInfo.ModTime().After(remoteInfo.ModTime) {
				if err := m.uploadFile(localPath, relPath); err != nil {
					m.opts.Output("  Error uploading %s: %s\n", relPath, err)
					continue
				}
				m.opts.Output("  Updated on server: %s (%s)\n", relPath, formatSize(localInfo.Size()))
				uploaded++
			}
		} else {
			// Local only — upload to remote
			if err := m.ensureRemoteDir(path.Dir(relPath)); err != nil {
				m.opts.Output("  Error creating remote dir for %s: %s\n", relPath, err)
				continue
			}
			if err := m.uploadFile(localPath, relPath); err != nil {
				m.opts.Output("  Error uploading %s: %s\n", relPath, err)
				continue
			}
			m.opts.Output("  Uploaded to server: %s (%s)\n", relPath, formatSize(localInfo.Size()))
			uploaded++
		}
	}

	// Remote only — download to local
	for relPath, remoteInfo := range remoteFiles {
		if remoteInfo.IsDir {
			continue
		}
		if _, ok := localFiles[relPath]; ok {
			continue // already handled above
		}
		if isTempFile(relPath) {
			continue
		}
		if skip, reason := isForbiddenByPath(relPath, m.opts.Dangerous); skip {
			m.opts.Output("  Skipping: %s (%s - use --dangerous to override)\n", relPath, reason)
			skipped++
			continue
		}

		localPath := filepath.Join(m.opts.LocalDir, filepath.FromSlash(relPath))

		os.MkdirAll(filepath.Dir(localPath), 0o755)
		if err := m.downloadFile(relPath, localPath, remoteInfo.ModTime); err != nil {
			m.opts.Output("  Error downloading %s: %s\n", relPath, err)
			continue
		}
		m.opts.Output("  Downloaded from server: %s (%s)\n", relPath, formatSize(remoteInfo.Size))
		downloaded++
	}

	// Create remote directories for local-only directories
	for relPath, localInfo := range localFiles {
		if !localInfo.IsDir() {
			continue
		}
		if _, ok := remoteFiles[relPath]; !ok {
			m.ensureRemoteDir(relPath)
		}
	}

	m.opts.Output("Sync complete: %d uploaded, %d downloaded, %d skipped\n", uploaded, downloaded, skipped)
	return nil
}

// ---------- Watch loop ----------

// Watch starts the filesystem watch loop. Blocks until ctx is cancelled.
func (m *StagingMounter) Watch() error {
	var err error
	m.watcher, err = fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("creating filesystem watcher: %w", err)
	}

	if err := m.addWatchRecursive(m.opts.LocalDir); err != nil {
		return fmt.Errorf("setting up filesystem watch: %w", err)
	}

	const debounceDelay = 500 * time.Millisecond
	const maxWait = 2 * time.Second

	pendingPaths := make(map[string]fsnotify.Op)
	hasPending := false

	debounceTimer := time.NewTimer(0)
	if !debounceTimer.Stop() {
		<-debounceTimer.C
	}
	maxWaitTimer := time.NewTimer(0)
	if !maxWaitTimer.Stop() {
		<-maxWaitTimer.C
	}

	for {
		select {
		case <-m.ctx.Done():
			// Flush any remaining pending changes
			if hasPending {
				m.processPending(pendingPaths)
			}
			return nil

		case event, ok := <-m.watcher.Events:
			if !ok {
				return nil
			}

			relPath, err := filepath.Rel(m.opts.LocalDir, event.Name)
			if err != nil {
				continue
			}
			relPath = filepath.ToSlash(relPath)

			if isTempFile(relPath) {
				continue
			}

			// If a new directory was created, add it to the watcher
			if event.Has(fsnotify.Create) {
				if info, err := os.Stat(event.Name); err == nil && info.IsDir() {
					m.watcher.Add(event.Name)
				}
			}

			pendingPaths[relPath] = event.Op

			if !hasPending {
				debounceTimer.Reset(debounceDelay)
				maxWaitTimer.Reset(maxWait)
				hasPending = true
			} else {
				debounceTimer.Reset(debounceDelay)
			}

		case err, ok := <-m.watcher.Errors:
			if !ok {
				return nil
			}
			m.opts.Output("  Watch error: %s\n", err)

		case <-debounceTimer.C:
			if hasPending {
				m.processPending(pendingPaths)
				pendingPaths = make(map[string]fsnotify.Op)
				hasPending = false
				maxWaitTimer.Stop()
			}

		case <-maxWaitTimer.C:
			if hasPending {
				m.processPending(pendingPaths)
				pendingPaths = make(map[string]fsnotify.Op)
				hasPending = false
				debounceTimer.Stop()
			}
		}
	}
}

// processPending syncs all accumulated file changes to the remote.
// If any files were actually synced (uploaded, deleted, or dirs created) and
// AutoBuild is enabled, a build is scheduled with debounce.
func (m *StagingMounter) processPending(pending map[string]fsnotify.Op) {
	synced := false
	for relPath, op := range pending {
		localPath := filepath.Join(m.opts.LocalDir, filepath.FromSlash(relPath))

		// Check if the file still exists (could have been deleted)
		info, statErr := os.Stat(localPath)

		if op.Has(fsnotify.Remove) || op.Has(fsnotify.Rename) {
			if statErr != nil {
				// File is gone — delete on remote
				if err := m.davClient.RemoveAll(m.ctx, m.remotePath(relPath)); err != nil {
					m.opts.Output("  Error deleting remote %s: %s\n", relPath, err)
				} else {
					m.opts.Output("  Deleted remote: %s\n", relPath)
					synced = true
				}
				continue
			}
		}

		if statErr != nil {
			continue // file doesn't exist locally, nothing to do
		}

		if info.IsDir() {
			if op.Has(fsnotify.Create) {
				m.ensureRemoteDir(relPath)
				m.opts.Output("  Created dir: %s\n", relPath)
				synced = true
			}
			continue
		}

		// File create or write — upload
		if skip, reason := isForbidden(relPath, info, m.opts.Dangerous); skip {
			m.opts.Output("  Skipping: %s (%s - use --dangerous to override)\n", relPath, reason)
			continue
		}

		if err := m.ensureRemoteDir(path.Dir(relPath)); err != nil {
			m.opts.Output("  Error creating remote dir for %s: %s\n", relPath, err)
			continue
		}
		if err := m.uploadFile(localPath, relPath); err != nil {
			m.opts.Output("  Error syncing %s: %s\n", relPath, err)
			continue
		}
		m.opts.Output("  Synced: %s (%s)\n", relPath, formatSize(info.Size()))
		synced = true
	}

	if synced {
		m.scheduleBuild()
	}
}

// ---------- Helpers ----------

// uploadFile uploads a local file to the remote WebDAV path.
// remoteRel is a relative path (with or without leading slash) that will be
// joined with the WebDAV endpoint URL.
func (m *StagingMounter) uploadFile(localPath, remoteRel string) error {
	f, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer f.Close()

	wc, err := m.davClient.Create(m.ctx, m.remotePath(remoteRel))
	if err != nil {
		return fmt.Errorf("creating remote file: %w", err)
	}
	if _, err := io.Copy(wc, f); err != nil {
		wc.Close()
		return fmt.Errorf("writing remote file: %w", err)
	}
	return wc.Close()
}

// downloadFile downloads a remote file to a local path and sets its modification time.
func (m *StagingMounter) downloadFile(remoteRel, localPath string, modTime time.Time) error {
	os.MkdirAll(filepath.Dir(localPath), 0o755)

	rc, err := m.davClient.Open(m.ctx, m.remotePath(remoteRel))
	if err != nil {
		return fmt.Errorf("opening remote file: %w", err)
	}
	defer rc.Close()

	f, err := os.Create(localPath)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, rc); err != nil {
		f.Close()
		return fmt.Errorf("writing local file: %w", err)
	}
	f.Close()

	if !modTime.IsZero() {
		os.Chtimes(localPath, modTime, modTime)
	}
	return nil
}

// ensureRemoteDir creates all parent directories on the remote for the given
// relative path. Paths like "/", ".", or "" are treated as the root (no-op).
func (m *StagingMounter) ensureRemoteDir(remoteRel string) error {
	remoteRel = strings.TrimPrefix(remoteRel, "/")
	if remoteRel == "" || remoteRel == "." {
		return nil
	}

	// Check if it already exists
	_, err := m.davClient.Stat(m.ctx, m.remotePath(remoteRel))
	if err == nil {
		return nil // already exists
	}

	// Create parent first
	parent := path.Dir(remoteRel)
	if parent != remoteRel && parent != "." {
		if err := m.ensureRemoteDir(parent); err != nil {
			return err
		}
	}

	// Create this directory (ignore error if it already exists)
	m.davClient.Mkdir(m.ctx, m.remotePath(remoteRel))
	return nil
}

// addWatchRecursive adds all directories under rootDir to the fsnotify watcher.
func (m *StagingMounter) addWatchRecursive(rootDir string) error {
	return filepath.Walk(rootDir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			relPath, _ := filepath.Rel(rootDir, p)
			if isTempFile(filepath.ToSlash(relPath)) {
				return filepath.SkipDir
			}
			return m.watcher.Add(p)
		}
		return nil
	})
}

// isForbiddenByPath checks path-only patterns (no os.FileInfo needed).
// Used for remote files where we don't have local permission bits.
func isForbiddenByPath(relPath string, dangerous bool) (bool, string) {
	if dangerous {
		return false, ""
	}
	base := filepath.Base(relPath)
	for _, pattern := range forbiddenPatterns {
		if matched, _ := filepath.Match(pattern, base); matched {
			return true, "security-sensitive file"
		}
		if matched, _ := filepath.Match(pattern, relPath); matched {
			return true, "security-sensitive file"
		}
	}
	parts := strings.Split(filepath.ToSlash(relPath), "/")
	for _, part := range parts {
		for _, pattern := range forbiddenPatterns {
			if matched, _ := filepath.Match(pattern, part); matched {
				return true, "inside security-sensitive directory"
			}
		}
	}
	return false, ""
}

// formatSize returns a human-readable file size.
func formatSize(bytes int64) string {
	switch {
	case bytes >= 1024*1024:
		return fmt.Sprintf("%.1f MB", float64(bytes)/(1024*1024))
	case bytes >= 1024:
		return fmt.Sprintf("%.1f KB", float64(bytes)/1024)
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}
