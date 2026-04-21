// Package forms is the gRPC FormsService implementation. It wraps the
// existing model.FormModel layer — same DB, same validation, just a
// typed gRPC surface instead of the legacy Fiber handlers.
package forms

import (
	"context"

	"github.com/tlalocweb/hulation/config"
	"github.com/tlalocweb/hulation/log"
	"github.com/tlalocweb/hulation/model"
	formsspec "github.com/tlalocweb/hulation/pkg/apispec/v1/forms"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

var fLog = log.GetTaggedLogger("forms-impl", "gRPC FormsService implementation")

// Server implements formsspec.FormsServiceServer.
type Server struct {
	formsspec.UnimplementedFormsServiceServer
}

// New returns a FormsService implementation.
func New() *Server { return &Server{} }

// validateServerID returns an InvalidArgument error if the server_id
// doesn't match any configured server. The permission middleware has
// already enforced that the caller holds a role on this server.
func validateServerID(serverID string) error {
	if serverID == "" {
		return status.Error(codes.InvalidArgument, "server_id is required")
	}
	cfg := config.GetConfig()
	if cfg == nil {
		return status.Error(codes.FailedPrecondition, "server not configured")
	}
	for _, srv := range cfg.Servers {
		if srv.ID == serverID {
			return nil
		}
	}
	return status.Errorf(codes.NotFound, "server %q not configured", serverID)
}

// formToProto marshals a model.FormModel into the proto Form message.
func formToProto(m *model.FormModel) *formsspec.Form {
	if m == nil {
		return nil
	}
	return &formsspec.Form{
		Id:          m.ID,
		Name:        m.Name,
		Description: m.Description,
		Schema:      m.Schema,
		Captcha:     m.Captcha,
		Feedback:    m.Feedback,
		CreatedAt:   timestamppb.New(m.CreatedAt),
		UpdatedAt:   timestamppb.New(m.UpdatedAt),
	}
}

// CreateForm validates the request and persists a new FormModel. Matches
// the legacy handler.FormCreate semantics: name + schema required.
func (s *Server) CreateForm(ctx context.Context, req *formsspec.CreateFormRequest) (*formsspec.CreateFormResponse, error) {
	if err := validateServerID(req.GetServerId()); err != nil {
		return nil, err
	}
	if req.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}
	if req.GetSchema() == "" {
		return nil, status.Error(codes.InvalidArgument, "schema is required")
	}

	// model.CreateNewFormModel(id, name, description, schema, captcha,
	// feedback). The id param is derived from the name (legacy behavior
	// in handler.FormCreate line 254).
	m, err := model.CreateNewFormModel(req.GetName(), req.GetName(), req.GetDescription(), req.GetSchema(), req.GetCaptcha(), req.GetFeedback())
	if err != nil {
		fLog.Errorf("CreateNewFormModel: %v", err)
		return nil, status.Errorf(codes.Internal, "create form model: %v", err)
	}

	if _, err := m.ValidateNewModel(model.GetDB()); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "validate form model: %v", err)
	}
	if err := m.Commit(model.GetDB()); err != nil {
		fLog.Errorf("commit form model %q: %v", m.ID, err)
		return nil, status.Errorf(codes.Internal, "commit form model: %v", err)
	}

	return &formsspec.CreateFormResponse{Form: formToProto(m)}, nil
}

// ModifyForm applies non-empty fields onto the existing model. Matches
// the PATCH semantics of handler.FormModify: only non-zero fields
// overwrite.
func (s *Server) ModifyForm(ctx context.Context, req *formsspec.ModifyFormRequest) (*formsspec.ModifyFormResponse, error) {
	if err := validateServerID(req.GetServerId()); err != nil {
		return nil, err
	}
	if req.GetFormId() == "" {
		return nil, status.Error(codes.InvalidArgument, "form_id is required")
	}

	m, err := model.GetFormModelById(model.GetDB(), req.GetFormId())
	if err != nil {
		fLog.Errorf("GetFormModelById %q: %v", req.GetFormId(), err)
		return nil, status.Errorf(codes.Internal, "lookup form: %v", err)
	}
	if m == nil {
		return nil, status.Errorf(codes.NotFound, "form %q not found", req.GetFormId())
	}

	if req.GetName() != "" {
		m.Name = req.GetName()
	}
	if req.GetDescription() != "" {
		m.Description = req.GetDescription()
	}
	if req.GetSchema() != "" {
		m.Schema = req.GetSchema()
	}
	if req.GetCaptcha() != "" {
		m.Captcha = req.GetCaptcha()
	}
	if req.GetFeedback() != "" {
		m.Feedback = req.GetFeedback()
	}

	if err := m.ValidateExistingModel(model.GetDB()); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "validate: %v", err)
	}
	if err := m.Commit(model.GetDB()); err != nil {
		return nil, status.Errorf(codes.Internal, "commit: %v", err)
	}
	return &formsspec.ModifyFormResponse{Form: formToProto(m)}, nil
}

// DeleteForm removes the given form. Matches handler.FormDelete.
func (s *Server) DeleteForm(ctx context.Context, req *formsspec.DeleteFormRequest) (*formsspec.DeleteFormResponse, error) {
	if err := validateServerID(req.GetServerId()); err != nil {
		return nil, err
	}
	if req.GetFormId() == "" {
		return nil, status.Error(codes.InvalidArgument, "form_id is required")
	}

	m, err := model.GetFormModelById(model.GetDB(), req.GetFormId())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "lookup form: %v", err)
	}
	if m == nil {
		return nil, status.Errorf(codes.NotFound, "form %q not found", req.GetFormId())
	}
	if err := m.Delete(model.GetDB()); err != nil {
		return nil, status.Errorf(codes.Internal, "delete: %v", err)
	}
	return &formsspec.DeleteFormResponse{Ok: true}, nil
}

// ListForms returns all forms. No filtering in Phase 0 — pagination and
// server-id-scoped listings can be added in a later phase if needed.
func (s *Server) ListForms(ctx context.Context, req *formsspec.ListFormsRequest) (*formsspec.ListFormsResponse, error) {
	if err := validateServerID(req.GetServerId()); err != nil {
		return nil, err
	}
	forms, err := model.ListFormModels(model.GetDB())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list forms: %v", err)
	}
	out := make([]*formsspec.Form, 0, len(forms))
	for _, f := range forms {
		out = append(out, formToProto(f))
	}
	return &formsspec.ListFormsResponse{Forms: out}, nil
}

// GetForm fetches one form by ID. Falls back to name lookup if ID
// lookup misses, matching legacy handler.FormSubmit's form-resolution
// strategy.
func (s *Server) GetForm(ctx context.Context, req *formsspec.GetFormRequest) (*formsspec.GetFormResponse, error) {
	if err := validateServerID(req.GetServerId()); err != nil {
		return nil, err
	}
	if req.GetFormId() == "" {
		return nil, status.Error(codes.InvalidArgument, "form_id is required")
	}
	m, err := model.GetFormModelById(model.GetDB(), req.GetFormId())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "lookup form: %v", err)
	}
	if m == nil {
		m, err = model.GetFormModelByName(model.GetDB(), req.GetFormId())
		if err != nil {
			return nil, status.Errorf(codes.Internal, "lookup form by name: %v", err)
		}
	}
	if m == nil {
		return nil, status.Errorf(codes.NotFound, "form %q not found", req.GetFormId())
	}
	return &formsspec.GetFormResponse{Form: formToProto(m)}, nil
}
