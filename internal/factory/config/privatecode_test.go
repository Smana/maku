// SPDX-License-Identifier: Apache-2.0

package config

import (
	"strings"
	"testing"
)

// v4 D3: a repository's privateCode.acknowledged states its code is sent to the configured models.
// Unset is no acknowledgement; true is one; an explicit false says nothing and is refused.
func TestPrivateCodeAcknowledgement(t *testing.T) {
	with := func(v string) string {
		raw := good
		if v == "" {
			return raw
		}
		return strings.Replace(raw, "  - name: Smana/cloud-native-ref\n", "  - name: Smana/cloud-native-ref\n    privateCode: "+v+"\n", 1)
	}
	for name, tc := range map[string]struct {
		in    string
		acked bool
		want  string
	}{
		"unset":        {in: with("")},
		"acknowledged": {in: with("{acknowledged: true}"), acked: true},
		"false":        {in: with("{acknowledged: false}"), want: "privateCode.acknowledged: false says nothing"},
		"empty":        {in: with("{}"), want: "privateCode.acknowledged: false says nothing"},
		"unknown key":  {in: with("{acknowledged: true, models: [x]}"), want: "unknown field"},
	} {
		t.Run(name, func(t *testing.T) {
			c, err := Parse([]byte(tc.in))
			if tc.want != "" {
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("got %v, want %q", err, tc.want)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := c.RepositoryFor("Smana/cloud-native-ref").PrivateCodeAcknowledged(); got != tc.acked {
				t.Fatalf("acknowledged %v, want %v", got, tc.acked)
			}
		})
	}
	var none *Repository
	if none.PrivateCodeAcknowledged() {
		t.Fatal("a repository the config does not serve acknowledges nothing")
	}
}
