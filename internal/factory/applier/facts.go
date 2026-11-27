// SPDX-License-Identifier: Apache-2.0

package applier

import (
	"fmt"
	"strings"

	"github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/hardening"
)

// factsBlock is what the factory adds to every pull request body it writes, below the model's four
// headings (developer review gap 6): how to check the branch out, and the room. Only facts the
// factory owns go here; nothing the model wrote.
func factsBlock(t *v1alpha1.Task, roomsURL string) string {
	room := t.Status.RoomRef
	if room == "" {
		room = t.Name
	}
	var b strings.Builder
	fmt.Fprintf(&b, "## Try it\n\n- `maku checkout %s` checks this branch out into a new worktree, beside your checkout.\n", room)
	if u := strings.TrimSuffix(roomsURL, "/"); u != "" {
		fmt.Fprintf(&b, "- The room, with every step the agents took: %s/r/%s\n", u, room)
	}
	return b.String()
}

// prBody is a pull request body as the factory publishes it: the model's text, the factory's facts,
// then the provenance footer as the last paragraph. applyPR writes it and find compares it, so the
// two never disagree on what the factory would have written.
func prBody(t *v1alpha1.Task, body string, p hardening.Provenance, roomsURL string) string {
	text := strings.TrimRight(body, " \t\r\n")
	if text != "" {
		text += "\n\n"
	}
	return hardening.WithFooter(text+factsBlock(t, roomsURL), p)
}
