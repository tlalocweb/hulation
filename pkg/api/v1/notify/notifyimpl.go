// Package notify implements NotifyService — per-user notification
// preferences + a test-send helper.

package notify

import (
	"context"

	notifyspec "github.com/tlalocweb/hulation/pkg/apispec/v1/notify"
	"github.com/tlalocweb/hulation/pkg/notifier"
	"github.com/tlalocweb/hulation/pkg/server/authware"
	hulabolt "github.com/tlalocweb/hulation/pkg/store/bolt"
	"github.com/tlalocweb/hulation/pkg/store/storage"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Server implements notifyspec.NotifyServiceServer.
type Server struct {
	notifyspec.UnimplementedNotifyServiceServer
}

// New constructs the impl.
func New() *Server { return &Server{} }

// --- Helpers ---------------------------------------------------------

func protoToStored(p *notifyspec.NotificationPrefs) hulabolt.StoredNotificationPrefs {
	if p == nil {
		return hulabolt.StoredNotificationPrefs{}
	}
	return hulabolt.StoredNotificationPrefs{
		UserID:          p.GetUserId(),
		EmailEnabled:    p.GetEmailEnabled(),
		PushEnabled:     p.GetPushEnabled(),
		Timezone:        p.GetTimezone(),
		QuietHoursStart: p.GetQuietHoursStart(),
		QuietHoursEnd:   p.GetQuietHoursEnd(),
	}
}

func storedToProto(s hulabolt.StoredNotificationPrefs) *notifyspec.NotificationPrefs {
	out := &notifyspec.NotificationPrefs{
		UserId:          s.UserID,
		EmailEnabled:    s.EmailEnabled,
		PushEnabled:     s.PushEnabled,
		Timezone:        s.Timezone,
		QuietHoursStart: s.QuietHoursStart,
		QuietHoursEnd:   s.QuietHoursEnd,
	}
	if !s.UpdatedAt.IsZero() {
		out.UpdatedAt = timestamppb.New(s.UpdatedAt)
	}
	return out
}

func callerID(ctx context.Context) string {
	c, ok := ctx.Value(authware.ClaimsKey).(*authware.Claims)
	if !ok || c == nil {
		return ""
	}
	if c.Email != "" {
		return c.Email
	}
	return c.Username
}

// isAdmin derives a pragmatic admin bit from the authware claims. The
// Phase-0 admin bearer flow identifies the root user by a distinct
// Username; every other claim shape is treated as non-admin here.
// Callers that need a finer gate should use the permission
// annotation on the proto instead.
func isAdmin(ctx context.Context) bool {
	c, ok := ctx.Value(authware.ClaimsKey).(*authware.Claims)
	if !ok || c == nil {
		return false
	}
	// Admin tokens carry "admin" in the Roles slice; non-admin ones
	// don't. This matches the authware.Claims shape documented in
	// pkg/server/authware/claims.go.
	for _, r := range c.Roles {
		if r == "admin" || r == "superadmin" {
			return true
		}
	}
	return false
}

// --- RPCs ------------------------------------------------------------

func (s *Server) GetNotificationPrefs(ctx context.Context, req *notifyspec.GetNotificationPrefsRequest) (*notifyspec.NotificationPrefs, error) {
	if req == nil || req.GetUserId() == "" {
		return nil, status.Error(codes.InvalidArgument, "user_id required")
	}
	caller := callerID(ctx)
	// Non-admin can only read their own row.
	if !isAdmin(ctx) && caller != req.GetUserId() {
		return nil, status.Error(codes.PermissionDenied, "cannot read another user's prefs")
	}
	p, err := hulabolt.GetNotificationPrefs(ctx, storage.Global(), req.GetUserId())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get prefs: %s", err)
	}
	return storedToProto(p), nil
}

func (s *Server) SetNotificationPrefs(ctx context.Context, req *notifyspec.SetNotificationPrefsRequest) (*notifyspec.NotificationPrefs, error) {
	if req == nil || req.GetUserId() == "" || req.GetPrefs() == nil {
		return nil, status.Error(codes.InvalidArgument, "user_id and prefs required")
	}
	caller := callerID(ctx)
	if !isAdmin(ctx) && caller != req.GetUserId() {
		return nil, status.Error(codes.PermissionDenied, "cannot modify another user's prefs")
	}
	p := protoToStored(req.GetPrefs())
	p.UserID = req.GetUserId() // URL wins over body
	saved, err := hulabolt.PutNotificationPrefs(ctx, storage.Global(), p)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "put prefs: %s", err)
	}
	return storedToProto(saved), nil
}

func (s *Server) ListNotificationPrefs(ctx context.Context, req *notifyspec.ListNotificationPrefsRequest) (*notifyspec.ListNotificationPrefsResponse, error) {
	rows, err := hulabolt.ListNotificationPrefs(ctx, storage.Global())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list prefs: %s", err)
	}
	out := make([]*notifyspec.NotificationPrefs, 0, len(rows))
	for _, r := range rows {
		out = append(out, storedToProto(r))
	}
	return &notifyspec.ListNotificationPrefsResponse{Rows: out}, nil
}

// TestNotification synthesises an envelope to every channel the
// target user has enabled and reports per-channel outcomes. Admin-
// gated via the proto permission annotation; non-admin callers don't
// reach this method.
func (s *Server) TestNotification(ctx context.Context, req *notifyspec.TestNotificationRequest) (*notifyspec.TestNotificationResponse, error) {
	if req == nil || req.GetUserId() == "" {
		return nil, status.Error(codes.InvalidArgument, "user_id required")
	}
	prefs, err := hulabolt.GetNotificationPrefs(ctx, storage.Global(), req.GetUserId())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get prefs: %s", err)
	}
	env := notifier.Envelope{
		ID:        "test-" + req.GetUserId(),
		Subject:   orDefault(req.GetSubject(), "Hula test notification"),
		HTMLBody:  orDefault(req.GetBody(), "<p>This is a test from Hula. You can safely ignore it.</p>"),
		ShortText: orDefault(req.GetBody(), "This is a test from Hula."),
	}
	if prefs.EmailEnabled {
		env.Recipients = append(env.Recipients, notifier.DeviceAddr{
			Channel: notifier.ChannelEmail,
			Email:   req.GetUserId(), // prefs.user_id is the email address in the Phase-5a single-tenant shell
			UserID:  req.GetUserId(),
		})
	}
	if prefs.PushEnabled {
		devs, err := hulabolt.ListDevicesForUser(ctx, storage.Global(), req.GetUserId())
		if err == nil {
			for _, d := range devs {
				if !d.Active {
					continue
				}
				// Push token stays sealed — TestNotification can't open
				// tokens without the master key, so we attach the sealed
				// blob and rely on the notifier backend to open via its
				// own reference (deferred: passing the key down cleanly
				// is a follow-up). For now: test pushes are rendered but
				// not delivered; per-channel result surfaces the gap.
				ch := notifier.ChannelAPNS
				if d.Platform == "fcm" {
					ch = notifier.ChannelFCM
				}
				env.Recipients = append(env.Recipients, notifier.DeviceAddr{
					Channel:   ch,
					UserID:    d.UserID,
					DeviceID:  d.ID,
					PushToken: "", // caller sees ErrNotConfigured / failure on push channel — acceptable for a test hook
				})
			}
		}
	}

	n := notifier.Global()
	if n == nil {
		return nil, status.Error(codes.FailedPrecondition, "notifier not configured on this host")
	}
	report, _ := n.Deliver(ctx, env)

	resp := &notifyspec.TestNotificationResponse{}
	for _, r := range report.Results {
		tr := &notifyspec.TestChannelResult{
			Channel: string(r.Channel),
			Ok:      r.OK,
		}
		if r.Err != nil {
			tr.Error = r.Err.Error()
		}
		resp.Results = append(resp.Results, tr)
	}
	return resp, nil
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
