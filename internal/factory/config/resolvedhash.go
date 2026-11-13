// SPDX-License-Identifier: Apache-2.0

package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// ResolvedHash is what an approval grant binds to (B13, Q9): the sha256 of the canonical JSON of
// a resolved class's variants, each with its mode cleared, so promoting a class keeps its grants
// and any other edit to the class suspends them. A binding has one variant. Lists are hashed as
// written: reordering holdoutChecks is a change, as mergepolicy.Structural reads it.
func ResolvedHash(variants ...Class) string {
	vs := make([]Class, len(variants))
	for i, v := range variants {
		v.Live, v.Shadow = false, false
		vs[i] = v
	}
	b, err := json.Marshal(vs)
	if err != nil { // a Class holds no type json cannot encode
		panic(err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// ShortHash is a hash's first 12 hex digits, as the decision record prints it (resolved=).
func ShortHash(h string) string {
	if len(h) < 12 {
		return h
	}
	return h[:12]
}
