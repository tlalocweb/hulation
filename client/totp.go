package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

type TotpSetupResponse struct {
	Secret        string   `json:"secret"`
	URL           string   `json:"url"`
	RecoveryCodes []string `json:"recovery_codes"`
}

func (c *Client) TotpSetup() (*TotpSetupResponse, error) {
	if c.token == "" {
		return nil, &ClientError{RootCause: fmt.Errorf("no token. use 'auth' command to get a token")}
	}
	url := c.apiUrl + "/api/auth/totp/setup"
	c.out("POST totp setup url: %s\n", url)
	req, err := http.NewRequest("POST", url, nil)
	if err != nil {
		return nil, &ClientError{RootCause: fmt.Errorf("error creating request: %s", err.Error())}
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	res, err := c.httpClient.Do(req)
	if err != nil {
		return nil, &ClientError{RootCause: fmt.Errorf("error making http request: %s", err.Error())}
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, &ClientError{StatusCode: res.StatusCode, RootCause: fmt.Errorf("could not read response body: %s", err.Error())}
	}
	c.out("response: %d body: %s\n", res.StatusCode, string(body))
	if res.StatusCode != 200 {
		return nil, &ClientError{StatusCode: res.StatusCode, Body: string(body)}
	}
	var resp TotpSetupResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, &ClientError{StatusCode: res.StatusCode, Body: string(body), RootCause: fmt.Errorf("error unmarshalling: %s", err.Error())}
	}
	return &resp, nil
}

func (c *Client) TotpVerifySetup(code string) error {
	if c.token == "" {
		return &ClientError{RootCause: fmt.Errorf("no token. use 'auth' command to get a token")}
	}
	url := c.apiUrl + "/api/auth/totp/verify-setup"
	c.out("POST totp verify-setup url: %s\n", url)
	payload, _ := json.Marshal(map[string]string{"code": code})
	req, err := http.NewRequest("POST", url, bytes.NewBuffer(payload))
	if err != nil {
		return &ClientError{RootCause: fmt.Errorf("error creating request: %s", err.Error())}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)
	res, err := c.httpClient.Do(req)
	if err != nil {
		return &ClientError{RootCause: fmt.Errorf("error making http request: %s", err.Error())}
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return &ClientError{StatusCode: res.StatusCode, RootCause: fmt.Errorf("could not read response body: %s", err.Error())}
	}
	c.out("response: %d body: %s\n", res.StatusCode, string(body))
	if res.StatusCode != 200 {
		return &ClientError{StatusCode: res.StatusCode, Body: string(body)}
	}
	return nil
}

type TotpValidateResponse struct {
	JWT string `json:"jwt"`
}

func (c *Client) TotpValidate(totpToken, code string, isRecoveryCode bool) (*TotpValidateResponse, error) {
	url := c.apiUrl + "/api/auth/totp/validate"
	c.out("POST totp validate url: %s\n", url)
	payload, _ := json.Marshal(map[string]interface{}{
		"totp_token":       totpToken,
		"code":             code,
		"is_recovery_code": isRecoveryCode,
	})
	req, err := http.NewRequest("POST", url, bytes.NewBuffer(payload))
	if err != nil {
		return nil, &ClientError{RootCause: fmt.Errorf("error creating request: %s", err.Error())}
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := c.httpClient.Do(req)
	if err != nil {
		return nil, &ClientError{RootCause: fmt.Errorf("error making http request: %s", err.Error())}
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, &ClientError{StatusCode: res.StatusCode, RootCause: fmt.Errorf("could not read response body: %s", err.Error())}
	}
	c.out("response: %d body: %s\n", res.StatusCode, string(body))
	if res.StatusCode != 200 {
		return nil, &ClientError{StatusCode: res.StatusCode, Body: string(body)}
	}
	var resp TotpValidateResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, &ClientError{StatusCode: res.StatusCode, Body: string(body), RootCause: fmt.Errorf("error unmarshalling: %s", err.Error())}
	}
	return &resp, nil
}
