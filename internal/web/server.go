// Package web assembles the HTTP surface of the server: the MCP endpoint, the
// OAuth endpoints, the home and privacy pages, and the optional Prometheus
// endpoint behind Basic auth (spec §5.1, §11, §12).
package web

import (
	"bytes"
	"crypto/subtle"
	"embed"
	"html/template"
	"log/slog"
	"net/http"
	"strings"

	"github.com/samber/google-contacts-mcp/internal/config"
	"github.com/samber/google-contacts-mcp/internal/oauth"
)

const (
	// pathMCP is the MCP Streamable HTTP endpoint (spec §5.1).
	pathMCP = "/mcp"
	// patternMCP accepts POST only: the server runs in stateless mode, so
	// there is no GET stream or DELETE session to serve.
	patternMCP = "POST " + pathMCP
	// patternHome matches "/" exactly; "{$}" keeps it from catching every
	// unknown path, which must fall through to the mux's 404.
	patternHome    = "GET /{$}"
	patternPrivacy = "GET /privacy"

	// Template names under templates/.
	tmplHome    = "home.html"
	tmplPrivacy = "privacy.html"

	// repoURL is the source repository linked from the home page.
	repoURL = "https://github.com/samber/google-contacts-mcp"
	// googlePermissionsURL is where a user revokes the Google grant themselves.
	googlePermissionsURL = "https://myaccount.google.com/permissions"
	// inactivityDays matches the purge window of the jobs package and the
	// refresh token lifetime (spec §6.4, §10).
	inactivityDays = 180

	// authHeaderChallenge names the protection space of the metrics 401 answer.
	authHeaderChallenge = `Basic realm="metrics"`
)

//go:embed templates/*.html
var templatesFS embed.FS

// templates are parsed once; a parse error is a build defect, so it panics.
var templates = template.Must(template.ParseFS(templatesFS, "templates/*.html"))

// pageData feeds both page templates.
type pageData struct {
	OperatorName   string
	OperatorEmail  string
	MCPURL         string
	RepoURL        string
	PermissionsURL string
	InactivityDays int
}

// New returns the main listener's handler. metricsHandler is served on
// cfg.MetricsPath behind Basic auth only when metrics are enabled without a
// separate listener; otherwise that path is an ordinary 404.
func New(cfg config.Config, oauthSrv *oauth.Server, mcpHandler, metricsHandler http.Handler) http.Handler {
	mux := http.NewServeMux()
	oauthSrv.Mount(mux)
	mux.Handle(patternMCP, mcpHandler)

	data := pageData{
		OperatorName:   cfg.OperatorName,
		OperatorEmail:  cfg.OperatorEmail,
		MCPURL:         baseURL(cfg) + pathMCP,
		RepoURL:        repoURL,
		PermissionsURL: googlePermissionsURL,
		InactivityDays: inactivityDays,
	}
	mux.Handle(patternHome, page(tmplHome, data))
	mux.Handle(patternPrivacy, page(tmplPrivacy, data))

	if !cfg.Metrics || cfg.MetricsListen != "" || metricsHandler == nil {
		return mux
	}

	return withMetrics(cfg, metricsHandler, mux)
}

// NewMetricsHandler returns the handler of the separate metrics listener
// (--metrics-listen): the metrics path behind Basic auth, 404 elsewhere.
func NewMetricsHandler(cfg config.Config, metricsHandler http.Handler) http.Handler {
	return withMetrics(cfg, metricsHandler, http.NotFoundHandler())
}

// withMetrics serves cfg.MetricsPath behind Basic auth and everything else
// with next. The path is matched exactly instead of being registered as a
// ServeMux pattern, so an operator-chosen path with pattern syntax cannot
// panic the mux.
func withMetrics(cfg config.Config, metricsHandler, next http.Handler) http.Handler {
	protected := basicAuth(cfg.MetricsUser, cfg.MetricsPassword, metricsHandler)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == cfg.MetricsPath {
			protected.ServeHTTP(w, r)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// basicAuth admits a request only with the configured user and password.
// Both comparisons always run, so timing does not tell which one failed.
func basicAuth(user, password string, next http.Handler) http.Handler {
	wantUser, wantPassword := []byte(user), []byte(password)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUser, gotPassword, ok := r.BasicAuth()
		userOK := subtle.ConstantTimeCompare([]byte(gotUser), wantUser)

		passwordOK := subtle.ConstantTimeCompare([]byte(gotPassword), wantPassword)
		if !ok || userOK&passwordOK != 1 {
			w.Header().Set("WWW-Authenticate", authHeaderChallenge)
			http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)

			return
		}

		next.ServeHTTP(w, r)
	})
}

// page renders a template into a buffer first, so a template error never
// sends a half-written page.
func page(name string, data pageData) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var buf bytes.Buffer
		if err := templates.ExecuteTemplate(&buf, name, data); err != nil {
			slog.ErrorContext(r.Context(), "web: render page", slog.String("template", name), slog.Any("error", err))
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)

			return
		}

		oauth.SetPageHeaders(w.Header())
		w.WriteHeader(http.StatusOK)

		if _, err := buf.WriteTo(w); err != nil {
			slog.DebugContext(r.Context(), "web: write page", slog.Any("error", err))
		}
	})
}

// baseURL is the public base URL without a trailing slash, as the OAuth
// issuer is built.
func baseURL(cfg config.Config) string {
	if cfg.BaseURL == nil {
		return ""
	}

	return strings.TrimSuffix(cfg.BaseURL.String(), "/")
}
