// SPDX-License-Identifier: Apache-2.0

package maku

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
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

// The file is written by whatever agent the developer runs, so it is bounded: past maxClients the
// entry measured longest ago goes, and no field is longer than one line.
func TestCapsStoreIsBounded(t *testing.T) {
	path := CapsPath(t.TempDir())
	store := CapsStore{Path: path}
	for i := range maxClients + 1 {
		name := "client-" + strconv.Itoa(i)
		if err := store.Put(Caps{Client: name + "\n" + strings.Repeat("x", maxCapsField), Version: "1"}); err != nil {
			t.Fatal(err)
		}
	}
	var f capsFile
	if err := LoadJSON(path, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Clients) != maxClients {
		t.Fatalf("%d entries", len(f.Clients))
	}
	for name := range f.Clients {
		if strings.ContainsAny(name, "\n") || len([]rune(name)) > maxCapsField {
			t.Fatalf("unbounded %q", name)
		}
	}
	if _, ok := store.Get("client-0"); ok {
		t.Fatal("the oldest entry was kept")
	}
}

// A store with no file refuses rather than writing somewhere unintended.
func TestCapsStoreRefusesAnEmptyPath(t *testing.T) {
	if err := (CapsStore{Path: ""}).Put(Caps{Client: "claude-code"}); err == nil {
		t.Fatal("a store with no file wrote one")
	}
}
