// SPDX-License-Identifier: Apache-2.0

package forge

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
)

// SnapshotRaw is the text an issue's task snapshot is taken from: its title and body, as a
// maintainer labelled them.
func SnapshotRaw(i Issue) string { return "# " + i.Title + "\n\n" + i.Body }

// SnapshotSHA256 is the sha256 of SnapshotRaw, hex: what a task records as its source's
// ContentSHA256 (intake), and what forge_read compares to vouch for the issue as labelled.
func SnapshotSHA256(i Issue) string {
	sum := sha256.Sum256([]byte(SnapshotRaw(i)))
	return hex.EncodeToString(sum[:])
}

// Generation is the latest label's place among every label event of one label, whoever labelled
// (R4, F-5): GitHub never deletes an event, so each label keeps its generation and a later one is
// always higher. ok is false when the latest label is not a maintainer's. intake reads the trigger
// label with it, forge_read the factory/vouch label.
func Generation(evs []LabelEvent, isMaintainer func(string) bool) (int, LabelEvent, bool) {
	if len(evs) == 0 {
		return 0, LabelEvent{}, false
	}
	evs = slices.Clone(evs)
	slices.SortStableFunc(evs, func(a, b LabelEvent) int { return a.At.Compare(b.At) })
	last := evs[len(evs)-1]
	return len(evs), last, isMaintainer(last.Actor)
}
