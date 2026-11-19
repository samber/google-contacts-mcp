// Package contacts holds the contact model, its mapping to Google People API
// v1 types, and the enrich rules used by upsert_contact_by_email. It does no
// network I/O: the People interface is implemented elsewhere.
package contacts

import (
	"context"
	"slices"

	"google.golang.org/api/people/v1"
)

// People API person field names, used both as updatePersonFields entries and
// as the "changed" list returned by Enrich.
const (
	// FieldNames covers GivenName and FamilyName.
	FieldNames = "names"
	// FieldOrganizations covers Company and JobTitle.
	FieldOrganizations = "organizations"
	// FieldBiographies covers Notes.
	FieldBiographies = "biographies"
	// FieldEmailAddresses covers Emails.
	FieldEmailAddresses = "emailAddresses"
	// FieldPhoneNumbers covers Phones.
	FieldPhoneNumbers = "phoneNumbers"
	// FieldURLs covers Websites.
	FieldURLs = "urls"
	// FieldAddresses covers Addresses.
	FieldAddresses = "addresses"
	// FieldMemberships covers Groups.
	FieldMemberships = "memberships"
)

const (
	// WebsiteTypeLinkedIn marks a website as the contact's LinkedIn profile;
	// the People API has no dedicated LinkedIn field.
	WebsiteTypeLinkedIn = "linkedin"
	// sourceTypeContact is the People API source type of a user-owned contact;
	// updates must carry that source with the current etag.
	sourceTypeContact = "CONTACT"
)

// Typed is one value of a multi-valued field with its type (home, work, ...).
type Typed struct {
	Value, Type string
}

// Address is a postal address.
type Address struct {
	Formatted, Street, City, Region, PostalCode, Country, Type string
}

// Contact is the subset of a People API person handled by the server.
type Contact struct {
	ResourceName, Etag                 string
	GivenName, FamilyName, DisplayName string
	Company, JobTitle, Notes           string
	Emails, Phones, Websites           []Typed // LinkedIn = Websites{Type: WebsiteTypeLinkedIn}
	Addresses                          []Address
	Groups                             []string // contactGroups/... resource names
}

// Group is a contact group. Type is "user" or "system".
type Group struct {
	ResourceName, Name, Type string
	MemberCount              int
}

// People is the Google People API surface the tools depend on.
type People interface {
	Search(ctx context.Context, query string, limit int) ([]Contact, error)
	List(ctx context.Context, pageSize int, pageToken, sortOrder string) ([]Contact, string, error)
	Get(ctx context.Context, resourceName string) (Contact, error)
	Create(ctx context.Context, c Contact) (Contact, error)
	Update(ctx context.Context, c Contact, fields []string) (Contact, error) // c.Etag required
	Delete(ctx context.Context, resourceName string) error
	ListGroups(ctx context.Context) ([]Group, error)
	CreateGroup(ctx context.Context, name string) (Group, error)
	ModifyMembers(ctx context.Context, group string, add, remove []string) error
}

// FromPerson maps a People API person to a Contact. Single-valued fields come
// from the first entry of each list, which the API marks as primary.
func FromPerson(p *people.Person) Contact {
	if p == nil {
		return Contact{}
	}
	c := Contact{ResourceName: p.ResourceName, Etag: p.Etag}
	if len(p.Names) > 0 && p.Names[0] != nil {
		n := p.Names[0]
		c.GivenName, c.FamilyName, c.DisplayName = n.GivenName, n.FamilyName, n.DisplayName
	}
	if len(p.Organizations) > 0 && p.Organizations[0] != nil {
		c.Company, c.JobTitle = p.Organizations[0].Name, p.Organizations[0].Title
	}
	if len(p.Biographies) > 0 && p.Biographies[0] != nil {
		c.Notes = p.Biographies[0].Value
	}
	for _, e := range p.EmailAddresses {
		if e != nil {
			c.Emails = append(c.Emails, Typed{Value: e.Value, Type: e.Type})
		}
	}
	for _, ph := range p.PhoneNumbers {
		if ph != nil {
			c.Phones = append(c.Phones, Typed{Value: ph.Value, Type: ph.Type})
		}
	}
	for _, u := range p.Urls {
		if u != nil {
			c.Websites = append(c.Websites, Typed{Value: u.Value, Type: u.Type})
		}
	}
	for _, a := range p.Addresses {
		if a != nil {
			c.Addresses = append(c.Addresses, Address{
				Formatted:  a.FormattedValue,
				Street:     a.StreetAddress,
				City:       a.City,
				Region:     a.Region,
				PostalCode: a.PostalCode,
				Country:    a.Country,
				Type:       a.Type,
			})
		}
	}
	for _, m := range p.Memberships {
		// Domain memberships carry no group resource name and are skipped.
		if m != nil && m.ContactGroupMembership != nil && m.ContactGroupMembership.ContactGroupResourceName != "" {
			c.Groups = append(c.Groups, m.ContactGroupMembership.ContactGroupResourceName)
		}
	}
	return c
}

// ToPerson maps a Contact to a People API person for create or update.
// DisplayName is output-only in the API and is never sent. Empty fields
// produce no entry, so no empty struct reaches Google.
func (c Contact) ToPerson() *people.Person {
	p := &people.Person{ResourceName: c.ResourceName, Etag: c.Etag}
	if c.Etag != "" {
		// Updates are rejected with 400 unless the contact source carries the etag.
		p.Metadata = &people.PersonMetadata{
			Sources: []*people.Source{{Type: sourceTypeContact, Etag: c.Etag}},
		}
	}
	if c.GivenName != "" || c.FamilyName != "" {
		p.Names = []*people.Name{{GivenName: c.GivenName, FamilyName: c.FamilyName}}
	}
	if c.Company != "" || c.JobTitle != "" {
		p.Organizations = []*people.Organization{{Name: c.Company, Title: c.JobTitle}}
	}
	if c.Notes != "" {
		p.Biographies = []*people.Biography{{Value: c.Notes}}
	}
	for _, e := range c.Emails {
		p.EmailAddresses = append(p.EmailAddresses, &people.EmailAddress{Value: e.Value, Type: e.Type})
	}
	for _, ph := range c.Phones {
		p.PhoneNumbers = append(p.PhoneNumbers, &people.PhoneNumber{Value: ph.Value, Type: ph.Type})
	}
	for _, u := range c.Websites {
		p.Urls = append(p.Urls, &people.Url{Value: u.Value, Type: u.Type})
	}
	for _, a := range c.Addresses {
		p.Addresses = append(p.Addresses, &people.Address{
			FormattedValue: a.Formatted,
			StreetAddress:  a.Street,
			City:           a.City,
			Region:         a.Region,
			PostalCode:     a.PostalCode,
			Country:        a.Country,
			Type:           a.Type,
		})
	}
	for _, g := range c.Groups {
		p.Memberships = append(p.Memberships, &people.Membership{
			ContactGroupMembership: &people.ContactGroupMembership{ContactGroupResourceName: g},
		})
	}
	return p
}

// UpdateFields returns the sorted People API field names for every non-empty
// field of c, suitable for updatePersonFields.
func (c Contact) UpdateFields() []string {
	var fields []string
	add := func(cond bool, name string) {
		if cond {
			fields = append(fields, name)
		}
	}
	add(c.GivenName != "" || c.FamilyName != "", FieldNames)
	add(c.Company != "" || c.JobTitle != "", FieldOrganizations)
	add(c.Notes != "", FieldBiographies)
	add(len(c.Emails) > 0, FieldEmailAddresses)
	add(len(c.Phones) > 0, FieldPhoneNumbers)
	add(len(c.Websites) > 0, FieldURLs)
	add(len(c.Addresses) > 0, FieldAddresses)
	add(len(c.Groups) > 0, FieldMemberships)
	slices.Sort(fields)
	return fields
}
