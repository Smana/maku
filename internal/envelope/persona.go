// SPDX-License-Identifier: Apache-2.0

package envelope

import "regexp"

var personaName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

// ValidPersona reports whether s is a persona name: lowercase, at most 32 characters, safe to
// render and to use as a CSS class suffix.
func ValidPersona(s string) bool { return personaName.MatchString(s) }
