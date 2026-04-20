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
	"time"

	"github.com/emersion/go-webdav"
	"github.com/fsnotify/fsnotify"
)

// StagingMountOptions configures a staging mount session.
type StagingMountOptions struct {
	ServerID    string
	LocalDir    string                            // absolute path to local mount point
	Dangerous   bool                              // allow executables and security-sensitive files
	Output      func(string, ...any) (int, error) // user-facing output (e.g., fmt.Printf)
	ConfirmFunc func() bool                       // ask user to confirm (yes/no)
}

// StagingMounter syncs a local directory with a remote staging folder via WebDAV.
type StagingMounter struct {
	opts      StagingMountOptions
	davClient *webdav.Client
	watcher   *fsnotify.Watcher
	ctx       context.Context
	cancel    context.CancelFunc
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
	davEndpoint := c.GetAPIUrl() + "/api/staging/" + opts.ServerID + "/dav"

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
		opts:      opts,
		davClient: davClient,
		ctx:       innerCtx,
		cancel:    cancel,
	}, nil
}

// Close shuts down the mounter and releases resources.
func (m *StagingMounter) Close() {
	m.cancel()
	if m.watcher != nil {
		m.watcher.Close()
	}
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

	remoteFiles, err := m.davClient.ReadDir(m.ctx, "/", true)
	if err != nil {
		return fmt.Errorf("listing remote files: %w", err)
	}

	// Filter to only files (ReadDir with recursive=true returns dirs too)
	var files []webdav.FileInfo
	var dirs []webdav.FileInfo
	for _, fi := range remoteFiles {
		if fi.IsDir {
			dirs = append(dirs, fi)
		} else {
			files = append(files, fi)
		}
	}

	// Create directories first
	for _, d := range dirs {
		localPath := filepath.Join(m.opts.LocalDir, filepath.FromSlash(d.Path))
		os.MkdirAll(localPath, 0o755)
	}

	// Download files
	skipped := 0
	for i, fi := range files {
		relPath := strings.TrimPrefix(fi.Path, "/")
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

		if err := m.downloadFile(fi.Path, localPath, fi.ModTime); err != nil {
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
	remoteList, err := m.davClient.ReadDir(m.ctx, "/", true)
	if err != nil {
		return fmt.Errorf("listing remote files: %w", err)
	}
	remoteFiles := make(map[string]webdav.FileInfo)
	for _, fi := range remoteList {
		relPath := strings.TrimPrefix(fi.Path, "/")
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

		remotePath := "/" + relPath
		localPath := filepath.Join(m.opts.LocalDir, filepath.FromSlash(relPath))

		if remoteInfo, ok := remoteFiles[relPath]; ok {
			// Both exist — upload if local differs
			if localInfo.Size() != remoteInfo.Size || localInfo.ModTime().After(remoteInfo.ModTime) {
				if err := m.uploadFile(localPath, remotePath); err != nil {
					m.opts.Output("  Error uploading %s: %s\n", relPath, err)
					continue
				}
				m.opts.Output("  Updated on server: %s (%s)\n", relPath, formatSize(localInfo.Size()))
				uploaded++
			}
		} else {
			// Local only — upload to remote
			if err := m.ensureRemoteDir(path.Dir(remotePath)); err != nil {
				m.opts.Output("  Error creating remote dir for %s: %s\n", relPath, err)
				continue
			}
			if err := m.uploadFile(localPath, remotePath); err != nil {
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
		remotePath := "/" + relPath

		os.MkdirAll(filepath.Dir(localPath), 0o755)
		if err := m.downloadFile(remotePath, localPath, remoteInfo.ModTime); err != nil {
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
			remotePath := "/" + relPath
			m.ensureRemoteDir(remotePath)
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
func (m *StagingMounter) processPending(pending map[string]fsnotify.Op) {
	for relPath, op := range pending {
		localPath := filepath.Join(m.opts.LocalDir, filepath.FromSlash(relPath))
		remotePath := "/" + relPath

		// Check if the file still exists (could have been deleted)
		info, statErr := os.Stat(localPath)

		if op.Has(fsnotify.Remove) || op.Has(fsnotify.Rename) {
			if statErr != nil {
				// File is gone — delete on remote
				if err := m.davClient.RemoveAll(m.ctx, remotePath); err != nil {
					m.opts.Output("  Error deleting remote %s: %s\n", relPath, err)
				} else {
					m.opts.Output("  Deleted remote: %s\n", relPath)
				}
				continue
			}
		}

		if statErr != nil {
			continue // file doesn't exist locally, nothing to do
		}

		if info.IsDir() {
			if op.Has(fsnotify.Create) {
				m.ensureRemoteDir(remotePath)
				m.opts.Output("  Created dir: %s\n", relPath)
			}
			continue
		}

		// File create or write — upload
		if skip, reason := isForbidden(relPath, info, m.opts.Dangerous); skip {
			m.opts.Output("  Skipping: %s (%s - use --dangerous to override)\n", relPath, reason)
			continue
		}

		if err := m.ensureRemoteDir(path.Dir(remotePath)); err != nil {
			m.opts.Output("  Error creating remote dir for %s: %s\n", relPath, err)
			continue
		}
		if err := m.uploadFile(localPath, remotePath); err != nil {
			m.opts.Output("  Error syncing %s: %s\n", relPath, err)
			continue
		}
		m.opts.Output("  Synced: %s (%s)\n", relPath, formatSize(info.Size()))
	}
}

// ---------- Helpers ----------

// uploadFile uploads a local file to the remote WebDAV path.
func (m *StagingMounter) uploadFile(localPath, remotePath string) error {
	f, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer f.Close()

	wc, err := m.davClient.Create(m.ctx, remotePath)
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
func (m *StagingMounter) downloadFile(remotePath, localPath string, modTime time.Time) error {
	os.MkdirAll(filepath.Dir(localPath), 0o755)

	rc, err := m.davClient.Open(m.ctx, remotePath)
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

// ensureRemoteDir creates all parent directories on the remote for the given path.
func (m *StagingMounter) ensureRemoteDir(remotePath string) error {
	if remotePath == "/" || remotePath == "" || remotePath == "." {
		return nil
	}

	// Check if it already exists
	_, err := m.davClient.Stat(m.ctx, remotePath)
	if err == nil {
		return nil // already exists
	}

	// Create parent first
	parent := path.Dir(remotePath)
	if parent != remotePath {
		if err := m.ensureRemoteDir(parent); err != nil {
			return err
		}
	}

	// Create this directory (ignore error if it already exists)
	m.davClient.Mkdir(m.ctx, remotePath)
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
