// Package google talks to Google: the OAuth flow that grants the server access
// to a user's contacts, the per-user token source, and a contacts.People
// implementation backed by the People API v1.
package google

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"
	googleoauth "golang.org/x/oauth2/google"
	"golang.org/x/sync/singleflight"
	"google.golang.org/api/option"
	"google.golang.org/api/people/v1"

	"github.com/samber/google-contacts-mcp/internal/contacts"
	"github.com/samber/google-contacts-mcp/internal/store"
)

const (
	// DefaultPeopleBaseURL is the production People API root.
	DefaultPeopleBaseURL = "https://people.googleapis.com/"
	// DefaultRevokeURL is Google's OAuth token revocation endpoint.
	DefaultRevokeURL = "https://oauth2.googleapis.com/revoke"

	// scopeContacts grants read and write access to the user's contacts.
	scopeContacts = "https://www.googleapis.com/auth/contacts"
	// oauthErrInvalidGrant is the OAuth error Google returns for a revoked or
	// expired refresh token (RFC 6749 §5.2).
	oauthErrInvalidGrant = "invalid_grant"
	// refreshTimeout bounds one token refresh; it runs detached from the
	// caller's context because concurrent requests share its result.
	refreshTimeout = 30 * time.Second
	// defaultHTTPTimeout bounds any Google call when the caller gives no client.
	defaultHTTPTimeout = 30 * time.Second
	// revokeTokenParam is the form field carrying the token to revoke.
	revokeTokenParam = "token"
	// formContentType is the body type the revoke endpoint expects.
	formContentType = "application/x-www-form-urlencoded"
	// tokenTypeBearer is the only token type Google issues.
	tokenTypeBearer = "Bearer"
)

// Scopes are the Google scopes requested at consent: identity to map the user
// to a store row, plus full contacts access.
var Scopes = []string{"openid", "email", scopeContacts}

// UpstreamConfig configures the Google side. Zero Endpoint, PeopleBaseURL and
// RevokeURL select the production Google endpoints; tests override them.
type UpstreamConfig struct {
	ClientID, ClientSecret, RedirectURL string
	Endpoint                            oauth2.Endpoint
	PeopleBaseURL, RevokeURL            string
}

// Upstream owns the Google OAuth client and every user's Google token source.
type Upstream struct {
	oauth     *oauth2.Config
	peopleURL string
	revokeURL string
	st        *store.Store
	http      *http.Client

	// refreshes allows one in-flight refresh per user id.
	refreshes singleflight.Group
	// warmed holds the user ids whose search cache was warmed in this process.
	warmed sync.Map
}

// NewUpstream returns an Upstream. A nil httpClient gets a client with a
// default timeout.
func NewUpstream(cfg UpstreamConfig, st *store.Store, httpClient *http.Client) *Upstream {
	if cfg.Endpoint == (oauth2.Endpoint{}) {
		cfg.Endpoint = googleoauth.Endpoint
	}
	if cfg.PeopleBaseURL == "" {
		cfg.PeopleBaseURL = DefaultPeopleBaseURL
	}
	if cfg.RevokeURL == "" {
		cfg.RevokeURL = DefaultRevokeURL
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultHTTPTimeout}
	}
	return &Upstream{
		oauth: &oauth2.Config{
			ClientID:     cfg.ClientID,
			ClientSecret: cfg.ClientSecret,
			RedirectURL:  cfg.RedirectURL,
			Endpoint:     cfg.Endpoint,
			Scopes:       Scopes,
		},
		peopleURL: cfg.PeopleBaseURL,
		revokeURL: cfg.RevokeURL,
		st:        st,
		http:      httpClient,
	}
}

// AuthCodeURL returns the Google consent URL. prompt=consent with offline
// access makes Google return a refresh token on every consent.
func (u *Upstream) AuthCodeURL(state, codeVerifier string) string {
	return u.oauth.AuthCodeURL(state,
		oauth2.AccessTypeOffline, oauth2.ApprovalForce, oauth2.S256ChallengeOption(codeVerifier))
}

// Exchange trades an authorization code for Google tokens.
func (u *Upstream) Exchange(ctx context.Context, code, codeVerifier string) (*oauth2.Token, error) {
	tok, err := u.oauth.Exchange(u.oauthContext(ctx), code, oauth2.VerifierOption(codeVerifier))
	if err != nil {
		return nil, fmt.Errorf("google code exchange: %w", sanitizeTokenError(err))
	}
	return tok, nil
}

// Revoke revokes a Google token, which also revokes the whole grant.
func (u *Upstream) Revoke(ctx context.Context, token string) error {
	form := url.Values{revokeTokenParam: {token}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.revokeURL, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("google revoke: %w", err)
	}
	req.Header.Set("Content-Type", formContentType)
	resp, err := u.http.Do(req)
	if err != nil {
		return fmt.Errorf("google revoke: %w", err)
	}
	// The body is drained so the connection can be reused, never read for content.
	_, drainErr := io.Copy(io.Discard, resp.Body)
	closeErr := resp.Body.Close()
	var statusErr error
	if resp.StatusCode != http.StatusOK {
		statusErr = fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if err := errors.Join(statusErr, drainErr, closeErr); err != nil {
		return fmt.Errorf("google revoke: %w", err)
	}
	return nil
}

// People returns a People API client acting as userID. It fails with
// contacts.ErrUpstreamAuth when the user has no stored Google grant.
func (u *Upstream) People(ctx context.Context, userID string) (contacts.People, error) {
	tok, err := u.loadToken(ctx, userID)
	if err != nil {
		return nil, err
	}
	src := &userTokenSource{up: u, userID: userID, ctx: context.WithoutCancel(ctx)}
	base := u.http.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	client := &http.Client{
		Transport: &oauth2.Transport{Source: oauth2.ReuseTokenSource(tok, src), Base: base},
		Timeout:   u.http.Timeout,
	}
	svc, err := people.NewService(ctx, option.WithEndpoint(u.peopleURL), option.WithHTTPClient(client))
	if err != nil {
		return nil, fmt.Errorf("google people client: %w", err)
	}
	return &peopleClient{svc: svc, userID: userID, warmed: &u.warmed}, nil
}

// oauthContext makes the oauth2 package use u.http for token requests.
func (u *Upstream) oauthContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, oauth2.HTTPClient, u.http)
}

// loadToken reads the stored Google token of userID.
func (u *Upstream) loadToken(ctx context.Context, userID string) (*oauth2.Token, error) {
	t, err := u.st.GoogleToken(ctx, userID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, contacts.ErrUpstreamAuth
	}
	if err != nil {
		return nil, err
	}
	return &oauth2.Token{AccessToken: t.AccessToken, RefreshToken: t.RefreshToken, Expiry: t.Expiry, TokenType: tokenTypeBearer}, nil
}

// refresh refreshes userID's access token and stores the result. It re-reads
// the store first: a refresh that finished just before this flight started
// already left a valid token there.
func (u *Upstream) refresh(ctx context.Context, userID string) (*oauth2.Token, error) {
	stored, err := u.loadToken(ctx, userID)
	if err != nil {
		return nil, err
	}
	if stored.Valid() {
		return stored, nil
	}
	ctx, cancel := context.WithTimeout(ctx, refreshTimeout)
	defer cancel()
	fresh, err := u.oauth.TokenSource(u.oauthContext(ctx), &oauth2.Token{RefreshToken: stored.RefreshToken}).Token()
	if err != nil {
		var re *oauth2.RetrieveError
		if errors.As(err, &re) && re.ErrorCode == oauthErrInvalidGrant {
			// The user revoked access or the grant expired: forget them so the
			// MCP client restarts the OAuth flow (spec §7).
			if delErr := u.st.DeleteUser(ctx, userID); delErr != nil && !errors.Is(delErr, store.ErrNotFound) {
				return nil, errors.Join(contacts.ErrUpstreamAuth, delErr)
			}
			return nil, contacts.ErrUpstreamAuth
		}
		return nil, fmt.Errorf("google token refresh: %w", sanitizeTokenError(err))
	}
	if fresh.RefreshToken == "" {
		fresh.RefreshToken = stored.RefreshToken
	}
	if err := u.st.SaveGoogleToken(ctx, userID, store.GoogleToken{
		RefreshToken: fresh.RefreshToken, AccessToken: fresh.AccessToken, Expiry: fresh.Expiry,
	}); err != nil {
		return nil, err
	}
	return fresh, nil
}

// sanitizeTokenError drops the token endpoint's response body, which
// oauth2.RetrieveError prints in full.
func sanitizeTokenError(err error) error {
	var re *oauth2.RetrieveError
	if !errors.As(err, &re) {
		return err
	}
	status := 0
	if re.Response != nil {
		status = re.Response.StatusCode
	}
	return fmt.Errorf("token endpoint returned HTTP %d (%s)", status, re.ErrorCode)
}

// userTokenSource yields a valid access token for one user, refreshing it
// through the store so every process-local client shares one refresh.
type userTokenSource struct {
	up     *Upstream
	userID string
	// ctx is detached from cancellation: the token source outlives the
	// request that created it and its refresh is shared by other callers.
	ctx context.Context
}

// Token implements oauth2.TokenSource.
func (s *userTokenSource) Token() (*oauth2.Token, error) {
	tok, err := s.up.loadToken(s.ctx, s.userID)
	if err != nil {
		return nil, err
	}
	if tok.Valid() {
		return tok, nil
	}
	v, err, _ := s.up.refreshes.Do(s.userID, func() (any, error) {
		return s.up.refresh(s.ctx, s.userID)
	})
	if err != nil {
		return nil, err
	}
	shared, ok := v.(*oauth2.Token)
	if !ok {
		return nil, fmt.Errorf("google token refresh: unexpected result %T", v)
	}
	// Every caller of the flight gets the same pointer, and
	// oauth2.ReuseTokenSource writes into the token it receives.
	cp := *shared
	return &cp, nil
}
