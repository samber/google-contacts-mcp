package google

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/oauth2"
	"google.golang.org/api/people/v1"

	"github.com/samber/google-contacts-mcp/internal/crypto"
	"github.com/samber/google-contacts-mcp/internal/store"
)

// Route keys used by fakeGoogle to count and override requests.
const (
	routeSearch      = "search"
	routeList        = "list"
	routeGet         = "get"
	routeCreate      = "create"
	routeUpdate      = "update"
	routeDelete      = "delete"
	routeListGroups  = "listGroups"
	routeCreateGroup = "createGroup"
	routeModify      = "modify"
	routeToken       = "token"
	routeRevoke      = "revoke"
)

// fakeGoogle serves the People API, the OAuth token endpoint and the revoke
// endpoint on one httptest server.
type fakeGoogle struct {
	t   *testing.T
	srv *httptest.Server

	mu            sync.Mutex
	hits          map[string]int
	emptySearches int
	lastQuery     map[string]string // route -> raw query of the last request
	lastBody      map[string][]byte // route -> body of the last request
	lastAuth      map[string]string // route -> Authorization header of the last request
	lastPath      map[string]string // route -> path of the last request
	groups        []*people.ContactGroup

	tokenHits   atomic.Int32
	tokenDelay  time.Duration
	tokenError  string // OAuth error code returned by the token endpoint, "" for success
	newAccess   string // access token returned by refresh and code grants
	newRefresh  string // refresh token returned, "" to omit it
	revokeCode  int    // status returned by the revoke endpoint, 0 for 200
	revokedForm []string
	// onToken, when set, runs before the token endpoint answers.
	onToken func()

	// override, when set, may answer a request itself and return true.
	override func(w http.ResponseWriter, r *http.Request, route string) bool
}

func newFakeGoogle(t *testing.T) *fakeGoogle {
	t.Helper()
	f := &fakeGoogle{
		t:         t,
		hits:      map[string]int{},
		lastQuery: map[string]string{},
		lastBody:  map[string][]byte{},
		lastAuth:  map[string]string{},
		lastPath:  map[string]string{},
		newAccess: "access-new",
	}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeGoogle) count(route string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hits[route]
}

func (f *fakeGoogle) last(route string) (query string, body []byte, auth, path string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastQuery[route], f.lastBody[route], f.lastAuth[route], f.lastPath[route]
}

func route(r *http.Request) string {
	p := r.URL.Path
	switch {
	case p == "/token":
		return routeToken
	case p == "/revoke":
		return routeRevoke
	case p == "/v1/people:searchContacts":
		return routeSearch
	case p == "/v1/people:createContact":
		return routeCreate
	case p == "/v1/people/me/connections":
		return routeList
	case strings.HasSuffix(p, ":updateContact"):
		return routeUpdate
	case strings.HasSuffix(p, ":deleteContact"):
		return routeDelete
	case strings.HasPrefix(p, "/v1/people/"):
		return routeGet
	case p == "/v1/contactGroups" && r.Method == http.MethodGet:
		return routeListGroups
	case p == "/v1/contactGroups" && r.Method == http.MethodPost:
		return routeCreateGroup
	case strings.HasSuffix(p, "/members:modify"):
		return routeModify
	}
	return ""
}

func (f *fakeGoogle) serve(w http.ResponseWriter, r *http.Request) {
	rt := route(r)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		f.t.Errorf("read body: %v", err)
	}
	f.mu.Lock()
	f.hits[rt]++
	f.lastQuery[rt] = r.URL.RawQuery
	f.lastBody[rt] = body
	f.lastAuth[rt] = r.Header.Get("Authorization")
	f.lastPath[rt] = r.URL.Path
	if rt == routeSearch && r.URL.Query().Get("query") == "" {
		f.emptySearches++
	}
	override := f.override
	f.mu.Unlock()

	if override != nil && override(w, r, rt) {
		return
	}
	resource := strings.TrimPrefix(r.URL.Path, "/v1/")
	switch rt {
	case routeToken:
		f.serveToken(w)
	case routeRevoke:
		f.mu.Lock()
		f.revokedForm = append(f.revokedForm, string(body))
		code := f.revokeCode
		f.mu.Unlock()
		if code == 0 {
			code = http.StatusOK
		}
		w.WriteHeader(code)
	case routeSearch:
		f.json(w, people.SearchResponse{Results: []*people.SearchResult{{Person: testPerson("people/c1", "e1")}}})
	case routeList:
		next := "page-2"
		if r.URL.Query().Get("pageToken") != "" {
			next = ""
		}
		f.json(w, people.ListConnectionsResponse{Connections: []*people.Person{testPerson("people/c1", "e1")}, NextPageToken: next})
	case routeGet:
		f.json(w, testPerson(resource, "e1"))
	case routeCreate, routeUpdate:
		var p people.Person
		if err := json.Unmarshal(body, &p); err != nil {
			f.t.Errorf("decode person: %v", err)
		}
		p.ResourceName = strings.TrimSuffix(resource, ":updateContact")
		if rt == routeCreate {
			p.ResourceName = "people/new"
		}
		p.Metadata = &people.PersonMetadata{Sources: []*people.Source{{Type: "CONTACT", Id: "c", Etag: "e2"}}}
		f.json(w, p)
	case routeDelete, routeModify:
		f.json(w, struct{}{})
	case routeListGroups:
		f.mu.Lock()
		groups := f.groups
		f.mu.Unlock()
		f.json(w, people.ListContactGroupsResponse{ContactGroups: groups})
	case routeCreateGroup:
		var req people.CreateContactGroupRequest
		if err := json.Unmarshal(body, &req); err != nil {
			f.t.Errorf("decode group: %v", err)
		}
		f.json(w, people.ContactGroup{ResourceName: "contactGroups/new", Name: req.ContactGroup.Name, FormattedName: req.ContactGroup.Name, GroupType: "USER_CONTACT_GROUP"})
	default:
		f.t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotImplemented)
	}
}

func (f *fakeGoogle) serveToken(w http.ResponseWriter) {
	f.tokenHits.Add(1)
	if f.tokenDelay > 0 {
		time.Sleep(f.tokenDelay)
	}
	f.mu.Lock()
	tokErr, access, refresh, hook := f.tokenError, f.newAccess, f.newRefresh, f.onToken
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	if tokErr != "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		f.write(w, `{"error":"`+tokErr+`","error_description":"secret detail"}`)
		return
	}
	resp := map[string]any{"access_token": access, "token_type": "Bearer", "expires_in": 3600}
	if refresh != "" {
		resp["refresh_token"] = refresh
	}
	f.json(w, resp)
}

func (f *fakeGoogle) json(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		f.t.Errorf("encode: %v", err)
	}
}

func (f *fakeGoogle) write(w http.ResponseWriter, s string) {
	if _, err := io.WriteString(w, s); err != nil {
		f.t.Errorf("write: %v", err)
	}
}

// testPerson returns a person carrying a CONTACT source with etag.
func testPerson(resourceName, etag string) *people.Person {
	return &people.Person{
		ResourceName:   resourceName,
		Etag:           "person-etag",
		Metadata:       &people.PersonMetadata{Sources: []*people.Source{{Type: "CONTACT", Id: "c1", Etag: etag}}},
		Names:          []*people.Name{{GivenName: "Ada", FamilyName: "Lovelace", DisplayName: "Ada Lovelace", UnstructuredName: "Ada Lovelace"}},
		EmailAddresses: []*people.EmailAddress{{Value: "ada@example.com", Type: "work"}},
	}
}

// testEnv is an Upstream wired to a fakeGoogle and a temp store holding one
// user with a valid access token.
type testEnv struct {
	up     *Upstream
	fake   *fakeGoogle
	st     *store.Store
	userID string
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	ctx := context.Background()
	f := newFakeGoogle(t)
	keys, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	kr, err := crypto.ParseKeyring(keys)
	if err != nil {
		t.Fatalf("ParseKeyring: %v", err)
	}
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "db.sqlite"), kr)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("store.Close: %v", err)
		}
	})
	u, err := st.UpsertUser(ctx, "sub-1", "user@example.com")
	if err != nil {
		t.Fatalf("UpsertUser: %v", err)
	}
	if err := st.SaveGoogleToken(ctx, u.ID, store.GoogleToken{
		RefreshToken: "refresh-1", AccessToken: "access-1", Expiry: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("SaveGoogleToken: %v", err)
	}
	up := NewUpstream(UpstreamConfig{
		ClientID:     "client-id",
		ClientSecret: "client-secret",
		RedirectURL:  "https://mcp.example.com/oauth/google/callback",
		Endpoint: oauth2.Endpoint{
			AuthURL:   f.srv.URL + "/auth",
			TokenURL:  f.srv.URL + "/token",
			AuthStyle: oauth2.AuthStyleInParams,
		},
		PeopleBaseURL: f.srv.URL + "/",
		RevokeURL:     f.srv.URL + "/revoke",
	}, st, f.srv.Client())
	return &testEnv{up: up, fake: f, st: st, userID: u.ID}
}

// expireToken replaces the stored token with an expired one.
func (e *testEnv) expireToken(t *testing.T) {
	t.Helper()
	if err := e.st.SaveGoogleToken(context.Background(), e.userID, store.GoogleToken{
		RefreshToken: "refresh-1", AccessToken: "access-old", Expiry: time.Now().Add(-time.Hour),
	}); err != nil {
		t.Fatalf("SaveGoogleToken: %v", err)
	}
}
