# google-contacts-mcp

## Purpose

- Remote, multi-tenant MCP server exposing user's Google Contacts to Claude and ChatGPT.
- OAuth 2.1 authorization server toward MCP clients (ory/fosite); confidential OAuth client toward Google (People API v1).
- One Go binary, one SQLite file, exactly one instance. Contact data never touches disk.
- Module `github.com/samber/google-contacts-mcp`, binary `google-contacts-mcp`, `go 1.26` in `go.mod`, `CGO_ENABLED=0`.

## Package map

- `cmd/google-contacts-mcp`: subcommands `serve`, `genkey`, `rekey`, `disconnect`, `version`; wiring.
- `internal/config`: flags plus `GCMCP_*` env, per-command required values.
- `internal/contacts`: contact model, People API mapping, enrich/merge rules, typed errors.
- `internal/crypto`: rotatable AES-256-GCM keyring (`keyid:base64`).
- `internal/google`: Google OAuth exchange, per-user token source, People API client.
- `internal/jobs`: per-minute flush of batched `last_seen_at` (`TouchUser` only writes memory; `Store.Close` flushes too), hourly expired-row cleanup, daily 180-day inactive-user purge (guarded against reconnects), purge of clients unused 180 days or never used after 24 h.
- `internal/limits`: per-user read/write token buckets, per-user write lock.
- `internal/oauth`: authorization server: metadata, CIMD, DCR, consent, Google callback, token, revoke.
- `internal/obs`: Prometheus metrics, per-call tool logging.
- `internal/store`: SQLite store, embedded migrations, fosite storage, users and encrypted Google tokens.
- `internal/tools`: MCP server and ten contact tools.
- `internal/web`: HTTP mux (MCP, OAuth), `GET /` redirect to `--site-url`, metrics basic auth. Renders no page.
- `site/`: Astro website (landing page, privacy policy, terms of service) for the hosted instance; own gates and rules in `site/CLAUDE.md`.

## Gates

Run all four before every commit:

```sh
gofmt -l .            # must print nothing
go vet ./...
golangci-lint run
go test -race ./...
```

## CI and automation

- `.github/workflows/ci.yml`: runs the four gates on push to `main` and on every PR. No CI job builds or checks `site/`.
- `.github/workflows/release.yml`: on a `v*` tag, reruns the gates, then GoReleaser publishes archives and a `ghcr.io` image (amd64, arm64).
- `.github/dependabot.yml`: Actions and Docker monthly, Go modules weekly, `site/` npm monthly (patch updates ignored, `astro` + `@astrojs/*` grouped). It follows the `samber/ro` config.
- `.github/FUNDING.yml`: GitHub Sponsors (`samber`) and Ko-fi, same as `samber/ro` and `samber/lo`.
- Dependabot does not bump the pinned tool versions in the workflows (`golangci-lint` `version:`, `goreleaser` `version:`). Bump them by hand, in both workflows.

## Domain jargon

- **CIMD**: Client ID Metadata Document. `client_id` is `https://` URL; server fetches JSON (SSRF guard, 5 s, 64 KiB, cached up to 24 h, served stale max 7 days, dropped on 404/410/400).
- **DCR**: Dynamic Client Registration (RFC 7591), `POST /oauth/register`, public clients only, `--enable-dcr`.
- **PKCE**: Proof Key for Code Exchange. S256 only, toward clients and toward Google.
- **RFC 9207 `iss`**: issuer parameter added to every authorization response.
- **RFC 8707 `resource`**: must equal `<base-url>/mcp`; tokens get that audience.
- **RFC 9728**: protected-resource metadata at `/.well-known/oauth-protected-resource` and `/.well-known/oauth-protected-resource/mcp`.
- **People API `resourceName`**: contact id (`people/c123`); groups are `contactGroups/abc`. Tools accept only these two shapes.
- **CONTACT source etag**: etag of person's `CONTACT` source; updates must send current one, stale etag = `failedPrecondition`.
- **Testing mode**: unverified Google app; max 100 test users, Google refresh tokens expire 7 days after consent.
- **Network volume**: the SQLite file may sit on a mounted network filesystem, so journal mode is DELETE, not WAL. No replication or backup ships with the project.

## Conventions

- Never log contact data, tool arguments or results above `debug`. Never log tokens, codes, secrets or Google response bodies.
- No magic numbers or strings inline: named constants, each with one-line "why" comment.
- Never ignore error. Structured `slog` logging.
- User deletion runs explicit `deleteUserChildren` list (`internal/store/users.go`); add every new per-user table to it.
- Flags must stay in sync with the configuration table in `docs/self-hosting.md`.
- Conventional Commits.

## References

- Design spec: `docs/superpowers/specs/2026-11-19-google-contacts-mcp-design.md`
- Roadmap: GitHub issues labelled `enhancement`
