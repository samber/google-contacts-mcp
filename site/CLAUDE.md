# site

## Purpose

- Static Astro website of the hosted google-contacts-mcp: landing page, privacy policy, terms of service.
- Plain CSS, no Tailwind, no UI framework. One page script: the copy button.
- Design: an index card (white stock, blue ruled lines, red header rule, yellow highlighter). Spec: `docs/superpowers/specs/2026-11-20-landing-site-design.md`.

## Domains

| Role                     | URL                                                   |
| ------------------------ | ----------------------------------------------------- |
| Website                  | `https://google-contacts-mcp.samber.dev`              |
| MCP server, OAuth issuer | `https://mcp.google-contacts-mcp.samber.dev` (`/mcp`) |

## Commands

Run from `site/`. Set `ASTRO_TELEMETRY_DISABLED=1` (no tracking; the sandbox also blocks the telemetry directory).

```sh
npm run check    # astro check (types)
npm test         # unit tests of the page scripts
npm run build    # static build into dist/
npm run verify   # contrast of the tokens + the dist gate (run after build)
node scripts/og.mjs  # regenerate public/og.png by hand when the design changes
```

## Gates

Run `check`, `test`, `build`, `verify` before every commit that touches `site/`.

- `scripts/check-contrast.mjs`: WCAG contrast of the palette tokens, light and dark.
- `scripts/check-dist.mjs`: built pages, canonical URLs, one `h1`, footer links, no external host, JSON-LD, sitemap, `robots.txt`, `llms.txt`, `og.png`, script under 1 KB gzipped, tool list equal to `internal/tools`, privacy and terms requirements.
- Lighthouse on `/` and `/privacy` is a manual pre-launch check: target above 90 on all four scores.

## Rules

- No analytics, no cookies, no third-party request: the privacy policy says so.
- Facts on the site must match the code: tools (`internal/tools`), what is stored (README, `internal/store`), retention (`internal/jobs`).
- Example data is fictional. Never the owner's or a real person's data.
- Never say the service is open to everyone: Google testing mode limits who can connect.
- Edit `LEGAL_UPDATED` in `src/data/site.ts` whenever `/privacy` or `/terms` changes.
- The rate limits in `/terms` (50 reads, 20 writes per user per minute) must equal `GCMCP_RATE_LIMIT_READS` and `GCMCP_RATE_LIMIT_WRITES` on the deployed server; the server defaults are 120 and 30.
- Build output is `build.format: 'file'`, `trailingSlash: 'never'`: the host must serve `/privacy` from `privacy.html` and `/terms` from `terms.html`. `python3 -m http.server` does not; use the `.html` URLs locally.

## Status

- Hosting not chosen. Deploy only once the server answers on the `mcp.` host, so the connector URL in the hero works.
- The Go server still serves its own `/` and `/privacy`. Replacing them (redirect `/`, remove `/privacy`) is a separate piece of work, after this site is live.
