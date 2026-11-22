// Renders public/favicon.ico (32x32) and public/apple-touch-icon.png (180x180) from public/favicon.svg.
// Run by hand when the logo changes: `node scripts/icons.mjs`, then commit the outputs.
// It is not part of the build.
import { writeFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import sharp from 'sharp';

const SVG = fileURLToPath(new URL('../public/favicon.svg', import.meta.url));
const ICO_OUT = fileURLToPath(new URL('../public/favicon.ico', import.meta.url));
const APPLE_OUT = fileURLToPath(new URL('../public/apple-touch-icon.png', import.meta.url));

const ICO_SIZE = 32;
const APPLE_SIZE = 180;
// The SVG is 32x32 units: render at a high density so the downscale stays sharp.
const SVG_UNITS = 32;
// iOS fills transparent pixels with black, so the touch icon gets an opaque white ground.
const APPLE_BACKGROUND = '#ffffff';

// ICO container layout (little endian): 6-byte header, one 16-byte directory entry, then the image.
const ICO_HEADER_BYTES = 6;
const ICO_ENTRY_BYTES = 16;
const ICO_TYPE_ICON = 1;
const ICO_IMAGE_COUNT = 1;
const ICO_COLOR_PLANES = 1;
const ICO_BITS_PER_PIXEL = 32;

const render = (size) => sharp(SVG, { density: (72 * size * 4) / SVG_UNITS }).resize(size, size);

/** Wraps PNG bytes in a single-image ICO file (PNG-in-ICO, supported by every current browser). */
function pngToIco(png, size) {
  const header = Buffer.alloc(ICO_HEADER_BYTES + ICO_ENTRY_BYTES);
  header.writeUInt16LE(0, 0); // reserved
  header.writeUInt16LE(ICO_TYPE_ICON, 2);
  header.writeUInt16LE(ICO_IMAGE_COUNT, 4);
  header.writeUInt8(size, 6); // width
  header.writeUInt8(size, 7); // height
  header.writeUInt8(0, 8); // palette size: none
  header.writeUInt8(0, 9); // reserved
  header.writeUInt16LE(ICO_COLOR_PLANES, 10);
  header.writeUInt16LE(ICO_BITS_PER_PIXEL, 12);
  header.writeUInt32LE(png.length, 14);
  header.writeUInt32LE(ICO_HEADER_BYTES + ICO_ENTRY_BYTES, 18);
  return Buffer.concat([header, png]);
}

const icoPng = await render(ICO_SIZE).png().toBuffer();
writeFileSync(ICO_OUT, pngToIco(icoPng, ICO_SIZE));
console.log(`wrote ${ICO_OUT}`);

await render(APPLE_SIZE).flatten({ background: APPLE_BACKGROUND }).png().toFile(APPLE_OUT);
console.log(`wrote ${APPLE_OUT}`);
