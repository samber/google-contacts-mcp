package contacts

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"google.golang.org/api/people/v1"
)

func fullPerson() *people.Person {
	return &people.Person{
		ResourceName: "people/c123",
		Etag:         "etag-1",
		Names: []*people.Name{
			{GivenName: "Ada", FamilyName: "Lovelace", DisplayName: "Ada Lovelace"},
			{GivenName: "Other", FamilyName: "Name", DisplayName: "Other Name"},
		},
		Organizations: []*people.Organization{
			{Name: "Analytical Engines", Title: "Engineer"},
			{Name: "Old Corp", Title: "Intern"},
		},
		Biographies: []*people.Biography{{Value: "First note"}, {Value: "Second note"}},
		EmailAddresses: []*people.EmailAddress{
			{Value: "ada@example.com", Type: "work"},
			nil,
			{Value: "ada@home.example", Type: "home"},
		},
		PhoneNumbers: []*people.PhoneNumber{{Value: "+33 6 12 34 56 78", Type: "mobile"}},
		Urls: []*people.Url{
			{Value: "https://www.linkedin.com/in/ada", Type: "linkedin"},
			{Value: "https://ada.example", Type: "homePage"},
		},
		Addresses: []*people.Address{{
			FormattedValue: "1 Main St, London",
			StreetAddress:  "1 Main St",
			City:           "London",
			Region:         "Greater London",
			PostalCode:     "N1",
			Country:        "UK",
			Type:           "home",
		}},
		Memberships: []*people.Membership{
			{ContactGroupMembership: &people.ContactGroupMembership{ContactGroupResourceName: "contactGroups/myContacts"}},
			{DomainMembership: &people.DomainMembership{InViewerDomain: true}},
			{ContactGroupMembership: &people.ContactGroupMembership{ContactGroupResourceName: "contactGroups/abc"}},
		},
	}
}

func TestFromPerson_Full(t *testing.T) {
	got := FromPerson(fullPerson())
	want := Contact{
		ResourceName: "people/c123",
		Etag:         "etag-1",
		GivenName:    "Ada",
		FamilyName:   "Lovelace",
		DisplayName:  "Ada Lovelace",
		Company:      "Analytical Engines",
		JobTitle:     "Engineer",
		Notes:        "First note",
		Emails: []Typed{
			{Value: "ada@example.com", Type: "work"},
			{Value: "ada@home.example", Type: "home"},
		},
		Phones: []Typed{{Value: "+33 6 12 34 56 78", Type: "mobile"}},
		Websites: []Typed{
			{Value: "https://www.linkedin.com/in/ada", Type: WebsiteTypeLinkedIn},
			{Value: "https://ada.example", Type: "homePage"},
		},
		Addresses: []Address{{
			Formatted:  "1 Main St, London",
			Street:     "1 Main St",
			City:       "London",
			Region:     "Greater London",
			PostalCode: "N1",
			Country:    "UK",
			Type:       "home",
		}},
		Groups: []string{"contactGroups/myContacts", "contactGroups/abc"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("FromPerson mismatch:\n got %#v\nwant %#v", got, want)
	}
}

func TestFromPerson_EmptyPerson(t *testing.T) {
	if got := FromPerson(&people.Person{}); !reflect.DeepEqual(got, Contact{}) {
		t.Fatalf("FromPerson(empty) = %#v, want zero Contact", got)
	}
	if got := FromPerson(nil); !reflect.DeepEqual(got, Contact{}) {
		t.Fatalf("FromPerson(nil) = %#v, want zero Contact", got)
	}
	// nil entries inside slices must not panic either.
	p := &people.Person{
		Names:         []*people.Name{nil},
		Organizations: []*people.Organization{nil},
		Biographies:   []*people.Biography{nil},
		Memberships:   []*people.Membership{nil, {}},
	}
	if got := FromPerson(p); !reflect.DeepEqual(got, Contact{}) {
		t.Fatalf("FromPerson(nil entries) = %#v, want zero Contact", got)
	}
}

func TestToPerson_RoundTrip(t *testing.T) {
	c := FromPerson(fullPerson())
	// DisplayName is output-only in the People API, so ToPerson never sends it.
	c.DisplayName = ""
	got := FromPerson(c.ToPerson())
	if !reflect.DeepEqual(got, c) {
		t.Fatalf("round trip mismatch:\n got %#v\nwant %#v", got, c)
	}
}

func TestToPerson_OmitsEmptyStructs(t *testing.T) {
	p := Contact{DisplayName: "Only Display"}.ToPerson()
	if p.Names != nil || p.Organizations != nil || p.Biographies != nil ||
		p.EmailAddresses != nil || p.PhoneNumbers != nil || p.Urls != nil ||
		p.Addresses != nil || p.Memberships != nil || p.Metadata != nil {
		t.Fatalf("ToPerson sent empty structs: %#v", p)
	}
	p = Contact{JobTitle: "CTO"}.ToPerson()
	if len(p.Organizations) != 1 || p.Organizations[0].Title != "CTO" || p.Organizations[0].Name != "" {
		t.Fatalf("organizations = %#v, want one entry with title only", p.Organizations)
	}
}

func TestToPerson_SetsEtagSource(t *testing.T) {
	p := Contact{ResourceName: "people/c1", Etag: "etag-42"}.ToPerson()
	if p.Etag != "etag-42" {
		t.Fatalf("Etag = %q, want etag-42", p.Etag)
	}
	if p.Metadata == nil || len(p.Metadata.Sources) != 1 {
		t.Fatalf("Metadata.Sources = %#v, want one source", p.Metadata)
	}
	src := p.Metadata.Sources[0]
	if src.Etag != "etag-42" || src.Type != sourceTypeContact {
		t.Fatalf("source = %#v, want etag-42 / %s", src, sourceTypeContact)
	}
	if p := (Contact{}).ToPerson(); p.Metadata != nil {
		t.Fatalf("Metadata = %#v without etag, want nil", p.Metadata)
	}
}

func TestUpdateFields(t *testing.T) {
	cases := []struct {
		name string
		c    Contact
		want []string
	}{
		{"empty", Contact{ResourceName: "people/c1", Etag: "e", DisplayName: "X"}, nil},
		{"family only", Contact{FamilyName: "L"}, []string{FieldNames}},
		{"job only", Contact{JobTitle: "CTO"}, []string{FieldOrganizations}},
		{"all", FromPerson(fullPerson()), []string{
			FieldAddresses, FieldBiographies, FieldEmailAddresses, FieldMemberships,
			FieldNames, FieldOrganizations, FieldPhoneNumbers, FieldURLs,
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.c.UpdateFields(); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("UpdateFields = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestNormalizeEmail(t *testing.T) {
	cases := map[string]string{
		"A@B.com ":           "a@b.com",
		"\tUser@Example.ORG": "user@example.org",
		"":                   "",
		"   ":                "",
	}
	for in, want := range cases {
		if got := NormalizeEmail(in); got != want {
			t.Errorf("NormalizeEmail(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRateLimitedError(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{30 * time.Second, "rate limited, retry after 30s"},
		{1500 * time.Millisecond, "rate limited, retry after 2s"},
		{0, "rate limited, retry after 0s"},
	}
	for _, tc := range cases {
		var err error = RateLimitedError{RetryAfter: tc.d}
		if got := err.Error(); got != tc.want {
			t.Errorf("Error() = %q, want %q", got, tc.want)
		}
		var rl RateLimitedError
		if !errors.As(err, &rl) || rl.RetryAfter != tc.d {
			t.Errorf("errors.As failed for %v", tc.d)
		}
	}
}

func TestSentinelErrorsDistinct(t *testing.T) {
	errs := []error{ErrNotFound, ErrConflict, ErrUpstreamAuth}
	for i, a := range errs {
		for j, b := range errs {
			if i != j && errors.Is(a, b) {
				t.Fatalf("%v matches %v", a, b)
			}
		}
	}
}
