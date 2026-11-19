package contacts

import (
	"context"
	"fmt"
	"strings"
)

// Group types reported in Group.Type.
const (
	// GroupTypeUser is a group the user created; only these match by name.
	GroupTypeUser = "user"
	// GroupTypeSystem is a Google-managed group (My Contacts, Starred, ...).
	GroupTypeSystem = "system"
)

// GroupLister lists the user's contact groups.
type GroupLister interface {
	ListGroups(ctx context.Context) ([]Group, error)
}

// IsGroupResourceName reports whether group is a contactGroups/... name.
func IsGroupResourceName(group string) bool {
	return strings.HasPrefix(group, contactGroupPrefix)
}

// ResolveGroup returns group as-is when it is a resource name, else the one
// user group whose name matches it case-insensitively (spec §8.1). System
// groups never match by name. Zero or several matches are errors.
func ResolveGroup(ctx context.Context, l GroupLister, group string) (string, error) {
	if IsGroupResourceName(group) {
		return group, nil
	}
	groups, err := l.ListGroups(ctx)
	if err != nil {
		return "", err
	}
	var matches []string
	for _, g := range groups {
		if g.Type == GroupTypeUser && strings.EqualFold(g.Name, group) {
			matches = append(matches, g.ResourceName)
		}
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("group %q not found", group)
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("group %q is ambiguous (%d matches)", group, len(matches))
	}
}
