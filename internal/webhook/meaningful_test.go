// SPDX-License-Identifier: Apache-2.0

package webhook

import (
	"slices"
	"testing"
)

// CD11's table is a decision per type: every type of the vocabulary has a row, true or false, and no
// row names a type the vocabulary does not hold, so a type added to Types fails here until someone
// decides whether a person is told about it.
func TestMeaningfulCoversTheVocabulary(t *testing.T) {
	for _, typ := range Types() {
		if _, ok := notify[typ]; !ok {
			t.Errorf("%s has no row in the notify table", typ)
		}
	}
	for typ := range notify {
		if !slices.Contains(Types(), typ) {
			t.Errorf("the notify table names %s, which is not a webhook type", typ)
		}
	}
	for _, typ := range Notify() {
		if !Meaningful(typ) || !slices.Contains(Types(), typ) {
			t.Errorf("Notify lists %s, which is not a meaningful webhook type", typ)
		}
	}
	if Meaningful("room.run_started") {
		t.Error("a run starting is news again (CD11)")
	}
}
