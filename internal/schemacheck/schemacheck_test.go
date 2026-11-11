// SPDX-License-Identifier: Apache-2.0

package schemacheck

import (
	"slices"
	"testing"
)

func TestUntyped(t *testing.T) {
	for name, tc := range map[string]struct {
		schema string
		want   []string
	}{
		"typed":         {`{"type":"object","properties":{"a":{"type":"string"},"b":{"type":["string","null"]}}}`, nil},
		"enum only":     {`{"type":"object","properties":{"state":{"enum":["a"]}}}`, []string{"/properties/state"}},
		"array items":   {`{"type":"array","items":{"enum":["a"]}}`, []string{"/items"}},
		"deep":          {`{"type":"object","properties":{"a":{"type":"array","items":{"type":"object","properties":{"b":{}}}}}}`, []string{"/properties/a/items/properties/b"}},
		"anyOf typed":   {`{"anyOf":[{"type":"string"},{"type":"integer"}]}`, nil},
		"anyOf untyped": {`{"anyOf":[{"type":"string"},{"enum":[1]}]}`, []string{"(root)", "/anyOf/1"}},
		"map values":    {`{"type":"object","additionalProperties":{"description":"x"}}`, []string{"/additionalProperties"}},
		"bool closed":   {`{"type":"object","additionalProperties":false}`, nil},
	} {
		if got := Untyped([]byte(tc.schema)); !slices.Equal(got, tc.want) {
			t.Errorf("%s: Untyped = %v, want %v", name, got, tc.want)
		}
	}
}
