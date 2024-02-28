package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/tlalocweb/hulation/handler"
	"github.com/tlalocweb/hulation/utils"
)

func (c *Client) FormCreate(modelreq string) (err error) {
	//	url := fmt.Sprintf("%s://%s:%d%s/form/create", c.proto, c.host, c.port, c.path)

	if c.token == "" {
		c.errout("No token\n")
		err = &ClientError{RootCause: fmt.Errorf("no token. use 'auth' command to get a token")}
		return
	}

	var modelcheck handler.FormModelReq
	modelcheckalt := map[string]json.RawMessage{}

	err = json.Unmarshal([]byte(modelreq), &modelcheck)

	//This client supports the "schema" field as a string or as an object.
	// The server expect the schema as a string, so we need to check if the schema is an object and then
	// JSON marshal it to a string.
	if err != nil || len(modelcheck.Schema) < 1 {
		// try alternate method
		c.out("FormCrate: modelreq: %s\n", modelreq)
		err = json.Unmarshal([]byte(modelreq), &modelcheckalt)
		if err != nil {
			err = &ClientError{RootCause: fmt.Errorf("error unmarshalling modelreq (alt) 1 - Check JSON: %v", err)}
			return
		}
		c.out("FormCreate: modelreq (alt) - Name: %v\nDesc: %v\nSchema: %v\n", modelcheckalt["name"], modelcheckalt["description"], modelcheckalt["schema"])
		err = handler.FormRawJSONMessageToFormModelReq(modelcheckalt, &modelcheck)
		if err != nil {
			err = &ClientError{RootCause: fmt.Errorf("error unmarshalling modelreq (alt) 2 - Check JSON: %v", err)}
			return
		}
		if len(modelcheck.Schema) < 1 {
			err = &ClientError{RootCause: fmt.Errorf("no schema in modelreq")}
			return
		}
		c.out("FormCreate: modelreq (alt) - Name: %s\n%s", modelcheck.Name, modelcheck.Schema)
	}

	modelbody, err := json.Marshal(modelcheck)
	if err != nil {
		err = &ClientError{RootCause: fmt.Errorf("error marshalling request body: %v", err)}
		return
	}
	url := c.apiUrl + "/form/create"
	c.out("FormCreate: POST url: %s\n", url)
	req, err := http.NewRequest("POST", url, bytes.NewBuffer(modelbody))
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
		c.errout("FormCreate: Error: %d  Body: %s\n", resp.StatusCode, string(body))
		return &ClientError{StatusCode: resp.StatusCode, Body: string(body)}
	}
	c.out("FormCreate: response: %s\n", body)
	return
}

func (c *Client) FormModify(formid string, modelreq string) (err error) {

	if c.token == "" {
		c.errout("No token\n")
		err = &ClientError{RootCause: fmt.Errorf("no token. use 'auth' command to get a token")}
		return
	}

	var modelcheck handler.FormModelReq
	modelcheckalt := map[string]json.RawMessage{}

	err = json.Unmarshal([]byte(modelreq), &modelcheck)

	//This client supports the "schema" field as a string or as an object.
	// The server expect the schema as a string, so we need to check if the schema is an object and then
	// JSON marshal it to a string.
	if err != nil || len(modelcheck.Schema) < 1 {
		// try alternate method
		c.out("FormModify: modelreq: %s\n", modelreq)
		err = json.Unmarshal([]byte(modelreq), &modelcheckalt)
		if err != nil {
			err = &ClientError{RootCause: fmt.Errorf("error unmarshalling modelreq (alt) 1 - Check JSON: %v", err)}
			return
		}
		c.out("FormCreate: modelreq (alt) - Name: %v\nDesc: %v\nSchema: %v\n", modelcheckalt["name"], modelcheckalt["description"], modelcheckalt["schema"])
		err = handler.FormRawJSONMessageToFormModelReq(modelcheckalt, &modelcheck)
		if err != nil {
			err = &ClientError{RootCause: fmt.Errorf("error unmarshalling modelreq (alt) 2 - Check JSON: %v", err)}
			return
		}
		if len(modelcheck.Schema) < 1 {
			err = &ClientError{RootCause: fmt.Errorf("no schema in modelreq")}
			return
		}
		c.out("FormCreate: modelreq (alt) - Name: %s\n%s\n", modelcheck.Name, modelcheck.Schema)
	}

	modelbody, err := json.Marshal(modelcheck)
	if err != nil {
		err = &ClientError{RootCause: fmt.Errorf("error marshalling request body: %v", err)}
		return
	}
	url := c.apiUrl + "/form/" + formid
	c.out("FormModify: PATCH url: %s\n", url)
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
		c.errout("FormModify: Error: %d  Body: %s\n", resp.StatusCode, string(body))
		return &ClientError{StatusCode: resp.StatusCode, Body: string(body)}
	}
	c.out("FormModify: response: %s\n", body)
	return
}

func (c *Client) FormDelete(formid string) (err error) {
	url := c.apiUrl + "/form/" + formid
	c.out("FormDelete: DELETE url: %s\n", url)
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

func (c *Client) FormSubmit(formsub []byte, formid string, hostid string) (resp *ClientResponse, err error) {
	r := utils.FastRandString(8)
	url := fmt.Sprintf("%s://%s:%d%s/v/sub/%s?h=%s&r=%s", c.proto, c.host, c.port, c.path, formid, hostid, r)
	c.out("FormSubmit url: %s\n", url)
	req, err := http.NewRequest("POST", url, bytes.NewBuffer(formsub))
	if err != nil {
		c.errout("Error creating request: %s\n", err.Error())
		err = &ClientError{RootCause: fmt.Errorf("error creating request: %s", err.Error())}
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp = NewResponse()
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		c.errout("client: error making http request: %s\n", err)
		err = &ClientError{RootCause: fmt.Errorf("error making http request: %s", err.Error())}
		return
	}
	resp.Finish(res.StatusCode, "", nil)
	c.out("response: %d\n", res.StatusCode)
	resBody, err := io.ReadAll(res.Body)
	if err != nil {
		c.errout("client: could not read response body: %s\n", err)
		err = fmt.Errorf("could not read response body (ReadAll): %s", err.Error())
		return
	}
	resp.Finish(res.StatusCode, string(resBody), nil)
	c.out("Response took: %s\n", resp.Duration())
	c.out("body: %s\n", string(resBody))

	if res.StatusCode != 200 {
		c.errout("Error: %d  Body: %s\n", res.StatusCode, string(resBody))
		err = &ClientError{StatusCode: res.StatusCode, Body: string(resBody)}
		return
	}
	var authresp AuthResponse
	err = json.Unmarshal(resBody, &authresp)

	if err != nil {
		c.errout("Error unmarshalling response: %s\n", err.Error())
		err = fmt.Errorf("error unmarshalling response: %s", err.Error())
		return
	}

	resp.Body = string(resBody)
	resp.Response = authresp
	return
}
