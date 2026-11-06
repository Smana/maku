// SPDX-License-Identifier: Apache-2.0

package maku

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"
)

// localRelay is a relay served on stdio, with the client's side of that stdio: a test reads what the
// relay recorded off the relay itself, and sends the messages the client would.
type localRelay struct {
	*Relay
	session
}

// relayAt is a relay whose files live in dir: capabilities.json is recorded beside config.json.
func relayAt(t *testing.T, dir string) localRelay {
	t.Helper()
	var rel *Relay
	s := relayTo(t, newMCPBroker(t), tok, func(r *Relay) { rel = r; r.Dir = dir })
	return localRelay{Relay: rel, session: s}
}

// Fast-follow 4: the relay records the client's own capabilities under its name and version.
func TestInitializeRecordsTheClientsCapabilities(t *testing.T) {
	r := relayAt(t, t.TempDir())
	r.request(t, 1, "initialize", json.RawMessage(`{"protocolVersion":"2025-06-18","capabilities":{"elicitation":{},"roots":{},"prompts":{}},
		"clientInfo":{"name":"claude-code","version":"2.0.1"}}`))
	got, ok := (CapsStore{Path: CapsPath(r.Dir)}).Get("claude-code")
	if !ok || !got.Forms || !got.Roots || !got.Prompts || got.Version != "2.0.1" || got.Protocol != "2025-06-18" {
		t.Fatalf("%+v %v", got, ok)
	}
}

// A client that declares nothing is recorded as declaring nothing: never left unmeasured once seen.
func TestAClientThatDeclaresNothing(t *testing.T) {
	r := relayAt(t, t.TempDir())
	r.request(t, 1, "initialize", json.RawMessage(`{"protocolVersion":"2025-06-18","clientInfo":{"name":"codex","version":"0.9.0"}}`))
	got, ok := (CapsStore{Path: CapsPath(r.Dir)}).Get("codex")
	if !ok || got.Forms || got.URLs || got.Roots || got.Prompts {
		t.Fatalf("%+v %v", got, ok)
	}
}

// The file keeps one entry per client: a second session replaces the first.
func TestCapsStoreKeepsOneEntryPerClient(t *testing.T) {
	store := CapsStore{Path: CapsPath(t.TempDir())}
	if err := store.Put(Caps{Client: "claude-code", Version: "2.0.0"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Put(Caps{Client: "claude-code", Version: "2.0.1", Forms: true}); err != nil {
		t.Fatal(err)
	}
	got, ok := store.Get("claude-code")
	if !ok || got.Version != "2.0.1" || !got.Forms {
		t.Fatalf("%+v %v", got, ok)
	}
}

// A client nobody has met has no entry: the record leaves the answer to the reader, which says it
// does not know rather than describing a client maku never saw.
func TestCapsStoreSaysNothingAboutAnUnseenClient(t *testing.T) {
	store := CapsStore{Path: CapsPath(t.TempDir())}
	if err := store.Put(Caps{Client: "claude-code", Version: "2.0.1"}); err != nil {
		t.Fatal(err)
	}
	if got, ok := store.Get("codex"); ok {
		t.Fatalf("%+v", got)
	}
}

// The file is written by whatever agent the developer runs, so it is bounded: a name or a version is
// one printable line of at most maxCapsField runes, and the entry is keyed by that bounded name —
// never by the raw one, which nothing else would match.
func TestCapsStoreIsBounded(t *testing.T) {
	path := CapsPath(t.TempDir())
	store := CapsStore{Path: path}
	raw := "claude\tcode\n" + strings.Repeat("x", maxCapsField)
	bounded := "claude code " + strings.Repeat("x", 52) // one line, cut at 64 runes
	if err := store.Put(Caps{Client: raw, Version: "2.0\n1"}); err != nil {
		t.Fatal(err)
	}
	got, ok := store.Get(bounded)
	if !ok || got.Client != bounded || got.Version != "2.0 1" {
		t.Fatalf("%+v %v", got, ok)
	}
	var f capsFile
	if err := LoadJSON(path, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Clients) != 1 {
		t.Fatalf("%d entries", len(f.Clients))
	}
	if _, ok := f.Clients[bounded]; !ok || len([]rune(bounded)) != maxCapsField {
		t.Fatalf("keys are %v, %d runes", f.Clients, len([]rune(bounded)))
	}
}

// Past maxClients the client measured longest ago goes — not the newest, and not the one just
// written. The seeded entries carry their own At, so eviction is decided by the record, not by how
// fast the test wrote them.
func TestCapsStoreEvictsTheClientMeasuredLongestAgo(t *testing.T) {
	path := CapsPath(t.TempDir())
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	seeded := capsFile{Clients: map[string]Caps{}}
	for i := range maxClients {
		name := "client-" + strconv.Itoa(i) // oldest first: client-0 was measured longest ago
		seeded.Clients[name] = Caps{Client: name, Version: "1", At: base.Add(time.Duration(i) * time.Hour)}
	}
	if err := SaveJSON(path, seeded); err != nil {
		t.Fatal(err)
	}
	store := CapsStore{Path: path}
	if err := store.Put(Caps{Client: "codex", Version: "0.9.0"}); err != nil { // the newest entry, now
		t.Fatal(err)
	}
	var f capsFile
	if err := LoadJSON(path, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Clients) != maxClients {
		t.Fatalf("%d entries", len(f.Clients))
	}
	if _, ok := store.Get("client-0"); ok {
		t.Fatal("the oldest entry was kept")
	}
	if _, ok := store.Get("client-1"); !ok {
		t.Fatal("the second oldest was evicted: eviction is not by At")
	}
	if got, ok := store.Get("codex"); !ok || got.Version != "0.9.0" {
		t.Fatalf("the entry just written went: %+v %v", got, ok)
	}
}

// A store with no file refuses rather than writing somewhere unintended.
func TestCapsStoreRefusesAnEmptyPath(t *testing.T) {
	if err := (CapsStore{Path: ""}).Put(Caps{Client: "claude-code"}); err == nil {
		t.Fatal("a store with no file wrote one")
	}
}
