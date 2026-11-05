// SPDX-License-Identifier: Apache-2.0

package roomview

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// briefGoldens are the three briefs golden 4 draws: a full read, one with no cursor, and the hostile
// title of Review Focus 4.
var briefGoldens = []string{"normal", "no-cursor", "hostile"}

// loadBrief decodes one brief fixture: the file is the renderer's whole input, its now included.
func loadBrief(t *testing.T, name string) BriefIn {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "brief", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var in BriefIn
	if err := json.Unmarshal(b, &in); err != nil {
		t.Fatal(err)
	}
	return in
}

// CD11: the brief is fixed prose and quoted data, and every rendering parses inert (offline proof 8).
func TestBriefGoldens(t *testing.T) {
	for _, name := range briefGoldens {
		t.Run(name, func(t *testing.T) {
			in := loadBrief(t, name)
			if len(in.Untrusted()) == 0 {
				t.Fatal("the fixture has no untrusted string: the inert assertion would be vacuous")
			}
			var b strings.Builder
			if err := Brief(&b, in, Options{Format: Markdown, Now: in.Now, Rooms: rooms}); err != nil {
				t.Fatal(err)
			}
			golden(t, "brief/"+name+".md", b.String())
			assertInert(t, b.String(), rooms, in.Untrusted())
		})
	}
}
