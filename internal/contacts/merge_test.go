package contacts

import (
	"reflect"
	"testing"
)

func TestEnrich(t *testing.T) {
	cases := []struct {
		name        string
		existing    Contact
		incoming    Contact
		want        Contact
		wantChanged []string
	}{
		{
			name:     "empty existing gets all",
			existing: Contact{ResourceName: "people/c1", Etag: "e1"},
			incoming: Contact{
				GivenName: "Ada", FamilyName: "Lovelace", Company: "AE", JobTitle: "Eng", Notes: "n",
				Emails:    []Typed{{Value: "ada@example.com", Type: "work"}},
				Phones:    []Typed{{Value: "+1 555 0100", Type: "mobile"}},
				Websites:  []Typed{{Value: "https://linkedin.com/in/ada", Type: WebsiteTypeLinkedIn}},
				Addresses: []Address{{Formatted: "1 Main St", Type: "home"}},
			},
			want: Contact{
				ResourceName: "people/c1", Etag: "e1",
				GivenName: "Ada", FamilyName: "Lovelace", Company: "AE", JobTitle: "Eng", Notes: "n",
				Emails:    []Typed{{Value: "ada@example.com", Type: "work"}},
				Phones:    []Typed{{Value: "+1 555 0100", Type: "mobile"}},
				Websites:  []Typed{{Value: "https://linkedin.com/in/ada", Type: WebsiteTypeLinkedIn}},
				Addresses: []Address{{Formatted: "1 Main St", Type: "home"}},
			},
			wantChanged: []string{
				FieldAddresses, FieldBiographies, FieldEmailAddresses, FieldNames,
				FieldOrganizations, FieldPhoneNumbers, FieldURLs,
			},
		},
		{
			name:        "non-empty JobTitle kept",
			existing:    Contact{JobTitle: "CEO"},
			incoming:    Contact{JobTitle: "Intern"},
			want:        Contact{JobTitle: "CEO"},
			wantChanged: nil,
		},
		{
			name:        "empty Company filled while JobTitle kept",
			existing:    Contact{JobTitle: "CEO"},
			incoming:    Contact{Company: "Acme", JobTitle: "Intern"},
			want:        Contact{Company: "Acme", JobTitle: "CEO"},
			wantChanged: []string{FieldOrganizations},
		},
		{
			name:        "same phone with different formatting unchanged",
			existing:    Contact{Phones: []Typed{{Value: "+33612345678", Type: "mobile"}}},
			incoming:    Contact{Phones: []Typed{{Value: "+33 6 12 34 56 78", Type: "work"}}},
			want:        Contact{Phones: []Typed{{Value: "+33612345678", Type: "mobile"}}},
			wantChanged: nil,
		},
		{
			name:     "new mobile appended keeps old",
			existing: Contact{Phones: []Typed{{Value: "+33612345678", Type: "mobile"}}},
			incoming: Contact{Phones: []Typed{{Value: "+33 7 00 00 00 01", Type: "mobile"}}},
			want: Contact{Phones: []Typed{
				{Value: "+33612345678", Type: "mobile"},
				{Value: "+33 7 00 00 00 01", Type: "mobile"},
			}},
			wantChanged: []string{FieldPhoneNumbers},
		},
		{
			name:        "linkedin url variants are equal",
			existing:    Contact{Websites: []Typed{{Value: "linkedin.com/in/x", Type: WebsiteTypeLinkedIn}}},
			incoming:    Contact{Websites: []Typed{{Value: "https://www.linkedin.com/in/x/", Type: WebsiteTypeLinkedIn}}},
			want:        Contact{Websites: []Typed{{Value: "linkedin.com/in/x", Type: WebsiteTypeLinkedIn}}},
			wantChanged: nil,
		},
		{
			name:        "email case and space insensitive",
			existing:    Contact{Emails: []Typed{{Value: "a@b.com"}}},
			incoming:    Contact{Emails: []Typed{{Value: "A@B.com "}}},
			want:        Contact{Emails: []Typed{{Value: "a@b.com"}}},
			wantChanged: nil,
		},
		{
			name:        "address normalized formatted value",
			existing:    Contact{Addresses: []Address{{Formatted: "1 Main St,\n London"}}},
			incoming:    Contact{Addresses: []Address{{Formatted: "1  MAIN st, london "}}},
			want:        Contact{Addresses: []Address{{Formatted: "1 Main St,\n London"}}},
			wantChanged: nil,
		},
		{
			name:        "duplicate and empty incoming values appended once",
			existing:    Contact{},
			incoming:    Contact{Emails: []Typed{{Value: "x@y.z"}, {Value: " X@Y.Z"}, {Value: "  "}}},
			want:        Contact{Emails: []Typed{{Value: "x@y.z"}}},
			wantChanged: []string{FieldEmailAddresses},
		},
		{
			name: "nothing new",
			existing: Contact{
				GivenName: "Ada", Emails: []Typed{{Value: "a@b.com"}},
			},
			incoming:    Contact{GivenName: "Other", Emails: []Typed{{Value: "a@b.com"}}},
			want:        Contact{GivenName: "Ada", Emails: []Typed{{Value: "a@b.com"}}},
			wantChanged: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, changed := Enrich(tc.existing, tc.incoming)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("merged mismatch:\n got %#v\nwant %#v", got, tc.want)
			}
			if !reflect.DeepEqual(changed, tc.wantChanged) {
				t.Errorf("changed = %#v, want %#v", changed, tc.wantChanged)
			}
		})
	}
}

func TestEnrich_NeverRemoves(t *testing.T) {
	existing := Contact{
		ResourceName: "people/c1", Etag: "e1", DisplayName: "Ada L",
		GivenName: "Ada", Company: "AE",
		Emails:    []Typed{{Value: "a@b.com"}, {Value: "c@d.com"}},
		Phones:    []Typed{{Value: "+1 555 0100"}, {Value: "+1 555 0101"}},
		Websites:  []Typed{{Value: "https://a.example"}, {Value: "https://b.example"}},
		Addresses: []Address{{Formatted: "A"}, {Formatted: "B"}},
		Groups:    []string{"contactGroups/g1"},
	}
	snapshot := Contact{
		ResourceName: existing.ResourceName, Etag: existing.Etag, DisplayName: existing.DisplayName,
		GivenName: existing.GivenName, Company: existing.Company,
		Emails:    append([]Typed(nil), existing.Emails...),
		Phones:    append([]Typed(nil), existing.Phones...),
		Websites:  append([]Typed(nil), existing.Websites...),
		Addresses: append([]Address(nil), existing.Addresses...),
		Groups:    append([]string(nil), existing.Groups...),
	}
	incoming := Contact{
		ResourceName: "people/other", Etag: "other", DisplayName: "Other",
		Emails:    []Typed{{Value: "c@d.com"}},
		Phones:    []Typed{},
		Addresses: []Address{{Formatted: "b"}},
		Groups:    []string{"contactGroups/g2"},
	}
	got, changed := Enrich(existing, incoming)
	if !reflect.DeepEqual(got, snapshot) {
		t.Fatalf("merged mismatch:\n got %#v\nwant %#v", got, snapshot)
	}
	if changed != nil {
		t.Fatalf("changed = %v, want nil", changed)
	}
	if !reflect.DeepEqual(existing, snapshot) {
		t.Fatalf("existing was mutated: %#v", existing)
	}
}

func TestEnrich_DoesNotAliasExisting(t *testing.T) {
	// existing.Emails has spare capacity: a naive append would write into the
	// caller's backing array.
	backing := make([]Typed, 1, 4)
	backing[0] = Typed{Value: "a@b.com"}
	existing := Contact{Emails: backing}
	got, _ := Enrich(existing, Contact{Emails: []Typed{{Value: "new@b.com"}}})
	got.Emails[0].Value = "mutated"
	if existing.Emails[0].Value != "a@b.com" {
		t.Fatalf("merged result aliases existing slice")
	}
}
