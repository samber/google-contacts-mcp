package oauth

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ory/fosite"

	"github.com/samber/google-contacts-mcp/internal/crypto"
	"github.com/samber/google-contacts-mcp/internal/store"
)

// testKey is a fixed keyring entry: the store needs one to open.
const testKey = "A:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="

func openStore(t *testing.T) *store.Store {
	t.Helper()
	kr, err := crypto.ParseKeyring(testKey)
	if err != nil {
		t.Fatalf("ParseKeyring: %v", err)
	}
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "db.sqlite"), kr)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return st
}

func TestGetClient_UnknownNonURLID(t *testing.T) {
	r := NewClientResolver(openStore(t), NewCIMDFetcher(CIMDOptions{}))
	_, err := r.GetClient(context.Background(), "no-such-client")
	if !errors.Is(err, fosite.ErrNotFound) {
		t.Fatalf("err = %v, want fosite.ErrNotFound", err)
	}
}

func TestGetClient_DCRRow(t *testing.T) {
	st := openStore(t)
	want := &store.OAuthClient{ID: "abc", Kind: store.ClientKindDCR, Name: "n", RedirectURIs: []string{"https://x.example.com/cb"}}
	if err := st.SaveClient(context.Background(), want); err != nil {
		t.Fatalf("SaveClient: %v", err)
	}
	r := NewClientResolver(st, NewCIMDFetcher(CIMDOptions{}))
	c, err := r.GetClient(context.Background(), "abc")
	if err != nil {
		t.Fatalf("GetClient: %v", err)
	}
	if c.GetID() != "abc" || c.GetRedirectURIs()[0] != "https://x.example.com/cb" {
		t.Errorf("client = %+v", c)
	}
}

func TestGetClient_CIMDCacheRefreshAndStale(t *testing.T) {
	var hits atomic.Int32
	var fail atomic.Bool
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if fail.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Cache-Control", "max-age=60")
		writeJSON(t, w, docFor(r))
	}))
	defer srv.Close()

	now := fetchNow
	f := newTestFetcher(t, srv, CIMDOptions{AllowPrivate: true})
	f.now = func() time.Time { return now }
	r := NewClientResolver(openStore(t), f)
	r.now = func() time.Time { return now }
	ctx := context.Background()
	id := srv.URL + testDocPath

	for i := range 2 {
		c, err := r.GetClient(ctx, id)
		if err != nil {
			t.Fatalf("GetClient #%d: %v", i, err)
		}
		if c.GetID() != id {
			t.Fatalf("id = %q", c.GetID())
		}
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("hits after cached read = %d, want 1", got)
	}

	now = now.Add(2 * time.Minute)
	if _, err := r.GetClient(ctx, id); err != nil {
		t.Fatalf("GetClient after expiry: %v", err)
	}
	if got := hits.Load(); got != 2 {
		t.Fatalf("hits after expiry = %d, want 2", got)
	}

	now = now.Add(2 * time.Minute)
	fail.Store(true)
	c, err := r.GetClient(ctx, id)
	if err != nil {
		t.Fatalf("stale row not served: %v", err)
	}
	if c.GetID() != id {
		t.Errorf("stale id = %q", c.GetID())
	}
	if got := hits.Load(); got != 3 {
		t.Errorf("hits after failed refetch = %d, want 3", got)
	}
}

func TestGetClient_CIMDFetchFailsWithoutCache(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	r := NewClientResolver(openStore(t), newTestFetcher(t, srv, CIMDOptions{AllowPrivate: true}))
	if _, err := r.GetClient(context.Background(), srv.URL+testDocPath); err == nil {
		t.Fatal("unfetchable client resolved")
	}
}

func TestRedirectMatches(t *testing.T) {
	tests := []struct {
		name       string
		registered []string
		got        string
		want       bool
	}{
		{"exact", []string{"https://claude.ai/api/mcp/auth_callback"}, "https://claude.ai/api/mcp/auth_callback", true},
		{"second of many", []string{"https://a.example.com/cb", "https://b.example.com/cb"}, "https://b.example.com/cb", true},
		{"path mismatch", []string{"https://claude.ai/api/mcp/auth_callback"}, "https://claude.ai/api/mcp/other", false},
		{"trailing slash", []string{"https://claude.ai/cb"}, "https://claude.ai/cb/", false},
		{"https port differs", []string{"https://claude.ai/cb"}, "https://claude.ai:8443/cb", false},
		{"host differs", []string{"https://claude.ai/cb"}, "https://evil.ai/cb", false},
		{"query differs", []string{"https://claude.ai/cb"}, "https://claude.ai/cb?x=1", false},
		{"loopback v4 any port", []string{"http://127.0.0.1/callback"}, "http://127.0.0.1:53124/callback", true},
		{"loopback v4 port to port", []string{"http://127.0.0.1:1000/callback"}, "http://127.0.0.1:2000/callback", true},
		{"localhost any port", []string{"http://localhost:3000/callback"}, "http://localhost:61000/callback", true},
		{"loopback v6 any port", []string{"http://[::1]/callback"}, "http://[::1]:9999/callback", true},
		{"loopback path mismatch", []string{"http://127.0.0.1/callback"}, "http://127.0.0.1:5000/other", false},
		{"loopback host differs", []string{"http://127.0.0.1/callback"}, "http://localhost:5000/callback", false},
		{"loopback scheme differs", []string{"http://127.0.0.1/callback"}, "https://127.0.0.1:5000/callback", false},
		{"loopback query differs", []string{"http://127.0.0.1/callback"}, "http://127.0.0.1:5000/callback?a=b", false},
		{"loopback userinfo", []string{"http://127.0.0.1/callback"}, "http://u@127.0.0.1:5000/callback", false},
		{"non-loopback http port", []string{"http://example.com/cb"}, "http://example.com:81/cb", false},
		{"empty registered", nil, "https://claude.ai/cb", false},
		{"unparsable", []string{"https://claude.ai/cb"}, "://bad", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := RedirectMatches(tt.registered, tt.got); got != tt.want {
				t.Errorf("RedirectMatches(%v, %q) = %v, want %v", tt.registered, tt.got, got, tt.want)
			}
		})
	}
}

func postRegister(t *testing.T, r *ClientResolver, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/oauth/register", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.RegisterHandler().ServeHTTP(rec, req)
	return rec
}

func TestRegister_OK(t *testing.T) {
	st := openStore(t)
	r := NewClientResolver(st, NewCIMDFetcher(CIMDOptions{}))
	r.now = func() time.Time { return fetchNow }

	rec := postRegister(t, r, `{
		"client_name": "Claude",
		"redirect_uris": ["https://claude.ai/api/mcp/auth_callback", "http://localhost:6274/cb"],
		"token_endpoint_auth_method": "none",
		"grant_types": ["authorization_code", "refresh_token"],
		"response_types": ["code"],
		"application_type": "web"
	}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("content-type = %q", ct)
	}
	var resp struct {
		ClientID                string   `json:"client_id"`
		ClientIDIssuedAt        int64    `json:"client_id_issued_at"`
		RedirectURIs            []string `json:"redirect_uris"`
		TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
		GrantTypes              []string `json:"grant_types"`
		ResponseTypes           []string `json:"response_types"`
		ClientName              string   `json:"client_name"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if raw, err := hex.DecodeString(resp.ClientID); err != nil || len(raw) != clientIDBytes {
		t.Errorf("client_id = %q, want %d hex bytes", resp.ClientID, clientIDBytes)
	}
	if resp.ClientIDIssuedAt != fetchNow.Unix() || resp.TokenEndpointAuthMethod != "none" || resp.ClientName != "Claude" {
		t.Errorf("resp = %+v", resp)
	}
	if len(resp.RedirectURIs) != 2 || len(resp.GrantTypes) != 2 || len(resp.ResponseTypes) != 1 {
		t.Errorf("resp lists = %+v", resp)
	}

	c, err := st.ClientByID(context.Background(), resp.ClientID)
	if err != nil {
		t.Fatalf("ClientByID: %v", err)
	}
	if c.Kind != store.ClientKindDCR || c.Name != "Claude" || len(c.RedirectURIs) != 2 {
		t.Errorf("stored = %+v", c)
	}
	if fc, err := r.GetClient(context.Background(), resp.ClientID); err != nil || fc.GetID() != resp.ClientID {
		t.Errorf("GetClient = %v, %v", fc, err)
	}
}

func TestRegister_MinimalDefaultsName(t *testing.T) {
	r := NewClientResolver(openStore(t), NewCIMDFetcher(CIMDOptions{}))
	rec := postRegister(t, r, `{"redirect_uris": ["https://app.example.com/cb"]}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"client_name":"app.example.com"`)) {
		t.Errorf("body = %s", rec.Body)
	}
}

func TestRegister_RejectsConfidential(t *testing.T) {
	r := NewClientResolver(openStore(t), NewCIMDFetcher(CIMDOptions{}))
	rec := postRegister(t, r, `{"redirect_uris": ["https://app.example.com/cb"], "token_endpoint_auth_method": "client_secret_basic"}`)
	assertRegisterError(t, rec, "invalid_client_metadata")
}

func TestRegister_Rejects(t *testing.T) {
	tests := []struct {
		name, body, wantErr string
	}{
		{"no redirect uris", `{}`, "invalid_redirect_uri"},
		{"empty redirect uris", `{"redirect_uris": []}`, "invalid_redirect_uri"},
		{"relative redirect", `{"redirect_uris": ["/cb"]}`, "invalid_redirect_uri"},
		{"http non-loopback", `{"redirect_uris": ["http://app.example.com/cb"]}`, "invalid_redirect_uri"},
		{"custom scheme", `{"redirect_uris": ["myapp://cb"]}`, "invalid_redirect_uri"},
		{"fragment", `{"redirect_uris": ["https://app.example.com/cb#x"]}`, "invalid_redirect_uri"},
		{"bad application type", `{"redirect_uris": ["https://app.example.com/cb"], "application_type": "service"}`, "invalid_client_metadata"},
		{"bad grant type", `{"redirect_uris": ["https://app.example.com/cb"], "grant_types": ["client_credentials"]}`, "invalid_client_metadata"},
		{"bad response type", `{"redirect_uris": ["https://app.example.com/cb"], "response_types": ["token"]}`, "invalid_client_metadata"},
		{"malformed json", `{"redirect_uris": [`, "invalid_client_metadata"},
		{"too large", `{"client_name": "` + strings.Repeat("x", int(maxRegistrationBody)) + `"}`, "invalid_client_metadata"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewClientResolver(openStore(t), NewCIMDFetcher(CIMDOptions{}))
			assertRegisterError(t, postRegister(t, r, tt.body), tt.wantErr)
		})
	}
}

func TestRegister_LoopbackHTTPAllowed(t *testing.T) {
	for _, uri := range []string{"http://127.0.0.1:33418/cb", "http://localhost/cb", "http://[::1]:8080/cb"} {
		r := NewClientResolver(openStore(t), NewCIMDFetcher(CIMDOptions{}))
		if rec := postRegister(t, r, `{"redirect_uris": ["`+uri+`"]}`); rec.Code != http.StatusCreated {
			t.Errorf("%s: status = %d, body %s", uri, rec.Code, rec.Body)
		}
	}
}

func TestRegister_MethodNotAllowed(t *testing.T) {
	r := NewClientResolver(openStore(t), NewCIMDFetcher(CIMDOptions{}))
	rec := httptest.NewRecorder()
	r.RegisterHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/oauth/register", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d", rec.Code)
	}
}

func assertRegisterError(t *testing.T, rec *httptest.ResponseRecorder, wantErr string) {
	t.Helper()
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	var body struct {
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body)
	}
	if body.Error != wantErr || body.Description == "" {
		t.Errorf("error = %+v, want %q", body, wantErr)
	}
}
