// SPDX-License-Identifier: Apache-2.0

package mergepolicy

import (
	"testing"
)

// B8, B15, Q13 (plan PR4): the classes shape, and conditions.any with it, are refused, whatever
// else the file says, with the way out.
func TestTheClassesShapeIsRefused(t *testing.T) {
	want := "mergepolicy: classes: " + ErrClassesRemoved
	for name, in := range map[string]string{
		"a class": "factory: aws-0\nclasses:\n  docs: {mode: shadow, author: agents, harness: p}\n",
		"any": "factory: aws-0\nclasses:\n  c:\n    mode: shadow\n    author: agents\n    harness: p\n" +
			"    conditions:\n      any:\n        - [{diffShape: links-only}]\n        - [{allowedFiles: ['^docs/']}]\n",
		"empty":          "factory: aws-0\nclasses: {}\n",
		"beside binding": "factory: aws-0\nclasses: {}\nmergePolicies: [{name: docs, mode: shadow}]\n",
		"not even a map": "factory: aws-0\nclasses: [no]\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(in)); err == nil || err.Error() != want {
				t.Fatalf("got %v, want %q", err, want)
			}
		})
	}
}
