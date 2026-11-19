package tools

import (
	"slices"
	"testing"

	"github.com/samber/google-contacts-mcp/internal/contacts"
)

func TestUpsert_Created(t *testing.T) {
	h := newHarness(t)
	var out UpsertOutput
	res := h.ok("upsert_contact_by_email", map[string]any{"email": "  Ada@X.io ", "given_name": "Ada", "company": "Analytical"}, &out)
	if out.Status != statusCreated || out.Contact == nil || out.Contact.ResourceName == "" {
		t.Fatalf("out = %+v", out)
	}
	stored := h.people.stored(out.Contact.ResourceName)
	if len(stored.Emails) != 1 || stored.Emails[0].Value != "Ada@X.io" || stored.GivenName != "Ada" || stored.Company != "Analytical" {
		t.Errorf("stored = %+v", stored)
	}
	if got := resultText(res); got != "created contact "+out.Contact.ResourceName {
		t.Errorf("summary = %q", got)
	}
}

func TestUpsert_Enriched(t *testing.T) {
	h := newHarness(t)
	h.people.searchStrip = true
	c := h.people.put(contacts.Contact{
		GivenName: "Ada", Company: "Analytical",
		Emails: []contacts.Typed{{Value: "ada@x.io"}},
		Phones: []contacts.Typed{{Value: "+33 6 12"}},
	})
	var out UpsertOutput
	res := h.ok("upsert_contact_by_email", map[string]any{
		"email":      "ada@x.io",
		"given_name": "Other",
		"company":    "Other Co",
		"job_title":  "Countess",
		"phones":     []map[string]string{{"value": "+33612"}, {"value": "+44 1", "type": "work"}},
	}, &out)
	if out.Status != statusEnriched {
		t.Fatalf("status = %q", out.Status)
	}
	wantFields := []string{contacts.FieldOrganizations, contacts.FieldPhoneNumbers}
	if !slices.Equal(out.ChangedFields, wantFields) || !slices.Equal(h.people.updateFields(), wantFields) {
		t.Errorf("changed = %v, update fields = %v, want %v", out.ChangedFields, h.people.updateFields(), wantFields)
	}
	stored := h.people.stored(c.ResourceName)
	if stored.GivenName != "Ada" || stored.Company != "Analytical" || stored.JobTitle != "Countess" || len(stored.Phones) != 2 {
		t.Errorf("stored = %+v", stored)
	}
	if h.people.callsOf("Get") != 1 {
		t.Errorf("Get calls = %d, want 1 (search results may be partial)", h.people.callsOf("Get"))
	}
	if got := resultText(res); got != "enriched contact "+c.ResourceName {
		t.Errorf("summary = %q", got)
	}
}

func TestUpsert_Unchanged(t *testing.T) {
	h := newHarness(t)
	c := h.people.put(contacts.Contact{GivenName: "Ada", Emails: []contacts.Typed{{Value: "ada@x.io"}}})
	var out UpsertOutput
	h.ok("upsert_contact_by_email", map[string]any{"email": "ada@x.io", "given_name": "Ada"}, &out)
	if out.Status != statusUnchanged || out.Contact == nil || out.Contact.ResourceName != c.ResourceName || len(out.ChangedFields) != 0 {
		t.Errorf("out = %+v", out)
	}
	if n := h.people.writes(); n != 0 {
		t.Errorf("writes = %d, want 0", n)
	}
}

func TestUpsert_Ambiguous(t *testing.T) {
	h := newHarness(t)
	h.people.setGroups([]contacts.Group{{ResourceName: "contactGroups/inv", Name: "Investors", Type: contacts.GroupTypeUser}})
	a := h.people.put(contacts.Contact{GivenName: "Ada", DisplayName: "Ada L", Company: "A Co", Emails: []contacts.Typed{{Value: "ada@x.io"}}})
	b := h.people.put(contacts.Contact{GivenName: "Ada2", Emails: []contacts.Typed{{Value: "ADA@x.io"}}})
	var out UpsertOutput
	h.ok("upsert_contact_by_email", map[string]any{"email": "ada@x.io", "job_title": "X", "group": "Investors"}, &out)
	if out.Status != statusAmbiguous || out.Contact != nil || len(out.Candidates) != 2 {
		t.Fatalf("out = %+v", out)
	}
	want := Candidate{ResourceName: a.ResourceName, DisplayName: "Ada L", Organization: "A Co"}
	if out.Candidates[0] != want || out.Candidates[1].ResourceName != b.ResourceName {
		t.Errorf("candidates = %+v", out.Candidates)
	}
	if n := h.people.writes(); n != 0 {
		t.Errorf("writes = %d, want 0", n)
	}
}

func TestUpsert_MatchesEmailCaseInsensitive(t *testing.T) {
	h := newHarness(t)
	c := h.people.put(contacts.Contact{GivenName: "Ada", Emails: []contacts.Typed{{Value: "Ada@X.IO"}}})
	var out UpsertOutput
	h.ok("upsert_contact_by_email", map[string]any{"email": "ada@x.io", "family_name": "Lovelace"}, &out)
	if out.Status != statusEnriched || out.Contact.ResourceName != c.ResourceName || out.Contact.FamilyName != "Lovelace" {
		t.Errorf("out = %+v", out)
	}
	if h.people.callsOf("Create") != 0 {
		t.Error("created a duplicate")
	}
}

func TestUpsert_SearchPrefixFalsePositiveIgnored(t *testing.T) {
	h := newHarness(t)
	h.people.put(contacts.Contact{GivenName: "Long", Emails: []contacts.Typed{{Value: "a@b.com"}}})
	exact := h.people.put(contacts.Contact{GivenName: "Short", Emails: []contacts.Typed{{Value: "a@b.co"}}})
	var out UpsertOutput
	h.ok("upsert_contact_by_email", map[string]any{"email": "a@b.co", "company": "Z"}, &out)
	if out.Status != statusEnriched || out.Contact.ResourceName != exact.ResourceName {
		t.Errorf("out = %+v", out)
	}

	var created UpsertOutput
	h.ok("upsert_contact_by_email", map[string]any{"email": "a@b.c"}, &created)
	if created.Status != statusCreated {
		t.Errorf("prefix-only matches: status = %q, want created", created.Status)
	}
}

func TestUpsert_GroupAdded(t *testing.T) {
	h := newHarness(t)
	h.people.setGroups([]contacts.Group{{ResourceName: "contactGroups/inv", Name: "Investors", Type: contacts.GroupTypeUser}})
	c := h.people.put(contacts.Contact{GivenName: "Ada", Emails: []contacts.Typed{{Value: "ada@x.io"}}})

	var out UpsertOutput
	h.ok("upsert_contact_by_email", map[string]any{"email": "ada@x.io", "group": "investors"}, &out)
	if out.Status != statusEnriched || !slices.Equal(out.ChangedFields, []string{contacts.FieldMemberships}) {
		t.Errorf("out = %+v", out)
	}
	mods := h.people.modifyCalls()
	if len(mods) != 1 || mods[0].group != "contactGroups/inv" || !slices.Equal(mods[0].add, []string{c.ResourceName}) {
		t.Errorf("modifies = %+v", mods)
	}
	if !slices.Contains(out.Contact.Groups, "contactGroups/inv") {
		t.Errorf("groups = %v", out.Contact.Groups)
	}

	// Already a member: nothing to do.
	var again UpsertOutput
	h.ok("upsert_contact_by_email", map[string]any{"email": "ada@x.io", "group": "contactGroups/inv"}, &again)
	if again.Status != statusUnchanged || len(h.people.modifyCalls()) != 1 {
		t.Errorf("again = %+v, modifies = %d", again, len(h.people.modifyCalls()))
	}

	// Created contacts join the group in the create call.
	var created UpsertOutput
	h.ok("upsert_contact_by_email", map[string]any{"email": "new@x.io", "group": "Investors"}, &created)
	if created.Status != statusCreated || !slices.Equal(h.people.stored(created.Contact.ResourceName).Groups, []string{"contactGroups/inv"}) {
		t.Errorf("created = %+v", created)
	}

	// An unknown group fails before any write.
	writes := h.people.writes()
	h.fail("upsert_contact_by_email", map[string]any{"email": "other@x.io", "group": "nope"}, `group "nope" not found`)
	if h.people.writes() != writes {
		t.Error("wrote despite an unknown group")
	}
}

func TestUpsert_EmailRequired(t *testing.T) {
	h := newHarness(t)
	h.fail("upsert_contact_by_email", map[string]any{"email": "   "}, "email is required")
}
