package middleware

import (
	"context"
	"fmt"
	"io"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/utils"
	"github.com/open-policy-agent/opa/rego"
	"github.com/tlalocweb/hulation/log"
	// "golang.org/x/crypto/openpgp"
)

// This is a fork of:
// https://github.com/gofiber/contrib/blob/main/opafiber/README.md

// InputCreationFunc is a function that creates the input for the rego policy
// returns the input, the status code to return in case of error along with a string message, and an error
type InputCreationFunc func(c *fiber.Ctx) (map[string]interface{}, int, string, error)

type OpaConfig struct {
	RegoPolicy            io.Reader
	CacheRegoPolicy       bool
	RegoQuery             string
	IncludeHeaders        []string
	IncludeQueryString    bool
	DeniedStatusCode      int
	DeniedResponseMessage string
	InputCreationMethod   InputCreationFunc
}

// var regoPolicyCached []byte

func NewOpaMiddleware(cfg OpaConfig) fiber.Handler {
	err := cfg.fillAndValidate()
	if err != nil {
		panic(err)
	}
	var opaPolicy []byte
	// if cfg.CacheRegoPolicy {
	// 	if len(regoPolicyCached) < 1 {
	// 		opaPolicy, err = io.ReadAll(cfg.RegoPolicy)
	// 		if err != nil {
	// 			panic(fmt.Sprint("could not read rego policy %w", err))
	// 		}
	// 		regoPolicyCached = opaPolicy
	// 	} else {
	// 		opaPolicy = regoPolicyCached
	// 	}
	// } else {
	opaPolicy, err = io.ReadAll(cfg.RegoPolicy)
	if err != nil {
		panic(fmt.Sprint("could not read rego policy %w", err))
	}
	// }
	query, err := rego.New(
		rego.Query(cfg.RegoQuery),
		rego.Module("policy.rego", utils.UnsafeString(opaPolicy)),
	).PrepareForEval(context.Background())
	if err != nil {
		panic(fmt.Sprint("rego policy error: %w", err))
	}
	return func(c *fiber.Ctx) error {
		input, statuscode, errorstring, err := cfg.InputCreationMethod(c)
		if err != nil {
			log.Debugf("Error creating input (opa): %v %d %s", err, statuscode, errorstring)
			if statuscode == 0 {
				statuscode = 533
			}
			if len(errorstring) < 1 {
				errorstring = fmt.Sprintf("Error creating input: %s", err)
			}
			c.Response().SetStatusCode(statuscode)
			c.Response().SetBodyString(errorstring)
			return fiber.NewError(statuscode, errorstring)
		}
		if cfg.IncludeQueryString {
			queryStringData := make(map[string][]string)
			c.Request().URI().QueryArgs().VisitAll(func(key, value []byte) {
				queryStringData[utils.UnsafeString(key)] = append(queryStringData[utils.UnsafeString(key)], utils.UnsafeString(value))
			})
			input["query"] = queryStringData
		}
		if len(cfg.IncludeHeaders) > 0 {
			headers := make(map[string]string)
			for _, header := range cfg.IncludeHeaders {
				headers[header] = c.Get(header)
			}
			input["headers"] = headers
		}
		res, err := query.Eval(context.Background(), rego.EvalInput(input))
		if err != nil {
			c.Response().SetStatusCode(534)
			c.Response().SetBodyString(fmt.Sprintf("Error evaluating rego policy: %s", err))
			return fiber.NewError(534, fmt.Sprintf("Error evaluating rego policy: %s", err))
		}

		if !res.Allowed() {
			c.Response().SetStatusCode(cfg.DeniedStatusCode)
			c.Response().SetBodyString(cfg.DeniedResponseMessage)
			return fiber.NewError(cfg.DeniedStatusCode, cfg.DeniedResponseMessage)
		}

		return c.Next()
	}
}

func (c *OpaConfig) fillAndValidate() error {
	if c.RegoQuery == "" {
		return fmt.Errorf("rego query can not be empty")
	}

	if c.DeniedStatusCode == 0 {
		c.DeniedStatusCode = fiber.StatusBadRequest
	}
	if c.DeniedResponseMessage == "" {
		c.DeniedResponseMessage = fiber.ErrBadRequest.Error()
	}
	if c.IncludeHeaders == nil {
		c.IncludeHeaders = []string{}
	}
	if c.InputCreationMethod == nil {
		c.InputCreationMethod = defaultInput
	}
	return nil
}

func defaultInput(ctx *fiber.Ctx) (map[string]interface{}, int, string, error) {
	input := map[string]interface{}{
		"method": ctx.Method(),
		"path":   ctx.Path(),
	}
	return input, 0, "", nil
}
