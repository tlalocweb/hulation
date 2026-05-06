package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

type StagingBuildRequest struct {
	ID string `json:"id"`
}

type StagingBuildResponse struct {
	BuildID    string   `json:"build_id"`
	Status     string   `json:"status"`
	StatusText string   `json:"status_text"`
	Logs       []string `json:"logs,omitempty"`
	Error      string   `json:"error,omitempty"`
}

// StagingBuild triggers a rebuild in the staging container.
func (c *Client) StagingBuild(serverID string) (resp *ClientResponse, result *StagingBuildResponse, err error) {
	url := c.apiUrl + "/api/staging/build"
	body, _ := json.Marshal(StagingBuildRequest{ID: serverID})

	c.out("POST %s\n", url)
	req, err := http.NewRequest("POST", url, bytes.NewBuffer(body))
	if err != nil {
		err = &ClientError{RootCause: fmt.Errorf("error creating request: %w", err)}
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)

	resp = NewResponse()
	res, err := c.httpClient.Do(req)
	if err != nil {
		err = &ClientError{RootCause: fmt.Errorf("error making request: %w", err)}
		return
	}
	defer res.Body.Close()

	resBody, err := io.ReadAll(res.Body)
	if err != nil {
		err = &ClientError{StatusCode: res.StatusCode, RootCause: fmt.Errorf("reading response: %w", err)}
		return
	}
	resp.Finish(res.StatusCode, string(resBody), nil)

	if res.StatusCode != 200 {
		err = &ClientError{StatusCode: res.StatusCode, Body: string(resBody)}
		return
	}

	result = &StagingBuildResponse{}
	err = json.Unmarshal(resBody, result)
	if err != nil {
		err = &ClientError{StatusCode: res.StatusCode, RootCause: fmt.Errorf("parsing response: %w", err)}
	}
	resp.Response = result
	return
}

// StagingPatch updates a byte range of a file on the staging site via
// WebDAV PATCH with the SabreDAV X-Update-Range convention.
//
//	PATCH /api/staging/{serverID}/dav/{remotePath}
//	X-Update-Range: bytes=<start>-<end>
//	body: the new bytes
//
// The byte range is zero-indexed and inclusive on both ends.
func (c *Client) StagingPatch(serverID, remotePath string, start, end int64, data []byte) error {
	if !strings.HasPrefix(remotePath, "/") {
		remotePath = "/" + remotePath
	}
	url := c.apiUrl + "/api/staging/" + serverID + "/dav" + remotePath

	c.out("PATCH %s\n", url)
	req, err := http.NewRequest(http.MethodPatch, url, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("X-Update-Range", fmt.Sprintf("bytes=%d-%d", start, end))

	res, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("patching file: %w", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusNoContent && res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(res.Body)
		return fmt.Errorf("patch failed (status %d): %s", res.StatusCode, string(body))
	}
	return nil
}

// StagingPatchDiff applies a unified-diff patch (e.g., the output of
// `git diff <file>`) to a file on the staging site.
//
//	PATCH /api/staging/{serverID}/dav/{remotePath}
//	X-Patch-Format: diff
//	body: unified diff text
//
// The file-path headers in the diff (--- a/x, +++ b/x) are ignored; the
// target file is determined by remotePath. Returns an error with details
// if the diff doesn't apply cleanly (context mismatch).
func (c *Client) StagingPatchDiff(serverID, remotePath, diffText string) error {
	if !strings.HasPrefix(remotePath, "/") {
		remotePath = "/" + remotePath
	}
	url := c.apiUrl + "/api/staging/" + serverID + "/dav" + remotePath

	c.out("PATCH %s (diff)\n", url)
	req, err := http.NewRequest(http.MethodPatch, url, bytes.NewReader([]byte(diffText)))
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("X-Patch-Format", "diff")
	req.Header.Set("Content-Type", "text/x-diff")

	res, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("patching file: %w", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusNoContent && res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(res.Body)
		return fmt.Errorf("patch failed (status %d): %s", res.StatusCode, string(body))
	}
	return nil
}

// --- Staging git verbs (Phase 5b) ---
//
// Round-trip flow: WebDAV edit → StagingStageFiles → StagingCommit →
// StagingPush. Each maps 1:1 to a hulactl subcommand and to a single
// admin-only POST against the unified server.

// StagingStageRequest mirrors handler.stagingStageRequest.
type StagingStageRequest struct {
	Paths []string `json:"paths,omitempty"`
}

// StagingStageResponse mirrors handler.stagingStageResponse.
type StagingStageResponse struct {
	Staged []string `json:"staged"`
}

// StagingCommitRequest mirrors handler.stagingCommitRequest.
type StagingCommitRequest struct {
	Message     string `json:"message"`
	AuthorName  string `json:"author_name,omitempty"`
	AuthorEmail string `json:"author_email,omitempty"`
}

// StagingCommitResponse mirrors handler.stagingCommitResponse.
type StagingCommitResponse struct {
	SHA string `json:"sha"`
}

// StagingPushResponse mirrors handler.stagingPushResponse.
type StagingPushResponse struct {
	SHA    string `json:"sha"`
	Branch string `json:"branch"`
}

// StagingPullResponse mirrors handler.stagingPullResponse.
type StagingPullResponse struct {
	SHA       string `json:"sha,omitempty"`
	Branch    string `json:"branch"`
	Advanced  bool   `json:"advanced"`
	Rewound   bool   `json:"rewound,omitempty"`
	RewoundTo string `json:"rewound_to,omitempty"`
	Error     string `json:"error,omitempty"`
}

// StagingSyncResponse mirrors handler.stagingSyncResponse.
type StagingSyncResponse struct {
	Branch        string `json:"branch"`
	PullSHA       string `json:"pull_sha,omitempty"`
	Pulled        bool   `json:"pulled"`
	PushSHA       string `json:"push_sha,omitempty"`
	Rewound       bool   `json:"rewound,omitempty"`
	RewoundTo     string `json:"rewound_to,omitempty"`
	PushFailedErr string `json:"push_error,omitempty"`
	// Error is populated on the 400 path (pull failed before push
	// was even attempted). Distinct from PushFailedErr which
	// covers the 502 path (pull OK, push refused).
	Error string `json:"error,omitempty"`
}

// StagingStageFiles stages the given paths in the staging server's git
// working tree. Empty `paths` stages everything.
func (c *Client) StagingStageFiles(serverID string, paths []string) (*StagingStageResponse, error) {
	body, _ := json.Marshal(StagingStageRequest{Paths: paths})
	out := &StagingStageResponse{}
	if err := c.postStagingGit(serverID, "stage", body, out); err != nil {
		return nil, err
	}
	return out, nil
}

// StagingCommit creates a commit on the staging server's git working
// tree with the given message. Hula appends a "Committed-by: Hula"
// trailer line server-side.
func (c *Client) StagingCommit(serverID, message, authorName, authorEmail string) (*StagingCommitResponse, error) {
	body, _ := json.Marshal(StagingCommitRequest{
		Message:     message,
		AuthorName:  authorName,
		AuthorEmail: authorEmail,
	})
	out := &StagingCommitResponse{}
	if err := c.postStagingGit(serverID, "commit", body, out); err != nil {
		return nil, err
	}
	return out, nil
}

// StagingPush pushes the staging server's HEAD to origin on the
// branch configured under root_git_autodeploy.ref.branch.
func (c *Client) StagingPush(serverID string) (*StagingPushResponse, error) {
	out := &StagingPushResponse{}
	if err := c.postStagingGit(serverID, "push", nil, out); err != nil {
		return nil, err
	}
	return out, nil
}

// StagingPull rebases the staging server's working tree onto
// origin/<branch>. On a rebase conflict the server rewinds — the
// response carries Rewound=true and Error set to the rebase error
// (HTTP status stays 200 because the rewind succeeded). Dirty-tree
// refusal returns a 400 with the response body still parseable for
// callers who want the structured form.
func (c *Client) StagingPull(serverID string) (*StagingPullResponse, error) {
	out := &StagingPullResponse{}
	if err := c.postStagingGit(serverID, "pull", nil, out); err != nil {
		// Server returned non-200. If it was a JSON body (rewind
		// path), parse it through anyway so the caller sees the
		// rewind details.
		if ce, ok := err.(*ClientError); ok && ce.Body != "" {
			parsed := &StagingPullResponse{}
			if jerr := json.Unmarshal([]byte(ce.Body), parsed); jerr == nil && parsed.Error != "" {
				return parsed, nil
			}
		}
		return nil, err
	}
	return out, nil
}

// StagingSync runs pull + push as one server-side operation. Result
// fields capture both halves; PushFailedErr is non-empty when the pull
// succeeded but the push was rejected (in which case the server
// rewound the working tree if it had advanced).
func (c *Client) StagingSync(serverID string) (*StagingSyncResponse, error) {
	out := &StagingSyncResponse{}
	if err := c.postStagingGit(serverID, "sync", nil, out); err != nil {
		if ce, ok := err.(*ClientError); ok && ce.Body != "" {
			parsed := &StagingSyncResponse{}
			if jerr := json.Unmarshal([]byte(ce.Body), parsed); jerr == nil {
				return parsed, nil
			}
		}
		return nil, err
	}
	return out, nil
}

func (c *Client) postStagingGit(serverID, verb string, reqBody []byte, out interface{}) error {
	url := c.apiUrl + "/api/staging/" + serverID + "/git/" + verb
	c.out("POST %s\n", url)

	var bodyReader io.Reader
	if len(reqBody) > 0 {
		bodyReader = bytes.NewBuffer(reqBody)
	} else {
		bodyReader = bytes.NewBuffer([]byte("{}"))
	}
	req, err := http.NewRequest("POST", url, bodyReader)
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)

	res, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request: %w", err)
	}
	defer res.Body.Close()

	resBody, err := io.ReadAll(res.Body)
	if err != nil {
		return fmt.Errorf("reading response: %w", err)
	}
	if res.StatusCode != http.StatusOK {
		return &ClientError{StatusCode: res.StatusCode, Body: string(resBody)}
	}
	if out != nil {
		if err := json.Unmarshal(resBody, out); err != nil {
			return fmt.Errorf("parsing response: %w", err)
		}
	}
	return nil
}

// StagingDownload pulls a single file off a staging server via the
// WebDAV GET surface and writes it to localFile. Mirror of
// StagingUpload but in the other direction; useful when an operator
// wants to inspect or back up a file the staging container is serving
// before they edit it.
func (c *Client) StagingDownload(serverID, remotePath, localFile string) error {
	if !strings.HasPrefix(remotePath, "/") {
		remotePath = "/" + remotePath
	}
	url := c.apiUrl + "/api/staging/" + serverID + "/dav" + remotePath

	c.out("GET %s\n", url)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)

	res, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("downloading file: %w", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(res.Body)
		return fmt.Errorf("download failed (status %d): %s", res.StatusCode, string(body))
	}

	// Write to a temp file first, rename atomically into place. Avoids
	// leaving a half-written file at the target path if the network
	// drops mid-stream.
	dir := "."
	base := localFile
	if i := strings.LastIndex(localFile, "/"); i >= 0 {
		dir = localFile[:i]
		base = localFile[i+1:]
	}
	tmp, err := os.CreateTemp(dir, "."+base+".part-*")
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := io.Copy(tmp, res.Body); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("writing temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("closing temp file: %w", err)
	}
	// os.CreateTemp creates files with mode 0600 (private to the
	// writing user). When hulactl runs in a container as root and the
	// destination is a bind-mounted host volume, that mode locks the
	// host user out. Bump to 0644 so the file is readable from the
	// host after the rename.
	if err := os.Chmod(tmpPath, 0o644); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if err := os.Rename(tmpPath, localFile); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("renaming to %s: %w", localFile, err)
	}
	return nil
}

// StagingUpload uploads a local file to the staging site via WebDAV PUT.
func (c *Client) StagingUpload(serverID, localFile, remotePath string) error {
	// Ensure remote path starts with /
	if !strings.HasPrefix(remotePath, "/") {
		remotePath = "/" + remotePath
	}

	url := c.apiUrl + "/api/staging/" + serverID + "/dav" + remotePath

	f, err := os.Open(localFile)
	if err != nil {
		return fmt.Errorf("opening local file: %w", err)
	}
	defer f.Close()

	c.out("PUT %s\n", url)
	req, err := http.NewRequest("PUT", url, f)
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)

	res, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("uploading file: %w", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusCreated && res.StatusCode != http.StatusNoContent && res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(res.Body)
		return fmt.Errorf("upload failed (status %d): %s", res.StatusCode, string(body))
	}

	return nil
}
