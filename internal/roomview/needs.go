// SPDX-License-Identifier: Apache-2.0

package roomview

import (
	"io"
	"strconv"
	"strings"

	"github.com/Smana/maku/internal/summary"
)

// Needs writes the caller's needs as a list to show as it is (CD21, CD28): one line per item — its kind,
// what it asks, and the one action's label, linked where that action happens — then the room it belongs
// to; and one line for the rooms whose state could not be read, so a list never passes them off as quiet
// (Review Focus 1). The items arrive in needs/v1's one order (summary.Ordered): this view never reorders
// them, so the tool, the CLI and the panel read one list.
func Needs(w io.Writer, items []summary.Need, unread []string, o Options) error {
	c := &card{o: o}
	head := c.bold("Needs across rooms")
	switch {
	case len(items) == 0:
		head += " · nothing needs you"
	default:
		// The words every view counts with (the overview's "N needs you").
		head += " · " + strconv.Itoa(len(items)) + " needs you"
	}
	c.line(head)
	for _, n := range items {
		c.line("- " + c.needItem(n))
	}
	if len(unread) > 0 {
		c.line("")
		c.line(unreadLine(unread, c))
	}
	_, err := io.WriteString(w, c.b.String())
	return err
}

// needItem is one item on one line: its kind (the broker's own vocabulary), what it asks, the action and
// where it happens, and the room. What is quoted as the room card quotes a need's words — an approval's
// What is the agent's, so only a code span holds it — and a What carrying the broker's own code spans
// (`/factory split`) is fenced wider rather than nested, which code's fence already does.
func (c *card) needItem(n summary.Need) string {
	parts := []string{c.text(n.Kind, false) + " — " + c.quoted(n.What, false)}
	if n.Action.Label != "" {
		url := n.Action.URL
		if url == "" {
			url = n.URL
		}
		parts = append(parts, "→ "+c.url(c.text(n.Action.Label, false), url))
	}
	if room := c.roomLink(n.Room); room != "" {
		parts = append(parts, "· "+room)
	}
	return strings.Join(parts, " ")
}

// roomLink is the item's room, linked to its page when the broker's rooms base builds it and named alone
// otherwise.
func (c *card) roomLink(room string) string {
	if room == "" {
		return ""
	}
	id := c.token(room, false)
	if base := strings.TrimRight(c.o.Rooms, "/"); base != "" {
		return "room " + c.url(id, base+"/r/"+room)
	}
	return "room " + id
}

// unreadLine is the one line for the rooms whose facts could not be read. They contribute no item, and
// the scan named only rooms the caller may read: a refusal to read never reveals another room's existence.
func unreadLine(unread []string, c *card) string {
	names := make([]string, len(unread))
	for i, room := range unread {
		names[i] = c.token(room, false)
	}
	s := plural(len(unread), "room", "rooms")
	if len(unread) == 1 {
		s += "'s state"
	} else {
		s += "' states"
	}
	s += " could not be read: " + strings.Join(names, ", ")
	if len(unread) == 1 {
		return s + "; its items are missing from this list"
	}
	return s + "; their items are missing from this list"
}
