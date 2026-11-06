// SPDX-License-Identifier: Apache-2.0

package app

import (
	"bufio"
	"io"
	"testing"

	"github.com/Smana/maku/internal/maku"
)

// Fast-follow 4: `maku mcp` hands the relay maku's own directory, so what the client declares at
// initialize is recorded beside config.json and `maku doctor --client <name>` has a measurement to
// print. A relay built without that directory records nothing, so this is the wiring's only test.
func TestMakuMCPRecordsTheClientsCapabilities(t *testing.T) {
	b := newLocalBroker(t)
	r, _ := configuredLocal(t, b)
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	r.In, r.Out = inR, outW
	done := make(chan error, 1)
	go func() { done <- r.Run(t.Context(), []string{"mcp"}) }()
	initialize := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18",` +
		`"capabilities":{"elicitation":{},"prompts":{}},"clientInfo":{"name":"claude-code","version":"2.0.1"}}}` + "\n"
	if _, err := inW.Write([]byte(initialize)); err != nil {
		t.Fatal(err)
	}
	// The answer comes after the relay recorded: reading it is what makes the file's presence certain.
	if line, err := bufio.NewReader(outR).ReadString('\n'); err != nil || line == "" {
		t.Fatalf("mcp: %q %v", line, err)
	}
	_ = inW.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	got, ok := (maku.CapsStore{Path: maku.CapsPath(r.Dir)}).Get("claude-code")
	if !ok || !got.Forms || !got.Prompts || got.Version != "2.0.1" || got.Protocol != "2025-06-18" {
		t.Fatalf("%+v %v", got, ok)
	}
}
