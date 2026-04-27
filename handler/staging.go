package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/emersion/go-webdav"

	hulation "github.com/tlalocweb/hulation/app"
	"github.com/tlalocweb/hulation/sitedeploy"
)

type stagingBuildRequest struct {
	ID string `json:"id"`
}

type stagingBuildResponse struct {
	BuildID    string   `json:"build_id"`
	Status     string   `json:"status"`
	StatusText string   `json:"status_text"`
	Logs       []string `json:"logs,omitempty"`
	Error      string   `json:"error,omitempty"`
}

// StagingBuild handles POST /api/staging/build.
// Triggers a rebuild in the staging container for the given server ID.
func StagingBuild(ctx RequestCtx) error {
	var req stagingBuildRequest
	body := ctx.Body()
	if err := json.Unmarshal(body, &req); err != nil {
		return ctx.Status(http.StatusBadRequest).SendString("bad request: " + err.Error())
	}
	if req.ID == "" {
		return ctx.Status(http.StatusBadRequest).SendString("server id required")
	}

	cfg := hulation.GetConfig()
	server := cfg.GetServerByID(req.ID)
	if server == nil {
		return ctx.Status(http.StatusNotFound).SendString("server not found: " + req.ID)
	}

	sm := sitedeploy.GetStagingManager()
	if sm == nil {
		return ctx.Status(http.StatusServiceUnavailable).SendString("staging manager not initialized")
	}

	sc := sm.GetStagingContainer(req.ID)
	if sc == nil {
		return ctx.Status(http.StatusNotFound).SendString("no staging container for server: " + req.ID)
	}

	bs, err := sm.RebuildStaging(req.ID)
	if errors.Is(err, sitedeploy.ErrBuildInProgress) {
		return ctx.Status(http.StatusConflict).SendString("build already in progress for server " + req.ID)
	}
	if err != nil {
		resp := stagingBuildResponse{
			Status:     "failed",
			StatusText: "failed",
			Error:      err.Error(),
		}
		if bs != nil {
			resp.BuildID = bs.BuildID
			snap := bs.Snapshot()
			resp.Logs = snap.Logs
		}
		return ctx.Status(http.StatusInternalServerError).SendJSON(resp)
	}

	snap := bs.Snapshot()
	return ctx.SendJSON(stagingBuildResponse{
		BuildID:    snap.BuildID,
		Status:     "complete",
		StatusText: snap.StatusText,
		Logs:       snap.Logs,
	})
}

// webdav handler cache per server
var (
	davHandlers   = make(map[string]*webdav.Handler)
	davHandlersMu sync.RWMutex
)

func getOrCreateDAVHandler(serverID, hostDir string) *webdav.Handler {
	davHandlersMu.RLock()
	h, ok := davHandlers[serverID]
	davHandlersMu.RUnlock()
	if ok {
		return h
	}

	davHandlersMu.Lock()
	defer davHandlersMu.Unlock()
	// double check
	if h, ok = davHandlers[serverID]; ok {
		return h
	}
	h = &webdav.Handler{
		FileSystem: webdav.LocalFileSystem(hostDir),
	}
	davHandlers[serverID] = h
	return h
}

// StagingWebDAVNetHTTP returns an http.Handler that serves WebDAV requests
// for staging containers. It does its own Bearer-token auth check (verifying
// the token has admin capability) then delegates to the emersion/go-webdav
// handler with the URL prefix stripped. Used by the net/http (h2) router.
func StagingWebDAVNetHTTP(verifyToken func(token string) (bool, bool, error)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Extract serverID from the path: /api/staging/{serverID}/dav/...
		path := r.URL.Path
		const prefix = "/api/staging/"
		if !strings.HasPrefix(path, prefix) {
			http.NotFound(w, r)
			return
		}
		rest := path[len(prefix):]
		slash := strings.Index(rest, "/")
		if slash < 0 {
			http.NotFound(w, r)
			return
		}
		serverID := rest[:slash]
		if serverID == "" {
			http.NotFound(w, r)
			return
		}

		// Auth check: Bearer token must have admin cap
		ahdr := r.Header.Get("Authorization")
		var token string
		n, err := fmt.Sscanf(ahdr, "Bearer %s", &token)
		if err != nil || n < 1 {
			http.Error(w, "missing or invalid Authorization header", http.StatusUnauthorized)
			return
		}
		valid, isAdmin, err := verifyToken(token)
		if err != nil || !valid {
			http.Error(w, "invalid token", http.StatusUnauthorized)
			return
		}
		if !isAdmin {
			http.Error(w, "admin required", http.StatusForbidden)
			return
		}

		sm := sitedeploy.GetStagingManager()
		if sm == nil {
			http.Error(w, "staging manager not initialized", http.StatusServiceUnavailable)
			return
		}
		sc := sm.GetStagingContainer(serverID)
		if sc == nil {
			http.Error(w, "no staging container for server: "+serverID, http.StatusNotFound)
			return
		}

		davHandler := getOrCreateDAVHandler(serverID, sc.HostSrcDir)
		davPrefix := "/api/staging/" + serverID + "/dav"
		// Strip the prefix. The WebDAV LocalFileSystem requires an absolute
		// path, so if the remaining path is empty, set it to "/".
		r2 := *r
		u2 := *r.URL
		u2.Path = strings.TrimPrefix(r.URL.Path, davPrefix)
		if u2.Path == "" {
			u2.Path = "/"
		}
		u2.RawPath = ""
		r2.URL = &u2

		// Intercept PATCH — the emersion/go-webdav Handler doesn't support it.
		// We implement the SabreDAV partial-update convention: PATCH /file with
		// X-Update-Range: bytes=START-END and the new bytes in the body.
		if r.Method == http.MethodPatch {
			handleStagingPatch(w, &r2, sc.HostSrcDir)
			return
		}

		davHandler.ServeHTTP(w, &r2)
	})
}

// handleStagingPatch dispatches PATCH requests to either the byte-range
// updater (SabreDAV convention) or the unified-diff applier, based on the
// X-Patch-Format header.
//
// Range mode (default, or X-Patch-Format: range):
//   PATCH /path/to/file
//   X-Update-Range: bytes=<start>-<end>
//   body: the new bytes for that range
//
// Diff mode (X-Patch-Format: diff):
//   PATCH /path/to/file
//   X-Patch-Format: diff
//   body: a unified diff (e.g., the output of `git diff <file>`)
//
// The byte range is zero-indexed and inclusive on both ends (HTTP range semantics).
// If the range extends beyond the current file size, the file is extended.
// The end byte may be omitted ("bytes=<start>-") to write from <start> through
// the end of the body.
func handleStagingPatch(w http.ResponseWriter, r *http.Request, hostDir string) {
	switch strings.ToLower(strings.TrimSpace(r.Header.Get("X-Patch-Format"))) {
	case "diff":
		handleStagingPatchDiff(w, r, hostDir)
		return
	case "", "range":
		// fall through to range update
	default:
		http.Error(w, "unsupported X-Patch-Format (must be 'range' or 'diff')", http.StatusBadRequest)
		return
	}

	rangeHdr := r.Header.Get("X-Update-Range")
	if rangeHdr == "" {
		http.Error(w, "PATCH requires X-Update-Range header (or X-Patch-Format: diff)", http.StatusBadRequest)
		return
	}

	start, end, hasEnd, err := parseUpdateRange(rangeHdr)
	if err != nil {
		http.Error(w, "invalid X-Update-Range: "+err.Error(), http.StatusBadRequest)
		return
	}

	filePath, err := resolveStagingPath(hostDir, r.URL.Path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Ensure target is a regular file; reject if it's a directory or missing.
	info, err := os.Stat(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			http.Error(w, "file does not exist", http.StatusNotFound)
			return
		}
		http.Error(w, "stat error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if info.IsDir() {
		http.Error(w, "cannot PATCH a directory", http.StatusBadRequest)
		return
	}

	f, err := os.OpenFile(filePath, os.O_RDWR, 0o644)
	if err != nil {
		http.Error(w, "open error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer f.Close()

	if _, err := f.Seek(start, io.SeekStart); err != nil {
		http.Error(w, "seek error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	var n int64
	if hasEnd {
		n, err = io.CopyN(f, r.Body, end-start+1)
	} else {
		n, err = io.Copy(f, r.Body)
	}
	if err != nil {
		http.Error(w, "write error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Fsync to ensure the change is visible to other readers (Hugo, etc).
	_ = f.Sync()

	w.Header().Set("X-Update-Range", fmt.Sprintf("bytes=%d-%d", start, start+n-1))
	w.WriteHeader(http.StatusNoContent)
}

// handleStagingPatchDiff applies a unified-diff body to the file identified
// by the request URL. The diff is expected to match the output of `git diff
// <file>` or `diff -u`. File-path headers in the diff (--- a/x, +++ b/x) are
// ignored; the target file is determined from the URL.
//
// Returns 204 No Content on success, 409 Conflict if the diff doesn't apply
// cleanly (context mismatch), or 400/404/500 on other errors.
func handleStagingPatchDiff(w http.ResponseWriter, r *http.Request, hostDir string) {
	filePath, err := resolveStagingPath(hostDir, r.URL.Path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	info, err := os.Stat(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			http.Error(w, "file does not exist", http.StatusNotFound)
			return
		}
		http.Error(w, "stat error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if info.IsDir() {
		http.Error(w, "cannot PATCH a directory", http.StatusBadRequest)
		return
	}

	original, err := os.ReadFile(filePath)
	if err != nil {
		http.Error(w, "read error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	diff, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "body read error: "+err.Error(), http.StatusBadRequest)
		return
	}

	updated, err := applyUnifiedDiff(original, diff)
	if err != nil {
		http.Error(w, "diff apply failed: "+err.Error(), http.StatusConflict)
		return
	}

	// Atomic write: write to temp file in same directory, then rename.
	tmp, err := os.CreateTemp(filepath.Dir(filePath), ".hulapatch-*")
	if err != nil {
		http.Error(w, "temp file error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(updated); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		http.Error(w, "write error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		http.Error(w, "sync error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	tmp.Close()
	// Preserve original file mode
	if err := os.Chmod(tmpPath, info.Mode().Perm()); err != nil {
		os.Remove(tmpPath)
		http.Error(w, "chmod error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := os.Rename(tmpPath, filePath); err != nil {
		os.Remove(tmpPath)
		http.Error(w, "rename error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// applyUnifiedDiff applies a unified-diff patch to the given original content.
// It processes hunks (each starting with "@@ ... @@") and ignores file-header
// lines (diff --git, index, ---, +++). Returns the patched content, or an
// error if the diff doesn't apply cleanly (context or removed lines don't
// match the original).
func applyUnifiedDiff(original, diff []byte) ([]byte, error) {
	// Track trailing-newline state so we can restore it.
	hadTrailingNewline := len(original) > 0 && original[len(original)-1] == '\n'

	// Split original into lines without trailing newlines. If the original
	// ends in \n, strings.Split produces a trailing empty string — drop it.
	origLines := strings.Split(string(original), "\n")
	if hadTrailingNewline {
		origLines = origLines[:len(origLines)-1]
	}

	diffLines := strings.Split(string(diff), "\n")
	// Trim final empty line from Split if present.
	if len(diffLines) > 0 && diffLines[len(diffLines)-1] == "" {
		diffLines = diffLines[:len(diffLines)-1]
	}

	var result []string
	origIdx := 0 // 0-based index into origLines

	i := 0
	for i < len(diffLines) {
		line := diffLines[i]
		if !strings.HasPrefix(line, "@@") {
			i++
			continue
		}
		oldStart, _, err := parseHunkHeader(line)
		if err != nil {
			return nil, fmt.Errorf("hunk header at diff line %d: %w", i+1, err)
		}
		// Copy original lines up to (oldStart - 1) (hunk is 1-indexed).
		target := oldStart - 1
		if target < origIdx {
			return nil, fmt.Errorf("hunk at diff line %d references line %d but we're already at %d (hunks out of order?)", i+1, oldStart, origIdx+1)
		}
		for origIdx < target {
			if origIdx >= len(origLines) {
				return nil, fmt.Errorf("hunk at diff line %d starts past end of original file", i+1)
			}
			result = append(result, origLines[origIdx])
			origIdx++
		}

		// Process hunk body until next hunk or end of diff.
		i++
		for i < len(diffLines) {
			l := diffLines[i]
			if strings.HasPrefix(l, "@@") {
				break
			}
			if len(l) == 0 {
				// A fully blank line in a unified diff represents a context line
				// with no content (i.e., a blank line in the file). Treat as " ".
				l = " "
			}
			prefix := l[0]
			body := l[1:]
			switch prefix {
			case ' ':
				if origIdx >= len(origLines) {
					return nil, fmt.Errorf("context line at diff line %d past end of original", i+1)
				}
				if origLines[origIdx] != body {
					return nil, fmt.Errorf("context mismatch at original line %d: have %q want %q", origIdx+1, origLines[origIdx], body)
				}
				result = append(result, body)
				origIdx++
			case '-':
				if origIdx >= len(origLines) {
					return nil, fmt.Errorf("removed line at diff line %d past end of original", i+1)
				}
				if origLines[origIdx] != body {
					return nil, fmt.Errorf("removed-line mismatch at original line %d: have %q want %q", origIdx+1, origLines[origIdx], body)
				}
				origIdx++
			case '+':
				result = append(result, body)
			case '\\':
				// "\ No newline at end of file" — ignore; trailing-newline state
				// is preserved from the original content regardless.
			default:
				// Skip unknown prefix lines (e.g., stray git diff headers).
			}
			i++
		}
	}

	// Copy remaining original lines.
	for origIdx < len(origLines) {
		result = append(result, origLines[origIdx])
		origIdx++
	}

	out := strings.Join(result, "\n")
	if hadTrailingNewline && !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	return []byte(out), nil
}

// parseHunkHeader parses a unified-diff hunk header of the form:
//
//	@@ -<oldStart>[,<oldCount>] +<newStart>[,<newCount>] @@ [optional function context]
//
// Returns oldStart and newStart (both 1-indexed). Counts default to 1.
func parseHunkHeader(line string) (oldStart, newStart int, err error) {
	// Strip leading "@@ "
	rest := strings.TrimPrefix(line, "@@")
	rest = strings.TrimSpace(rest)
	// Expect "-X,Y +A,B @@ ..."
	parts := strings.SplitN(rest, " ", 3)
	if len(parts) < 2 {
		return 0, 0, fmt.Errorf("malformed hunk header %q", line)
	}
	oldSpec, newSpec := parts[0], parts[1]
	if !strings.HasPrefix(oldSpec, "-") || !strings.HasPrefix(newSpec, "+") {
		return 0, 0, fmt.Errorf("malformed hunk header %q", line)
	}
	oldStart, _, err = parseRange(strings.TrimPrefix(oldSpec, "-"))
	if err != nil {
		return 0, 0, fmt.Errorf("old range: %w", err)
	}
	newStart, _, err = parseRange(strings.TrimPrefix(newSpec, "+"))
	if err != nil {
		return 0, 0, fmt.Errorf("new range: %w", err)
	}
	// Special case: empty-file additions use start=0; treat as line 1.
	if oldStart == 0 {
		oldStart = 1
	}
	if newStart == 0 {
		newStart = 1
	}
	return oldStart, newStart, nil
}

// parseRange parses "<start>" or "<start>,<count>" into (start, count).
// If count is omitted, it defaults to 1.
func parseRange(s string) (start, count int, err error) {
	if i := strings.Index(s, ","); i >= 0 {
		start, err = strconv.Atoi(s[:i])
		if err != nil {
			return 0, 0, err
		}
		count, err = strconv.Atoi(s[i+1:])
		if err != nil {
			return 0, 0, err
		}
		return start, count, nil
	}
	start, err = strconv.Atoi(s)
	if err != nil {
		return 0, 0, err
	}
	return start, 1, nil
}

// parseUpdateRange parses an X-Update-Range header value.
// Supported forms:
//   "bytes=START-END"   — write bytes [START, END] inclusive
//   "bytes=START-"      — write from START to end of body
// Returns (start, end, hasEnd, err).
func parseUpdateRange(s string) (int64, int64, bool, error) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "bytes=") {
		return 0, 0, false, fmt.Errorf("must start with 'bytes='")
	}
	s = strings.TrimPrefix(s, "bytes=")
	dash := strings.Index(s, "-")
	if dash < 0 {
		return 0, 0, false, fmt.Errorf("missing '-' separator")
	}
	startStr := strings.TrimSpace(s[:dash])
	endStr := strings.TrimSpace(s[dash+1:])
	if startStr == "" {
		return 0, 0, false, fmt.Errorf("missing start byte")
	}
	start, err := strconv.ParseInt(startStr, 10, 64)
	if err != nil {
		return 0, 0, false, fmt.Errorf("invalid start: %s", err)
	}
	if start < 0 {
		return 0, 0, false, fmt.Errorf("start must be >= 0")
	}
	if endStr == "" {
		return start, 0, false, nil
	}
	end, err := strconv.ParseInt(endStr, 10, 64)
	if err != nil {
		return 0, 0, false, fmt.Errorf("invalid end: %s", err)
	}
	if end < start {
		return 0, 0, false, fmt.Errorf("end must be >= start")
	}
	return start, end, true, nil
}

// resolveStagingPath converts a WebDAV-absolute path to a filesystem path
// rooted at hostDir. Validates that the resulting path stays within hostDir.
func resolveStagingPath(hostDir, davPath string) (string, error) {
	if strings.Contains(davPath, "\x00") {
		return "", fmt.Errorf("invalid character in path")
	}
	cleaned := path.Clean(davPath)
	if !path.IsAbs(cleaned) {
		return "", fmt.Errorf("expected absolute path, got %q", davPath)
	}
	// Join with hostDir; strip the leading slash so filepath.Join doesn't
	// treat it as a rooted replacement on Windows.
	joined := filepath.Join(hostDir, filepath.FromSlash(strings.TrimPrefix(cleaned, "/")))
	// Safety: the resolved path must remain inside hostDir.
	absHostDir, err := filepath.Abs(hostDir)
	if err != nil {
		return "", err
	}
	absJoined, err := filepath.Abs(joined)
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(absJoined, absHostDir) {
		return "", fmt.Errorf("path escapes host directory")
	}
	return joined, nil
}

