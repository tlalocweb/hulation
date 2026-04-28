package client

// gRPC-backed methods for Landers, Site, Staging, BadActor, Status,
// and the Auth service's public RPCs. Pair with client/grpc_forms.go;
// gated on a prior DialGRPC() call.

import (
	"context"

	authspec "github.com/tlalocweb/hulation/pkg/apispec/v1/auth"
	badactorspec "github.com/tlalocweb/hulation/pkg/apispec/v1/badactor"
	landersspec "github.com/tlalocweb/hulation/pkg/apispec/v1/landers"
	sitespec "github.com/tlalocweb/hulation/pkg/apispec/v1/site"
	stagingspec "github.com/tlalocweb/hulation/pkg/apispec/v1/staging"
	statusspec "github.com/tlalocweb/hulation/pkg/apispec/v1/status"
)

// =========================================================================
// Status
// =========================================================================

// GrpcStatus calls StatusService.Status.
func (c *Client) GrpcStatus(ctx context.Context) (*statusspec.StatusResponse, error) {
	if c.grpc == nil {
		return nil, ErrNoGRPC
	}
	return c.grpc.status.Status(c.authCtx(ctx), &statusspec.StatusRequest{})
}

// GrpcAuthOk calls StatusService.AuthOk.
func (c *Client) GrpcAuthOk(ctx context.Context) (*statusspec.AuthOkResponse, error) {
	if c.grpc == nil {
		return nil, ErrNoGRPC
	}
	return c.grpc.status.AuthOk(c.authCtx(ctx), &statusspec.AuthOkRequest{})
}

// =========================================================================
// Landers
// =========================================================================

// GrpcLanderCreate creates a lander. The Lander proto carries all
// editable fields; the caller builds it and passes it in.
func (c *Client) GrpcLanderCreate(ctx context.Context, serverID string, lander *landersspec.Lander) (*landersspec.Lander, error) {
	if c.grpc == nil {
		return nil, ErrNoGRPC
	}
	resp, err := c.grpc.landers.CreateLander(c.authCtx(ctx), &landersspec.CreateLanderRequest{
		ServerId: serverID,
		Lander:   lander,
	})
	if err != nil {
		return nil, err
	}
	return resp.GetLander(), nil
}

// GrpcLanderModify applies a partial update.
func (c *Client) GrpcLanderModify(ctx context.Context, serverID, landerID string, patch *landersspec.Lander) (*landersspec.Lander, error) {
	if c.grpc == nil {
		return nil, ErrNoGRPC
	}
	resp, err := c.grpc.landers.ModifyLander(c.authCtx(ctx), &landersspec.ModifyLanderRequest{
		ServerId: serverID,
		LanderId: landerID,
		Lander:   patch,
	})
	if err != nil {
		return nil, err
	}
	return resp.GetLander(), nil
}

// GrpcLanderDelete removes a lander.
func (c *Client) GrpcLanderDelete(ctx context.Context, serverID, landerID string) error {
	if c.grpc == nil {
		return ErrNoGRPC
	}
	_, err := c.grpc.landers.DeleteLander(c.authCtx(ctx), &landersspec.DeleteLanderRequest{
		ServerId: serverID,
		LanderId: landerID,
	})
	return err
}

// GrpcLanderList returns every lander.
func (c *Client) GrpcLanderList(ctx context.Context, serverID string) ([]*landersspec.Lander, error) {
	if c.grpc == nil {
		return nil, ErrNoGRPC
	}
	resp, err := c.grpc.landers.ListLanders(c.authCtx(ctx), &landersspec.ListLandersRequest{
		ServerId: serverID,
	})
	if err != nil {
		return nil, err
	}
	return resp.GetLanders(), nil
}

// GrpcLanderGet fetches one lander. Falls back to name lookup
// server-side.
func (c *Client) GrpcLanderGet(ctx context.Context, serverID, landerID string) (*landersspec.Lander, error) {
	if c.grpc == nil {
		return nil, ErrNoGRPC
	}
	resp, err := c.grpc.landers.GetLander(c.authCtx(ctx), &landersspec.GetLanderRequest{
		ServerId: serverID,
		LanderId: landerID,
	})
	if err != nil {
		return nil, err
	}
	return resp.GetLander(), nil
}

// =========================================================================
// Site (production build triggers)
// =========================================================================

// GrpcTriggerBuild kicks off a production site build.
func (c *Client) GrpcTriggerBuild(ctx context.Context, serverID, branch, commit string) (*sitespec.BuildInfo, error) {
	if c.grpc == nil {
		return nil, ErrNoGRPC
	}
	resp, err := c.grpc.site.TriggerBuild(c.authCtx(ctx), &sitespec.TriggerBuildRequest{
		ServerId: serverID,
		Branch:   branch,
		Commit:   commit,
	})
	if err != nil {
		return nil, err
	}
	return resp.GetBuild(), nil
}

// GrpcGetBuildStatus polls a build by id.
func (c *Client) GrpcGetBuildStatus(ctx context.Context, serverID, buildID string) (*sitespec.BuildInfo, error) {
	if c.grpc == nil {
		return nil, ErrNoGRPC
	}
	resp, err := c.grpc.site.GetBuildStatus(c.authCtx(ctx), &sitespec.GetBuildStatusRequest{
		ServerId: serverID,
		BuildId:  buildID,
	})
	if err != nil {
		return nil, err
	}
	return resp.GetBuild(), nil
}

// GrpcListBuilds returns recent builds.
func (c *Client) GrpcListBuilds(ctx context.Context, serverID string, limit int32) ([]*sitespec.BuildInfo, error) {
	if c.grpc == nil {
		return nil, ErrNoGRPC
	}
	resp, err := c.grpc.site.ListBuilds(c.authCtx(ctx), &sitespec.ListBuildsRequest{
		ServerId: serverID,
		Limit:    limit,
	})
	if err != nil {
		return nil, err
	}
	return resp.GetBuilds(), nil
}

// =========================================================================
// Staging
// =========================================================================

// GrpcStagingBuild rebuilds the long-lived staging container's output.
func (c *Client) GrpcStagingBuild(ctx context.Context, serverID string, force bool) (*sitespec.BuildInfo, error) {
	if c.grpc == nil {
		return nil, ErrNoGRPC
	}
	resp, err := c.grpc.staging.StagingBuild(c.authCtx(ctx), &stagingspec.StagingBuildRequest{
		ServerId: serverID,
		Force:    force,
	})
	if err != nil {
		return nil, err
	}
	return resp.GetBuild(), nil
}

// =========================================================================
// BadActor
// =========================================================================

// GrpcListBadActors returns the current blocked-IP set.
func (c *Client) GrpcListBadActors(ctx context.Context, serverID string, limit int32) ([]*badactorspec.BadActor, error) {
	if c.grpc == nil {
		return nil, ErrNoGRPC
	}
	resp, err := c.grpc.badactor.ListBadActors(c.authCtx(ctx), &badactorspec.ListBadActorsRequest{
		ServerId: serverID,
		Limit:    limit,
	})
	if err != nil {
		return nil, err
	}
	return resp.GetActors(), nil
}

// GrpcManualBlock blocks an IP manually.
func (c *Client) GrpcManualBlock(ctx context.Context, serverID, ip, reason string) (*badactorspec.BadActor, error) {
	if c.grpc == nil {
		return nil, ErrNoGRPC
	}
	resp, err := c.grpc.badactor.ManualBlock(c.authCtx(ctx), &badactorspec.ManualBlockRequest{
		ServerId: serverID,
		Ip:       ip,
		Reason:   reason,
	})
	if err != nil {
		return nil, err
	}
	return resp.GetActor(), nil
}

// GrpcEvictBadActor lifts a block.
func (c *Client) GrpcEvictBadActor(ctx context.Context, serverID, ip string) error {
	if c.grpc == nil {
		return ErrNoGRPC
	}
	_, err := c.grpc.badactor.EvictBadActor(c.authCtx(ctx), &badactorspec.EvictBadActorRequest{
		ServerId: serverID,
		Ip:       ip,
	})
	return err
}

// GrpcListAllowlist returns the allowlist.
func (c *Client) GrpcListAllowlist(ctx context.Context, serverID string) ([]*badactorspec.AllowlistEntry, error) {
	if c.grpc == nil {
		return nil, ErrNoGRPC
	}
	resp, err := c.grpc.badactor.ListAllowlist(c.authCtx(ctx), &badactorspec.ListAllowlistRequest{
		ServerId: serverID,
	})
	if err != nil {
		return nil, err
	}
	return resp.GetEntries(), nil
}

// GrpcAddToAllowlist whitelists an IP.
func (c *Client) GrpcAddToAllowlist(ctx context.Context, serverID, ip, reason string) (*badactorspec.AllowlistEntry, error) {
	if c.grpc == nil {
		return nil, ErrNoGRPC
	}
	resp, err := c.grpc.badactor.AddToAllowlist(c.authCtx(ctx), &badactorspec.AddToAllowlistRequest{
		ServerId: serverID,
		Ip:       ip,
		Reason:   reason,
	})
	if err != nil {
		return nil, err
	}
	return resp.GetEntry(), nil
}

// GrpcRemoveFromAllowlist removes an IP from the allowlist.
func (c *Client) GrpcRemoveFromAllowlist(ctx context.Context, serverID, ip string) error {
	if c.grpc == nil {
		return ErrNoGRPC
	}
	_, err := c.grpc.badactor.RemoveFromAllowlist(c.authCtx(ctx), &badactorspec.RemoveFromAllowlistRequest{
		ServerId: serverID,
		Ip:       ip,
	})
	return err
}

// GrpcBadActorStats returns aggregate stats.
func (c *Client) GrpcBadActorStats(ctx context.Context, serverID string) (*badactorspec.BadActorStatsResponse, error) {
	if c.grpc == nil {
		return nil, ErrNoGRPC
	}
	return c.grpc.badactor.BadActorStats(c.authCtx(ctx), &badactorspec.BadActorStatsRequest{
		ServerId: serverID,
	})
}

// GrpcListSignatures returns all loaded signatures (system-scoped).
func (c *Client) GrpcListSignatures(ctx context.Context) ([]*badactorspec.Signature, error) {
	if c.grpc == nil {
		return nil, ErrNoGRPC
	}
	resp, err := c.grpc.badactor.ListSignatures(c.authCtx(ctx), &badactorspec.ListSignaturesRequest{})
	if err != nil {
		return nil, err
	}
	return resp.GetSignatures(), nil
}

// =========================================================================
// Auth (public RPCs)
// =========================================================================

// GrpcLoginAdmin authenticates the built-in admin with username + sha256
// hash. Returns the JWT. Does not store the token on the Client — the
// caller is responsible for that.
func (c *Client) GrpcLoginAdmin(ctx context.Context, username, sha256Hash string) (*authspec.LoginAdminResponse, error) {
	if c.grpc == nil {
		return nil, ErrNoGRPC
	}
	return c.grpc.auth.LoginAdmin(ctx, &authspec.LoginAdminRequest{
		Username: username,
		Hash:     sha256Hash,
	})
}

// GrpcWhoAmI returns identity info from the caller's JWT.
func (c *Client) GrpcWhoAmI(ctx context.Context) (*authspec.WhoAmIResponse, error) {
	if c.grpc == nil {
		return nil, ErrNoGRPC
	}
	return c.grpc.auth.WhoAmI(c.authCtx(ctx), &authspec.WhoAmIRequest{})
}

// GrpcListAuthProviders lists configured auth providers.
func (c *Client) GrpcListAuthProviders(ctx context.Context) ([]*authspec.AuthProviderInfo, error) {
	if c.grpc == nil {
		return nil, ErrNoGRPC
	}
	resp, err := c.grpc.auth.ListAuthProviders(ctx, &authspec.ListAuthProvidersRequest{})
	if err != nil {
		return nil, err
	}
	return resp.GetProviders(), nil
}
