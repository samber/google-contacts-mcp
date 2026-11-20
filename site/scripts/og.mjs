// Renders public/og.png (1200x630), the social preview image, from an inline SVG.
// Run by hand when the design changes: `node scripts/og.mjs`, then commit the PNG.
// It is not part of the build. Fonts are system fonts: librsvg cannot load the web fonts.
import { fileURLToPath } from 'node:url';
import sharp from 'sharp';

const OUT = fileURLToPath(new URL('../public/og.png', import.meta.url));

const WIDTH = 1200;
const HEIGHT = 630;
const SANS = "Helvetica Neue, Helvetica, Arial, sans-serif";
const MONO = "Courier New, Courier, monospace";

// Same fictional contact as the hero card.
const FIELDS = [
  ['Company', 'Acme'],
  ['Title', 'CTO'],
  ['E-mail', 'camille@acme.example'],
  ['LinkedIn', 'linkedin.com/in/c-example'],
];

const fieldRows = FIELDS.map(([label, value], index) => {
  const y = 236 + index * 80;
  return `<text x="730" y="${y}" font-family="${SANS}" font-size="20" fill="#5d6873">${label}</text>
    <text x="730" y="${y + 34}" font-family="${MONO}" font-size="24" fill="#16202a">${value}</text>
    <rect x="718" y="${y + 48}" width="396" height="2" fill="#b4cbe3"/>`;
}).join('\n    ');

const svg = `<svg xmlns="http://www.w3.org/2000/svg" width="${WIDTH}" height="${HEIGHT}" viewBox="0 0 ${WIDTH} ${HEIGHT}">
  <rect width="${WIDTH}" height="${HEIGHT}" fill="#e4e9ee"/>
  <g fill="#16202a" font-family="${SANS}" font-weight="800">
    <text x="72" y="200" font-size="58">Your assistant</text>
    <text x="72" y="270" font-size="58">keeps your address</text>
    <text x="72" y="340" font-size="58">book up to date.</text>
  </g>
  <g fill="#16202a" font-family="${SANS}" font-size="30">
    <text x="72" y="430">Claude and ChatGPT search, create</text>
    <text x="72" y="470">and enrich your Google Contacts.</text>
  </g>
  <text x="72" y="566" font-family="${MONO}" font-size="28" fill="#16202a">google-contacts-mcp.samber.dev</text>

  <rect x="700" y="80" width="448" height="470" rx="8" fill="#ffffff"/>
  <text x="718" y="140" font-family="${MONO}" font-size="30" font-weight="700" fill="#16202a">Durand, Camille</text>
  <rect x="700" y="160" width="448" height="4" fill="#d33f49"/>
  ${fieldRows}
  <rect x="1148" y="214" width="40" height="44" rx="4" fill="#ffe066"/>
  <text x="1168" y="245" text-anchor="middle" font-family="${MONO}" font-size="26" font-weight="700" fill="#16202a">D</text>
</svg>`;

await sharp(Buffer.from(svg)).png().toFile(OUT);
console.log(`wrote ${OUT}`);
