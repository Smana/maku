// SPDX-License-Identifier: Apache-2.0

// Package grant is a saved approval grant as the broker stores it and the merge gate reads it
// (merge policy stage 5, MP9, MP10): a deterministic predicate inside one live class's
// requiresApproval subset — a repository, a class, the changed-file patterns every file the subset
// caught must match, an optional diff shape — bound to the policy hash it was made under. It is
// never similarity: what it matches can be shown, tested and revoked as written.
package grant

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
)

// The statuses a grant moves between. Only an active grant is ever matched; revoked and disabled
// are terminal.
const (
	StatusActive    = "active"
	StatusSuspended = "suspended"
	StatusRevoked   = "revoked"
	StatusExpired   = "expired"
	StatusDisabled  = "disabled"
)

// The audit events, one per change of a grant (approval_grant_events, insert-only).
const (
	EventCreated     = "created"
	EventUsed        = "used"
	EventSuspended   = "suspended"
	EventReconfirmed = "reconfirmed"
	EventRevoked     = "revoked"
	EventExpired     = "expired"
	EventDisabled    = "disabled"
	EventRenewed     = "renewed"
)

// Events is the closed list of grant events: the metric's event label takes no other value.
func Events() []string {
	return []string{EventCreated, EventUsed, EventSuspended, EventReconfirmed, EventRevoked, EventExpired, EventDisabled, EventRenewed}
}

// Shapes are the diff shapes a grant may require: those the gate computes from the diff alone.
// added-entry reads its class's entry files, which a grant does not carry.
func Shapes() []string { return []string{"links-only", "semver-patch", "semver-minor"} }

// The predicate's bounds. RE2 never backtracks, so a pattern's cost is bounded by its length.
const (
	MaxFiles       = 32
	MaxPatternLen  = 256
	MinPolicyHash  = 12 // the decision record's policy= is 12 hex digits
	MaxReasonBytes = 200
)

var (
	// IDRE is a grant id: 16 random bytes in lowercase base32, unpadded.
	IDRE         = regexp.MustCompile(`^[a-z2-7]{26}$`)
	repositoryRE = regexp.MustCompile(`^[a-z0-9_.-]{1,100}/[a-z0-9_.-]{1,100}$`)
	classRE      = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,38}[a-z0-9])?$`)
	hashRE       = regexp.MustCompile(`^[0-9a-f]{12,64}$`)
)

// Grant is one saved grant. Version moves on every renewal and reconfirmation: a decision binds to
// the id and the version it read (MP12).
type Grant struct {
	ID         string    `json:"id"`
	Version    int       `json:"version"`
	Repository string    `json:"repository"`
	Class      string    `json:"class"`
	Files      []string  `json:"files"`
	DiffShape  string    `json:"diffShape,omitempty"`
	PolicyHash string    `json:"policyHash"`
	Status     string    `json:"status"`
	ExpiresAt  time.Time `json:"expiresAt"`
}

// Ref is the grant as a decision records it: "<id>:<version>".
func (g Grant) Ref() string { return fmt.Sprintf("%s:%d", g.ID, g.Version) }

// BoundTo reports whether the grant was made under policyHash: its own hash, at least 12 hex
// digits, is a prefix of it. A grant under another policy is suspended until reconfirmed.
func (g Grant) BoundTo(policyHash string) bool {
	return len(g.PolicyHash) >= MinPolicyHash && strings.HasPrefix(policyHash, g.PolicyHash)
}

// Covers reports whether every path matches one of the grant's patterns, each anchored to the
// whole path. No path is covered by no pattern, and an empty list covers nothing.
func (g Grant) Covers(paths []string) bool {
	if len(paths) == 0 {
		return false
	}
	var res []*regexp.Regexp
	for _, p := range g.Files {
		re, err := regexp.Compile("^(?:" + p + ")$")
		if err != nil {
			return false // fail closed: a pattern that does not compile matches nothing
		}
		res = append(res, re)
	}
	return !slices.ContainsFunc(paths, func(p string) bool {
		return !slices.ContainsFunc(res, func(re *regexp.Regexp) bool { return re.MatchString(p) })
	})
}

// Predicate is what a maintainer asks a grant to match. Validate is the one check the broker's
// human API and its store both rely on.
type Predicate struct {
	Repository string   `json:"repository"`
	Class      string   `json:"class"`
	Files      []string `json:"files"`
	DiffShape  string   `json:"diffShape,omitempty"`
	PolicyHash string   `json:"policyHash"`
}

// Validate refuses a predicate the gate could not match exactly: an unknown shape, a pattern
// that does not compile or is too long, no pattern or too many, a repository or class that is not
// a name, a policy hash that is not 12 to 64 lowercase hex digits.
func (p Predicate) Validate() error {
	switch {
	case !repositoryRE.MatchString(p.Repository):
		return fmt.Errorf("repository %q is not a lowercase owner/name", p.Repository)
	case !classRE.MatchString(p.Class):
		return fmt.Errorf("class %q is not a class name", p.Class)
	case !hashRE.MatchString(p.PolicyHash):
		return fmt.Errorf("policyHash is 12 to 64 lowercase hex digits, the decision record's policy=")
	case len(p.Files) == 0 || len(p.Files) > MaxFiles:
		return fmt.Errorf("files holds 1 to %d patterns", MaxFiles)
	case p.DiffShape != "" && !slices.Contains(Shapes(), p.DiffShape):
		return fmt.Errorf("diffShape %q is not one of %v", p.DiffShape, Shapes())
	}
	for _, f := range p.Files {
		if f == "" || len(f) > MaxPatternLen {
			return fmt.Errorf("a file pattern is 1 to %d bytes", MaxPatternLen)
		}
		if _, err := regexp.Compile("^(?:" + f + ")$"); err != nil {
			return fmt.Errorf("file pattern %q is not a regular expression", f)
		}
	}
	return nil
}

// Exact is the pattern that matches path and nothing else: what "merge and create rule" proposes
// for each file the approval subset caught.
func Exact(path string) string { return regexp.QuoteMeta(path) }
