// SPDX-License-Identifier: Apache-2.0

package config

import (
	"slices"
	"strings"
)

// Ambiguous reports whether two adopted classes could both adopt one pull request (B6, plan PR9):
// the same diffShape, a login in common (GitHub logins compare case-insensitively), and a branch
// prefix of one that is a prefix of one of the other's. Central bindings and the stage-4 file's
// read it alike.
func Ambiguous(a, b Class) bool {
	if !a.Adopted() || !b.Adopted() || a.DiffShape != b.DiffShape {
		return false
	}
	login := slices.ContainsFunc(a.Author.Adopt.Logins, func(x string) bool {
		return slices.ContainsFunc(b.Author.Adopt.Logins, func(y string) bool { return strings.EqualFold(x, y) })
	})
	branch := slices.ContainsFunc(a.Author.Adopt.Branches, func(x string) bool {
		return slices.ContainsFunc(b.Author.Adopt.Branches, func(y string) bool {
			x, y := strings.TrimPrefix(x, "^"), strings.TrimPrefix(y, "^")
			return strings.HasPrefix(x, y) || strings.HasPrefix(y, x)
		})
	})
	return login && branch
}

// AmbiguousWithAny reports whether cl is Ambiguous with one of classes.
func AmbiguousWithAny(cl Class, classes map[string]Class) bool {
	for _, other := range classes {
		if Ambiguous(cl, other) {
			return true
		}
	}
	return false
}
