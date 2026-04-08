package handler

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/open-policy-agent/opa/rego"
	"github.com/tlalocweb/hulation/log"
)

// OpaInputCreationFunc creates the input map for the rego policy evaluation.
// Returns the input, the status code to return in case of error, an error message, and an error.
type OpaInputCreationFunc func(ctx RequestCtx) (map[string]interface{}, int, string, error)

// OpaConfig configures the OPA middleware.
type OpaConfig struct {
	RegoPolicy            io.Reader
	RegoQuery             string
	IncludeHeaders        []string
	IncludeQueryString    bool
	DeniedStatusCode      int
	DeniedResponseMessage string
	InputCreationMethod   OpaInputCreationFunc
}

// NewOpaMiddleware creates a unified OPA middleware.
func NewOpaMiddleware(cfg OpaConfig) Middleware {
	if err := cfg.fillAndValidate(); err != nil {
		panic(err)
	}
	opaPolicy, err := io.ReadAll(cfg.RegoPolicy)
	if err != nil {
		panic(fmt.Sprintf("could not read rego policy: %v", err))
	}
	query, err := rego.New(
		rego.Query(cfg.RegoQuery),
		rego.Module("policy.rego", string(opaPolicy)),
	).PrepareForEval(context.Background())
	if err != nil {
		panic(fmt.Sprintf("rego policy error: %v", err))
	}

	return func(next Handler) Handler {
		return func(ctx RequestCtx) error {
			input, statuscode, errorstring, err := cfg.InputCreationMethod(ctx)
			if err != nil {
				log.Debugf("Error creating input (opa): %v %d %s", err, statuscode, errorstring)
				if statuscode == 0 {
					statuscode = 533
				}
				if len(errorstring) < 1 {
					errorstring = fmt.Sprintf("Error creating input: %s", err)
				}
				return ctx.Status(statuscode).SendString(errorstring)
			}
			if cfg.IncludeQueryString {
				qs := ctx.QueryString()
				queryStringData := make(map[string][]string)
				if len(qs) > 0 {
					values, _ := url.ParseQuery(qs)
					for k, v := range values {
						queryStringData[k] = v
					}
				}
				input["query"] = queryStringData
			}
			if len(cfg.IncludeHeaders) > 0 {
				headers := make(map[string]string)
				for _, header := range cfg.IncludeHeaders {
					headers[header] = ctx.Header(header)
				}
				input["headers"] = headers
			}
			res, err := query.Eval(context.Background(), rego.EvalInput(input))
			if err != nil {
				return ctx.Status(534).SendString(fmt.Sprintf("Error evaluating rego policy: %s", err))
			}

			if !res.Allowed() {
				return ctx.Status(cfg.DeniedStatusCode).SendString(cfg.DeniedResponseMessage)
			}

			return next(ctx)
		}
	}
}

func opaDefaultInput(ctx RequestCtx) (map[string]interface{}, int, string, error) {
	input := map[string]interface{}{
		"method": ctx.Method(),
		"path":   ctx.Path(),
	}
	return input, 0, "", nil
}

func (c *OpaConfig) fillAndValidate() error {
	if c.RegoQuery == "" {
		return fmt.Errorf("rego query can not be empty")
	}
	if c.DeniedStatusCode == 0 {
		c.DeniedStatusCode = http.StatusBadRequest
	}
	if c.DeniedResponseMessage == "" {
		c.DeniedResponseMessage = "Bad Request"
	}
	if c.IncludeHeaders == nil {
		c.IncludeHeaders = []string{}
	}
	if c.InputCreationMethod == nil {
		c.InputCreationMethod = opaDefaultInput
	}
	return nil
}

