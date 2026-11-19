# Roadmap

Ideas for after v1, in no particular order. None is scheduled yet; open an issue to discuss one before you start on it.

- **"Other contacts" read access.** Read the people Google saves automatically from your e-mail exchanges (`contacts.other.readonly` scope), so the assistant can find someone you wrote to but never saved.
- **Workspace directory.** Search your organization's directory for Google Workspace accounts.
- **Google RISC token-revoked events.** Subscribe to Google's Cross-Account Protection (RISC) events, so the server deletes a user's data as soon as they revoke access, instead of the next time it calls Google.
- **Contact photos.** Read and set a contact's photo.
- **Bulk import and export.** Import contacts from, and export them to, formats such as CSV or vCard.
- **Duplicate merging.** Find and merge duplicate contacts, for example those that make `upsert_contact_by_email` return `ambiguous`.
- **Custom-scheme redirect URIs for native clients.** Accept private-use URI scheme redirects (such as `com.example.app:/callback`) for native applications that register through Dynamic Client Registration. Today only `https` and loopback redirect URIs are accepted.
