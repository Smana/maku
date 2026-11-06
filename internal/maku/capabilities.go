// SPDX-License-Identifier: Apache-2.0

package maku

import (
	"errors"
	"fmt"
	"path/filepath"
	"time"
)

// The record's bounds. The file is written by whatever agent the developer runs, so each field is
// one quoted line and the entries themselves are capped: a client nobody taught restraint must not
// grow maku's config directory.
const (
	maxClients   = 32 // one entry per client; past it, the client measured longest ago goes
	maxCapsField = 64 // a client name, a version, a protocol revision
)

// Caps is what one client declared about itself at initialize (fast-follow 4): the name and version
// it gives, the protocol revision it asked for, and the capabilities the relay acts on. A client
// nobody has seen has no entry — a reader says so, and never fills the gap with a guess.
type Caps struct {
	Client   string    `json:"client"`             // clientInfo.name: the key of the entry
	Version  string    `json:"version,omitempty"`  // clientInfo.version
	Protocol string    `json:"protocol,omitempty"` // the protocolVersion the client asked for
	Forms    bool      `json:"forms"`              // elicitation, form mode
	URLs     bool      `json:"urls"`               // elicitation, URL mode
	Roots    bool      `json:"roots"`              // answers roots/list
	Prompts  bool      `json:"prompts"`            // serves prompts/list
	At       time.Time `json:"at"`                 // when that client last initialized
}

// capsFile is capabilities.json: one entry per client, keyed by the name the client gives itself.
type capsFile struct {
	Clients map[string]Caps `json:"clients"`
}

// CapsStore is maku's record of what each client declared, in the directory maku keeps its config
// in: `maku doctor --client <name>` reads it, so a claim about a client is one maku observed rather
// than one it assumed (fast-follow 4).
type CapsStore struct {
	// Path is the record's file; CapsPath names it.
	Path string
}

// CapsPath is the record's file in dir, beside the config.json `maku configure` saves.
func CapsPath(dir string) string { return filepath.Join(dir, "capabilities.json") }

// Put records c under the client's own name, replacing that client's previous session: a client is
// measured by its last initialize, never by an average of them. The file is owner-only and written
// through a new file renamed into place, as every file maku keeps is.
func (s CapsStore) Put(c Caps) error {
	if s.Path == "" {
		return errors.New("capabilities: no file to write")
	}
	c.Client = capsField(c.Client)
	if c.Client == "" {
		return errors.New("capabilities: a client that gave no name is not recorded")
	}
	c.Version, c.Protocol = capsField(c.Version), capsField(c.Protocol)
	c.At = time.Now().UTC()
	f := s.load()
	f.Clients[c.Client] = c
	for len(f.Clients) > maxClients { // the oldest entry goes: a client seen once, long ago
		oldest := ""
		for name, e := range f.Clients {
			if oldest == "" || e.At.Before(f.Clients[oldest].At) {
				oldest = name
			}
		}
		delete(f.Clients, oldest)
	}
	if err := SaveJSON(s.Path, f); err != nil {
		return fmt.Errorf("capabilities: %w", err)
	}
	return nil
}

// Get is what was recorded for client, and whether that client was ever seen. An absent or
// unreadable file reads as nothing seen: the caller has to say it does not know.
func (s CapsStore) Get(client string) (Caps, bool) {
	c, ok := s.load().Clients[capsField(client)]
	return c, ok
}

// load reads the file; an absent or malformed one is an empty record, because a broken file must
// not stop the relay and the next Put rewrites it.
func (s CapsStore) load() capsFile {
	var f capsFile
	if s.Path != "" {
		_ = LoadJSON(s.Path, &f)
	}
	if f.Clients == nil {
		f.Clients = map[string]Caps{}
	}
	return f
}

// capsField is a declared string as the record keeps it: one printable line, bounded.
func capsField(s string) string {
	s = OneLine(s)
	if r := []rune(s); len(r) > maxCapsField {
		return string(r[:maxCapsField])
	}
	return s
}

// recordCaps writes what one client declared at initialize, under the name it gave, so that a later
// `maku doctor --client <name>` reports a measurement rather than an assumption (fast-follow 4). A
// write error is logged and never fails the initialize: the relay's job is the conversation.
func (r *Relay) recordCaps(c Caps) {
	if r.Dir == "" || c.Client == "" {
		return // a relay built as a probe, or a client that gave no name: there is nothing to key on
	}
	if err := (CapsStore{Path: CapsPath(r.Dir)}).Put(c); err != nil {
		r.log().Warn("capabilities not recorded", "client", c.Client, "err", err)
	}
}
