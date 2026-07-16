package mobile

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/golang-jwt/jwt/v5"
	"github.com/tlalocweb/hulation/config"
	mobilespec "github.com/tlalocweb/hulation/pkg/apispec/v1/mobile"
	"github.com/tlalocweb/hulation/pkg/server/authware"
	hulabolt "github.com/tlalocweb/hulation/pkg/store/bolt"
	"github.com/tlalocweb/hulation/pkg/store/storage"
	"github.com/tlalocweb/hulation/pkg/store/storage/local"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func makeSitesServer(t *testing.T) (*Server, context.Context) {
	t.Helper()
	store, err := local.Open(local.Options{Path: filepath.Join(t.TempDir(), "test.bolt")})
	if err != nil {
		t.Fatalf("open storage: %s", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	prev := storage.Global()
	storage.SetGlobal(store)
	t.Cleanup(func() { storage.SetGlobal(prev) })

	cfg := &config.Config{
		Servers: []*config.Server{
			{ID: "site-a", Host: "a.example.com"},
			{ID: "site-b", Host: "b.example.com"},
			{ID: "site-c", Host: "c.example.com"},
			{ID: "", Host: "ignored.example.com"},
		},
	}
	srv := New(nil, nil, nil, nil, cfg, nil)
	ctx := context.WithValue(context.Background(), authware.ClaimsKey, &authware.Claims{
		RegisteredClaims: jwt.RegisteredClaims{Subject: "user@example.com"},
		Username:         "user@example.com",
		Email:            "user@example.com",
		Roles:            []string{"user"},
	})
	return srv, ctx
}

func TestListMobileSites_AdminGetsAllConfiguredSites(t *testing.T) {
	srv, _ := makeSitesServer(t)
	ctx := context.WithValue(context.Background(), authware.ClaimsKey, &authware.Claims{
		RegisteredClaims: jwt.RegisteredClaims{Subject: "admin@example.com"},
		Username:         "admin@example.com",
		Roles:            []string{"admin"},
	})

	got, err := srv.ListMobileSites(ctx, &mobilespec.ListMobileSitesRequest{})
	if err != nil {
		t.Fatalf("ListMobileSites: %s", err)
	}
	wantIDs := []string{"site-a", "site-b", "site-c"}
	if len(got.GetSites()) != len(wantIDs) {
		t.Fatalf("site count: got %d want %d (%+v)", len(got.GetSites()), len(wantIDs), got.GetSites())
	}
	for i, want := range wantIDs {
		if got.GetSites()[i].GetId() != want {
			t.Errorf("site[%d].id: got %q want %q", i, got.GetSites()[i].GetId(), want)
		}
		if got.GetSites()[i].GetName() != want {
			t.Errorf("site[%d].name: got %q want %q", i, got.GetSites()[i].GetName(), want)
		}
	}
	if got.GetSites()[0].GetHost() != "a.example.com" {
		t.Errorf("host: got %q", got.GetSites()[0].GetHost())
	}
	if got.GetCurrentServerId() != "site-a" {
		t.Errorf("current_server_id: got %q want site-a", got.GetCurrentServerId())
	}
}

func TestListMobileSites_UserGetsOnlyGrantedConfiguredSites(t *testing.T) {
	srv, ctx := makeSitesServer(t)
	if err := hulabolt.GrantServerAccess(ctx, storage.Global(), "user@example.com", "site-b", "viewer"); err != nil {
		t.Fatalf("grant site-b: %s", err)
	}
	if err := hulabolt.GrantServerAccess(ctx, storage.Global(), "user@example.com", "removed-site", "viewer"); err != nil {
		t.Fatalf("grant stale: %s", err)
	}

	got, err := srv.ListMobileSites(ctx, &mobilespec.ListMobileSitesRequest{})
	if err != nil {
		t.Fatalf("ListMobileSites: %s", err)
	}
	if len(got.GetSites()) != 1 {
		t.Fatalf("site count: got %d want 1 (%+v)", len(got.GetSites()), got.GetSites())
	}
	site := got.GetSites()[0]
	if site.GetId() != "site-b" || site.GetHost() != "b.example.com" || site.GetName() != "site-b" {
		t.Fatalf("site mismatch: %+v", site)
	}
	if got.GetCurrentServerId() != "site-b" {
		t.Errorf("current_server_id: got %q want site-b", got.GetCurrentServerId())
	}
}

func TestListMobileSites_UserWithNoGrantsGetsEmptyList(t *testing.T) {
	srv, ctx := makeSitesServer(t)

	got, err := srv.ListMobileSites(ctx, &mobilespec.ListMobileSitesRequest{})
	if err != nil {
		t.Fatalf("ListMobileSites: %s", err)
	}
	if len(got.GetSites()) != 0 {
		t.Fatalf("sites: got %+v want empty", got.GetSites())
	}
	if got.GetCurrentServerId() != "" {
		t.Errorf("current_server_id: got %q want empty", got.GetCurrentServerId())
	}
}

func TestListMobileSites_RequiresAuthenticatedCaller(t *testing.T) {
	srv, _ := makeSitesServer(t)

	_, err := srv.ListMobileSites(context.Background(), &mobilespec.ListMobileSitesRequest{})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("want Unauthenticated, got %v", err)
	}
}
