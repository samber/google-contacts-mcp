import type { APIRoute } from 'astro';
import {
  MCP_URL,
  NEVER_STORED,
  REPO_URL,
  SITE_NAME,
  SITE_SUMMARY,
  SITE_URL,
  STORED,
  TOOLS,
  TOOL_GROUPS,
} from '../data/site';

// Built from src/data/site.ts, the list the gate compares with internal/tools, so the
// summary for assistants cannot drift from the tools and the privacy facts on the pages.
const toolSection = TOOL_GROUPS.flatMap(({ group, title }) => [
  `### ${title}`,
  '',
  ...TOOLS.filter((tool) => tool.group === group).map((tool) => `- \`${tool.name}\`: ${tool.summary}`),
  '',
]);

const lines = [
  `# ${SITE_NAME}`,
  '',
  `> ${SITE_SUMMARY} Every call goes to Google directly; contacts are never stored. Open source, written in Go.`,
  '',
  '## How to connect',
  '',
  `Connector URL to add in an assistant: ${MCP_URL}`,
  '',
  '1. Claude: Settings, Connectors, Add custom connector. ChatGPT: turn on developer mode, then Create app.',
  '2. Paste the connector URL.',
  '3. Sign in with Google and approve access to your contacts. Access can be revoked at any time from the Google account.',
  '',
  '## Tools',
  '',
  ...toolSection,
  '## What the server stores',
  '',
  ...STORED.map((item) => `- ${item}`),
  '',
  '## What the server never stores',
  '',
  ...NEVER_STORED.map((item) => `- ${item}`),
  '',
  '## Pages',
  '',
  `- [Home](${SITE_URL}/): what the service does and how to connect it.`,
  `- [Privacy policy](${SITE_URL}/privacy): what is stored, what is never stored, and how to erase it.`,
  `- [Terms of service](${SITE_URL}/terms): the conditions for using the hosted service.`,
  '',
  '## Source',
  '',
  `- [Source code and documentation](${REPO_URL}): the Go server, the list of tools, self-hosting instructions.`,
  '',
];

export const GET: APIRoute = () =>
  new Response(lines.join('\n'), { headers: { 'Content-Type': 'text/plain; charset=utf-8' } });
