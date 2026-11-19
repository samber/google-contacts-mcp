// Package contacts holds the contact model, its mapping to Google People API
// v1 types, and the enrich rules used by upsert_contact_by_email. It does no
// network I/O: the People interface is implemented elsewhere.
package contacts

import (
	"context"
	"slices"
	"strings"

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
	// updates must carry that source with its current etag.
	sourceTypeContact = "CONTACT"
	// contactGroupPrefix starts every contact group resource name.
	contactGroupPrefix = "contactGroups/"
)

// systemGroupIDs are the People API system contact groups. Their memberships
// are managed by Google, so ToPerson keeps them even when Groups omits them.
var systemGroupIDs = []string{"myContacts", "starred", "friends", "family", "coworkers", "chatBuddies", "all", "blocked"}

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
	ResourceName, Etag                 string // Etag is the CONTACT source etag
	GivenName, FamilyName, DisplayName string
	Company, JobTitle, Notes           string
	Emails, Phones, Websites           []Typed // LinkedIn = Websites{Type: WebsiteTypeLinkedIn}
	Addresses                          []Address
	Groups                             []string // contactGroups/... resource names

	// raw is a copy of the fetched person, nil for contacts built by callers.
	// ToPerson overlays the Contact on it so writes keep every field and
	// sub-field this model does not map (spec §8.1: never remove a value).
	raw *people.Person
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
	c := Contact{ResourceName: p.ResourceName, Etag: p.Etag, raw: clonePerson(p)}
	// updateContact checks the CONTACT source etag, which differs from the
	// person etag for contacts linked to a Google profile.
	if src := contactSource(p); src != nil && src.Etag != "" {
		c.Etag = src.Etag
	}
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
			c.Addresses = append(c.Addresses, fromAddress(a))
		}
	}
	for _, m := range p.Memberships {
		if g := membershipGroup(m); g != "" {
			c.Groups = append(c.Groups, g)
		}
	}
	return c
}

// ToPerson maps a Contact to a People API person for create or update.
// When the Contact came from FromPerson, it starts from the fetched person and
// edits only mapped fields: extra entries, sub-fields (middle name, department,
// PO box, ...) and primary flags survive. Multi-valued lists follow the
// Contact (a value missing from it is dropped). DisplayName is output-only and
// never sent. Without a fetched person, empty fields produce no entry.
func (c Contact) ToPerson() *people.Person {
	p := clonePerson(c.raw)
	if p == nil {
		p = &people.Person{Etag: c.Etag}
	}
	p.ResourceName = c.ResourceName
	p.Metadata = c.sourceMetadata()

	if c.GivenName != "" || c.FamilyName != "" || len(p.Names) > 0 {
		n := firstOrNew(&p.Names)
		n.GivenName, n.FamilyName = c.GivenName, c.FamilyName
	}
	if c.Company != "" || c.JobTitle != "" || len(p.Organizations) > 0 {
		o := firstOrNew(&p.Organizations)
		o.Name, o.Title = c.Company, c.JobTitle
	}
	if c.Notes != "" || len(p.Biographies) > 0 {
		firstOrNew(&p.Biographies).Value = c.Notes
	}

	p.EmailAddresses = overlay(p.EmailAddresses, c.Emails, typedOverlay(
		func(e *people.EmailAddress) Typed { return Typed{Value: e.Value, Type: e.Type} },
		func(t Typed) *people.EmailAddress { return &people.EmailAddress{Value: t.Value, Type: t.Type} },
		func(e *people.EmailAddress, typ string) { e.Type = typ },
		emailKey))
	p.PhoneNumbers = overlay(p.PhoneNumbers, c.Phones, typedOverlay(
		func(e *people.PhoneNumber) Typed { return Typed{Value: e.Value, Type: e.Type} },
		func(t Typed) *people.PhoneNumber { return &people.PhoneNumber{Value: t.Value, Type: t.Type} },
		func(e *people.PhoneNumber, typ string) { e.Type = typ },
		phoneKey))
	p.Urls = overlay(p.Urls, c.Websites, typedOverlay(
		func(e *people.Url) Typed { return Typed{Value: e.Value, Type: e.Type} },
		func(t Typed) *people.Url { return &people.Url{Value: t.Value, Type: t.Type} },
		func(e *people.Url, typ string) { e.Type = typ },
		websiteKey))
	p.Addresses = overlay(p.Addresses, c.Addresses, overlayFuncs[*people.Address, Address]{
		key:     func(e *people.Address) string { return addressKey(fromAddress(e)) },
		valKey:  addressKey,
		create:  toAddress,
		setType: func(e *people.Address, a Address) { e.Type = a.Type },
	})
	p.Memberships = c.memberships(p.Memberships)
	return p
}

// sourceMetadata returns the metadata carrying the CONTACT source and its
// etag, or nil when the Contact has no etag (create).
func (c Contact) sourceMetadata() *people.PersonMetadata {
	src := &people.Source{Type: sourceTypeContact}
	if raw := contactSource(c.raw); raw != nil {
		src.Id = raw.Id
	}
	src.Etag = c.Etag
	if src.Etag == "" {
		return nil
	}
	return &people.PersonMetadata{Sources: []*people.Source{src}}
}

// memberships keeps raw system-group and domain memberships, which Google
// manages, and makes user-group memberships follow c.Groups.
func (c Contact) memberships(raw []*people.Membership) []*people.Membership {
	var out []*people.Membership
	present := map[string]bool{}
	userEntries := map[string]*people.Membership{}
	for _, m := range raw {
		if m == nil {
			continue
		}
		g := membershipGroup(m)
		switch {
		case g == "" || isSystemGroup(g):
			out = append(out, m)
			present[g] = true
		case userEntries[g] == nil:
			userEntries[g] = m
		}
	}
	for _, g := range c.Groups {
		if g == "" || present[g] {
			continue
		}
		present[g] = true
		m := userEntries[g]
		if m == nil {
			m = &people.Membership{
				ContactGroupMembership: &people.ContactGroupMembership{ContactGroupResourceName: g},
			}
		}
		out = append(out, m)
	}
	return out
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

// overlayFuncs adapts one People API entry type E to one Contact value type V.
type overlayFuncs[E any, V any] struct {
	key     func(E) string // key of a raw entry
	valKey  func(V) string // key of a Contact value, same normalization
	create  func(V) E      // new entry for a value absent from raw
	setType func(E, V)     // copies the value's type onto a reused entry
}

func typedOverlay[E any](toTyped func(E) Typed, create func(Typed) E, setType func(E, string), key func(Typed) string) overlayFuncs[E, Typed] {
	return overlayFuncs[E, Typed]{
		key:     func(e E) string { return key(toTyped(e)) },
		valKey:  key,
		create:  create,
		setType: func(e E, t Typed) { setType(e, t.Type) },
	}
}

// overlay builds the entry list for values: a value whose key matches an
// unused raw entry reuses that entry (keeping metadata, primary flag and
// unmapped sub-fields) with only its type updated; other values become new
// entries; raw entries matching no value are dropped. raw entries must already
// be private copies, since they are modified in place.
func overlay[E comparable, V any](raw []E, values []V, f overlayFuncs[E, V]) []E {
	var zero E
	var out []E
	used := make([]bool, len(raw))
	for _, v := range values {
		k := f.valKey(v)
		idx := -1
		for i, e := range raw {
			if k != "" && !used[i] && e != zero && f.key(e) == k {
				idx = i
				break
			}
		}
		if idx < 0 {
			out = append(out, f.create(v))
			continue
		}
		used[idx] = true
		f.setType(raw[idx], v)
		out = append(out, raw[idx])
	}
	return out
}

// firstOrNew returns the first entry of *list, creating it when the list is
// empty or starts with nil.
func firstOrNew[E any](list *[]*E) *E {
	if len(*list) == 0 {
		*list = []*E{new(E)}
	} else if (*list)[0] == nil {
		(*list)[0] = new(E)
	}
	return (*list)[0]
}

func fromAddress(a *people.Address) Address {
	return Address{
		Formatted:  a.FormattedValue,
		Street:     a.StreetAddress,
		City:       a.City,
		Region:     a.Region,
		PostalCode: a.PostalCode,
		Country:    a.Country,
		Type:       a.Type,
	}
}

func toAddress(a Address) *people.Address {
	return &people.Address{
		FormattedValue: a.Formatted,
		StreetAddress:  a.Street,
		City:           a.City,
		Region:         a.Region,
		PostalCode:     a.PostalCode,
		Country:        a.Country,
		Type:           a.Type,
	}
}

// membershipGroup returns the contact group resource name of m, or "" for nil
// and domain memberships.
func membershipGroup(m *people.Membership) string {
	if m == nil || m.ContactGroupMembership == nil {
		return ""
	}
	return m.ContactGroupMembership.ContactGroupResourceName
}

func isSystemGroup(resourceName string) bool {
	id, ok := strings.CutPrefix(resourceName, contactGroupPrefix)
	return ok && slices.Contains(systemGroupIDs, id)
}

// contactSource returns the CONTACT entry of p.Metadata.Sources, or nil.
func contactSource(p *people.Person) *people.Source {
	if p == nil || p.Metadata == nil {
		return nil
	}
	for _, s := range p.Metadata.Sources {
		if s != nil && s.Type == sourceTypeContact {
			return s
		}
	}
	return nil
}

// clonePerson copies p and every entry of the lists ToPerson edits, so edits
// never reach the caller's person. Nested pointers inside entries (field
// metadata, dates, membership details) stay shared: this package never
// writes through them.
func clonePerson(p *people.Person) *people.Person {
	if p == nil {
		return nil
	}
	cp := *p
	cp.Names = cloneEntries(p.Names)
	cp.Organizations = cloneEntries(p.Organizations)
	cp.Biographies = cloneEntries(p.Biographies)
	cp.EmailAddresses = cloneEntries(p.EmailAddresses)
	cp.PhoneNumbers = cloneEntries(p.PhoneNumbers)
	cp.Urls = cloneEntries(p.Urls)
	cp.Addresses = cloneEntries(p.Addresses)
	cp.Memberships = cloneEntries(p.Memberships)
	if p.Metadata != nil {
		md := *p.Metadata
		md.Sources = cloneEntries(p.Metadata.Sources)
		cp.Metadata = &md
	}
	return &cp
}

func cloneEntries[E any](list []*E) []*E {
	if list == nil {
		return nil
	}
	out := make([]*E, len(list))
	for i, e := range list {
		if e != nil {
			v := *e
			out[i] = &v
		}
	}
	return out
}
