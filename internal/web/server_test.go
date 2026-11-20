package web

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samber/google-contacts-mcp/internal/config"
	"github.com/samber/google-contacts-mcp/internal/crypto"
	"github.com/samber/google-contacts-mcp/internal/google"
	"github.com/samber/google-contacts-mcp/internal/oauth"
	"github.com/samber/google-contacts-mcp/internal/store"
)

const (
	testKey           = "A:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	testBaseURL       = "https://contacts.example.com"
	testOperatorName  = "Ada Operator"
	testOperatorEmail = "ops@example.com"
	testMetricsPath   = "/metrics"
	testMetricsUser   = "prom"
	testMetricsPass   = "0123456789abcdef-pass"
	metricsBody       = "metrics-ok"
	mcpBody           = "mcp-ok"
)

func testConfig(t *testing.T) config.Config {
	t.Helper()

	u, err := url.Parse(testBaseURL)
	if err != nil {
		t.Fatalf("parse base: %v", err)
	}

	return config.Config{
		BaseURL:         u,
		GoogleClientID:  "client-id",
		EnableDCR:       true,
		MetricsPath:     testMetricsPath,
		MetricsUser:     testMetricsUser,
		MetricsPassword: testMetricsPass,
		OperatorName:    testOperatorName,
		OperatorEmail:   testOperatorEmail,
	}
}

func newOAuth(t *testing.T, cfg config.Config) *oauth.Server {
	t.Helper()

	kr, err := crypto.ParseKeyring(testKey)
	if err != nil {
		t.Fatalf("ParseKeyring: %v", err)
	}

	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "db.sqlite"), kr)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}

	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})

	up := google.NewUpstream(google.UpstreamConfig{ClientID: "client-id", ClientSecret: "secret"}, st, nil)

	srv, err := oauth.New(cfg, oauth.Deps{
		Store:    st,
		Clients:  oauth.NewClientResolver(st, oauth.NewCIMDFetcher(oauth.CIMDOptions{})),
		Upstream: up,
		Keyring:  kr,
	})
	if err != nil {
		t.Fatalf("oauth.New: %v", err)
	}

	return srv
}

func fixed(body string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, body)
	})
}

func newHandler(t *testing.T, mutate func(*config.Config)) http.Handler {
	t.Helper()

	cfg := testConfig(t)
	if mutate != nil {
		mutate(&cfg)
	}

	return New(cfg, newOAuth(t, cfg), fixed(mcpBody), fixed(metricsBody))
}

type response struct {
	status int
	body   string
	header http.Header
}

func do(t *testing.T, h http.Handler, method, target string, setup func(*http.Request)) response {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), method, target, nil)
	if setup != nil {
		setup(req)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	return response{status: rec.Code, body: rec.Body.String(), header: rec.Header()}
}

func assertSame404(t *testing.T, got, want response) {
	t.Helper()

	if got.status != http.StatusNotFound || want.status != http.StatusNotFound {
		t.Fatalf("status = %d and %d, want both 404", got.status, want.status)
	}

	if got.body != want.body {
		t.Errorf("body = %q, want %q", got.body, want.body)
	}

	if got.header.Get("Content-Type") != want.header.Get("Content-Type") {
		t.Errorf("Content-Type = %q, want %q", got.header.Get("Content-Type"), want.header.Get("Content-Type"))
	}
}

func TestMetrics_DisabledSame404AsUnknown(t *testing.T) {
	t.Parallel()

	h := newHandler(t, func(c *config.Config) { c.Metrics = false })
	unknown := do(t, h, http.MethodGet, "/no-such-page", nil)
	metrics := do(t, h, http.MethodGet, testMetricsPath, func(r *http.Request) {
		r.SetBasicAuth(testMetricsUser, testMetricsPass)
	})
	assertSame404(t, metrics, unknown)
}

func TestMetrics_BasicAuth(t *testing.T) {
	t.Parallel()

	h := newHandler(t, func(c *config.Config) { c.Metrics = true })

	cases := []struct {
		name       string
		user, pass string
		noCreds    bool
		want       int
	}{
		{name: "no credentials", noCreds: true, want: http.StatusUnauthorized},
		{name: "wrong password", user: testMetricsUser, pass: "wrong-password-123456", want: http.StatusUnauthorized},
		{name: "wrong user", user: "someone", pass: testMetricsPass, want: http.StatusUnauthorized},
		{name: "empty", user: "", pass: "", want: http.StatusUnauthorized},
		{name: "right", user: testMetricsUser, pass: testMetricsPass, want: http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			res := do(t, h, http.MethodGet, testMetricsPath, func(r *http.Request) {
				if !tc.noCreds {
					r.SetBasicAuth(tc.user, tc.pass)
				}
			})
			if res.status != tc.want {
				t.Fatalf("status = %d, want %d", res.status, tc.want)
			}

			if tc.want == http.StatusUnauthorized {
				if got := res.header.Get("WWW-Authenticate"); got != `Basic realm="metrics"` {
					t.Errorf("WWW-Authenticate = %q", got)
				}

				if strings.Contains(res.body, metricsBody) {
					t.Error("401 leaked the metrics body")
				}

				return
			}

			if res.body != metricsBody {
				t.Errorf("body = %q, want %q", res.body, metricsBody)
			}
		})
	}
}

func TestMetrics_SeparateListenerNotOnMain(t *testing.T) {
	t.Parallel()

	cfg := testConfig(t)
	cfg.Metrics = true
	cfg.MetricsListen = "127.0.0.1:9090"
	main := New(cfg, newOAuth(t, cfg), fixed(mcpBody), fixed(metricsBody))
	auth := func(r *http.Request) { r.SetBasicAuth(testMetricsUser, testMetricsPass) }

	unknown := do(t, main, http.MethodGet, "/no-such-page", nil)
	onMain := do(t, main, http.MethodGet, testMetricsPath, auth)
	assertSame404(t, onMain, unknown)

	side := NewMetricsHandler(cfg, fixed(metricsBody))
	if res := do(t, side, http.MethodGet, testMetricsPath, auth); res.status != http.StatusOK || res.body != metricsBody {
		t.Errorf("metrics listener: status %d body %q", res.status, res.body)
	}

	if res := do(t, side, http.MethodGet, testMetricsPath, nil); res.status != http.StatusUnauthorized {
		t.Errorf("metrics listener without credentials: status %d, want 401", res.status)
	}

	assertSame404(t, do(t, side, http.MethodGet, "/", nil), unknown)
	assertSame404(t, do(t, side, http.MethodPost, "/mcp", nil), unknown)
}

func TestRoutes_MCPAndOAuthMounted(t *testing.T) {
	t.Parallel()

	h := newHandler(t, nil)
	if res := do(t, h, http.MethodPost, "/mcp", nil); res.status != http.StatusOK || res.body != mcpBody {
		t.Errorf("POST /mcp: status %d body %q", res.status, res.body)
	}

	res := do(t, h, http.MethodGet, "/.well-known/oauth-authorization-server", nil)
	if res.status != http.StatusOK || !strings.Contains(res.body, `"issuer":"`+testBaseURL+`"`) {
		t.Errorf("AS metadata: status %d body %q", res.status, res.body)
	}
}

func TestPages_ShowOperator(t *testing.T) {
	t.Parallel()

	h := newHandler(t, nil)
	for _, path := range []string{"/", "/privacy"} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()

			res := do(t, h, http.MethodGet, path, nil)
			if res.status != http.StatusOK {
				t.Fatalf("status = %d", res.status)
			}

			for _, want := range []string{testOperatorName, testOperatorEmail} {
				if !strings.Contains(res.body, want) {
					t.Errorf("page lacks %q", want)
				}
			}

			if got := res.header.Get("Content-Security-Policy"); !strings.Contains(got, "default-src 'none'") {
				t.Errorf("Content-Security-Policy = %q", got)
			}

			if got := res.header.Get("X-Frame-Options"); got != "DENY" {
				t.Errorf("X-Frame-Options = %q", got)
			}
		})
	}

	home := do(t, h, http.MethodGet, "/", nil)
	for _, want := range []string{testBaseURL + "/mcp", "early access", "github.com/samber/google-contacts-mcp", `href="/privacy"`} {
		if !strings.Contains(home.body, want) {
			t.Errorf("home page lacks %q", want)
		}
	}
}

func TestPages_EscapeOperator(t *testing.T) {
	t.Parallel()

	h := newHandler(t, func(c *config.Config) { c.OperatorName = "<script>x</script>" })

	res := do(t, h, http.MethodGet, "/", nil)
	if strings.Contains(res.body, "<script>x") {
		t.Error("operator name rendered unescaped")
	}
}

func TestPrivacy_ListsStoredData(t *testing.T) {
	t.Parallel()

	h := newHandler(t, nil)

	res := do(t, h, http.MethodGet, "/privacy", nil)
	for _, want := range []string{
		"e-mail address", "never stored", "Google account id", "encrypted",
		"contacts", "tool inputs", "in clear", "180 days",
		"myaccount.google.com/permissions",
	} {
		if !strings.Contains(res.body, want) {
			t.Errorf("privacy page lacks %q", want)
		}
	}
}

func TestPrivacy_LimitedUseDisclosure(t *testing.T) {
	t.Parallel()

	h := newHandler(t, nil)

	body := do(t, h, http.MethodGet, "/privacy", nil).body
	for _, want := range []string{
		"This service's use and transfer of information received from Google APIs adheres to the",
		`href="https://developers.google.com/terms/api-services-user-data-policy"`,
		"Google API Services User Data Policy</a>, including the Limited Use requirements.",
	} {
		// html/template escapes the apostrophe in text.
		if !strings.Contains(strings.ReplaceAll(body, "&#39;", "'"), want) {
			t.Errorf("privacy page lacks %q", want)
		}
	}
}

func TestPages_NonGETRejected(t *testing.T) {
	t.Parallel()

	h := newHandler(t, nil)
	if res := do(t, h, http.MethodPost, "/privacy", nil); res.status != http.StatusMethodNotAllowed {
		t.Errorf("POST /privacy: status %d, want 405", res.status)
	}
}

func TestPrivacy_DeletionTimingAccurate(t *testing.T) {
	t.Parallel()

	h := newHandler(t, nil)

	body := do(t, h, http.MethodGet, "/privacy", nil).body
	for _, want := range []string{"expire after 10 minutes", "deleted within the hour", "backups", "backup retention"} {
		if !strings.Contains(body, want) {
			t.Errorf("privacy page lacks %q", want)
		}
	}

	if strings.Contains(body, "deleted after 10 minutes") {
		t.Error("privacy page claims pending sign-ins are deleted after 10 minutes")
	}
}
