package api

import (
	"fmt"
	"strconv"
	"strings"
)

// Resource is the neutral identity information needed for name resolution.
type Resource struct {
	ID       int64
	Name     string
	Username string
	FullName string
	Email    string
}

// Resolve deterministically resolves an ID, exact name, or unique substring.
// When user is true, exact username is preferred and additional user fields are
// considered for substring matching.
func Resolve(selector string, resources []Resource, user bool) (Resource, error) {
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return Resource{}, &Error{Type: "validation", Message: "resource selector must not be empty"}
	}
	if id, err := strconv.ParseInt(selector, 10, 64); err == nil {
		if id <= 0 {
			return Resource{}, &Error{Type: "validation", Message: "resource ID must be positive"}
		}
		for _, resource := range resources {
			if resource.ID == id {
				return resource, nil
			}
		}
		return Resource{}, &Error{Type: "not_found", Message: fmt.Sprintf("resource %d was not found", id)}
	}
	folded := strings.ToLower(selector)
	if user {
		for _, resource := range resources {
			if strings.EqualFold(resource.Username, selector) {
				return resource, nil
			}
		}
	}
	for _, resource := range resources {
		if strings.EqualFold(resource.Name, selector) || (user && strings.EqualFold(resource.FullName, selector)) {
			return resource, nil
		}
	}
	matches := make([]Resource, 0)
	for _, resource := range resources {
		values := []string{resource.Name}
		if user {
			values = append(values, resource.Username, resource.FullName, resource.Email)
		}
		matched := false
		for _, value := range values {
			if strings.Contains(strings.ToLower(value), folded) {
				matched = true
				break
			}
		}
		if matched {
			matches = append(matches, resource)
		}
	}
	switch len(matches) {
	case 0:
		return Resource{}, &Error{Type: "not_found", Message: fmt.Sprintf("no resource matches %q", selector)}
	case 1:
		return matches[0], nil
	default:
		if len(matches) > 10 {
			matches = matches[:10]
		}
		parts := make([]string, 0, len(matches))
		for _, resource := range matches {
			name := resource.Name
			if user && resource.Username != "" {
				name = resource.Username
			}
			parts = append(parts, fmt.Sprintf("%s (%d)", name, resource.ID))
		}
		return Resource{}, &Error{Type: "validation", Message: "selector is ambiguous; candidates: " + strings.Join(parts, ", ")}
	}
}
