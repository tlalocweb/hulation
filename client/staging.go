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
