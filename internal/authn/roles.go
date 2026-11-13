// SPDX-License-Identifier: Apache-2.0

package authn

import (
	"bytes"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strings"
)

// The roles claim's bounds: a token past either holds no group (fail closed).
const (
	maxRoleNames    = 256
	maxRoleNameSize = 256
)

// Why a roles claim gave no group: a closed set, the label of rooms_human_roles_claim_refused_total.
const (
	RolesClaimMissing = "missing" // the claim, or a step of its pointer, is absent
	RolesClaimType    = "type"    // neither an array of strings, an object nor a string
	RolesClaimBounds  = "bounds"  // over maxRoleNames names, or a name over maxRoleNameSize bytes
)

// ErrRolesClaim is every refusal of a token's roles claim. The token is still valid: its principal
// holds no group, which the policy refuses as "not in an agents group".
var ErrRolesClaim = errors.New("roles claim refused")

// RolesClaimError says why a roles claim gave no group. It never carries the claim's value.
type RolesClaimError struct{ Reason string }

func (e *RolesClaimError) Error() string { return "roles claim refused: " + e.Reason }

// Is makes every RolesClaimError an ErrRolesClaim.
func (e *RolesClaimError) Is(target error) bool { return target == ErrRolesClaim }

// UnmarshalJSON keeps the raw claim set beside the typed fields, so a configured roles claim can be
// read by name or pointer. groups and the ZITADEL roles claim decode leniently: a token whose
// groups is a string (Authentik) must still verify, and RoleNames judges the shape.
func (c *Claims) UnmarshalJSON(b []byte) error {
	type plain Claims
	var p struct {
		plain
		Groups       json.RawMessage `json:"groups,omitempty"`
		ProjectRoles json.RawMessage `json:"urn:zitadel:iam:org:project:roles,omitempty"`
	}
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	*c = Claims(p.plain)
	c.raw = raw
	// Today's typed reads: an array of strings, an object. Another shape reads as absent here and
	// is marked, so the strict parse refuses the token as decoding it always did.
	if len(p.Groups) > 0 && json.Unmarshal(p.Groups, &c.Groups) != nil {
		c.Groups, c.rolesMalformed = nil, true
	}
	if len(p.ProjectRoles) > 0 && json.Unmarshal(p.ProjectRoles, &c.ProjectRoles) != nil {
		c.ProjectRoles, c.rolesMalformed = nil, true
	}
	return nil
}

// RoleNames is the group names the roles claim holds. claim is a claim name, or an RFC 6901 pointer
// into the claim set when it starts with "/"; "" is GroupNames, the zitadel preset's rule. The value
// may be an array of strings, an object (its keys, sorted) or one string. A missing claim, another
// shape, or one over the bounds is a *RolesClaimError and no names. The bounds apply to a
// configured claim only: the zitadel rule keeps today's behaviour.
func (c *Claims) RoleNames(claim string) ([]string, error) {
	if claim == "" {
		return c.GroupNames(), nil // the zitadel preset's rule, unbounded as it always was
	}
	v, ok := c.lookup(claim)
	if !ok {
		return nil, &RolesClaimError{Reason: RolesClaimMissing}
	}
	names, err := roleValue(v)
	if err != nil {
		return nil, err
	}
	if err := roleBounds(names); err != nil {
		return nil, err
	}
	return names, nil
}

// lookup finds claim in the raw claim set: by name, or by an RFC 6901 pointer through objects.
func (c *Claims) lookup(claim string) (json.RawMessage, bool) {
	if !strings.HasPrefix(claim, "/") {
		v, ok := c.raw[claim]
		return v, ok
	}
	cur := c.raw
	steps := strings.Split(claim[1:], "/")
	for i, step := range steps {
		key, ok := unescapePointer(step)
		if !ok {
			return nil, false
		}
		v, ok := cur[key]
		if !ok {
			return nil, false
		}
		if i == len(steps)-1 {
			return v, true
		}
		cur = nil
		if json.Unmarshal(v, &cur) != nil || cur == nil {
			return nil, false
		}
	}
	return nil, false
}

// unescapePointer decodes one RFC 6901 reference token: ~1 is "/", ~0 is "~", and any other "~" is
// not a pointer.
func unescapePointer(s string) (string, bool) {
	if !strings.Contains(s, "~") {
		return s, true
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '~' {
			b.WriteByte(s[i])
			continue
		}
		if i+1 == len(s) || (s[i+1] != '0' && s[i+1] != '1') {
			return "", false
		}
		if s[i+1] == '0' {
			b.WriteByte('~')
		} else {
			b.WriteByte('/')
		}
		i++
	}
	return b.String(), true
}

// ValidRolesClaim reports whether claim is a claim name, or a well-formed RFC 6901 pointer.
func ValidRolesClaim(claim string) bool {
	if claim == "" || !strings.HasPrefix(claim, "/") {
		return true
	}
	for _, step := range strings.Split(claim[1:], "/") {
		if _, ok := unescapePointer(step); !ok {
			return false
		}
	}
	return true
}

// roleValue reads a roles claim's names from its three accepted shapes.
func roleValue(v json.RawMessage) ([]string, error) {
	switch t := bytes.TrimSpace(v); {
	case len(t) > 0 && t[0] == '[':
		var names []string
		if json.Unmarshal(t, &names) == nil {
			return names, nil
		}
	case len(t) > 0 && t[0] == '{':
		var obj map[string]json.RawMessage
		if json.Unmarshal(t, &obj) == nil {
			return slices.Sorted(maps.Keys(obj)), nil
		}
	case len(t) > 0 && t[0] == '"':
		var name string
		if json.Unmarshal(t, &name) == nil {
			return []string{name}, nil
		}
	}
	return nil, &RolesClaimError{Reason: RolesClaimType}
}

func roleBounds(names []string) error {
	if len(names) > maxRoleNames {
		return &RolesClaimError{Reason: RolesClaimBounds}
	}
	for _, n := range names {
		if len(n) > maxRoleNameSize {
			return &RolesClaimError{Reason: RolesClaimBounds}
		}
	}
	return nil
}
