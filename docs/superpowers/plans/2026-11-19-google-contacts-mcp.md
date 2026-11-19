# google-contacts-mcp Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a multi-tenant Go MCP server that exposes a user's Google Contacts to Claude and ChatGPT over OAuth 2.1, with state in one SQLite file.

**Architecture:** One binary. Toward MCP clients it is an OAuth 2.1 authorization server (ory/fosite) and a stateless Streamable HTTP MCP server (go-sdk). Toward Google it is a confidential OAuth client calling People API v1 on every tool call. SQLite holds users, encrypted Google tokens, fosite token signatures and clients; never contact data.

**Tech Stack:** Go 1.25+, `github.com/modelcontextprotocol/go-sdk` v1.8.0, `github.com/ory/fosite` v0.49.0, `modernc.org/sqlite`, `google.golang.org/api/people/v1`, `golang.org/x/oauth2`, `github.com/coreos/go-oidc/v3` (ID token verification), `golang.org/x/time/rate`, `github.com/prometheus/client_golang`, `log/slog`, `html/template`.

**Spec:** `docs/superpowers/specs/2026-11-19-google-contacts-mcp-design.md` (read it before any task; section numbers below refer to it).

## Global Constraints

- Module path `github.com/samber/google-contacts-mcp`; binary `google-contacts-mcp`; `go 1.25` in go.mod; `CGO_ENABLED=0` builds.
- MIT license; all code, comments and docs in English.
- Never write contact data, tool arguments or tool results to disk; to logs only at `debug`.
- Never log tokens, codes, secrets, or Google response bodies at any level.
- Every flag also reads `GCMCP_<FLAG_UPPER_SNAKE>`; flag wins over env. `GCMCP_GOOGLE_CLIENT_SECRET` and `GCMCP_ENCRYPTION_KEYS` are env-only.
- PKCE S256 only. Access token 1 h, authorization code 10 min, refresh token 180 days (rotated each use), pending authorization 10 min.
- Google scopes exactly `openid email https://www.googleapis.com/auth/contacts`. MCP scope exactly `contacts`.
- MCP endpoint `POST /mcp`; Google redirect `<base-url>/oauth/google/callback`.
- No magic numbers or strings inline: named constants, each with a one-line comment saying why.
- Never ignore an error. Structured `slog` logging.
- Gates before every commit: `gofmt -l .` empty, `go vet ./...`, `golangci-lint run`, `go test -race ./...`.
- Commits follow Conventional Commits, author Samuel Berthe, no mention of Claude.

## Review Focus

1. **Client name with HTML in a CIMD document or DCR request** (`<script>`). Consent page must escape it and still show the redirect host in bold. Test: Task 9 `TestConsentPage_EscapesClientName`.
2. **Existing contact stores the e-mail with different case or spaces** (`Julie.Martin@X.com `). Upsert must match it, not create a duplicate. Test: Task 10 `TestUpsert_MatchesEmailCaseInsensitive`.
3. **Contacts Google returns with missing parts** (no `names`, no `organizations`, nil slices). Every tool must return them without panicking and with an empty display name. Test: Task 4 `TestFromPerson_EmptyPerson`.
4. **Two parallel tool calls for one user while the Google access token is expired.** Exactly one refresh hits Google; both calls succeed. Test: Task 5 `TestTokenSource_ConcurrentRefreshOnce`.
5. **`list_contacts` with `page_size` 0, negative, or above 1000, or a garbage `page_token`.** Clamp the size to 1..1000 (default 100) and return a clear tool error for a bad token. Test: Task 10 `TestListContacts_PageSizeClamp` and `TestListContacts_BadPageToken`.

---

## File Structure

```
cmd/google-contacts-mcp/main.go        subcommand dispatch (serve, genkey, rekey, disconnect, version)
internal/config/config.go              flags + env + validation -> Config
internal/crypto/keyring.go             AES-256-GCM keyring, HKDF secrets, genkey
internal/store/store.go                Open, migrations, Tx helper
internal/store/migrations/001_init.sql schema
internal/store/users.go                users + Google tokens (encrypted), rekey, delete, purge
internal/store/oauth.go                fosite storage adapter, clients, pending authorizations
internal/contacts/contact.go           Contact model, Person <-> Contact mapping
internal/contacts/merge.go             enrich rules
internal/contacts/errors.go            sentinel errors
internal/google/people.go              People API wrapper implementing contacts.People
internal/google/upstream.go            Google OAuth config, per-user token source, revoke
internal/limits/limits.go              per-user rate limit + write lock
internal/oauth/provider.go             fosite compose, config
internal/oauth/metadata.go             RFC 8414 handler
internal/oauth/cimd.go                 CIMD fetcher (SSRF-safe)
internal/oauth/clients.go              client resolution (CIMD / DCR) + DCR handler
internal/oauth/flow.go                 authorize, consent, Google callback handlers
internal/oauth/token.go                token + revoke handlers, MCP TokenVerifier
internal/oauth/templates/*.html        consent, error pages
internal/tools/server.go               MCP server, tool registration, middleware (logs, metrics, limits)
internal/tools/handlers.go             one handler per tool
internal/tools/upsert.go               upsert_contact_by_email
internal/web/server.go                 router, 404, pages, metrics endpoint
internal/web/templates/{home,privacy}.html
internal/jobs/jobs.go                  hourly + daily cleanup
internal/obs/metrics.go                Prometheus collectors
deploy/litestream.yml, deploy/start.sh, deploy/clevercloud-prebuild.sh, Dockerfile, .goreleaser.yaml
README.md, docs/ROADMAP.md, CLAUDE.md, LICENSE, .gitignore, .golangci.yml, .github/workflows/ci.yml
```

---

### Task 1: Scaffold, config and `version`

**Files:**
- Create: `go.mod`, `.gitignore` (`/data/`, `/dist/`, `.env`), `.golangci.yml`, `.github/workflows/ci.yml` (runs the four gates), `LICENSE` (MIT, Samuel Berthe, 2026), `cmd/google-contacts-mcp/main.go`, `internal/config/config.go`
- Test: `internal/config/config_test.go`

**Interfaces:**
- Produces: `config.Load(args []string, getenv func(string) string) (Config, error)`; `type Config struct { BaseURL *url.URL; Listen, DBPath, GoogleClientID, GoogleClientSecret string; EncryptionKeys string; AllowedDomains []string; EnableDCR bool; LogLevel slog.Level; Metrics bool; MetricsPath, MetricsListen, MetricsUser, MetricsPassword string; RateLimitReads, RateLimitWrites int; OperatorName, OperatorEmail string }`. `main.go` dispatches `os.Args[1]` in {`serve` (default), `genkey`, `rekey`, `disconnect`, `version`}; later tasks fill each subcommand. `version` prints `main.version` (set by `-ldflags`, default `dev`).

- [ ] **Step 1: Write failing tests** in `config_test.go`:
  - `TestLoad_Defaults`: `Listen == "127.0.0.1:8080"`, `DBPath == "./data/google-contacts-mcp.db"`, `EnableDCR`, `LogLevel == slog.LevelInfo`, `MetricsPath == "/metrics"`, `RateLimitReads == 120`, `RateLimitWrites == 30`.
  - `TestLoad_PortEnv`: `PORT=9000` gives `Listen == "0.0.0.0:9000"`; an explicit `--listen` still wins.
  - `TestLoad_EnvAndFlagPrecedence`: `GCMCP_DB_PATH=/a` plus `--db-path=/b` gives `/b`.
  - `TestLoad_SecretsEnvOnly`: `--google-client-secret=x` is an unknown-flag error.
  - `TestLoad_Required`: each missing required value (`base-url`, `google-client-id`, client secret, encryption keys, `operator-name`, `operator-email`) yields an error naming it.
  - `TestLoad_BaseURLMustBeHTTPS`: `http://x` rejected, except `http://localhost` and `http://127.0.0.1` (local dev).
  - `TestLoad_Metrics`: `--metrics` without user is an error; password of 15 chars is an error; 16 OK; `--metrics-password-file` read and trimmed.
  - `TestLoad_AllowedDomains`: `" Acme.com, gmail.com "` gives `["acme.com","gmail.com"]`.
- [ ] **Step 2:** `go test ./internal/config/...`, expect FAIL (undefined `Load`).
- [ ] **Step 3:** Implement `Load` with stdlib `flag.FlagSet` (`ContinueOnError`); env fallback applied to each flag's default before parsing.
- [ ] **Step 4:** `go test -race ./internal/config/...` PASS; `go build ./...`; `go run ./cmd/google-contacts-mcp version` prints `dev`.
- [ ] **Step 5:** Commit `chore: scaffold module, config and version command`.

### Task 2: Crypto keyring and `genkey`

**Files:** Create `internal/crypto/keyring.go`; Test `internal/crypto/keyring_test.go`.

**Interfaces:**
- Produces: `crypto.ParseKeyring(s string) (*Keyring, error)` where `s` is `keyid:base64(32 bytes)` comma list, first active; `(*Keyring).Encrypt(plaintext, aad []byte) ([]byte, error)`; `(*Keyring).Decrypt(ciphertext, aad []byte) ([]byte, error)`; `(*Keyring).ActiveID() string`; `(*Keyring).KeyID(ciphertext []byte) (string, error)`; `(*Keyring).HMACSecrets(label string) (active []byte, rotated [][]byte)` (HKDF-SHA256, 32 bytes, one per key, active first); `crypto.GenerateKey() (string, error)` returns a new `keyid:base64` (key id = 8 random hex chars).
- Ciphertext layout: `version(1 byte = 1) | len(keyid)(1 byte) | keyid | nonce(12) | sealed`.

- [ ] **Step 1: Failing tests:** `TestRoundTrip`; `TestDecrypt_WrongAAD` (error); `TestDecrypt_OldKeyStillListed` (encrypt under key A, keyring `B,A` decrypts, `KeyID == "A"`); `TestDecrypt_KeyRemoved` (error mentions key id); `TestParse_Invalid` (bad base64, 16-byte key, duplicate id, empty); `TestHMACSecrets_DifferPerLabel`; `TestGenerateKey_Parses`.
- [ ] **Step 2:** Run, FAIL.
- [ ] **Step 3:** Implement with `crypto/aes`, `crypto/cipher`, `golang.org/x/crypto/hkdf`. Wire `genkey` subcommand to print `GenerateKey()`.
- [ ] **Step 4:** `go test -race ./internal/crypto/...` PASS.
- [ ] **Step 5:** Commit `feat(crypto): add rotatable AES-GCM keyring`.

### Task 3: SQLite store, users and Google tokens

**Files:** Create `internal/store/store.go`, `internal/store/migrations/001_init.sql`, `internal/store/users.go`; Test `internal/store/users_test.go`.

**Interfaces:**
- Consumes: `*crypto.Keyring`.
- Produces: `store.Open(ctx, path string, kr *crypto.Keyring) (*Store, error)` (creates parent dir, WAL, `foreign_keys=ON`, `busy_timeout=5000`, `SetMaxOpenConns(1)`, applies embedded migrations tracked in `schema_migrations`); `(*Store).Close() error`;
  `type User struct { ID, GoogleSub, Email string; CreatedAt, LastSeenAt time.Time }`;
  `UpsertUser(ctx, sub, email string) (User, error)` (ID = random 16-byte hex; keyed by sub; updates e-mail);
  `UserBySubOrEmail(ctx, s string) (User, error)` (`store.ErrNotFound`);
  `TouchUser(ctx, id string) error` (sets `last_seen_at`, at most once per minute per user, in memory);
  `type GoogleToken struct { RefreshToken, AccessToken string; Expiry time.Time }`;
  `SaveGoogleToken(ctx, userID string, t GoogleToken) error` (encrypt both with AAD = userID);
  `GoogleToken(ctx, userID string) (GoogleToken, error)`;
  `DeleteUser(ctx, userID string) error` (cascades: Google tokens and every fosite row whose session subject is the user);
  `InactiveUsers(ctx, before time.Time) ([]User, error)`;
  `Rekey(ctx) (int, error)` (re-encrypts every row not under the active key; returns count).
- Schema (`001_init.sql`): `users`, `google_tokens(user_id PK FK ON DELETE CASCADE, refresh_ct BLOB, access_ct BLOB, expiry, key_id)`, `oauth_clients(id PK, kind CHECK IN ('cimd','dcr'), metadata JSON, fetched_at, expires_at, last_used_at)`, `oauth_tokens(signature PK, kind CHECK IN ('code','access','refresh','pkce'), request_id, user_id FK ON DELETE CASCADE NULL, client_id, data BLOB, expires_at, active INTEGER)` with index on `request_id`, `pending_authorizations(id PK, data BLOB, expires_at)`.

- [ ] **Step 1: Failing tests:** `TestOpen_MigratesTwiceIdempotent`; `TestUpsertUser_SameSubUpdatesEmail`; `TestUserBySubOrEmail`; `TestGoogleToken_RoundTripAndCiphertextOnDisk` (raw `SELECT refresh_ct` must not contain the plaintext); `TestDeleteUser_Cascades` (insert an `oauth_tokens` row for the user, gone after); `TestRekey` (open with `A`, save, reopen with `B,A`, `Rekey` returns 1, reopen with `B` only, token readable); `TestInactiveUsers`.
- [ ] **Step 2:** Run, FAIL.
- [ ] **Step 3:** Implement. Migrations via `embed.FS`, applied in filename order inside one transaction each.
- [ ] **Step 4:** `go test -race ./internal/store/...` PASS. Wire `rekey` subcommand (prints count) and `disconnect` (lookup only for now; Task 5 adds the Google revoke).
- [ ] **Step 5:** Commit `feat(store): add sqlite store with encrypted google tokens`.

### Task 4: Contact model and enrich rules

**Files:** Create `internal/contacts/contact.go`, `internal/contacts/merge.go`, `internal/contacts/errors.go`; Test `internal/contacts/contact_test.go`, `internal/contacts/merge_test.go`.

**Interfaces:**
- Produces:
  ```go
  type Typed struct{ Value, Type string }
  type Address struct{ Formatted, Street, City, Region, PostalCode, Country, Type string }
  type Contact struct {
      ResourceName, Etag                     string
      GivenName, FamilyName, DisplayName     string
      Company, JobTitle, Notes               string
      Emails, Phones, Websites               []Typed // LinkedIn = Websites{Type:"linkedin"}
      Addresses                              []Address
      Groups                                 []string // contactGroups/... resource names
  }
  type Group struct{ ResourceName, Name, Type string; MemberCount int } // Type: "user" | "system"
  func FromPerson(p *people.Person) Contact
  func (c Contact) ToPerson() *people.Person
  func (c Contact) UpdateFields() []string // People API updatePersonFields for non-empty fields
  func Enrich(existing, incoming Contact) (merged Contact, changed []string)
  func NormalizeEmail(s string) string
  var ErrNotFound, ErrConflict, ErrUpstreamAuth error
  type RateLimitedError struct{ RetryAfter time.Duration }
  type People interface {
      Search(ctx context.Context, query string, limit int) ([]Contact, error)
      List(ctx context.Context, pageSize int, pageToken, sortOrder string) ([]Contact, string, error)
      Get(ctx context.Context, resourceName string) (Contact, error)
      Create(ctx context.Context, c Contact) (Contact, error)
      Update(ctx context.Context, c Contact, fields []string) (Contact, error) // c.Etag required
      Delete(ctx context.Context, resourceName string) error
      ListGroups(ctx context.Context) ([]Group, error)
      CreateGroup(ctx context.Context, name string) (Group, error)
      ModifyMembers(ctx context.Context, group string, add, remove []string) error
  }
  ```
- `ToPerson` sets `Metadata.Sources` from `Etag` when set (People API returns 400 without it on update).
- Enrich rules (spec §8.1): single-valued (`GivenName`, `FamilyName`, `Company`, `JobTitle`, `Notes`) set only when existing is empty; multi-valued appended when not present. Equality: e-mails by `NormalizeEmail`; phones by digits only (keep leading `+` significance: compare digit strings); websites lowercase without scheme, `www.`, trailing `/`; addresses by `Formatted` lowercased with whitespace collapsed. `changed` lists People field names (`names`, `organizations`, `biographies`, `emailAddresses`, `phoneNumbers`, `urls`, `addresses`), deduplicated, sorted.

- [ ] **Step 1: Failing tests:** `TestFromPerson_Full` (every field mapped, LinkedIn url type kept); `TestFromPerson_EmptyPerson` (`&people.Person{}` gives zero `Contact`, no panic); `TestToPerson_RoundTrip`; `TestToPerson_SetsEtagSource`; table `TestEnrich` with cases: empty existing gets all; non-empty `JobTitle` kept; new phone `+33 6 12 34 56 78` vs existing `+33612345678` unchanged; new mobile appended keeps old; URL `https://www.linkedin.com/in/x/` equals `linkedin.com/in/x`; e-mail `A@B.com ` equals `a@b.com`; nothing new gives `changed == nil`. `TestEnrich_NeverRemoves` (incoming with fewer values keeps existing list intact).
- [ ] **Step 2:** Run, FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** PASS with `-race`.
- [ ] **Step 5:** Commit `feat(contacts): add contact model and enrich rules`.

### Task 5: Google upstream and People client

**Files:** Create `internal/google/upstream.go`, `internal/google/people.go`, `internal/google/fake_test.go` (fake People + token endpoint on `httptest`); Test `internal/google/people_test.go`, `internal/google/upstream_test.go`.

**Interfaces:**
- Consumes: `*store.Store`, `contacts.People`, `contacts` errors.
- Produces:
  - `google.NewUpstream(cfg UpstreamConfig, st *store.Store, httpClient *http.Client) *Upstream`; `UpstreamConfig{ClientID, ClientSecret, RedirectURL string; Endpoint oauth2.Endpoint; PeopleBaseURL, RevokeURL string}` (overridable for tests).
  - `(*Upstream).AuthCodeURL(state, codeVerifier string) string` (scopes per Global Constraints, `access_type=offline`, `prompt=consent`, S256).
  - `(*Upstream).Exchange(ctx, code, codeVerifier string) (*oauth2.Token, error)`.
  - `(*Upstream).People(ctx, userID string) (contacts.People, error)`: token source reads the stored token, refreshes when expired, saves the new token; a per-user `singleflight` ensures one refresh at a time. On `invalid_grant`: `st.DeleteUser`, return `contacts.ErrUpstreamAuth`.
  - `(*Upstream).Revoke(ctx, refreshToken string) error`.
- People client: `personFields` = `names,emailAddresses,phoneNumbers,organizations,urls,addresses,biographies,memberships,metadata`. Search: before a user's first search in this process, one warmup call with empty query (tracked in a `sync.Map`); `readMask` same fields; `pageSize` capped at 30 (API max). Error mapping: 404 to `ErrNotFound`; 400 with reason `failedPrecondition` to `ErrConflict`; 429 to `*RateLimitedError` (parse `Retry-After`, default 30 s); 401 to `ErrUpstreamAuth`; others wrapped with status. Reads retry once on 5xx; writes never retry.
- Group modify: `ModifyMembers` resolves `group` per spec §8.1 (resource name as-is; else case-insensitive match over user groups; zero or several matches is an error naming the count).

- [ ] **Step 1: Failing tests:** `TestSearch_WarmupOncePerUser` (fake counts empty-query calls: 1 after 3 searches); `TestErrorMapping` table (404, 400 failedPrecondition, 429 with `Retry-After: 7`, 401, 503 retried once for Get, not for Create); `TestList_Pagination` (returns next token); `TestUpdate_SendsEtagAndFields`; `TestModifyMembers_ByName` (exact, case-insensitive, system group ignored, ambiguous, missing); `TestTokenSource_RefreshPersists`; `TestTokenSource_ConcurrentRefreshOnce` (20 goroutines, fake token endpoint hit once); `TestTokenSource_InvalidGrantDeletesUser`; `TestMalformedJSON` and `TestSlowResponse` (ctx timeout returns error, no hang).
- [ ] **Step 2:** Run, FAIL.
- [ ] **Step 3:** Implement with `google.golang.org/api/people/v1` and `option.WithEndpoint` + `option.WithHTTPClient`.
- [ ] **Step 4:** PASS with `-race`. Wire `disconnect` subcommand: lookup, `Revoke`, `DeleteUser`; revoke failure logs a warning and still deletes.
- [ ] **Step 5:** Commit `feat(google): add people client and per-user token source`.

### Task 6: Per-user limits

**Files:** Create `internal/limits/limits.go`; Test `internal/limits/limits_test.go`.

**Interfaces:**
- Produces: `limits.New(readsPerMin, writesPerMin int) *Limiter`; `(*Limiter).Allow(userID string, write bool) (ok bool, retryAfter time.Duration)`; `(*Limiter).WriteLock(userID string) (unlock func())`. Buckets via `rate.Limiter` with burst = per-minute value / 6 (min 1); idle entries evicted after 10 minutes by a janitor started with `(*Limiter).Run(ctx)`.

- [ ] **Step 1: Failing tests:** `TestAllow_SeparateBuckets`; `TestAllow_PerUserIsolation`; `TestAllow_RetryAfterPositive`; `TestWriteLock_Serializes` (two goroutines, overlap counter stays 1); `TestEviction`.
- [ ] **Step 2:** FAIL. **Step 3:** Implement. **Step 4:** PASS `-race`.
- [ ] **Step 5:** Commit `feat(limits): add per-user rate limit and write lock`.

### Task 7: fosite storage adapter

**Files:** Create `internal/store/oauth.go`; Test `internal/store/oauth_test.go`.

**Interfaces:**
- Produces `*Store` methods satisfying `fosite.ClientManager`, `oauth2.CoreStorage`, `oauth2.TokenRevocationStorage`, `pkce.PKCERequestStorage` (copy method sets from fosite v0.49.0 `storage.MemoryStore`). Plus:
  - `SaveClient(ctx, c *OAuthClient) error`, `ClientByID(ctx, id string) (*OAuthClient, error)`; `type OAuthClient struct { ID, Kind, Name string; RedirectURIs []string; ExpiresAt time.Time }` implementing `fosite.Client` (public, grant types code+refresh, response type code, scope `contacts`).
  - `SavePending(ctx, id string, data []byte, ttl time.Duration) error`, `TakePending(ctx, id string) ([]byte, error)` (delete-on-read; expired returns `ErrNotFound`).
  - `DeleteExpired(ctx, now time.Time) (int, error)`, `PurgeUnusedClients(ctx, before time.Time) (int, error)` (DCR and stale CIMD cache).
- Sessions serialized as JSON of `fosite.Request` with `*fosite.DefaultSession`; `Subject` = our user id, also written to `oauth_tokens.user_id` for cascade. Signatures stored, never tokens (fosite passes signatures already).
- `RevokeRefreshToken` and `RevokeAccessToken` mark all rows of the `request_id` inactive (family revocation).

- [ ] **Step 1: Failing tests:** run fosite's own storage expectations where practical; at minimum `TestAuthorizeCode_InvalidateThenGetReturnsErrInvalidatedAuthorizeCode`; `TestRefresh_RevokeFamily`; `TestPKCE_CreateGetDelete`; `TestPending_TakeOnce`; `TestPending_Expired`; `TestDeleteExpired`; `TestDeleteUser_RemovesTokens`.
- [ ] **Step 2:** FAIL. **Step 3:** Implement. **Step 4:** PASS `-race`.
- [ ] **Step 5:** Commit `feat(store): add fosite storage adapter`.

### Task 8: Clients: CIMD fetcher and DCR

**Files:** Create `internal/oauth/cimd.go`, `internal/oauth/clients.go`; Test `internal/oauth/cimd_test.go`, `internal/oauth/clients_test.go`.

**Interfaces:**
- Consumes: `store.OAuthClient`, `SaveClient`, `ClientByID`.
- Produces: `oauth.NewCIMDFetcher(opts CIMDOptions) *CIMDFetcher`; `CIMDOptions{Timeout time.Duration /*5s*/; MaxBytes int64 /*64 KiB*/; MaxCache time.Duration /*24h*/; AllowPrivate bool /*tests only*/}`; `(*CIMDFetcher).Fetch(ctx, clientID string) (*store.OAuthClient, time.Time, error)`.
  `oauth.NewClientResolver(st *store.Store, f *CIMDFetcher) *ClientResolver` with `GetClient(ctx, id string) (fosite.Client, error)`: `https://` ids go through the cache then the fetcher; others look up DCR rows.
  `(*ClientResolver).RegisterHandler() http.Handler` (RFC 7591; only `token_endpoint_auth_method` `none`; requires `redirect_uris`; returns 201 with `client_id`).
  `oauth.RedirectMatches(registered []string, got string) bool`: exact match, except loopback (`127.0.0.1`, `localhost`, `[::1]`) ignores port.
- SSRF guard: custom `net.Dialer.Control` rejecting loopback, private, link-local, CGNAT, multicast and unspecified addresses after resolution; `CheckRedirect` re-applies; HTTPS only; the document's `client_id` must equal the URL.

- [ ] **Step 1: Failing tests:** `TestFetch_OK`; `TestFetch_RejectsPrivateIP` (`https://127.0.0.1/...`); `TestFetch_RejectsRedirectToPrivate`; `TestFetch_Oversize`; `TestFetch_Timeout`; `TestFetch_ClientIDMismatch`; `TestFetch_HTTPScheme`; `TestFetch_CacheControlCappedAt24h`; `TestRedirectMatches` table (loopback any port, exact otherwise, path mismatch rejected); `TestRegister_OK`, `TestRegister_RejectsConfidential`, `TestGetClient_UnknownNonURLID` (returns `fosite.ErrNotFound`).
- [ ] **Step 2:** FAIL. **Step 3:** Implement. **Step 4:** PASS `-race`.
- [ ] **Step 5:** Commit `feat(oauth): add CIMD fetcher and dynamic client registration`.

### Task 9: OAuth provider and flow

**Files:** Create `internal/oauth/provider.go`, `internal/oauth/metadata.go`, `internal/oauth/flow.go`, `internal/oauth/token.go`, `internal/oauth/templates/consent.html`, `internal/oauth/templates/error.html`; Test `internal/oauth/flow_test.go` (end-to-end with a fake Google: token endpoint + ID token signed by a test key + JWKS).

**Interfaces:**
- Consumes: `*store.Store`, `*ClientResolver`, `*google.Upstream`, `*crypto.Keyring`, `config.Config`.
- Produces: `oauth.New(cfg Config, deps Deps) (*Server, error)`; `(*Server).Mount(mux *http.ServeMux)` mounting the paths of spec §5.1 (register only when DCR on); `(*Server).Verifier() auth.TokenVerifier` for go-sdk (introspects with fosite, checks audience `<base-url>/mcp` and scope `contacts`, returns `*auth.TokenInfo` with `Extra["uid"]`); `oauth.UserID(ctx context.Context) (string, bool)` reading that value.
- fosite config: `compose.Compose` with `OAuth2AuthorizeExplicitFactory`, `OAuth2RefreshTokenGrantFactory`, `OAuth2TokenRevocationFactory`, `OAuth2TokenIntrospectionFactory`, `OAuth2PKCEFactory`; `EnforcePKCE: true`, `EnablePKCEPlainChallengeMethod: false`, lifespans per Global Constraints, `GlobalSecret`/`RotatedGlobalSecrets` from `kr.HMACSecrets("fosite-hmac")`, `ScopeStrategy: fosite.ExactScopeStrategy`.
- Flow (spec §6.3): `resource` param must equal `<base-url>/mcp` when present, then `ar.GrantAudience` it (absent `resource` grants it too, so legacy clients work). Pending data = serialized authorize request + Google PKCE verifier + CSRF token. Consent page cookie `gcmcp_csrf`, `HttpOnly`, `Secure` (unless base URL is http localhost), `SameSite=Lax`, 10 min. Callback: verify ID token with `go-oidc` (`aud` = Google client id, `email_verified`); check granted scopes contain contacts (from token response `scope`); domain check (`hd` claim or e-mail domain in `AllowedDomains`); Google `error=access_denied` shows the early-access message with `OperatorEmail`. Then `UpsertUser`, `SaveGoogleToken`, `NewAuthorizeResponse` with session subject = user id, and append `iss=<base-url>` to the redirect.
- Token endpoint: form-encoded only; `/oauth/revoke` revokes our tokens only (spec §7).
- Metadata JSON exactly per spec §6.1.

- [ ] **Step 1: Failing tests:** `TestMetadata_Fields` (asserts every key/value of §6.1, no `registration_endpoint` when DCR off); `TestFlow_HappyPath` (authorize, consent POST, callback, token; access token works with `Verifier`; redirect has `iss`); `TestFlow_RefreshRotationAndReuse` (second use of old refresh token fails and kills the new one); `TestFlow_PKCERequired` and `TestFlow_PlainPKCERejected`; `TestFlow_WrongResource`; `TestFlow_ContactsScopeNotGranted` (error page text "access to Contacts is required"); `TestFlow_DomainRefused`; `TestFlow_AccessDeniedShowsEarlyAccess`; `TestFlow_CSRFMismatch`; `TestFlow_PendingExpired`; `TestConsentPage_EscapesClientName` (`client_name` `<script>x</script>` rendered escaped; redirect host inside `<strong>`); `TestRevoke_KeepsGoogleToken`; `TestVerifier_WrongAudience`.
- [ ] **Step 2:** FAIL. **Step 3:** Implement. **Step 4:** PASS `-race`.
- [ ] **Step 5:** Commit `feat(oauth): add fosite authorization server and google login flow`.

### Task 10: MCP server and tools

**Files:** Create `internal/tools/server.go`, `internal/tools/handlers.go`, `internal/tools/upsert.go`; Test `internal/tools/handlers_test.go`, `internal/tools/upsert_test.go`, `internal/tools/protocol_test.go`.

**Interfaces:**
- Consumes: `contacts.People`, `*google.Upstream` (as `PeopleFactory interface{ People(ctx, userID string) (contacts.People, error) }`), `*limits.Limiter`, `oauth.UserID`, `(*oauth.Server).Verifier()`.
- Produces: `tools.NewHandler(deps Deps) http.Handler` = `auth.RequireBearerToken(verifier, opts)` wrapping `mcp.NewStreamableHTTPHandler(..., &mcp.StreamableHTTPOptions{Stateless: true})`. `ServerOptions.SupportedProtocolVersions` includes `2026-07-28`, `2025-11-25`, `2025-06-18`. Bearer failure header per spec §5.2.
- Tools and input schemas per spec §8 (typed Go structs with `jsonschema` tags via `mcp.AddTool`). Annotations: read tools `ReadOnlyHint: true`; `delete_contact` `DestructiveHint: ptr(true)`; other writes `DestructiveHint: ptr(false)`. Every tool returns structured output + a one-line text summary.
- Middleware per call, in order: resolve user id; `Store.TouchUser`; `Limiter.Allow` (rejection returns tool error `rate limited, retry after Ns`); for writes `WriteLock`; call; map errors (`ErrNotFound` "contact not found", `ErrConflict` "contact changed, re-read it", `*RateLimitedError` as above, `ErrUpstreamAuth` "Google access was revoked; reconnect this connector": the user rows are already deleted, so the client's next request gets 401 and restarts OAuth). Logging and metrics hooks are added in Task 11.
- `list_contacts`: `page_size` default 100, clamp to 1..1000; `sort` in {`FIRST_NAME_ASCENDING`, `LAST_NAME_ASCENDING`, `LAST_MODIFIED_DESCENDING`}; bad page token from Google (400 invalid argument) maps to tool error "invalid page_token".
- `update_contact`: `Get` (fresh etag) then `Update` with only provided fields.
- `upsert_contact_by_email` algorithm per spec §8.1; result `{status: created|enriched|unchanged|ambiguous, contact?, candidates?, changed_fields?}`; group membership ensured except `ambiguous`.

- [ ] **Step 1: Failing tests** (fake `contacts.People` in memory):
  - `TestUpsert_Created`, `TestUpsert_Enriched` (asserts `changed_fields`), `TestUpsert_Unchanged`, `TestUpsert_Ambiguous` (no writes, 2 candidates), `TestUpsert_MatchesEmailCaseInsensitive`, `TestUpsert_SearchPrefixFalsePositiveIgnored` (search returns `a@b.co` and `a@b.com`; only exact match counts), `TestUpsert_GroupAdded`.
  - `TestUpdateContact_OnlyProvidedFields`, `TestUpdateContact_Conflict`, `TestDeleteContact`, `TestListContacts_PageSizeClamp` (0 -> 100, -5 -> 100, 5000 -> 1000), `TestListContacts_BadPageToken`, `TestRateLimitedToolError`, `TestUpstreamAuthMessage`.
  - `TestToolAnnotations` (list tools via an in-memory client; check every hint).
  - Protocol tests over `httptest` with a fake verifier: `TestLegacyInitialize_20250618`, `TestLegacyInitialize_20251125` (initialize then `tools/call` `search_contacts` succeed), `TestStateless_20260728` (`tools/call` with `_meta` protocol version, no initialize), `TestNoBearer_401WithResourceMetadata`.
- [ ] **Step 2:** FAIL. **Step 3:** Implement. **Step 4:** PASS `-race`.
- [ ] **Step 5:** Commit `feat(tools): add MCP server and contact tools`.

### Task 11: Logging and metrics

**Files:** Create `internal/obs/metrics.go`; Modify `internal/tools/server.go` (middleware); Test `internal/obs/metrics_test.go`, `internal/tools/logging_test.go`.

**Interfaces:**
- Produces: `obs.NewMetrics(reg prometheus.Registerer) *Metrics` with `ToolCall(tool, status string, d time.Duration)`, `GoogleCall(method string, code int, d time.Duration)`, `RateLimited(kind string)`, `Grant(kind string)`. Metric names prefixed `gcmcp_`. Labels only `tool`, `status`, `method`, `code`, `kind`.
- Tool log line at `info`: msg `tool call`, attrs `tool`, `user_id`, `duration_ms`, `status`, `google_status`, `result_count`. At `debug` add `args` and `result`. `serve` at `debug` logs once at start: `warn` "debug log level writes personal data (tool arguments and results) to logs".

- [ ] **Step 1: Failing tests:** `TestToolLog_InfoHasNoPayload` (capture `slog` JSON handler; assert no `args`/`result` keys and no e-mail substring from the fake contact); `TestToolLog_DebugHasPayload`; `TestMetrics_NoUserLabels` (gather; no label named `user*` or `client*`).
- [ ] **Step 2:** FAIL. **Step 3:** Implement; also count Google calls inside `google.people` via an injected hook. **Step 4:** PASS `-race`.
- [ ] **Step 5:** Commit `feat(obs): add tool call logs and prometheus metrics`.

### Task 12: HTTP assembly, pages, jobs and `serve`

**Files:** Create `internal/web/server.go`, `internal/web/templates/home.html`, `internal/web/templates/privacy.html`, `internal/jobs/jobs.go`; Modify `cmd/google-contacts-mcp/main.go`; Test `internal/web/server_test.go`, `internal/jobs/jobs_test.go`.

**Interfaces:**
- Consumes: everything above.
- Produces: `web.New(cfg config.Config, oauthSrv *oauth.Server, mcpHandler http.Handler, metricsHandler http.Handler) http.Handler`; `jobs.Run(ctx, st *store.Store, up *google.Upstream, now func() time.Time)` (hourly `DeleteExpired`, daily `PurgeUnusedClients(180d)` and inactive users: `Revoke` then `DeleteUser`).
- Routing: `/mcp`, OAuth paths, `/`, `/privacy`, and when `--metrics` without `--metrics-listen`, the metrics path behind Basic auth (`subtle.ConstantTimeCompare`). With `--metrics-listen`, a second listener serves only the metrics path. Disabled metrics path and unknown paths return the identical 404 body.
- Home page: what it is, repo link, connect instructions (`<base-url>/mcp`), early-access line with `OperatorEmail`. Privacy page: the stored / never-stored lists of spec §10 and §14 tutorial 1, operator name and e-mail, deletion paths.
- `serve`: load config, open store, build everything, `http.Server` with `ReadHeaderTimeout` 10 s, graceful shutdown on SIGINT/SIGTERM (drain 15 s).

- [ ] **Step 1: Failing tests:** `TestMetrics_DisabledSame404AsUnknown`; `TestMetrics_BasicAuth` (no creds 401, wrong 401, right 200); `TestMetrics_SeparateListenerNotOnMain`; `TestPages_ShowOperator`; `TestPrivacy_ListsStoredData` (contains "e-mail address" and "never stored"); `TestJobs_PurgesInactiveAndRevokes` (fake clock, fake revoke endpoint hit once).
- [ ] **Step 2:** FAIL. **Step 3:** Implement. **Step 4:** PASS `-race`; manual smoke: `GCMCP_...=... go run ./cmd/google-contacts-mcp serve --base-url http://localhost:8080 ...`, then `curl -s localhost:8080/.well-known/oauth-authorization-server | jq .issuer` prints the base URL.
- [ ] **Step 5:** Commit `feat: wire http server, pages and background jobs`.

### Task 13: Packaging and deployment

**Files:** Create `Dockerfile`, `deploy/litestream.yml`, `deploy/start.sh`, `deploy/clevercloud-prebuild.sh`, `.goreleaser.yaml`, `.github/workflows/release.yml`.

**Interfaces:**
- `deploy/start.sh`: when `LITESTREAM_REPLICA_URL` is set, `litestream restore -if-db-not-exists -if-replica-exists -config deploy/litestream.yml "$GCMCP_DB_PATH"` then `exec litestream replicate -config deploy/litestream.yml -exec "google-contacts-mcp serve"`; else `exec google-contacts-mcp serve`. `set -eu`.
- `litestream.yml`: one db at `${GCMCP_DB_PATH}`, replica `url: ${LITESTREAM_REPLICA_URL}`, `endpoint: ${LITESTREAM_ENDPOINT}`, credentials from `LITESTREAM_ACCESS_KEY_ID` / `LITESTREAM_SECRET_ACCESS_KEY` (Cellar).
- `clevercloud-prebuild.sh`: download a pinned Litestream release (version in a variable with a comment) for linux/amd64, verify its SHA-256, install to `$APP_HOME/bin`.
- Dockerfile: multi-stage, `golang:1.25` builder, `CGO_ENABLED=0`; runtime `debian:stable-slim` with the same pinned Litestream and `deploy/` files; non-root user; `ENTRYPOINT ["/app/deploy/start.sh"]`.
- goreleaser: linux and darwin, amd64 and arm64, ldflags `-s -w -X main.version={{.Version}}`, ghcr.io image.

- [ ] **Step 1:** `shellcheck deploy/*.sh` clean.
- [ ] **Step 2:** `docker build .` succeeds; `docker run --rm <img> google-contacts-mcp version` prints a version. (Local check only; never used for the dev environment escape.)
- [ ] **Step 3:** `goreleaser check` passes.
- [ ] **Step 4:** Commit `build: add docker image, litestream wrapper and release config`.

### Task 14: Documentation

**Files:** Create `README.md`, `docs/ROADMAP.md`, `CLAUDE.md`.

- README: pitch, quick start, flag table (spec §13), "what is stored" table, the five tutorials and the verbatim sync prompt (spec §14), known limits (search lag after writes; Testing mode 7-day re-consent; 100-user cap).
- `docs/ROADMAP.md`: items listed in spec §14.
- `CLAUDE.md`: project purpose, package map, gates (the four commands), domain jargon (CIMD, DCR, PKCE, RFC 9207 `iss`, RFC 8707 `resource`, People API `resourceName`, etag, Testing mode), link to spec and plan.

- [ ] **Step 1:** `npx prettier --check README.md docs/ROADMAP.md CLAUDE.md` (run `--write` first on these new files).
- [ ] **Step 2:** Check every flag in `config.go` appears in the README table (`grep -o -- '--[a-z-]*' internal/config/config.go | sort -u` versus README).
- [ ] **Step 3:** Commit `docs: add readme, roadmap and agent guide`.

---

## Self-review notes

- Spec §7 "return 401 on the MCP request" on `invalid_grant` is implemented as: delete the user's rows (which kills our tokens), return a tool error telling the user to reconnect; the client's next request gets 401 and restarts OAuth. A tool handler cannot change the HTTP status of a response already in flight.
- Spec §6.2 Claude 10 s timeout: CIMD fetch timeout 5 s keeps a cold `/oauth/authorize` within budget.
