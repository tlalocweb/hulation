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

	"github.com/tlalocweb/hulation/model"
	goalsspec "github.com/tlalocweb/hulation/pkg/apispec/v1/goals"
	hulabolt "github.com/tlalocweb/hulation/pkg/store/bolt"
	"github.com/tlalocweb/hulation/pkg/store/storage"
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
	saved, err := hulabolt.PutGoal(ctx, storage.Global(), g)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "create goal: %s", err)
	}
	return storedToProto(saved), nil
}

func (s *Server) UpdateGoal(ctx context.Context, req *goalsspec.UpdateGoalRequest) (*goalsspec.Goal, error) {
	if req == nil || req.GetServerId() == "" || req.GetGoalId() == "" || req.GetGoal() == nil {
		return nil, status.Error(codes.InvalidArgument, "server_id, goal_id, and goal required")
	}
	existing, err := hulabolt.GetGoal(ctx, storage.Global(), req.GetGoalId())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get goal: %s", err)
	}
	if existing == nil || existing.ServerID != req.GetServerId() {
		return nil, status.Error(codes.NotFound, "goal not found")
	}
	g := protoToStored(req.GetGoal())
	g.ID = req.GetGoalId()
	g.ServerID = req.GetServerId()
	saved, err := hulabolt.PutGoal(ctx, storage.Global(), g)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "update goal: %s", err)
	}
	return storedToProto(saved), nil
}

func (s *Server) DeleteGoal(ctx context.Context, req *goalsspec.DeleteGoalRequest) (*goalsspec.DeleteGoalResponse, error) {
	if req == nil || req.GetGoalId() == "" {
		return nil, status.Error(codes.InvalidArgument, "goal_id required")
	}
	if err := hulabolt.DeleteGoal(ctx, storage.Global(), req.GetGoalId()); err != nil {
		return nil, status.Errorf(codes.Internal, "delete goal: %s", err)
	}
	return &goalsspec.DeleteGoalResponse{Ok: true}, nil
}

func (s *Server) ListGoals(ctx context.Context, req *goalsspec.ListGoalsRequest) (*goalsspec.ListGoalsResponse, error) {
	serverID := ""
	if req != nil {
		serverID = req.GetServerId()
	}
	rows, err := hulabolt.ListGoals(ctx, storage.Global(), serverID)
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
	g, err := hulabolt.GetGoal(ctx, storage.Global(), req.GetGoalId())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get goal: %s", err)
	}
	if g == nil || g.ServerID != req.GetServerId() {
		return nil, status.Error(codes.NotFound, "goal not found")
	}
	return storedToProto(*g), nil
}

// ListConversions still inherits Unimplemented — that needs the
// per-goal conversions aggregate MV which lands alongside the alerts
// engine in Phase 4.

// TestGoal dry-runs a goal rule against the last N days of raw events.
// Returns the count of matching events + the total events scanned so
// the admin UI can show "would fire X / scanned Y".
//
// This does not write anything: no is_goal flag is set on matching
// rows, no conversions row is inserted. Safe to call repeatedly.
//
// Scale assumption: called once per goal-author-click, not on a loop.
// Scans are bounded by server_id + a TTL-clamped time window, so even
// on a large events table the query is cheap (partition pruning +
// ORDER BY (when, server_id, ...) makes this a range scan).
func (s *Server) TestGoal(ctx context.Context, req *goalsspec.TestGoalRequest) (*goalsspec.TestGoalResponse, error) {
	if req.GetServerId() == "" {
		return nil, status.Error(codes.InvalidArgument, "server_id is required")
	}
	g := req.GetGoal()
	if g == nil {
		return nil, status.Error(codes.InvalidArgument, "goal is required")
	}
	days := req.GetDays()
	if days <= 0 {
		days = 7
	}
	if days > 90 {
		days = 90 // cap scan to 90d — dry-run is a UX affordance, not a report
	}

	db := model.GetSQLDB()
	if db == nil {
		return nil, status.Error(codes.FailedPrecondition, "ClickHouse not available")
	}

	since := time.Now().UTC().Add(-time.Duration(days) * 24 * time.Hour)

	where, args, err := goalWhereClause(g)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "goal rule: %s", err)
	}

	// Total events scanned = all events for this server in the window;
	// would_fire = subset that matches the rule.
	const qScanned = `SELECT count() FROM events WHERE server_id = ? AND when >= ?`
	var scanned int64
	if err := db.QueryRowContext(ctx, qScanned, req.GetServerId(), since).Scan(&scanned); err != nil {
		return nil, status.Errorf(codes.Internal, "scan count: %s", err)
	}

	qMatch := `SELECT count() FROM events WHERE server_id = ? AND when >= ? AND ` + where
	matchArgs := append([]any{req.GetServerId(), since}, args...)
	var fired int64
	if err := db.QueryRowContext(ctx, qMatch, matchArgs...).Scan(&fired); err != nil {
		return nil, status.Errorf(codes.Internal, "match count: %s", err)
	}

	return &goalsspec.TestGoalResponse{
		WouldFire:     fired,
		ScannedEvents: scanned,
	}, nil
}

// EventCode values mirrored from model/event.go. Only the subset
// referenced by goal rules (Form + Lander) is listed — the others
// aren't needed for match generation.
const (
	eventCodeFormSubmission = 0x00000020
	eventCodeLanderHit      = 0x00000100
)

// goalWhereClause translates a proto Goal into a SQL fragment + args.
// Returned fragment must be safe to concatenate onto a larger WHERE
// clause — it's parameterised via `?` placeholders and never
// interpolates user-controlled strings.
//
// For the FORM and LANDER kinds we look for the form/lander id inside
// the `data` column — the ingest path stores those as a small JSON
// blob. A substring match is good enough for a dry-run preview; the
// ingest-side evaluator does the same thing.
func goalWhereClause(g *goalsspec.Goal) (string, []any, error) {
	switch g.GetKind() {
	case goalsspec.GoalKind_GOAL_KIND_URL_VISIT:
		rx := g.GetRuleUrlRegex()
		if rx == "" {
			return "", nil, errEmptyRule("rule_url_regex")
		}
		return "match(url_path, ?)", []any{rx}, nil

	case goalsspec.GoalKind_GOAL_KIND_EVENT:
		code := g.GetRuleEventCode()
		if code == 0 {
			return "", nil, errEmptyRule("rule_event_code")
		}
		return "code = ?", []any{code}, nil

	case goalsspec.GoalKind_GOAL_KIND_FORM:
		fid := g.GetRuleFormId()
		if fid == "" {
			return "", nil, errEmptyRule("rule_form_id")
		}
		return "code = ? AND position(data, ?) > 0", []any{int64(eventCodeFormSubmission), fid}, nil

	case goalsspec.GoalKind_GOAL_KIND_LANDER:
		lid := g.GetRuleLanderId()
		if lid == "" {
			return "", nil, errEmptyRule("rule_lander_id")
		}
		return "code = ? AND position(data, ?) > 0", []any{int64(eventCodeLanderHit), lid}, nil
	}
	return "", nil, errEmptyRule("kind")
}

type errEmptyRule string

func (e errEmptyRule) Error() string { return string(e) + " is required for this goal kind" }
