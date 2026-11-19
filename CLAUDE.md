# google-contacts-mcp

## Purpose

- Remote, multi-tenant MCP server exposing a user's Google Contacts to Claude and ChatGPT.
- OAuth 2.1 authorization server toward MCP clients (ory/fosite); confidential OAuth client toward Google (People API v1).
- One Go binary, one SQLite file, exactly one instance. Contact data never touches disk.
- Module `github.com/samber/google-contacts-mcp`, binary `google-contacts-mcp`, `go 1.25` in `go.mod`, `CGO_ENABLED=0`.

## Package map

- `cmd/google-contacts-mcp`: subcommands `serve`, `genkey`, `rekey`, `disconnect`, `version`; wiring.
- `internal/config`: flags plus `GCMCP_*` env, per-command required values.
- `internal/contacts`: contact model, People API mapping, enrich/merge rules, typed errors.
- `internal/crypto`: rotatable AES-256-GCM keyring (`keyid:base64`).
- `internal/google`: Google OAuth exchange, per-user token source, People API client.
- `internal/jobs`: hourly expired-row cleanup, daily 180-day inactive-user purge.
- `internal/limits`: per-user read/write token buckets, per-user write lock.
- `internal/oauth`: authorization server: metadata, CIMD, DCR, consent, Google callback, token, revoke.
- `internal/obs`: Prometheus metrics, per-call tool logging.
- `internal/store`: SQLite store, embedded migrations, fosite storage, users and encrypted Google tokens.
- `internal/tools`: MCP server and the ten contact tools.
- `internal/web`: HTTP mux, `/` and `/privacy` pages, metrics basic auth.
- `deploy/`: Litestream config, start script, Clever Cloud pre-build hook.

## Gates

Run all four before every commit:

```sh
gofmt -l .            # must print nothing
go vet ./...
golangci-lint run
go test -race ./...
```

## Domain jargon

- **CIMD**: Client ID Metadata Document. `client_id` is an `https://` URL; server fetches the JSON (SSRF guard, 5 s, 64 KiB, cached up to 24 h).
- **DCR**: Dynamic Client Registration (RFC 7591), `POST /oauth/register`, public clients only, `--enable-dcr`.
- **PKCE**: Proof Key for Code Exchange. S256 only, toward clients and toward Google.
- **RFC 9207 `iss`**: issuer parameter added to every authorization response.
- **RFC 8707 `resource`**: must equal `<base-url>/mcp`; tokens get that audience.
- **RFC 9728**: protected-resource metadata at `/.well-known/oauth-protected-resource` and `/.well-known/oauth-protected-resource/mcp`.
- **People API `resourceName`**: contact id (`people/c123`); groups are `contactGroups/abc`.
- **CONTACT source etag**: etag of the person's `CONTACT` source; updates must send the current one, stale etag = `failedPrecondition`.
- **Testing mode**: unverified Google app; max 100 test users, Google refresh tokens expire 7 days after consent.
- **Litestream**: replicates the SQLite file to S3 (Cellar); `deploy/start.sh` restores at boot, then runs the server under `litestream replicate -exec`.

## Conventions

- Never log contact data, tool arguments or results above `debug`. Never log tokens, codes, secrets or Google response bodies.
- No magic numbers or strings inline: named constants, each with a one-line "why" comment.
- Never ignore an error. Structured `slog` logging.
- User deletion runs the explicit `deleteUserChildren` list (`internal/store/users.go`); add every new per-user table to it.
- Flags must stay in sync with the README configuration table.
- Conventional Commits.

## References

- Design spec: `docs/superpowers/specs/2026-11-19-google-contacts-mcp-design.md`
- Implementation plan: `docs/superpowers/plans/2026-11-19-google-contacts-mcp.md`
- Roadmap: `docs/ROADMAP.md`
