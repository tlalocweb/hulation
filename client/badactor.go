package client

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

type BadActorListEntry struct {
	IP         string    `json:"ip"`
	Score      int       `json:"score"`
	DetectedAt time.Time `json:"detected_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	LastReason string    `json:"last_reason"`
	Blocked    bool      `json:"blocked"`
}

type BadActorStatsResponse struct {
	Enabled        bool   `json:"enabled"`
	DryRun         bool   `json:"dry_run"`
	BlockThreshold int    `json:"block_threshold"`
	TTL            string `json:"ttl"`
	BlockedIPs     int    `json:"blocked_ips"`
	AllowlistedIPs int    `json:"allowlisted_ips"`
	Signatures     int    `json:"signatures"`
}

func (c *Client) BadActorList() (entries []BadActorListEntry, err error) {
	if c.token == "" {
		err = &ClientError{RootCause: fmt.Errorf("no token. use 'auth' command to get a token")}
		return
	}
	url := c.apiUrl + "/api/badactor/list"
	c.out("GET badactor list url: %s\n", url)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		err = &ClientError{RootCause: fmt.Errorf("error creating request: %s", err.Error())}
		return
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	res, err := c.httpClient.Do(req)
	if err != nil {
		err = &ClientError{RootCause: fmt.Errorf("error making http request: %s", err.Error())}
		return
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		err = &ClientError{StatusCode: res.StatusCode, RootCause: fmt.Errorf("could not read response body: %s", err.Error())}
		return
	}
	c.out("response: %d body: %s\n", res.StatusCode, string(body))
	if res.StatusCode != 200 {
		err = &ClientError{StatusCode: res.StatusCode, Body: string(body)}
		return
	}
	err = json.Unmarshal(body, &entries)
	if err != nil {
		err = &ClientError{StatusCode: res.StatusCode, Body: string(body), RootCause: fmt.Errorf("error unmarshalling response: %s", err.Error())}
	}
	return
}

func (c *Client) BadActorStats() (stats *BadActorStatsResponse, err error) {
	if c.token == "" {
		err = &ClientError{RootCause: fmt.Errorf("no token. use 'auth' command to get a token")}
		return
	}
	url := c.apiUrl + "/api/badactor/stats"
	c.out("GET badactor stats url: %s\n", url)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		err = &ClientError{RootCause: fmt.Errorf("error creating request: %s", err.Error())}
		return
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	res, err := c.httpClient.Do(req)
	if err != nil {
		err = &ClientError{RootCause: fmt.Errorf("error making http request: %s", err.Error())}
		return
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		err = &ClientError{StatusCode: res.StatusCode, RootCause: fmt.Errorf("could not read response body: %s", err.Error())}
		return
	}
	c.out("response: %d body: %s\n", res.StatusCode, string(body))
	if res.StatusCode != 200 {
		err = &ClientError{StatusCode: res.StatusCode, Body: string(body)}
		return
	}
	stats = &BadActorStatsResponse{}
	err = json.Unmarshal(body, stats)
	if err != nil {
		err = &ClientError{StatusCode: res.StatusCode, Body: string(body), RootCause: fmt.Errorf("error unmarshalling response: %s", err.Error())}
	}
	return
}
