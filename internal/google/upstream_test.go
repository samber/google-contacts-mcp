package google

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"github.com/samber/google-contacts-mcp/internal/contacts"
	"github.com/samber/google-contacts-mcp/internal/store"
)

func TestAuthCodeURL(t *testing.T) {
	env := newTestEnv(t)
	verifier := oauth2.GenerateVerifier()
	raw := env.up.AuthCodeURL("state-1", verifier)
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	sum := sha256.Sum256([]byte(verifier))
	want := map[string]string{
		"client_id":             "client-id",
		"redirect_uri":          "https://mcp.example.com/oauth/google/callback",
		"response_type":         "code",
		"scope":                 "openid email https://www.googleapis.com/auth/contacts",
		"state":                 "state-1",
		"access_type":           "offline",
		"prompt":                "consent",
		"code_challenge_method": "S256",
		"code_challenge":        base64.RawURLEncoding.EncodeToString(sum[:]),
	}
	q := u.Query()
	for k, v := range want {
		if q.Get(k) != v {
			t.Errorf("%s = %q, want %q", k, q.Get(k), v)
		}
	}
	if !strings.HasPrefix(raw, env.fake.srv.URL+"/auth?") {
		t.Errorf("url = %q", raw)
	}
}

func TestExchange_SendsVerifier(t *testing.T) {
	env := newTestEnv(t)
	env.fake.newRefresh = "refresh-new"
	tok, err := env.up.Exchange(context.Background(), "code-1", "verifier-1")
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if tok.AccessToken != "access-new" || tok.RefreshToken != "refresh-new" {
		t.Errorf("token = %+v", tok)
	}
	_, body, _, _ := env.fake.last(routeToken)
	form, err := url.ParseQuery(string(body))
	if err != nil {
		t.Fatalf("ParseQuery: %v", err)
	}
	if form.Get("code") != "code-1" || form.Get("code_verifier") != "verifier-1" || form.Get("grant_type") != "authorization_code" {
		t.Errorf("token form = %v", form)
	}

	env.fake.tokenError = "invalid_grant"
	_, err = env.up.Exchange(context.Background(), "code-2", "verifier-2")
	if err == nil || strings.Contains(err.Error(), "secret detail") {
		t.Errorf("err = %v, want an error without the response body", err)
	}
}

func TestTokenSource_RefreshPersists(t *testing.T) {
	env := newTestEnv(t)
	env.expireToken(t)
	ctx := context.Background()
	if _, err := env.people(t).Get(ctx, "people/c1"); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if n := env.fake.tokenHits.Load(); n != 1 {
		t.Errorf("token hits = %d, want 1", n)
	}
	if _, _, auth, _ := env.fake.last(routeGet); auth != "Bearer access-new" {
		t.Errorf("Authorization = %q", auth)
	}
	_, body, _, _ := env.fake.last(routeToken)
	if form, _ := url.ParseQuery(string(body)); form.Get("refresh_token") != "refresh-1" || form.Get("grant_type") != "refresh_token" {
		t.Errorf("token form = %s", body)
	}
	got, err := env.st.GoogleToken(ctx, env.userID)
	if err != nil {
		t.Fatalf("GoogleToken: %v", err)
	}
	// Google omitted refresh_token: the stored one is kept.
	if got.AccessToken != "access-new" || got.RefreshToken != "refresh-1" || !got.Expiry.After(time.Now()) {
		t.Errorf("stored token = access %q refresh %q expiry %v", got.AccessToken, got.RefreshToken, got.Expiry)
	}

	// A rotated refresh token replaces the stored one.
	env.expireToken(t)
	env.fake.newRefresh = "refresh-2"
	if _, err := env.people(t).Get(ctx, "people/c1"); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got, err := env.st.GoogleToken(ctx, env.userID); err != nil || got.RefreshToken != "refresh-2" {
		t.Errorf("stored refresh = %q, err %v; want refresh-2", got.RefreshToken, err)
	}
}

func TestTokenSource_ConcurrentRefreshOnce(t *testing.T) {
	env := newTestEnv(t)
	env.expireToken(t)
	// The delay keeps the first refresh in flight while the others arrive.
	env.fake.tokenDelay = 100 * time.Millisecond
	const workers = 20
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for range workers {
		wg.Go(func() {
			p, err := env.up.People(context.Background(), env.userID)
			if err == nil {
				_, err = p.Get(context.Background(), "people/c1")
			}
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("Get: %v", err)
		}
	}
	if n := env.fake.tokenHits.Load(); n != 1 {
		t.Errorf("token hits = %d, want 1", n)
	}
}

func TestTokenSource_InvalidGrantDeletesUser(t *testing.T) {
	env := newTestEnv(t)
	env.expireToken(t)
	env.fake.tokenError = "invalid_grant"
	ctx := context.Background()
	_, err := env.people(t).Get(ctx, "people/c1")
	if !errors.Is(err, contacts.ErrUpstreamAuth) {
		t.Fatalf("err = %v, want ErrUpstreamAuth", err)
	}
	if strings.Contains(err.Error(), "secret detail") {
		t.Errorf("error leaks the token response body: %v", err)
	}
	if n := env.fake.count(routeGet); n != 0 {
		t.Errorf("people calls = %d, want 0", n)
	}
	if _, err := env.st.UserBySubOrEmail(ctx, "sub-1"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("UserBySubOrEmail err = %v, want ErrNotFound", err)
	}
	if _, err := env.up.People(ctx, env.userID); !errors.Is(err, contacts.ErrUpstreamAuth) {
		t.Errorf("People after delete err = %v, want ErrUpstreamAuth", err)
	}
}

func TestTokenSource_OtherRefreshErrorKeepsUser(t *testing.T) {
	env := newTestEnv(t)
	env.expireToken(t)
	env.fake.tokenError = "server_error"
	ctx := context.Background()
	_, err := env.people(t).Get(ctx, "people/c1")
	if err == nil || errors.Is(err, contacts.ErrUpstreamAuth) {
		t.Fatalf("err = %v, want a plain error", err)
	}
	if strings.Contains(err.Error(), "secret detail") {
		t.Errorf("error leaks the token response body: %v", err)
	}
	if _, err := env.st.UserBySubOrEmail(ctx, "sub-1"); err != nil {
		t.Errorf("UserBySubOrEmail: %v, want the user kept", err)
	}
}

func TestRevoke(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	if err := env.up.Revoke(ctx, "refresh-1"); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	env.fake.mu.Lock()
	forms := append([]string(nil), env.fake.revokedForm...)
	env.fake.mu.Unlock()
	if len(forms) != 1 {
		t.Fatalf("revoke calls = %d, want 1", len(forms))
	}
	if form, _ := url.ParseQuery(forms[0]); form.Get("token") != "refresh-1" {
		t.Errorf("revoke form = %q", forms[0])
	}

	env.fake.revokeCode = 400
	err := env.up.Revoke(ctx, "refresh-1")
	if err == nil || strings.Contains(err.Error(), "refresh-1") {
		t.Errorf("err = %v, want an error without the token", err)
	}
}
