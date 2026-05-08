package client

// hulactl-side wrappers for the hulaagent management API. Phase 2
// ships CreateAgent; revoke / list land in Phase 6 alongside their
// server-side handlers.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// CreateAgentSiteAllow mirrors handler.CreateAgentSiteRequest.
type CreateAgentSiteAllow struct {
	Allow map[string]string `json:"allow"`
}

// CreateAgentRequest mirrors handler.CreateAgentRequest.
type CreateAgentRequest struct {
	ExpiresInSeconds int64                           `json:"expires_in_seconds,omitempty"`
	Sites            map[string]CreateAgentSiteAllow `json:"sites"`
	HulaHost         string                          `json:"hula_host,omitempty"`
}

// CreateAgentResponse mirrors handler.CreateAgentResponse.
type CreateAgentResponse struct {
	AgentID string `json:"agent_id"`
	Yaml    string `json:"yaml"`
}

// CreateAgent calls POST /api/agent/create on a running hula. Returns
// the issued agent id + the rendered yaml (which the CLI typically
// writes verbatim to stdout for the operator to redirect).
func (c *Client) CreateAgent(req *CreateAgentRequest) (*CreateAgentResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("nil request")
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}

	url := c.apiUrl + "/api/agent/create"
	c.out("POST %s\n", url)
	httpReq, err := http.NewRequest(http.MethodPost, url, bytes.NewBuffer(body))
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.token)

	res, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request: %w", err)
	}
	defer res.Body.Close()

	resBody, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if res.StatusCode != http.StatusOK {
		return nil, &ClientError{StatusCode: res.StatusCode, Body: string(resBody)}
	}
	out := &CreateAgentResponse{}
	if err := json.Unmarshal(resBody, out); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return out, nil
}
