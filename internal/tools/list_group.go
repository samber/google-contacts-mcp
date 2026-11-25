package tools

import (
	"context"
	"encoding/base64"
	"errors"
	"slices"
	"strconv"
	"strings"

	"github.com/samber/google-contacts-mcp/internal/contacts"
)

const (
	// groupScanPageSize is the Google page size of a group-filtered list: the
	// connections.list maximum, so a scan costs as few calls as possible. It
	// must never vary, because group page tokens resume inside such a page and
	// Google requires the same parameters on every page of one listing.
	groupScanPageSize = maxPageSize
	// maxGroupScanPages bounds the Google pages one group-filtered call reads,
	// 5,000 contacts, so a single tool call keeps a bounded latency and a
	// bounded share of the user's People API read quota. Larger address books
	// continue through next_page_token.
	maxGroupScanPages = 5
	// groupTokenPrefix marks list_contacts page tokens built for a group
	// filter, so they are never confused with Google's own page tokens.
	groupTokenPrefix = "g1."
	// groupTokenSep separates the fields of a group page token; base64url
	// never produces it.
	groupTokenSep = "."
	// groupTokenFields is the field count of a group page token: group,
	// sort, Google token and skip.
	groupTokenFields = 4

	// msgInvalidPageToken is the list_contacts error for a page_token that
	// Google rejects or that belongs to another request.
	msgInvalidPageToken = "invalid page_token"
)

// errBadGroupToken reports a group page token that does not decode or does
// not belong to the current request.
var errBadGroupToken = errors.New(msgInvalidPageToken)

// groupCursor is the state behind a group-filtered page token: the request it
// belongs to, the Google page token to read, and how many contacts of that
// page were already consumed.
type groupCursor struct {
	Group string
	Sort  string
	Token string
	Skip  int
}

// encodeGroupToken writes the cursor as groupTokenPrefix followed by its
// four fields joined by groupTokenSep; the strings are base64url-encoded so
// they can never contain the separator.
func encodeGroupToken(c groupCursor) string {
	enc := base64.RawURLEncoding.EncodeToString

	return groupTokenPrefix + strings.Join([]string{enc([]byte(c.Group)), enc([]byte(c.Sort)), enc([]byte(c.Token)), strconv.Itoa(c.Skip)}, groupTokenSep)
}

// decodeGroupToken parses a token made by encodeGroupToken and checks it
// belongs to the same group and sort order.
func decodeGroupToken(token, group, sort string) (groupCursor, error) {
	raw, ok := strings.CutPrefix(token, groupTokenPrefix)
	if !ok {
		return groupCursor{}, errBadGroupToken
	}

	parts := strings.Split(raw, groupTokenSep)
	if len(parts) != groupTokenFields {
		return groupCursor{}, errBadGroupToken
	}

	var fields [groupTokenFields - 1]string

	for i := range fields {
		b, err := base64.RawURLEncoding.DecodeString(parts[i])
		if err != nil {
			return groupCursor{}, errBadGroupToken
		}

		fields[i] = string(b)
	}

	skip, err := strconv.Atoi(parts[groupTokenFields-1])
	if err != nil {
		return groupCursor{}, errBadGroupToken
	}

	c := groupCursor{Group: fields[0], Sort: fields[1], Token: fields[2], Skip: skip}
	if c.Group != group || c.Sort != sort || c.Skip < 0 || c.Skip >= groupScanPageSize {
		return groupCursor{}, errBadGroupToken
	}

	return c, nil
}

// listGroupContacts returns up to size members of in.Group. connections.list
// has no group filter, so it reads Google pages in the requested sort order
// and keeps the members: the result keeps Google's global order. A page can
// hold fewer than size contacts, even none, while more remain; the agent
// continues while next_page_token is set.
func listGroupContacts(ctx context.Context, p contacts.People, in ListInput, size int) (ListOutput, error) {
	cur, err := groupStart(ctx, p, in)
	if err != nil {
		return ListOutput{}, err
	}

	out := ListOutput{Contacts: []ContactOutput{}}

	for range maxGroupScanPages {
		page, next, err := p.List(ctx, groupScanPageSize, cur.Token, cur.Sort)
		if err != nil {
			return ListOutput{}, listError(err, cur.Token)
		}

		if token, full := collectMembers(&out, page, cur, next, size); full {
			out.NextPageToken = token
			return out, nil
		}

		if next == "" {
			return out, nil
		}

		cur = groupCursor{Group: cur.Group, Sort: cur.Sort, Token: next}
	}

	out.NextPageToken = encodeGroupToken(cur)

	return out, nil
}

// groupStart resolves the requested group and returns where the scan starts:
// the first Google page, or the position saved in in.PageToken.
func groupStart(ctx context.Context, p contacts.People, in ListInput) (groupCursor, error) {
	if err := checkGroup(in.Group); err != nil {
		return groupCursor{}, err
	}

	group, err := contacts.ResolveGroup(ctx, p, in.Group)
	if err != nil {
		return groupCursor{}, err
	}

	if in.PageToken == "" {
		return groupCursor{Group: group, Sort: in.Sort}, nil
	}

	cur, err := decodeGroupToken(in.PageToken, group, in.Sort)
	if err != nil {
		return groupCursor{}, newToolError(msgInvalidPageToken)
	}

	return cur, nil
}

// collectMembers appends the group members of page, from cur.Skip on, to out.
// Once out holds size contacts it stops and returns true with the token that
// resumes right after the last one kept, "" when nothing follows.
func collectMembers(out *ListOutput, page []contacts.Contact, cur groupCursor, next string, size int) (string, bool) {
	// Contacts deleted since the token was made can shrink the page.
	for i := min(cur.Skip, len(page)); i < len(page); i++ {
		if !slices.Contains(page[i].Groups, cur.Group) {
			continue
		}

		out.Contacts = append(out.Contacts, toOutput(page[i]))
		if len(out.Contacts) < size {
			continue
		}

		switch {
		case i+1 < len(page):
			cur.Skip = i + 1
			return encodeGroupToken(cur), true
		case next != "":
			return encodeGroupToken(groupCursor{Group: cur.Group, Sort: cur.Sort, Token: next}), true
		default:
			return "", true
		}
	}

	return "", false
}

// listError maps a People list error; Google rejecting a page token we sent
// means the caller's page_token was stale or forged.
func listError(err error, pageToken string) error {
	if pageToken != "" && errors.Is(err, contacts.ErrInvalidArgument) {
		return newToolError(msgInvalidPageToken)
	}

	return err
}
