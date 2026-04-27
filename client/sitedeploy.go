package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

type TriggerBuildRequest struct {
	ID   string   `json:"id"`
	Args []string `json:"args,omitempty"`
}

type TriggerBuildResponse struct {
	Status  string `json:"status"`
	BuildID string `json:"build_id"`
}

type BuildStatusResponse struct {
	BuildID    string   `json:"build_id"`
	ServerID   string   `json:"server_id"`
	Status     int      `json:"status"`
	StatusText string   `json:"status_text"`
	StartedAt  string   `json:"started_at"`
	EndedAt    *string  `json:"ended_at,omitempty"`
	Logs       []string `json:"logs"`
	Error      string   `json:"error,omitempty"`
}

// TriggerBuild triggers a site build for the given server ID.
func (c *Client) TriggerBuild(serverID string) (resp *ClientResponse, result *TriggerBuildResponse, err error) {
	url := c.apiUrl + "/api/site/trigger-build"
	body, _ := json.Marshal(TriggerBuildRequest{ID: serverID})

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

	result = &TriggerBuildResponse{}
	err = json.Unmarshal(resBody, result)
	if err != nil {
		err = &ClientError{StatusCode: res.StatusCode, RootCause: fmt.Errorf("parsing response: %w", err)}
	}
	resp.Response = result
	return
}

// BuildStatus gets the status of a build by ID.
func (c *Client) BuildStatus(buildID string) (resp *ClientResponse, result *BuildStatusResponse, err error) {
	url := c.apiUrl + "/api/site/build-status/" + buildID

	c.out("GET %s\n", url)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		err = &ClientError{RootCause: fmt.Errorf("error creating request: %w", err)}
		return
	}
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

	result = &BuildStatusResponse{}
	err = json.Unmarshal(resBody, result)
	if err != nil {
		err = &ClientError{StatusCode: res.StatusCode, RootCause: fmt.Errorf("parsing response: %w", err)}
	}
	resp.Response = result
	return
}

// ListBuilds lists all builds for a server.
func (c *Client) ListBuilds(serverID string) (resp *ClientResponse, result []BuildStatusResponse, err error) {
	url := c.apiUrl + "/api/site/builds/" + serverID

	c.out("GET %s\n", url)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		err = &ClientError{RootCause: fmt.Errorf("error creating request: %w", err)}
		return
	}
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

	err = json.Unmarshal(resBody, &result)
	if err != nil {
		err = &ClientError{StatusCode: res.StatusCode, RootCause: fmt.Errorf("parsing response: %w", err)}
	}
	resp.Response = result
	return
}
