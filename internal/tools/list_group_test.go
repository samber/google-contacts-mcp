package tools

import (
	"slices"
	"strings"
	"testing"

	"github.com/samber/google-contacts-mcp/internal/contacts"
)

const (
	// testClientsGroup is the user group the filter tests select.
	testClientsGroup = "contactGroups/clients"
	// testClientsName is its display name.
	testClientsName = "Clients"
	// testOtherGroup is a second group, never selected.
	testOtherGroup = "contactGroups/other"
	// argPageSize and argPageToken are list_contacts argument names.
	argPageSize  = "page_size"
	argPageToken = "page_token"
	// argSort is the list_contacts sort argument name.
	argSort = "sort"
)

// seedGroupContacts stores count contacts; contact i joins testClientsGroup
// when member(i). It returns the members' resource names in List order.
func seedGroupContacts(h *harness, count int, member func(i int) bool) []string {
	h.t.Helper()
	h.people.setGroups([]contacts.Group{
		{ResourceName: testClientsGroup, Name: testClientsName, Type: contacts.GroupTypeUser},
		{ResourceName: testOtherGroup, Name: "Other", Type: contacts.GroupTypeUser},
	})

	for i := range count {
		groups := []string{testOtherGroup}
		if member(i) {
			groups = append(groups, testClientsGroup)
		}

		h.people.put(contacts.Contact{GivenName: testGivenName, Groups: groups})
	}

	var want []string

	for _, c := range h.people.sorted() {
		if slices.Contains(c.Groups, testClientsGroup) {
			want = append(want, c.ResourceName)
		}
	}

	return want
}

// walkGroup pages list_contacts with args until next_page_token is empty and
// returns every resource name seen, in order. It fails after maxCalls pages.
func walkGroup(h *harness, args map[string]any, maxCalls int) []string {
	h.t.Helper()

	var got []string

	for range maxCalls {
		var out ListOutput

		h.ok("list_contacts", args, &out)

		for _, c := range out.Contacts {
			got = append(got, c.ResourceName)
		}

		if out.NextPageToken == "" {
			return got
		}

		args[argPageToken] = out.NextPageToken
	}

	h.t.Fatalf("still paging after %d calls", maxCalls)

	return nil
}

func TestListContacts_GroupWalksEveryMemberOnce(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name               string
		listPage, pageSize int
	}{
		{"page size 1 resumes inside Google pages", 3, 1},
		{"page size 2", 3, 2},
		{"page ends exactly on a Google page", 2, 1},
		{"one Google page holds everything", 100, 100},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			h := newHarness(t)
			h.people.listPage = tc.listPage
			want := seedGroupContacts(h, 11, func(i int) bool { return i%3 != 1 })

			got := walkGroup(h, map[string]any{argGroup: testClientsName, argPageSize: tc.pageSize}, 20)
			if !slices.Equal(got, want) {
				t.Errorf("members = %v, want %v", got, want)
			}
		})
	}
}

func TestListContacts_GroupPageHoldsAtMostPageSize(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.people.listPage = 4
	want := seedGroupContacts(h, 9, func(int) bool { return true })

	var out ListOutput

	h.ok("list_contacts", map[string]any{argGroup: testClientsGroup, argPageSize: 3}, &out)

	if len(out.Contacts) != 3 || out.Contacts[0].ResourceName != want[0] || out.NextPageToken == "" {
		t.Errorf("out = %d contacts, next %q", len(out.Contacts), out.NextPageToken)
	}
}

func TestListContacts_GroupKeepsSortAndScanPageSize(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.people.listPage = 2
	seedGroupContacts(h, 5, func(int) bool { return true })

	walkGroup(h, map[string]any{argGroup: testClientsGroup, argPageSize: 1, argSort: sortLastModified}, 10)

	got := h.people.listArgs()
	if got.order != sortLastModified || got.pageSize != groupScanPageSize {
		t.Errorf("last Google list = %+v, want sort %s and page size %d", got, sortLastModified, groupScanPageSize)
	}
}

func TestListContacts_GroupScanIsBounded(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.people.listPage = 1
	// The only member sits past what one call may scan.
	want := seedGroupContacts(h, maxGroupScanPages+2, func(i int) bool { return i == maxGroupScanPages+1 })

	var out ListOutput

	h.ok("list_contacts", map[string]any{argGroup: testClientsGroup}, &out)

	if len(out.Contacts) != 0 || out.NextPageToken == "" {
		t.Fatalf("first call = %d contacts, next %q; want 0 and a token", len(out.Contacts), out.NextPageToken)
	}

	if n := h.people.callsOf("List"); n != maxGroupScanPages {
		t.Errorf("Google list calls = %d, want %d", n, maxGroupScanPages)
	}

	got := walkGroup(h, map[string]any{argGroup: testClientsGroup, argPageToken: out.NextPageToken}, 5)
	if !slices.Equal(got, want) {
		t.Errorf("members = %v, want %v", got, want)
	}
}

func TestListContacts_GroupEmpty(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.people.listPage = 2
	seedGroupContacts(h, 3, func(int) bool { return false })

	var out ListOutput

	h.ok("list_contacts", map[string]any{argGroup: testClientsGroup}, &out)

	if out.Contacts == nil || len(out.Contacts) != 0 || out.NextPageToken != "" {
		t.Errorf("out = %+v, want an empty non-nil list and no token", out)
	}
}

func TestListContacts_GroupErrors(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.people.listPage = 1
	seedGroupContacts(h, 3, func(int) bool { return true })

	var first ListOutput

	h.ok("list_contacts", map[string]any{argGroup: testClientsGroup, argPageSize: 1}, &first)

	if first.NextPageToken == "" {
		t.Fatal("no next_page_token")
	}

	token := first.NextPageToken

	cases := []struct {
		name string
		args map[string]any
		want string
	}{
		{"unknown group name", map[string]any{argGroup: "Nope"}, `group "Nope" not found`},
		{"malformed group resource name", map[string]any{argGroup: "contactGroups/../x"}, `group resource name must look like "contactGroups/abc"`},
		{"group token without group", map[string]any{argPageToken: token}, msgInvalidPageToken},
		{"group token for another group", map[string]any{argGroup: testOtherGroup, argPageToken: token}, msgInvalidPageToken},
		{"group token with another sort", map[string]any{argGroup: testClientsGroup, argSort: sortFirstName, argPageToken: token}, msgInvalidPageToken},
		{"plain token with group", map[string]any{argGroup: testClientsGroup, argPageToken: "1"}, msgInvalidPageToken},
		{"garbled group token", map[string]any{argGroup: testClientsGroup, argPageToken: groupTokenPrefix + "!!"}, msgInvalidPageToken},
		{"empty group token", map[string]any{argGroup: testClientsGroup, argPageToken: groupTokenPrefix}, msgInvalidPageToken},
		{"bad sort", map[string]any{argGroup: testClientsGroup, argSort: "RANDOM"}, "sort must be one of FIRST_NAME_ASCENDING, LAST_NAME_ASCENDING, LAST_MODIFIED_DESCENDING"},
	}
	for _, tc := range cases {
		h.fail("list_contacts", tc.args, tc.want)
	}
}

func TestListContacts_GroupNegativeSkipRejected(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.people.listPage = 2
	seedGroupContacts(h, 3, func(int) bool { return true })

	token := encodeGroupToken(groupCursor{Group: testClientsGroup, Skip: -1})

	h.fail("list_contacts", map[string]any{argGroup: testClientsGroup, argPageToken: token}, msgInvalidPageToken)
}

func TestServerInstructions(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	got := h.session.InitializeResult().Instructions
	if got != serverInstructions || !strings.Contains(got, "Does not send messages") {
		t.Errorf("instructions = %q", got)
	}
}

func TestListContacts_GroupListErrorPropagates(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	seedGroupContacts(h, 2, func(int) bool { return true })
	h.people.setErr("List", contacts.ErrUpstreamAuth)

	h.fail("list_contacts", map[string]any{argGroup: testClientsGroup}, msgUpstreamAuth)
}
