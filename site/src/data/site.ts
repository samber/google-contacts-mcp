/** Public origin of the website; canonical URLs and the sitemap derive from it. */
export const SITE_URL = 'https://google-contacts-mcp.samber.dev';

/** Connector URL users paste into Claude or ChatGPT: the MCP server's endpoint. */
export const MCP_URL = 'https://mcp.google-contacts-mcp.samber.dev/mcp';

/** Source repository of the open-source server. */
export const REPO_URL = 'https://github.com/samber/google-contacts-mcp';

/** Person who operates the hosted instance, named in the policy and the terms. */
export const OPERATOR_NAME = 'Samuel Berthe';

/** Public contact address for privacy and terms questions. */
export const OPERATOR_EMAIL = 'hello@samuel-berthe.fr';

export type ToolGroup = 'find' | 'write' | 'label';

export type Tool = {
  /** MCP tool name, exactly as registered in internal/tools; scripts/check-dist.mjs compares them. */
  name: string;
  /** What it does, in the user's words. */
  summary: string;
  group: ToolGroup;
};

export const TOOLS: readonly Tool[] = [
  { name: 'search_contacts', summary: 'Find people by the start of a name, e-mail, phone or company.', group: 'find' },
  { name: 'list_contacts', summary: 'Browse your whole address book, page by page.', group: 'find' },
  { name: 'get_contact', summary: 'Read everything stored on one contact.', group: 'find' },
  { name: 'create_contact', summary: 'Add a new contact.', group: 'write' },
  { name: 'update_contact', summary: 'Change only the fields you give. A list you give replaces the old list.', group: 'write' },
  {
    name: 'upsert_contact_by_email',
    summary: 'Find a contact by e-mail and fill its empty fields, or create it. Never overwrites a value.',
    group: 'write',
  },
  { name: 'delete_contact', summary: 'Delete a contact for good.', group: 'write' },
  { name: 'list_contact_groups', summary: 'See your labels.', group: 'label' },
  { name: 'create_contact_group', summary: 'Create a label.', group: 'label' },
  { name: 'modify_contact_group_members', summary: 'Add contacts to a label, or remove them.', group: 'label' },
];

export const TOOL_GROUPS: readonly { group: ToolGroup; title: string }[] = [
  { group: 'find', title: 'Find your people' },
  { group: 'write', title: 'Keep them current' },
  { group: 'label', title: 'Organize with labels' },
];

/** What the server keeps. Mirrors README "What is stored" and the privacy policy. */
export const STORED: readonly string[] = [
  'Your Google account id and e-mail address, with created and last-used dates',
  'Your Google refresh and access tokens, encrypted with AES-256-GCM',
  'Hashes of the tokens the server issues to your assistant, with their expiry',
  'The assistants (OAuth clients) registered with the server',
  'Sign-ins in progress, which expire after 10 minutes',
];

/** What the server never keeps. Mirrors README "What is stored" and the privacy policy. */
export const NEVER_STORED: readonly string[] = [
  'Your contacts: names, e-mail addresses, phone numbers, notes or any other field',
  'Tool inputs and outputs exchanged with your assistant',
  'Any token in clear',
  'Personal data in logs at the default log level',
];
