import { defineConfig } from 'astro/config';
import sitemap from '@astrojs/sitemap';

// SITE is the public origin; Astro builds canonical URLs and the sitemap from it.
const SITE = 'https://google-contacts-mcp.samber.dev';

export default defineConfig({
  site: SITE,
  output: 'static',
  // "/privacy" and "/terms" are the canonical paths, with no trailing slash:
  // the privacy URL registered with Google must stay valid.
  trailingSlash: 'never',
  build: { format: 'file' },
  integrations: [sitemap()],
});
