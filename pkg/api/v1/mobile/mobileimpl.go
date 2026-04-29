// Package mobile implements MobileService — a compact,
// phone-sized projection of the analytics read surface + push-
// device registration. Summary/Timeseries reuse the Phase-1 query
// builder; the mobile layer only downsamples and strips fields.

package mobile

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/tlalocweb/hulation/model"
	analyticsspec "github.com/tlalocweb/hulation/pkg/apispec/v1/analytics"
	mobilespec "github.com/tlalocweb/hulation/pkg/apispec/v1/mobile"
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
	tokenKeyFn   TokenKeyFn
}

// New constructs the MobileService implementation. summaryFn /
// timeseriesFn are usually (&analyticsimpl.Server{}).Summary +
// .Timeseries method values — decouples the package graph.
func New(
	summaryFn func(context.Context, *analyticsspec.SummaryRequest) (*analyticsspec.SummaryResponse, error),
	timeseriesFn func(context.Context, *analyticsspec.TimeseriesRequest) (*analyticsspec.TimeseriesResponse, error),
	tokenKeyFn TokenKeyFn,
) *Server {
	return &Server{
		summaryFn:    summaryFn,
		timeseriesFn: timeseriesFn,
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

func (s *Server) RegisterDevice(ctx context.Context, req *mobilespec.RegisterDeviceRequest) (*mobilespec.Device, error) {
	if req == nil || req.GetToken() == "" || req.GetPlatform() == mobilespec.Platform_PLATFORM_UNSPECIFIED {
		return nil, status.Error(codes.InvalidArgument, "token and platform required")
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
	sealed, err := tokenbox.Seal(req.GetToken(), key)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "seal token: %s", err)
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
		ID:                id,
		UserID:            userID,
		Platform:          platformString(req.GetPlatform()),
		DeviceFingerprint: req.GetDeviceFingerprint(),
		Label:             req.GetLabel(),
		TokenCipher:       sealed,
		Active:            true,
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

// Unused import guards for the trimmed-down impl — keep `model`
// imported so this file compiles even before the analytics wire-up
// supplies summaryFn/timeseriesFn. Remove when tests exercise both.
var _ = fmt.Sprintf
var _ sql.NullInt64
var _ = model.GetSQLDB
