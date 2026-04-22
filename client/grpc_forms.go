package client

// gRPC-backed FormsService methods on Client. Require DialGRPC()
// first; each call returns ErrNoGRPC if the gRPC path hasn't been
// initialized.
//
// These sit alongside the legacy HTTP methods in forms.go. Callers can
// migrate incrementally — or run both and compare.

import (
	"context"
	"fmt"

	formsspec "github.com/tlalocweb/hulation/pkg/apispec/v1/forms"
)

// ErrNoGRPC is returned when a Grpc*-prefixed method is called before
// DialGRPC has run.
var ErrNoGRPC = fmt.Errorf("gRPC client not dialed — call Client.DialGRPC first")

// GrpcFormCreate creates a form via the gRPC FormsService. server_id
// is the permission-scoping server identifier.
func (c *Client) GrpcFormCreate(ctx context.Context, serverID, name, description, schema, captcha, feedback string) (*formsspec.Form, error) {
	if c.grpc == nil {
		return nil, ErrNoGRPC
	}
	resp, err := c.grpc.forms.CreateForm(c.authCtx(ctx), &formsspec.CreateFormRequest{
		ServerId:    serverID,
		Name:        name,
		Description: description,
		Schema:      schema,
		Captcha:     captcha,
		Feedback:    feedback,
	})
	if err != nil {
		return nil, err
	}
	return resp.GetForm(), nil
}

// GrpcFormModify applies a PATCH-style update. Empty strings are left
// unchanged on the server.
func (c *Client) GrpcFormModify(ctx context.Context, serverID, formID, name, description, schema, captcha, feedback string) (*formsspec.Form, error) {
	if c.grpc == nil {
		return nil, ErrNoGRPC
	}
	resp, err := c.grpc.forms.ModifyForm(c.authCtx(ctx), &formsspec.ModifyFormRequest{
		ServerId:    serverID,
		FormId:      formID,
		Name:        name,
		Description: description,
		Schema:      schema,
		Captcha:     captcha,
		Feedback:    feedback,
	})
	if err != nil {
		return nil, err
	}
	return resp.GetForm(), nil
}

// GrpcFormDelete removes a form by id.
func (c *Client) GrpcFormDelete(ctx context.Context, serverID, formID string) error {
	if c.grpc == nil {
		return ErrNoGRPC
	}
	_, err := c.grpc.forms.DeleteForm(c.authCtx(ctx), &formsspec.DeleteFormRequest{
		ServerId: serverID,
		FormId:   formID,
	})
	return err
}

// GrpcFormList returns every form configured on the server.
func (c *Client) GrpcFormList(ctx context.Context, serverID string) ([]*formsspec.Form, error) {
	if c.grpc == nil {
		return nil, ErrNoGRPC
	}
	resp, err := c.grpc.forms.ListForms(c.authCtx(ctx), &formsspec.ListFormsRequest{
		ServerId: serverID,
	})
	if err != nil {
		return nil, err
	}
	return resp.GetForms(), nil
}

// GrpcFormGet fetches one form by id. The server falls back to name
// lookup if the id doesn't match any record.
func (c *Client) GrpcFormGet(ctx context.Context, serverID, formID string) (*formsspec.Form, error) {
	if c.grpc == nil {
		return nil, ErrNoGRPC
	}
	resp, err := c.grpc.forms.GetForm(c.authCtx(ctx), &formsspec.GetFormRequest{
		ServerId: serverID,
		FormId:   formID,
	})
	if err != nil {
		return nil, err
	}
	return resp.GetForm(), nil
}
