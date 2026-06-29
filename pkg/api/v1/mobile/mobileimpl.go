// Package mobile implements MobileService — a compact,
// phone-sized projection of the analytics read surface + push-
// device registration. Summary/Timeseries reuse the Phase-1 query
// builder; the mobile layer only downsamples and strips fields.

package mobile

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/tlalocweb/hulation/config"
	"github.com/tlalocweb/hulation/model"
	analyticsspec "github.com/tlalocweb/hulation/pkg/apispec/v1/analytics"
	mobilespec "github.com/tlalocweb/hulation/pkg/apispec/v1/mobile"
	chatpkg "github.com/tlalocweb/hulation/pkg/chat"
	"github.com/tlalocweb/hulation/pkg/mobile/tokenbox"
	"github.com/tlalocweb/hulation/pkg/server/authware"
	hulabolt "github.com/tlalocweb/hulation/pkg/store/bolt"
	"github.com/tlalocweb/hulation/pkg/store/storage"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// SparklineSize is the fixed bucket count on MobileSummary's embedded
// sparkline. Chosen to comfortably fit a phone-width KPI card (12
// buckets ≈ one bar per two hours across a 24h window).
const SparklineSize = 12

// TokenKeyFn returns the master key used to seal push tokens.
// Installed by server.RunUnified; nil → Register/Unregister error
// cleanly rather than falling back to plaintext storage.
type TokenKeyFn func() ([]byte, error)

// Server implements mobilespec.MobileServiceServer.
type Server struct {
	mobilespec.UnimplementedMobileServiceServer
	// Summary/Timeseries reach the analytics service through the
	// shared query builder via this handle; the wire-up is done by
	// construction from server.BootUnifiedServer.
	summaryFn    func(ctx context.Context, req *analyticsspec.SummaryRequest) (*analyticsspec.SummaryResponse, error)
	timeseriesFn func(ctx context.Context, req *analyticsspec.TimeseriesRequest) (*analyticsspec.TimeseriesResponse, error)
	// pagesFn drives MobileTopPages; usually analyticsimpl.Server.Pages.
	pagesFn func(ctx context.Context, req *analyticsspec.PagesRequest) (*analyticsspec.PagesResponse, error)
	// chatStore drives MobileLiveChats. nil → that RPC returns
	// FailedPrecondition (chat not wired into this build).
	chatStore  *chatpkg.Store
	cfg        *config.Config
	tokenKeyFn TokenKeyFn
}

// New constructs the MobileService implementation. The fn parameters
// are usually method values from analyticsimpl.Server — decouples the
// package graph.
func New(
	summaryFn func(context.Context, *analyticsspec.SummaryRequest) (*analyticsspec.SummaryResponse, error),
	timeseriesFn func(context.Context, *analyticsspec.TimeseriesRequest) (*analyticsspec.TimeseriesResponse, error),
	pagesFn func(context.Context, *analyticsspec.PagesRequest) (*analyticsspec.PagesResponse, error),
	chatStore *chatpkg.Store,
	cfg *config.Config,
	tokenKeyFn TokenKeyFn,
) *Server {
	return &Server{
		summaryFn:    summaryFn,
		timeseriesFn: timeseriesFn,
		pagesFn:      pagesFn,
		chatStore:    chatStore,
		cfg:          cfg,
		tokenKeyFn:   tokenKeyFn,
	}
}

// --- Helpers ---------------------------------------------------------

func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// currentUserID pulls the caller's identity from the authware Claims
// installed by the AdminBearerInterceptor. Admins can spoof via
// explicit user_id args in write endpoints; read endpoints always
// trust the claim.
func currentUserID(ctx context.Context) string {
	c, ok := ctx.Value(authware.ClaimsKey).(*authware.Claims)
	if !ok || c == nil {
		return ""
	}
	if c.Email != "" {
		return c.Email
	}
	return c.Username
}

func currentClaims(ctx context.Context) *authware.Claims {
	c, _ := ctx.Value(authware.ClaimsKey).(*authware.Claims)
	return c
}

func claimsUserID(c *authware.Claims) string {
	if c == nil {
		return ""
	}
	if c.Subject != "" {
		return c.Subject
	}
	if c.Email != "" {
		return c.Email
	}
	return c.Username
}

func claimsHasRole(c *authware.Claims, want string) bool {
	if c == nil {
		return false
	}
	for _, r := range c.Roles {
		if r == want {
			return true
		}
	}
	return false
}

// presetToRange maps "24h" / "7d" / "30d" to (from, to, granularity).
func presetToRange(preset string) (time.Time, time.Time, string) {
	to := time.Now().UTC()
	var from time.Time
	grain := "hour"
	switch preset {
	case "24h":
		from = to.Add(-24 * time.Hour)
		grain = "hour"
	case "30d":
		from = to.Add(-30 * 24 * time.Hour)
		grain = "day"
	case "", "7d":
		from = to.Add(-7 * 24 * time.Hour)
		grain = "day"
	default:
		from = to.Add(-7 * 24 * time.Hour)
	}
	return from, to, grain
}

// downsample takes a longer timeseries and bucketises to `target`
// points by summing adjacent buckets. When the input is already
// shorter than target, returns it unchanged.
func downsample(values []int64, target int) []int64 {
	if target <= 0 || len(values) <= target {
		return values
	}
	out := make([]int64, target)
	for i, v := range values {
		bucket := i * target / len(values)
		if bucket >= target {
			bucket = target - 1
		}
		out[bucket] += v
	}
	return out
}

func platformString(p mobilespec.Platform) string {
	switch p {
	case mobilespec.Platform_PLATFORM_APNS:
		return "apns"
	case mobilespec.Platform_PLATFORM_FCM:
		return "fcm"
	}
	return ""
}

func platformEnum(s string) mobilespec.Platform {
	switch s {
	case "apns":
		return mobilespec.Platform_PLATFORM_APNS
	case "fcm":
		return mobilespec.Platform_PLATFORM_FCM
	}
	return mobilespec.Platform_PLATFORM_UNSPECIFIED
}

func storedDeviceToProto(d hulabolt.StoredDevice) *mobilespec.Device {
	out := &mobilespec.Device{
		Id:                d.ID,
		UserId:            d.UserID,
		Platform:          platformEnum(d.Platform),
		DeviceFingerprint: d.DeviceFingerprint,
		Label:             d.Label,
		Active:            d.Active,
	}
	if !d.RegisteredAt.IsZero() {
		out.RegisteredAt = timestamppb.New(d.RegisteredAt)
	}
	if !d.LastSeenAt.IsZero() {
		out.LastSeenAt = timestamppb.New(d.LastSeenAt)
	}
	return out
}

// --- RPCs ------------------------------------------------------------

func (s *Server) ListMobileSites(ctx context.Context, req *mobilespec.ListMobileSitesRequest) (*mobilespec.ListMobileSitesResponse, error) {
	claims := currentClaims(ctx)
	if claims == nil {
		return nil, status.Error(codes.Unauthenticated, "caller has no identity")
	}
	if s.cfg == nil {
		return &mobilespec.ListMobileSitesResponse{Sites: []*mobilespec.MobileSite{}}, nil
	}

	allowed := map[string]struct{}{}
	superadmin := claimsHasRole(claims, "admin") || claimsHasRole(claims, "root")
	if superadmin {
		for _, configured := range s.cfg.Servers {
			if configured != nil && configured.ID != "" {
				allowed[configured.ID] = struct{}{}
			}
		}
	} else {
		userID := claimsUserID(claims)
		ids, err := hulabolt.AllowedServerIDsForUser(ctx, storage.Global(), userID)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "list server access: %s", err)
		}
		for _, id := range ids {
			if id != "" {
				allowed[id] = struct{}{}
			}
		}
	}

	out := &mobilespec.ListMobileSitesResponse{
		Sites: []*mobilespec.MobileSite{},
	}
	for _, configured := range s.cfg.Servers {
		if configured == nil || configured.ID == "" {
			continue
		}
		if _, ok := allowed[configured.ID]; !ok {
			continue
		}
		out.Sites = append(out.Sites, &mobilespec.MobileSite{
			Id:   configured.ID,
			Host: configured.Host,
			Name: configured.ID,
		})
		if out.CurrentServerId == "" {
			out.CurrentServerId = configured.ID
		}
	}
	return out, nil
}

func (s *Server) RegisterDevice(ctx context.Context, req *mobilespec.RegisterDeviceRequest) (*mobilespec.Device, error) {
	if req == nil || req.GetPlatform() == mobilespec.Platform_PLATFORM_UNSPECIFIED {
		return nil, status.Error(codes.InvalidArgument, "platform required")
	}
	// At least one path must be supplied — relay (3 fields) or legacy (raw token).
	hasRelay := req.GetRelayChannelId() != "" ||
		req.GetRelayChannelAuth() != "" ||
		req.GetNoiseEncryptionPubB64() != ""
	if !hasRelay && req.GetToken() == "" {
		return nil, status.Error(codes.InvalidArgument,
			"either `token` (legacy path) or all three relay fields are required")
	}
	if hasRelay {
		if req.GetRelayChannelId() == "" || req.GetRelayChannelAuth() == "" ||
			req.GetNoiseEncryptionPubB64() == "" {
			return nil, status.Error(codes.InvalidArgument,
				"relay_channel_id, relay_channel_auth, and noise_encryption_pub_b64 must all be set together")
		}
	}

	userID := currentUserID(ctx)
	if userID == "" {
		return nil, status.Error(codes.Unauthenticated, "caller has no identity")
	}
	if s.tokenKeyFn == nil {
		return nil, status.Error(codes.FailedPrecondition, "token encryption key not installed; device registration disabled")
	}
	key, err := s.tokenKeyFn()
	if err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "token key: %s", err)
	}

	// Seal the legacy token (if supplied) and the relay channel auth (if supplied)
	// with the same tokenbox key — both are sensitive material we never want at
	// rest in plaintext.
	var tokenSealed []byte
	if t := req.GetToken(); t != "" {
		tokenSealed, err = tokenbox.Seal(t, key)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "seal token: %s", err)
		}
	}
	var relayAuthSealed []byte
	var noisePubB64 string
	if hasRelay {
		relayAuthSealed, err = tokenbox.Seal(req.GetRelayChannelAuth(), key)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "seal relay auth: %s", err)
		}
		// Validate the noise public key parses cleanly and is exactly 32 bytes —
		// fan-out time is the wrong place to discover a malformed key.
		pubBytes, derr := base64.StdEncoding.DecodeString(req.GetNoiseEncryptionPubB64())
		if derr != nil {
			return nil, status.Errorf(codes.InvalidArgument,
				"noise_encryption_pub_b64 is not valid base64: %s", derr)
		}
		if len(pubBytes) != 32 {
			return nil, status.Errorf(codes.InvalidArgument,
				"noise_encryption_pub_b64 must decode to 32 bytes, got %d", len(pubBytes))
		}
		noisePubB64 = req.GetNoiseEncryptionPubB64()
	}

	// Idempotency: replace an existing (user_id, fingerprint) row
	// rather than creating a second.
	existing, _ := hulabolt.FindDeviceByFingerprint(ctx, storage.Global(), userID, req.GetDeviceFingerprint())
	id := ""
	if existing != nil {
		id = existing.ID
	} else {
		id = newID()
	}

	d := hulabolt.StoredDevice{
		ID:                     id,
		UserID:                 userID,
		Platform:               platformString(req.GetPlatform()),
		DeviceFingerprint:      req.GetDeviceFingerprint(),
		Label:                  req.GetLabel(),
		TokenCipher:            tokenSealed,
		Active:                 true,
		RelayChannelID:         req.GetRelayChannelId(),
		RelayChannelAuthCipher: relayAuthSealed,
		NoiseEncryptionPub:     noisePubB64,
	}
	saved, err := hulabolt.PutDevice(ctx, storage.Global(), d)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "put device: %s", err)
	}
	return storedDeviceToProto(saved), nil
}

func (s *Server) UnregisterDevice(ctx context.Context, req *mobilespec.UnregisterDeviceRequest) (*mobilespec.UnregisterDeviceResponse, error) {
	if req == nil || req.GetDeviceId() == "" {
		return nil, status.Error(codes.InvalidArgument, "device_id required")
	}
	// Only the owner can unregister.
	existing, err := hulabolt.GetDevice(ctx, storage.Global(), req.GetDeviceId())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get device: %s", err)
	}
	if existing == nil {
		return nil, status.Error(codes.NotFound, "device not found")
	}
	if existing.UserID != currentUserID(ctx) {
		return nil, status.Error(codes.PermissionDenied, "not your device")
	}
	if err := hulabolt.DeleteDevice(ctx, storage.Global(), req.GetDeviceId()); err != nil {
		return nil, status.Errorf(codes.Internal, "delete device: %s", err)
	}
	return &mobilespec.UnregisterDeviceResponse{Ok: true}, nil
}

func (s *Server) ListMyDevices(ctx context.Context, req *mobilespec.ListMyDevicesRequest) (*mobilespec.ListMyDevicesResponse, error) {
	userID := currentUserID(ctx)
	if userID == "" {
		return nil, status.Error(codes.Unauthenticated, "caller has no identity")
	}
	rows, err := hulabolt.ListDevicesForUser(ctx, storage.Global(), userID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list devices: %s", err)
	}
	out := make([]*mobilespec.Device, 0, len(rows))
	for _, r := range rows {
		out = append(out, storedDeviceToProto(r))
	}
	return &mobilespec.ListMyDevicesResponse{Devices: out}, nil
}

func (s *Server) MobileSummary(ctx context.Context, req *mobilespec.MobileSummaryRequest) (*mobilespec.MobileSummaryResponse, error) {
	if req == nil || req.GetServerId() == "" {
		return nil, status.Error(codes.InvalidArgument, "server_id required")
	}
	from, to, grain := presetToRange(req.GetPreset())
	filters := &analyticsspec.Filters{
		From:        from.Format(time.RFC3339),
		To:          to.Format(time.RFC3339),
		Granularity: grain,
	}
	sum, err := s.summaryFn(ctx, &analyticsspec.SummaryRequest{
		ServerId: req.GetServerId(),
		Filters:  filters,
	})
	if err != nil {
		return nil, err
	}
	ts, err := s.timeseriesFn(ctx, &analyticsspec.TimeseriesRequest{
		ServerId: req.GetServerId(),
		Filters:  filters,
	})
	if err != nil {
		return nil, err
	}
	visitors := make([]int64, 0, len(ts.Buckets))
	for _, b := range ts.Buckets {
		visitors = append(visitors, b.Visitors)
	}
	return &mobilespec.MobileSummaryResponse{
		Visitors:                  sum.Visitors,
		Pageviews:                 sum.Pageviews,
		BounceRate:                sum.BounceRate,
		AvgSessionDurationSeconds: sum.AvgSessionDurationSeconds,
		SparklineVisitors:         downsample(visitors, SparklineSize),
	}, nil
}

func (s *Server) MobileTimeseries(ctx context.Context, req *mobilespec.MobileTimeseriesRequest) (*mobilespec.MobileTimeseriesResponse, error) {
	if req == nil || req.GetServerId() == "" {
		return nil, status.Error(codes.InvalidArgument, "server_id required")
	}
	from, to, defaultGrain := presetToRange(req.GetPreset())
	grain := req.GetGranularity()
	if grain == "" {
		grain = defaultGrain
	}
	ts, err := s.timeseriesFn(ctx, &analyticsspec.TimeseriesRequest{
		ServerId: req.GetServerId(),
		Filters: &analyticsspec.Filters{
			From:        from.Format(time.RFC3339),
			To:          to.Format(time.RFC3339),
			Granularity: grain,
		},
	})
	if err != nil {
		return nil, err
	}
	target := 24
	if grain == "hour" {
		target = 12
	}
	tsOut := make([]string, 0, target)
	visitors := make([]int64, 0, len(ts.Buckets))
	pageviews := make([]int64, 0, len(ts.Buckets))
	for _, b := range ts.Buckets {
		tsOut = append(tsOut, b.Ts)
		visitors = append(visitors, b.Visitors)
		pageviews = append(pageviews, b.Pageviews)
	}
	return &mobilespec.MobileTimeseriesResponse{
		Ts:        downsampleStrings(tsOut, target),
		Visitors:  downsample(visitors, target),
		Pageviews: downsample(pageviews, target),
	}, nil
}

// downsampleStrings keeps evenly-spaced timestamps from a longer
// series. Used for the Ts labels on MobileTimeseries so the label
// count matches the downsampled numeric arrays.
func downsampleStrings(values []string, target int) []string {
	if target <= 0 || len(values) <= target {
		return values
	}
	out := make([]string, target)
	for i := 0; i < target; i++ {
		idx := i * len(values) / target
		out[i] = values[idx]
	}
	return out
}

func (s *Server) MobileTopPages(ctx context.Context, req *mobilespec.MobileTopPagesRequest) (*mobilespec.MobileTopPagesResponse, error) {
	if req == nil || req.GetServerId() == "" {
		return nil, status.Error(codes.InvalidArgument, "server_id required")
	}
	if s.pagesFn == nil {
		return nil, status.Error(codes.FailedPrecondition, "top-pages not wired (analytics service unavailable)")
	}
	// Phone Overview card fits ~5 rows above the fold; cap at 25 so a
	// misconfigured client can't ask for thousands.
	limit := req.GetLimit()
	if limit <= 0 {
		limit = 5
	}
	if limit > 25 {
		limit = 25
	}
	from, to, grain := presetToRange(req.GetPreset())
	pages, err := s.pagesFn(ctx, &analyticsspec.PagesRequest{
		ServerId: req.GetServerId(),
		Filters: &analyticsspec.Filters{
			From:        from.Format(time.RFC3339),
			To:          to.Format(time.RFC3339),
			Granularity: grain,
		},
		Limit: limit,
	})
	if err != nil {
		return nil, err
	}
	out := make([]*mobilespec.MobileTopPage, 0, len(pages.GetRows()))
	for _, r := range pages.GetRows() {
		out = append(out, &mobilespec.MobileTopPage{
			Path:      r.GetKey(),
			Visitors:  r.GetVisitors(),
			Pageviews: r.GetPageviews(),
		})
	}
	return &mobilespec.MobileTopPagesResponse{Pages: out}, nil
}

func (s *Server) MobileLiveChats(ctx context.Context, req *mobilespec.MobileLiveChatsRequest) (*mobilespec.MobileLiveChatsResponse, error) {
	if req == nil || req.GetServerId() == "" {
		return nil, status.Error(codes.InvalidArgument, "server_id required")
	}
	if s.chatStore == nil {
		return nil, status.Error(codes.FailedPrecondition, "live-chats not wired (chat store unavailable)")
	}
	// One query per status — ListSessions returns the pre-paging
	// total in the second return value, so Limit=1 keeps the row
	// scan tiny while still letting us read the count.
	count := func(statuses ...string) (int32, error) {
		_, total, err := s.chatStore.ListSessions(ctx, chatpkg.ListSessionsFilter{
			ServerID: req.GetServerId(),
			Statuses: statuses,
			Limit:    1,
		})
		if err != nil {
			return 0, err
		}
		if total > 1<<31-1 {
			total = 1<<31 - 1
		}
		return int32(total), nil
	}
	awaiting, err := count(chatpkg.StatusQueued)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "count queued: %s", err)
	}
	assigned, err := count(chatpkg.StatusAssigned)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "count assigned: %s", err)
	}
	open, err := count(chatpkg.StatusOpen)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "count open: %s", err)
	}
	return &mobilespec.MobileLiveChatsResponse{
		AwaitingReply: awaiting,
		Assigned:      assigned,
		Open:          open,
	}, nil
}

// Unused import guards for the trimmed-down impl — keep `model`
// imported so this file compiles even before the analytics wire-up
// supplies summaryFn/timeseriesFn. Remove when tests exercise both.
var _ = fmt.Sprintf
var _ sql.NullInt64
var _ = model.GetSQLDB
