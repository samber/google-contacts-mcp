package oauth

import (
	"bytes"
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/ory/fosite"
	"golang.org/x/oauth2"

	"github.com/samber/google-contacts-mcp/internal/google"
	"github.com/samber/google-contacts-mcp/internal/store"
)

const (
	// csrfCookieName binds the consent form and Google's callback to the
	// browser that opened /oauth/authorize.
	csrfCookieName = "gcmcp_csrf"
	// cookiePath limits the CSRF cookie to the OAuth endpoints.
	cookiePath = "/oauth"
	// maxFormBody bounds consent, token and revocation bodies; real ones are under 2 KiB.
	maxFormBody int64 = 64 << 10

	// Form fields of the consent page.
	fieldPending = "pending"
	fieldCSRF    = "csrf"
	fieldAction  = "action"
	// actionApprove is the consent button value that grants access; any other value denies.
	actionApprove = "approve"

	// Authorization request parameters read directly.
	paramClientID     = "client_id"
	paramRedirectURI  = "redirect_uri"
	paramResource     = "resource"
	paramResponseMode = "response_mode"
	// paramCodeChallenge and paramCodeChallengeMethod carry the client's PKCE challenge.
	paramCodeChallenge       = "code_challenge"
	paramCodeChallengeMethod = "code_challenge_method"
	// minPKCEChallenge and maxPKCEChallenge are the RFC 7636 §4.2 length bounds.
	minPKCEChallenge = 43
	maxPKCEChallenge = 128
	// paramIss is the RFC 9207 issuer parameter added to every redirect to the client.
	paramIss = "iss"
	// responseModeQuery is the only response mode allowed besides the default;
	// form_post would render the redirect URI into an HTML form.
	responseModeQuery = "query"

	// Google callback parameters.
	paramState = "state"
	paramCode  = "code"
	paramError = "error"
	// googleErrAccessDenied is what Google returns when the user is not a test
	// user of the project, or declined consent.
	googleErrAccessDenied = "access_denied"
	// tokenExtraIDToken and tokenExtraScope are fields of Google's token response.
	tokenExtraIDToken = "id_token"
	tokenExtraScope   = "scope"

	// errInvalidTarget is the RFC 8707 error for a resource we do not serve.
	errInvalidTarget = "invalid_target"

	// Template names under templates/.
	tmplConsent = "consent.html"
	tmplError   = "error.html"

	// Security headers of every HTML page. The pages carry no script and
	// must never be framed: the consent page is a clickjacking target.
	headerCSP       = "default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; frame-ancestors 'none'"
	headerNoReferer = "no-referrer"
	mediaTypeHTML   = "text/html; charset=utf-8"
)

// authorizeParamNames are the authorization request parameters kept in a
// pending authorization; anything else a client sends is dropped.
var authorizeParamNames = []string{
	"response_type", paramClientID, paramRedirectURI, "scope", paramState,
	paramCodeChallenge, paramCodeChallengeMethod, paramResource, paramResponseMode,
}

// pkceChallengeRe is the RFC 7636 unreserved character set.
var pkceChallengeRe = regexp.MustCompile(`^[A-Za-z0-9._~-]+$`)

//go:embed templates/*.html
var templatesFS embed.FS

func parseTemplates() (*template.Template, error) {
	t, err := template.ParseFS(templatesFS, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("oauth: parse templates: %w", err)
	}
	return t, nil
}

// pending is an authorization in flight between /oauth/authorize and
// Google's callback, stored in SQLite.
type pending struct {
	// Form holds the client's authorization request parameters.
	Form url.Values `json:"form"`
	CSRF string     `json:"csrf"`
	// GoogleVerifier is our PKCE verifier toward Google, set at consent.
	GoogleVerifier string `json:"google_verifier,omitempty"`
	// Deadline is fixed at /oauth/authorize; re-saving does not extend it.
	Deadline time.Time `json:"deadline"`
}

// page is an HTML error page shown in the user's browser instead of a
// redirect to the client.
type page struct {
	status  int
	Title   string
	Message string
	// RetryURL is our own authorize URL; empty for no retry link.
	RetryURL string
}

var (
	pageExpired = &page{status: http.StatusBadRequest, Title: "Sign-in expired",
		Message: "This sign-in request expired or was already used. Start again from your assistant."}
	pageBadSession = &page{status: http.StatusBadRequest, Title: "Sign-in not verified",
		Message: "This sign-in was not started in this browser, or its session cookie is missing. Start again from your assistant."}
	pageInternal = &page{status: http.StatusInternalServerError, Title: "Something went wrong",
		Message: "The server could not complete the sign-in. Try again in a moment."}
	pageGoogleFailed = &page{status: http.StatusBadGateway, Title: "Google sign-in failed",
		Message: "Google did not complete the sign-in. Try again from your assistant."}
	pageEmailUnverified = &page{status: http.StatusForbidden, Title: "E-mail not verified",
		Message: "Your Google account e-mail address is not verified."}
)

// authorize serves GET /oauth/authorize: it validates the request and shows
// the consent page.
func (s *Server) authorize(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	form := pickAuthorizeParams(r.URL.Query())
	ar, pg, err := s.parseAuthorize(ctx, form)
	if pg != nil {
		s.renderError(w, r, pg)
		return
	}
	if err != nil {
		s.writeAuthorizeError(ctx, w, r, ar, err)
		return
	}

	id, err := randomToken()
	if err != nil {
		s.fail(w, r, "generate pending id", err)
		return
	}
	csrf, err := randomToken()
	if err != nil {
		s.fail(w, r, "generate csrf token", err)
		return
	}
	if err := s.savePending(ctx, id, &pending{Form: form, CSRF: csrf, Deadline: s.now().Add(pendingTTL)}); err != nil {
		s.fail(w, r, "save pending authorization", err)
		return
	}
	s.setCSRFCookie(w, csrf, int(pendingTTL/time.Second))

	host := ar.GetRedirectURI().Hostname()
	name := host
	if pc, ok := ar.GetClient().(*providerClient); ok && pc.name() != "" {
		name = pc.name()
	}
	s.render(w, r, http.StatusOK, tmplConsent, map[string]string{
		"ClientName":   name,
		"RedirectHost": host,
		"Pending":      id,
		"CSRF":         csrf,
		"Action":       pathConsent,
	})
}

// consent serves POST /oauth/consent: deny returns access_denied to the
// client, approve sends the browser to Google.
func (s *Server) consent(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBody)
	if err := r.ParseForm(); err != nil {
		s.renderError(w, r, &page{status: http.StatusBadRequest, Title: "Bad request", Message: "The consent form could not be read."})
		return
	}
	p, pg := s.takePending(ctx, r.PostForm.Get(fieldPending))
	if pg != nil {
		s.renderError(w, r, pg)
		return
	}
	if !s.cookieMatches(r, p.CSRF) || !equalSecret(r.PostForm.Get(fieldCSRF), p.CSRF) {
		s.renderError(w, r, pageBadSession)
		return
	}
	ar, pg, err := s.parseAuthorize(ctx, p.Form)
	if pg != nil {
		s.renderError(w, r, pg)
		return
	}
	if err != nil {
		s.writeAuthorizeError(ctx, w, r, ar, err)
		return
	}
	if r.PostForm.Get(fieldAction) != actionApprove {
		s.clearCSRFCookie(w)
		s.writeAuthorizeError(ctx, w, r, ar, fosite.ErrAccessDenied.WithHint("The user denied the request."))
		return
	}

	state, err := randomToken()
	if err != nil {
		s.fail(w, r, "generate google state", err)
		return
	}
	p.GoogleVerifier = oauth2.GenerateVerifier()
	if err := s.savePending(ctx, state, p); err != nil {
		s.fail(w, r, "save pending authorization", err)
		return
	}
	http.Redirect(w, r, s.up.AuthCodeURL(state, p.GoogleVerifier), http.StatusSeeOther)
}

// idClaims are the Google ID token claims read besides sub.
type idClaims struct {
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
	// HD is the Google Workspace domain; empty for consumer accounts.
	HD string `json:"hd"`
}

// callback serves GET /oauth/google/callback: it finishes the Google login,
// stores the user and their Google grant, and redirects to the client with
// our authorization code.
func (s *Server) callback(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()
	p, pg := s.takePending(ctx, q.Get(paramState))
	if pg != nil {
		s.renderError(w, r, pg)
		return
	}
	if !s.cookieMatches(r, p.CSRF) {
		s.renderError(w, r, pageBadSession)
		return
	}
	s.clearCSRFCookie(w)

	if gerr := q.Get(paramError); gerr != "" {
		s.log.InfoContext(ctx, "oauth: google returned an error", slog.String("error", gerr))
		if gerr == googleErrAccessDenied {
			s.renderError(w, r, &page{status: http.StatusForbidden, Title: "Access not granted",
				Message: "This server is in early access: e-mail " + s.operatorEmail + " to join. " +
					"If you declined access on Google's page, start again from your assistant."})
			return
		}
		s.renderError(w, r, pageGoogleFailed)
		return
	}

	ar, pg, err := s.parseAuthorize(ctx, p.Form)
	if pg != nil {
		s.renderError(w, r, pg)
		return
	}
	if err != nil {
		s.writeAuthorizeError(ctx, w, r, ar, err)
		return
	}

	tok, err := s.up.Exchange(ctx, q.Get(paramCode), p.GoogleVerifier)
	if err != nil {
		s.log.WarnContext(ctx, "oauth: google code exchange failed", slog.Any("error", err))
		s.renderError(w, r, pageGoogleFailed)
		return
	}
	sub, claims, pg := s.verifyIDToken(ctx, tok)
	if pg != nil {
		s.renderError(w, r, pg)
		return
	}
	if !grantedContacts(tok) {
		s.renderError(w, r, &page{status: http.StatusForbidden, Title: "Contacts access missing",
			Message:  "You did not tick the Contacts permission on Google's page, and access to Contacts is required for this server to work.",
			RetryURL: s.issuer + pathAuthorize + "?" + p.Form.Encode()})
		return
	}
	if domain, ok := s.domainAllowed(claims); !ok {
		s.log.InfoContext(ctx, "oauth: account domain refused", slog.String("domain", domain))
		s.renderError(w, r, &page{status: http.StatusForbidden, Title: "Account not allowed",
			Message: "Google accounts from the domain " + domain + " are not allowed on this server."})
		return
	}
	if tok.RefreshToken == "" {
		s.log.WarnContext(ctx, "oauth: google returned no refresh token")
		s.renderError(w, r, pageGoogleFailed)
		return
	}

	user, err := s.st.UpsertUser(ctx, sub, claims.Email)
	if err != nil {
		s.fail(w, r, "upsert user", err)
		return
	}
	// A new consent always replaces the stored grant (spec §7).
	if err := s.st.SaveGoogleToken(ctx, user.ID, store.GoogleToken{
		RefreshToken: tok.RefreshToken, AccessToken: tok.AccessToken, Expiry: tok.Expiry,
	}); err != nil {
		s.fail(w, r, "save google token", err)
		return
	}

	// The subject becomes oauth_tokens.user_id, which DeleteUser relies on.
	resp, err := s.provider.NewAuthorizeResponse(ctx, ar, &fosite.DefaultSession{Subject: user.ID})
	if err != nil {
		s.writeAuthorizeError(ctx, w, r, ar, err)
		return
	}
	s.log.InfoContext(ctx, "oauth: user connected",
		slog.String("user_id", user.ID), slog.String("client_id", ar.GetClient().GetID()))
	s.provider.WriteAuthorizeResponse(ctx, &issWriter{ResponseWriter: w, iss: s.issuer}, ar, resp)
}

// parseAuthorize validates an authorization request. It returns a page when
// the error must be shown to the user rather than sent to the redirect URI,
// or a fosite error with the request to send it to the client.
func (s *Server) parseAuthorize(ctx context.Context, form url.Values) (fosite.AuthorizeRequester, *page, error) {
	if rm := form.Get(paramResponseMode); rm != "" && rm != responseModeQuery {
		return nil, &page{status: http.StatusBadRequest, Title: "Unsupported request",
			Message: "This server only returns authorization responses in the query string."}, nil
	}
	clientID := form.Get(paramClientID)
	client, err := s.clients.GetClient(ctx, clientID)
	if err != nil {
		s.log.InfoContext(ctx, "oauth: unknown client", slog.String("client_id", clientID), slog.Any("error", err))
		return nil, &page{status: http.StatusBadRequest, Title: "Unknown application",
			Message: "This application is not registered with this server, or its metadata could not be fetched."}, nil
	}

	// Redirect URIs are checked here, before fosite, for every client kind:
	// a CIMD document may list any scheme, and fosite would redirect errors
	// to whatever URI it accepted.
	redirect := form.Get(paramRedirectURI)
	registered := client.GetRedirectURIs()
	if redirect == "" && len(registered) == 1 {
		redirect = registered[0]
	}
	if redirect == "" || !RedirectMatches(registered, redirect) || !safeRedirect(redirect) {
		s.log.InfoContext(ctx, "oauth: redirect uri refused", slog.String("client_id", clientID))
		return nil, &page{status: http.StatusBadRequest, Title: "Invalid redirect",
			Message: "The application asked to return to an address it did not register, or one that is not allowed."}, nil
	}

	ctx = context.WithValue(ctx, approvedRedirectKey{}, approvedRedirect{clientID: clientID, uri: redirect})
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.issuer+pathAuthorize+"?"+form.Encode(), nil)
	if err != nil {
		return nil, nil, fosite.ErrServerError.WithWrap(err)
	}
	ar, err := s.provider.NewAuthorizeRequest(ctx, req)
	if err != nil {
		return ar, nil, err
	}
	// fosite checks PKCE only when issuing the code, after the Google login;
	// checking it here fails fast, before the user signs in for nothing.
	if err := checkPKCE(form); err != nil {
		return ar, nil, err
	}
	if res, ok := form[paramResource]; ok && (len(res) != 1 || res[0] != s.resource) {
		return ar, nil, &fosite.RFC6749Error{
			ErrorField:       errInvalidTarget,
			DescriptionField: "The requested resource is not served by this authorization server.",
			HintField:        "Use resource=" + s.resource + ".",
			CodeField:        http.StatusBadRequest,
		}
	}
	// Clients that omit resource still get the MCP audience, so legacy
	// clients work; the verifier always checks it.
	ar.GrantAudience(s.resource)
	ar.GrantScope(MCPScope)
	return ar, nil, nil
}

// checkPKCE requires an S256 code challenge (RFC 7636 §4.2: 43 to 128
// characters of the unreserved set).
func checkPKCE(form url.Values) error {
	challenge := form.Get(paramCodeChallenge)
	if form.Get(paramCodeChallengeMethod) != pkceMethodS256 {
		return fosite.ErrInvalidRequest.WithHint("code_challenge_method must be S256.")
	}
	if len(challenge) < minPKCEChallenge || len(challenge) > maxPKCEChallenge || !pkceChallengeRe.MatchString(challenge) {
		return fosite.ErrInvalidRequest.WithHint("A valid S256 code_challenge is required.")
	}
	return nil
}

// writeAuthorizeError sends err to the client's redirect URI, with iss, or
// shows it as a page when no redirect URI was validated.
func (s *Server) writeAuthorizeError(ctx context.Context, w http.ResponseWriter, r *http.Request, ar fosite.AuthorizeRequester, err error) {
	rfc := fosite.ErrorToRFC6749Error(err)
	s.log.InfoContext(ctx, "oauth: authorization request rejected",
		slog.String("error", rfc.ErrorField), slog.String("hint", rfc.HintField))
	if ar == nil || !ar.IsRedirectURIValid() {
		status := rfc.CodeField
		if status < http.StatusBadRequest {
			status = http.StatusBadRequest
		}
		s.renderError(w, r, &page{status: status, Title: "Authorization failed",
			Message: "The application sent an invalid authorization request (" + rfc.ErrorField + ")."})
		return
	}
	s.provider.WriteAuthorizeError(ctx, &issWriter{ResponseWriter: w, iss: s.issuer}, ar, err)
}

// verifyIDToken checks the ID token in Google's token response: signature,
// issuer, audience (our Google client id), expiry and a verified e-mail.
func (s *Server) verifyIDToken(ctx context.Context, tok *oauth2.Token) (string, idClaims, *page) {
	raw, ok := tok.Extra(tokenExtraIDToken).(string)
	if !ok || raw == "" {
		s.log.WarnContext(ctx, "oauth: google returned no id token")
		return "", idClaims{}, pageGoogleFailed
	}
	idt, err := s.idTokens.Verify(ctx, raw)
	if err != nil {
		s.log.WarnContext(ctx, "oauth: google id token rejected", slog.Any("error", err))
		return "", idClaims{}, pageGoogleFailed
	}
	var c idClaims
	if err := idt.Claims(&c); err != nil {
		s.log.WarnContext(ctx, "oauth: decode id token claims", slog.Any("error", err))
		return "", idClaims{}, pageGoogleFailed
	}
	if !c.EmailVerified || c.Email == "" || idt.Subject == "" {
		return "", idClaims{}, pageEmailUnverified
	}
	return idt.Subject, c, nil
}

// grantedContacts reports whether the user ticked the contacts permission:
// Google's granular consent can grant fewer scopes than requested.
func grantedContacts(tok *oauth2.Token) bool {
	scope, _ := tok.Extra(tokenExtraScope).(string)
	return slices.Contains(strings.Fields(scope), google.ScopeContacts)
}

// domainAllowed checks --allowed-domains against the hd claim and the e-mail
// domain. It returns the domain to name in a refusal.
func (s *Server) domainAllowed(c idClaims) (string, bool) {
	emailDomain := ""
	if at := strings.LastIndex(c.Email, "@"); at >= 0 {
		emailDomain = strings.ToLower(c.Email[at+1:])
	}
	hd := strings.ToLower(c.HD)
	if len(s.allowedDomains) == 0 {
		return emailDomain, true
	}
	if (hd != "" && slices.Contains(s.allowedDomains, hd)) || slices.Contains(s.allowedDomains, emailDomain) {
		return emailDomain, true
	}
	if hd != "" {
		return hd, false
	}
	return emailDomain, false
}

// safeRedirect accepts only https, or http on a loopback host, without
// userinfo or fragment: a client document may list javascript: or data: URIs.
func safeRedirect(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Fragment != "" || strings.Contains(raw, "#") || u.Hostname() == "" {
		return false
	}
	switch u.Scheme {
	case schemeHTTPS:
		return true
	case schemeHTTP:
		return isLoopbackHost(u.Hostname())
	default:
		return false
	}
}

func pickAuthorizeParams(q url.Values) url.Values {
	out := url.Values{}
	for _, k := range authorizeParamNames {
		if v, ok := q[k]; ok {
			out[k] = v
		}
	}
	return out
}

func (s *Server) savePending(ctx context.Context, id string, p *pending) error {
	ttl := p.Deadline.Sub(s.now())
	if ttl <= 0 {
		return errors.New("oauth: pending authorization already expired")
	}
	data, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("oauth: encode pending authorization: %w", err)
	}
	return s.st.SavePending(ctx, id, data, ttl)
}

// takePending consumes a pending authorization. A missing, used or expired
// one yields pageExpired.
func (s *Server) takePending(ctx context.Context, id string) (*pending, *page) {
	if id == "" {
		return nil, pageExpired
	}
	data, err := s.st.TakePending(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, pageExpired
	}
	if err != nil {
		s.log.ErrorContext(ctx, "oauth: take pending authorization", slog.Any("error", err))
		return nil, pageInternal
	}
	var p pending
	if err := json.Unmarshal(data, &p); err != nil {
		s.log.ErrorContext(ctx, "oauth: decode pending authorization", slog.Any("error", err))
		return nil, pageInternal
	}
	if !s.now().Before(p.Deadline) {
		return nil, pageExpired
	}
	return &p, nil
}

func (s *Server) setCSRFCookie(w http.ResponseWriter, value string, maxAge int) {
	// Secure is false only for an http://localhost or http://127.0.0.1 base URL, where browsers drop Secure cookies.
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // G124: Secure follows the base URL scheme, see above
		Name:     csrfCookieName,
		Value:    value,
		Path:     cookiePath,
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   s.secureCookie,
		// Lax, not Strict: the cookie must come back on Google's top-level
		// redirect to the callback.
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) clearCSRFCookie(w http.ResponseWriter) {
	s.setCSRFCookie(w, "", -1)
}

func (s *Server) cookieMatches(r *http.Request, want string) bool {
	c, err := r.Cookie(csrfCookieName)
	return err == nil && equalSecret(c.Value, want)
}

func equalSecret(got, want string) bool {
	return got != "" && subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// issWriter adds the RFC 9207 iss parameter to the redirect fosite writes.
// Only query-mode redirects reach it: other response modes are refused.
type issWriter struct {
	http.ResponseWriter
	iss string
}

func (w *issWriter) WriteHeader(code int) {
	if code >= http.StatusMultipleChoices && code < http.StatusBadRequest {
		if loc, err := url.Parse(w.Header().Get("Location")); err == nil {
			q := loc.Query()
			q.Set(paramIss, w.iss)
			loc.RawQuery = q.Encode()
			w.Header().Set("Location", loc.String())
		}
	}
	w.ResponseWriter.WriteHeader(code)
}

func (s *Server) fail(w http.ResponseWriter, r *http.Request, op string, err error) {
	s.log.ErrorContext(r.Context(), "oauth: "+op, slog.Any("error", err))
	s.renderError(w, r, pageInternal)
}

func (s *Server) renderError(w http.ResponseWriter, r *http.Request, pg *page) {
	s.render(w, r, pg.status, tmplError, pg)
}

// render executes a template into a buffer first, so a template error never
// sends a half-written page.
func (s *Server) render(w http.ResponseWriter, r *http.Request, status int, name string, data any) {
	var buf bytes.Buffer
	if err := s.templates.ExecuteTemplate(&buf, name, data); err != nil {
		s.log.ErrorContext(r.Context(), "oauth: render page", slog.String("template", name), slog.Any("error", err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", mediaTypeHTML)
	h.Set("Cache-Control", cacheControlNoStore)
	h.Set("Content-Security-Policy", headerCSP)
	h.Set("X-Frame-Options", "DENY")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", headerNoReferer)
	w.WriteHeader(status)
	if _, err := buf.WriteTo(w); err != nil {
		s.log.DebugContext(r.Context(), "oauth: write page", slog.Any("error", err))
	}
}
