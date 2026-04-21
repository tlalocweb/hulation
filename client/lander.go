package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/tlalocweb/hulation/handler"
)

func (c *Client) LanderCreate(modelreq string) (ret *handler.LanderPostResp, err error) {
	url := fmt.Sprintf("%s://%s:%d%s/api/lander/create", c.proto, c.host, c.port, c.path)

	if c.token == "" {
		c.errout("No token\n")
		err = &ClientError{RootCause: fmt.Errorf("no token. use 'auth' command to get a token")}
		return
	}

	var modelcheck handler.LanderReq
	err = json.Unmarshal([]byte(modelreq), &modelcheck)
	if err != nil {
		err = &ClientError{RootCause: fmt.Errorf("error unmarshalling modelreq - Check JSON: %v", err)}
		return
	}

	modelbody, err := json.Marshal(modelcheck)
	if err != nil {
		err = &ClientError{RootCause: fmt.Errorf("error marshalling request body: %v", err)}
		return
	}

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(modelbody))
	if err != nil {
		err = &ClientError{RootCause: fmt.Errorf("error creating request: %v", err)}
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)

	client := &http.Client{Timeout: time.Second * 10}
	clientresp := NewResponse()
	resp, err := client.Do(req)
	if err != nil {
		err = &ClientError{RootCause: fmt.Errorf("error sending request: %v", err)}
		return
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	clientresp.Finish(resp.StatusCode, string(body), nil)
	if err != nil {
		return nil, &ClientError{RootCause: fmt.Errorf("error reading response: %v", err)}
	}
	if resp.StatusCode != 201 {
		c.errout("LanderCreate: Error: %d  Body: %s\n", resp.StatusCode, string(body))
		return nil, &ClientError{StatusCode: resp.StatusCode, Body: string(body)}
	}

	ret = &handler.LanderPostResp{}
	err = json.Unmarshal(body, ret)
	if err != nil {
		return nil, &ClientError{RootCause: fmt.Errorf("error unmarshalling response: %v", err)}
	}
	c.out("LanderCreate: response: %s\n", body)

	return
}

func (c *Client) LanderModify(formid string, modelreq string) (err error) {

	if c.token == "" {
		c.errout("No token\n")
		err = &ClientError{RootCause: fmt.Errorf("no token. use 'auth' command to get a token")}
		return
	}

	var modelcheck handler.LanderReq
	err = json.Unmarshal([]byte(modelreq), &modelcheck)
	if err != nil {
		err = &ClientError{RootCause: fmt.Errorf("error unmarshalling modelreq - Check JSON: %v", err)}
		return
	}

	modelbody, err := json.Marshal(modelcheck)
	if err != nil {
		err = &ClientError{RootCause: fmt.Errorf("error marshalling request body: %v", err)}
		return
	}

	url := c.apiUrl + "/api/lander/" + formid
	c.out("LanderModify: PATCH url: %s\n", url)
	req, err := http.NewRequest("PATCH", url, bytes.NewBuffer(modelbody))
	if err != nil {
		return &ClientError{RootCause: fmt.Errorf("error creating request: %v", err)}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)

	c.out("Body: %s\n", string(modelbody))
	client := &http.Client{Timeout: time.Second * 10}
	clientresp := NewResponse()
	resp, err := client.Do(req)
	if err != nil {
		return &ClientError{RootCause: fmt.Errorf("error sending request: %v", err)}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	clientresp.Finish(resp.StatusCode, string(body), nil)
	if err != nil {
		return &ClientError{RootCause: fmt.Errorf("error reading response: %v", err)}
	}
	if resp.StatusCode != 200 {
		c.errout("LanderModify: Error: %d  Body: %s\n", resp.StatusCode, string(body))
		return &ClientError{StatusCode: resp.StatusCode, Body: string(body)}
	}
	c.out("FormModify: response: %s\n", body)
	return
}

func (c *Client) LanderDelete(formid string) (err error) {
	url := c.apiUrl + "/api/lander/" + formid
	c.out("LanderDelete: DELETE url: %s\n", url)
	req, err := http.NewRequest("DELETE", url, nil)
	if err != nil {
		return &ClientError{RootCause: fmt.Errorf("error creating request: %v", err)}
	}
	// req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)

	client := &http.Client{Timeout: time.Second * 10}
	clientresp := NewResponse()
	resp, err := client.Do(req)
	if err != nil {
		return &ClientError{RootCause: fmt.Errorf("error sending request: %v", err)}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	clientresp.Finish(resp.StatusCode, string(body), nil)
	if err != nil {
		return &ClientError{RootCause: fmt.Errorf("error reading response: %v", err)}
	}
	if resp.StatusCode != 200 {
		c.errout("FormDelete: Error: %d  Body: %s\n", resp.StatusCode, string(body))
		return &ClientError{StatusCode: resp.StatusCode, Body: string(body)}
	}
	c.out("FormDelete: response: %s\n", body)
	return
}
