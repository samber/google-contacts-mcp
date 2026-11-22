---
name: get-started
description: Use when the user wants to find, add, change, delete or organize entries in their Google Contacts address book.
---

# Google Contacts

Use the `google-contacts` MCP server tools. Each tool acts on the signed-in user's own Google Contacts.

## Pick a tool

- Find someone by name, e-mail, phone or organization: `search_contacts`.
- Browse everything: `list_contacts`, one page at a time.
- Read one contact in full, including its etag: `get_contact`.
- Add a new contact: `create_contact`.
- Change fields of a contact: `update_contact`. A list you pass replaces the whole list.
- Add details to a contact found by exact e-mail, or create it if none exists, without overwriting anything: `upsert_contact_by_email`.
- Remove a contact permanently: `delete_contact`.
- Read or create labels: `list_contact_groups`, `create_contact_group`.
- Put contacts in a group or take them out: `modify_contact_group_members`.

## Rules

1. Search before you write, so you do not create a duplicate.
2. Tell the user what you are about to change before you call a tool that writes.
3. Ask for confirmation before `delete_contact`. The deletion cannot be undone.
4. Pass `resourceName` values exactly as the tools return them (`people/c123`, `contactGroups/abc`).
5. If a tool reports that Google access was revoked, ask the user to reconnect the connector.
