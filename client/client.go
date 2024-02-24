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

type Client struct {
	apiUrl   string `yaml:"apiurl" json:"apiurl"`
	token    string `yaml:"token" json:"token"`
	Noisy    bool   // set to true to print output to OutputFunc
	NoisyErr bool   // set to true to error output to ErrOutFunc
	// url breakdown
	proto  string                            // protocol
	host   string                            // host
	port   int64                             // port
	path   string                            // path
	Output func(string, ...any) (int, error) // defaults to fmt.Printf
	ErrOut func(string, ...any) (int, error) // defaults to fmt.Printf
}

func (c *Client) out(format string, a ...any) {
	if c.Noisy {
		c.Output(format, a...)
	}
}

func (c *Client) errout(format string, a ...any) {
	if c.NoisyErr {
		c.ErrOut(format, a...)
	}
}

// NewClient creates a new client
// Url is mandatory, token is optional - if no token is provided
// then a call of Auth() will be needed
func NewClient(url string, token string) (c *Client) {
	c = &Client{
		apiUrl:   url,
		token:    token,
		Noisy:    false,
		NoisyErr: false,
		Output:   fmt.Printf,
		ErrOut:   fmt.Printf,
	}
	c.proto, c.host, c.port, c.path = utils.GetURLPieces(url)
	return
}

func (c *Client) GetAPIUrl() string {
	return c.apiUrl
}

func (c *Client) GetToken() string {
	return c.token
}

type ClientError struct {
	StatusCode int
	Body       string
	RootCause  error
}

func (e *ClientError) Error() (ret string) {
	ret = "ClientError "
	if e.StatusCode > 0 {
		ret = fmt.Sprintf("%s: response %d", ret, e.StatusCode)
	}
	if e.RootCause != nil {
		ret = fmt.Sprintf("%s: %s", ret, e.RootCause.Error())
	}
	return
}

type ClientResponse struct {
	StatusCode int
	Body       string
	Response   interface{}
	start      time.Time
	finish     time.Time
}

func NewResponse() (r *ClientResponse) {
	r = &ClientResponse{
		start: time.Now(),
	}
	return
}

func (r *ClientResponse) Finish(statuscode int, body string, response interface{}) {
	r.finish = time.Now()
	r.StatusCode = statuscode
	r.Body = body
	r.Response = response
}

func (r *ClientResponse) Duration() time.Duration {
	return r.finish.Sub(r.start)
}

type AuthResponse struct {
	Token string `json:"jwt"`
}

// Auth get a JWT using the /auth/login endpoint
func (c *Client) Auth(identity string, pass string) (resp *ClientResponse, token string, err error) {
	url := fmt.Sprintf("%s://%s:%d%s/auth/login", c.proto, c.host, c.port, c.path)

	hash := utils.GenerateHulaNetworkPassHash(pass)
	// make request
	//		http.Post(url, "application/json", bytes.NewBuffer([]byte(fmt.Sprintf(`{"userid": "%s", "hash": "%s"}`, identity, hash))))
	c.out("auth url: %s\n", url)
	req, err := http.NewRequest("POST", url, bytes.NewBuffer([]byte(fmt.Sprintf(`{"userid": "%s", "hash": "%s"}`, identity, hash))))
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
	}
	resp.Finish(res.StatusCode, "", nil)
	c.out("response: %d\n", res.StatusCode)
	resBody, err := io.ReadAll(res.Body)
	if err != nil {
		c.errout("client: could not read response body: %s\n", err)
		err = fmt.Errorf("could not read response body (ReadAll): %s", err.Error())
	}
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
	c.out("Token: %s\n", authresp.Token)
	c.out("Response took: %s\n", resp.Duration())
	token = authresp.Token
	resp.Body = string(resBody)
	resp.Response = authresp
	return
}

func (c *Client) StatusAuthOK() (resp *ClientResponse, err error) {
	url := fmt.Sprintf("%s://%s:%d%s/api/auth/ok", c.proto, c.host, c.port, c.path)
	c.out("GET statusauthok url: %s\n", url)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		c.errout("Error creating request: %s\n", err.Error())
		err = &ClientError{RootCause: fmt.Errorf("error creating request: %s", err.Error())}
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)
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
		err = &ClientError{StatusCode: res.StatusCode, RootCause: fmt.Errorf("could not read response body (ReadAll): %s", err.Error())}
		return
	}
	c.out("body: %s\n", string(resBody))
	resp.Body = string(resBody)
	if res.StatusCode != 200 {
		c.errout("Error: %d  Body: %s\n", res.StatusCode, string(resBody))
		err = &ClientError{StatusCode: res.StatusCode, Body: string(resBody)}
		return
	}
	var respok handler.StatusAuthOKResp
	err = json.Unmarshal(resBody, &respok)
	if err != nil {
		c.errout("Error unmarshalling response: %s\n", err.Error())
		err = &ClientError{StatusCode: res.StatusCode, Body: string(resBody), RootCause: fmt.Errorf("error unmarshalling response: %s", err.Error())}
	} else {
		resp.Response = &respok
	}

	return
}
