// Package goals implements the GoalsService gRPC API — per-server
// conversion-goal CRUD plus TestGoal / ListConversions read paths.
//
// Goals persist in pkg/store/bolt; ingest-time tagging of events is
// a separate sub-stage (3.3b). For now ListConversions applies the
// goal rule at query time against the raw events table, which is
// fine for the admin-page scale (1–100 goals per server).

package goals

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"

	goalsspec "github.com/tlalocweb/hulation/pkg/apispec/v1/goals"
	hulabolt "github.com/tlalocweb/hulation/pkg/store/bolt"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Server implements goalsspec.GoalsServiceServer.
type Server struct {
	goalsspec.UnimplementedGoalsServiceServer
}

// New constructs a GoalsService implementation.
func New() *Server { return &Server{} }

// --- helpers ---------------------------------------------------------

func protoToStored(g *goalsspec.Goal) hulabolt.StoredGoal {
	return hulabolt.StoredGoal{
		ID:            g.GetId(),
		ServerID:      g.GetServerId(),
		Name:          g.GetName(),
		Description:   g.GetDescription(),
		Kind:          kindEnumToString(g.GetKind()),
		RuleURLRegex:  g.GetRuleUrlRegex(),
		RuleEventCode: g.GetRuleEventCode(),
		RuleFormID:    g.GetRuleFormId(),
		RuleLanderID:  g.GetRuleLanderId(),
		Enabled:       g.GetEnabled(),
	}
}

func storedToProto(g hulabolt.StoredGoal) *goalsspec.Goal {
	out := &goalsspec.Goal{
		Id:            g.ID,
		ServerId:      g.ServerID,
		Name:          g.Name,
		Description:   g.Description,
		Kind:          kindStringToEnum(g.Kind),
		RuleUrlRegex:  g.RuleURLRegex,
		RuleEventCode: g.RuleEventCode,
		RuleFormId:    g.RuleFormID,
		RuleLanderId:  g.RuleLanderID,
		Enabled:       g.Enabled,
	}
	if !g.CreatedAt.IsZero() {
		out.CreatedAt = timestamppb.New(g.CreatedAt)
	}
	if !g.UpdatedAt.IsZero() {
		out.UpdatedAt = timestamppb.New(g.UpdatedAt)
	}
	return out
}

func kindEnumToString(k goalsspec.GoalKind) string {
	switch k {
	case goalsspec.GoalKind_GOAL_KIND_URL_VISIT:
		return "url_visit"
	case goalsspec.GoalKind_GOAL_KIND_EVENT:
		return "event"
	case goalsspec.GoalKind_GOAL_KIND_FORM:
		return "form"
	case goalsspec.GoalKind_GOAL_KIND_LANDER:
		return "lander"
	}
	return ""
}

func kindStringToEnum(s string) goalsspec.GoalKind {
	switch s {
	case "url_visit":
		return goalsspec.GoalKind_GOAL_KIND_URL_VISIT
	case "event":
		return goalsspec.GoalKind_GOAL_KIND_EVENT
	case "form":
		return goalsspec.GoalKind_GOAL_KIND_FORM
	case "lander":
		return goalsspec.GoalKind_GOAL_KIND_LANDER
	}
	return goalsspec.GoalKind_GOAL_KIND_UNSPECIFIED
}

// newID returns a compact random 16-byte ID rendered hex. Kept local
// so the goals package doesn't depend on google/uuid (already pulled
// in by other packages but the hex form is more ergonomic in BoltDB
// keys).
func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// --- RPCs ------------------------------------------------------------

func (s *Server) CreateGoal(ctx context.Context, req *goalsspec.CreateGoalRequest) (*goalsspec.Goal, error) {
	if req == nil || req.GetServerId() == "" || req.GetGoal() == nil {
		return nil, status.Error(codes.InvalidArgument, "server_id and goal required")
	}
	g := protoToStored(req.GetGoal())
	g.ServerID = req.GetServerId() // trust the URL path over the body
	if g.ID == "" {
		g.ID = newID()
	}
	g.CreatedAt = time.Time{} // let the store set it
	saved, err := hulabolt.PutGoal(g)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "create goal: %s", err)
	}
	return storedToProto(saved), nil
}

func (s *Server) UpdateGoal(ctx context.Context, req *goalsspec.UpdateGoalRequest) (*goalsspec.Goal, error) {
	if req == nil || req.GetServerId() == "" || req.GetGoalId() == "" || req.GetGoal() == nil {
		return nil, status.Error(codes.InvalidArgument, "server_id, goal_id, and goal required")
	}
	existing, err := hulabolt.GetGoal(req.GetGoalId())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get goal: %s", err)
	}
	if existing == nil || existing.ServerID != req.GetServerId() {
		return nil, status.Error(codes.NotFound, "goal not found")
	}
	g := protoToStored(req.GetGoal())
	g.ID = req.GetGoalId()
	g.ServerID = req.GetServerId()
	saved, err := hulabolt.PutGoal(g)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "update goal: %s", err)
	}
	return storedToProto(saved), nil
}

func (s *Server) DeleteGoal(ctx context.Context, req *goalsspec.DeleteGoalRequest) (*goalsspec.DeleteGoalResponse, error) {
	if req == nil || req.GetGoalId() == "" {
		return nil, status.Error(codes.InvalidArgument, "goal_id required")
	}
	if err := hulabolt.DeleteGoal(req.GetGoalId()); err != nil {
		return nil, status.Errorf(codes.Internal, "delete goal: %s", err)
	}
	return &goalsspec.DeleteGoalResponse{Ok: true}, nil
}

func (s *Server) ListGoals(ctx context.Context, req *goalsspec.ListGoalsRequest) (*goalsspec.ListGoalsResponse, error) {
	serverID := ""
	if req != nil {
		serverID = req.GetServerId()
	}
	rows, err := hulabolt.ListGoals(serverID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list goals: %s", err)
	}
	out := make([]*goalsspec.Goal, 0, len(rows))
	for _, r := range rows {
		out = append(out, storedToProto(r))
	}
	return &goalsspec.ListGoalsResponse{Goals: out}, nil
}

func (s *Server) GetGoal(ctx context.Context, req *goalsspec.GetGoalRequest) (*goalsspec.Goal, error) {
	if req == nil || req.GetGoalId() == "" {
		return nil, status.Error(codes.InvalidArgument, "goal_id required")
	}
	g, err := hulabolt.GetGoal(req.GetGoalId())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get goal: %s", err)
	}
	if g == nil || g.ServerID != req.GetServerId() {
		return nil, status.Error(codes.NotFound, "goal not found")
	}
	return storedToProto(*g), nil
}

// ListConversions + TestGoal land in a follow-up: both need the
// pkg/analytics/query builder + a rule evaluator that translates
// StoredGoal into a WHERE clause. CRUD is sufficient for the admin
// UI to author rules and see them listed.
//
// Inherit Unimplemented from UnimplementedGoalsServiceServer.
