// Gate over the built site: run `npm run build` first, then `node scripts/check-dist.mjs`.
// Exits 1 and prints every failure when the build breaks a rule of the design spec.
import { existsSync, readFileSync, readdirSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { gzipSync } from 'node:zlib';

const {
  SITE_URL,
  MCP_URL,
  REPO_URL,
  OPERATOR_EMAIL,
  LEGAL_UPDATED = '',
  TOOLS = [],
  STORED = [],
  NEVER_STORED = [],
} = await import('../src/data/site.ts');

// Go sources registering the MCP tools; the site's tool list must match them.
const GO_TOOLS_DIR = fileURLToPath(new URL('../../internal/tools/', import.meta.url));
const GO_TOOL_NAME = /Name:\s+"([a-z_]+)"/g;

const DIST = fileURLToPath(new URL('../dist/', import.meta.url));

// Pages the build must produce, with their canonical path. The 404 page is
// not canonical anywhere: it must be marked noindex instead.
const PAGES = [
  { file: 'index.html', path: '/' },
  { file: 'privacy.html', path: '/privacy' },
  { file: 'terms.html', path: '/terms' },
  { file: '404.html', path: null },
];

// Hosts the pages may link to. Anything else is a third-party request or an
// unreviewed outbound link.
const ALLOWED_HOSTS = new Set([
  new URL(SITE_URL).host,
  'github.com',
  'developers.google.com',
  'myaccount.google.com',
  'modelcontextprotocol.io',
  // The French data protection authority, named in the privacy policy.
  'www.cnil.fr',
]);

// Google requires this exact sentence in the privacy policy of apps that use its APIs.
const LIMITED_USE_SENTENCE =
  "This service's use and transfer of information received from Google APIs adheres to the Google API Services User Data Policy, including the Limited Use requirements.";
const GOOGLE_POLICY_URL = 'https://developers.google.com/terms/api-services-user-data-policy';

// Spec: the page script (copy button and card reveal) stays under 1 KB gzipped.
const MAX_SCRIPT_GZIP_BYTES = 1024;

const failures = [];
const fail = (message) => failures.push(message);

const read = (name) => readFileSync(`${DIST}${name}`, 'utf8');
const countMatches = (text, pattern) => (text.match(pattern) ?? []).length;

if (!existsSync(DIST)) {
  console.error('dist/ is missing: run `npm run build` first.');
  process.exit(1);
}

const jsonLdPattern = /<script[^>]*type="application\/ld\+json"[^>]*>([\s\S]*?)<\/script>/i;

function checkPage({ file, path }) {
  if (!existsSync(`${DIST}${file}`)) {
    fail(`${file}: page was not built`);
    return;
  }
  const html = read(file);
  if (!/<html[^>]*\slang="en"/.test(html)) fail(`${file}: <html lang="en"> is missing`);
  if (!/<title>[^<]+<\/title>/.test(html)) fail(`${file}: <title> is missing or empty`);
  if (!/<meta\s+name="description"\s+content="[^"]+"/.test(html)) fail(`${file}: meta description is missing`);
  if (countMatches(html, /<h1[\s>]/g) !== 1) fail(`${file}: expected exactly one <h1>`);

  if (path === null) {
    if (!/<meta\s+name="robots"\s+content="[^"]*noindex/.test(html)) fail(`${file}: must be noindex`);
  } else {
    const want = new URL(path, SITE_URL).href;
    const canonical = html.match(/<link\s+rel="canonical"\s+href="([^"]+)"/)?.[1];
    if (canonical !== want) fail(`${file}: canonical is ${canonical ?? 'missing'}, want ${want}`);
  }

  for (const href of ['/privacy', '/terms', REPO_URL]) {
    if (!html.includes(`href="${href}"`)) fail(`${file}: footer link to ${href} is missing`);
  }

  for (const [, url] of html.matchAll(/(?:href|src|action)="(https?:\/\/[^"]+)"/g)) {
    if (!ALLOWED_HOSTS.has(new URL(url).host)) fail(`${file}: reference to disallowed host ${url}`);
  }
}

PAGES.forEach(checkPage);

// External references inside built CSS (for example a font URL).
const assetsDir = `${DIST}_astro/`;
if (existsSync(assetsDir)) {
  for (const name of readdirSync(assetsDir).filter((n) => n.endsWith('.css'))) {
    const css = readFileSync(`${assetsDir}${name}`, 'utf8');
    for (const [, url] of css.matchAll(/url\(\s*["']?(https?:\/\/[^)"']+)/g)) {
      fail(`_astro/${name}: external url() ${url}`);
    }
  }
}

// Home page rules.
if (existsSync(`${DIST}index.html`)) {
  const home = read('index.html');
  if (!home.includes(MCP_URL)) fail(`index.html: connector URL ${MCP_URL} is missing`);

  const jsonLd = home.match(jsonLdPattern)?.[1];
  if (!jsonLd) {
    fail('index.html: JSON-LD block is missing');
  } else {
    try {
      if (JSON.parse(jsonLd)['@type'] !== 'SoftwareApplication') fail('index.html: JSON-LD @type is not SoftwareApplication');
    } catch {
      fail('index.html: JSON-LD is not valid JSON');
    }
  }

  const ogImage = home.match(/<meta\s+property="og:image"\s+content="([^"]+)"/)?.[1];
  if (!ogImage) {
    fail('index.html: og:image is missing');
  } else if (!existsSync(`${DIST}${new URL(ogImage).pathname.slice(1)}`)) {
    fail(`index.html: og:image ${ogImage} does not exist in dist/`);
  }

  // Page scripts: inline ones plus local ones, JSON-LD excluded.
  let scriptBytes = 0;
  for (const [, attrs, body] of home.matchAll(/<script([^>]*)>([\s\S]*?)<\/script>/g)) {
    if (/application\/ld\+json/.test(attrs)) continue;
    const src = attrs.match(/\ssrc="([^"]+)"/)?.[1];
    const code = src && src.startsWith('/') ? readFileSync(`${DIST}${src.slice(1)}`, 'utf8') : body;
    scriptBytes += gzipSync(code).length;
  }
  if (scriptBytes > MAX_SCRIPT_GZIP_BYTES) fail(`index.html: page scripts are ${scriptBytes} B gzipped, limit ${MAX_SCRIPT_GZIP_BYTES} B`);
}

// Facts shown on the home page must match the code.
if (TOOLS.length === 0) fail('site.ts: TOOLS is empty');
if (STORED.length === 0 || NEVER_STORED.length === 0) fail('site.ts: STORED or NEVER_STORED is empty');
if (existsSync(`${DIST}index.html`)) {
  const home = read('index.html');
  for (const { name } of TOOLS) {
    if (!home.includes(name)) fail(`index.html: tool ${name} is not listed`);
  }
  for (const item of [...STORED, ...NEVER_STORED]) {
    if (!home.includes(item)) fail(`index.html: stored/never-stored item is missing: ${item}`);
  }
}
if (existsSync(GO_TOOLS_DIR)) {
  const goNames = new Set();
  for (const name of readdirSync(GO_TOOLS_DIR).filter((n) => n.endsWith('.go') && !n.endsWith('_test.go'))) {
    for (const [, tool] of readFileSync(`${GO_TOOLS_DIR}${name}`, 'utf8').matchAll(GO_TOOL_NAME)) goNames.add(tool);
  }
  const siteNames = new Set(TOOLS.map((t) => t.name));
  for (const name of goNames) if (!siteNames.has(name)) fail(`site.ts: TOOLS lacks ${name}, registered in internal/tools`);
  for (const name of siteNames) if (!goNames.has(name)) fail(`site.ts: tool ${name} is not registered in internal/tools`);
}

// Legal pages.
const visibleText = (html) =>
  html
    .replace(/<(script|style)[\s\S]*?<\/\1>/g, '')
    .replace(/<[^>]+>/g, '')
    .replace(/&#39;|&apos;/g, "'")
    .replace(/&amp;/g, '&')
    .replace(/\s+/g, ' ');
const mainOf = (html) => html.match(/<main[\s\S]*<\/main>/)?.[0] ?? '';

if (!LEGAL_UPDATED) fail('site.ts: LEGAL_UPDATED is missing');
for (const [file, other] of [
  ['privacy.html', '/terms'],
  ['terms.html', '/privacy'],
]) {
  if (!existsSync(`${DIST}${file}`)) continue;
  const html = read(file);
  if (!html.includes(OPERATOR_EMAIL)) fail(`${file}: operator e-mail is missing`);
  if (LEGAL_UPDATED && !html.includes(LEGAL_UPDATED)) fail(`${file}: last-updated date ${LEGAL_UPDATED} is missing`);
  if (!mainOf(html).includes(`href="${other}"`)) fail(`${file}: the page body does not link ${other}`);
}
if (existsSync(`${DIST}privacy.html`)) {
  const html = read('privacy.html');
  if (!visibleText(html).includes(LIMITED_USE_SENTENCE)) fail('privacy.html: the Google Limited Use sentence is missing or altered');
  if (!html.includes(`href="${GOOGLE_POLICY_URL}"`)) fail('privacy.html: link to the Google API Services User Data Policy is missing');
  if (!visibleText(html).includes('This website sets no cookies and runs no analytics.')) {
    fail('privacy.html: the no-cookies, no-analytics statement is missing');
  }
}
if (existsSync(`${DIST}terms.html`) && !visibleText(read('terms.html')).includes('French law')) {
  fail('terms.html: governing law (French law) is missing');
}

// Crawler and assistant discovery files.
if (!existsSync(`${DIST}sitemap-index.xml`)) fail('sitemap-index.xml is missing');
if (!existsSync(`${DIST}robots.txt`)) {
  fail('robots.txt is missing');
} else if (!read('robots.txt').includes(`Sitemap: ${SITE_URL}/sitemap-index.xml`)) {
  fail('robots.txt does not point to the sitemap');
}
if (!existsSync(`${DIST}llms.txt`)) {
  fail('llms.txt is missing');
} else {
  const llms = read('llms.txt');
  for (const link of [`${SITE_URL}/privacy`, `${SITE_URL}/terms`, REPO_URL]) {
    if (!llms.includes(link)) fail(`llms.txt does not link ${link}`);
  }
}
if (!existsSync(`${DIST}og.png`)) fail('og.png is missing');

if (failures.length > 0) {
  console.error(`${failures.length} check(s) failed:`);
  for (const message of failures) console.error(`  - ${message}`);
  process.exit(1);
}
console.log('dist checks passed');
