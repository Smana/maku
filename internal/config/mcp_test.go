// SPDX-License-Identifier: Apache-2.0

package config

import (
	"strings"
	"testing"
)

// Factory interfaces F2, behind a switch (D4): /mcp is off unless enabled, and on it needs the
// maku client, whose tokens are the only ones it takes.
func TestLoadMCP(t *testing.T) {
	c, err := Load(write(t, valid))
	if err != nil || c.Human.MCP.Enabled {
		t.Fatalf("off by default: %+v, %v", c.Human.MCP, err)
	}
	maku := strings.Replace(valid, "  projectIDFile:", "  roomctlClientIDFile: /etc/room-broker/roomctl/client-id\n  projectIDFile:", 1)
	c, err = Load(write(t, maku+"  mcp:\n    enabled: true\n"))
	if err != nil || !c.Human.MCP.Enabled {
		t.Fatalf("on: %+v, %v", c.Human.MCP, err)
	}
	if _, err := Load(write(t, valid+"  mcp:\n    enabled: true\n")); err == nil || !strings.Contains(err.Error(), "human.mcp.enabled") {
		t.Fatalf("on without the maku client: %v", err)
	}
}

// human.mcp.sessionStart was removed in favour of human.mcp.start.
func TestLoadMCPSessionStart(t *testing.T) {
	maku := strings.Replace(valid, "  projectIDFile:", "  roomctlClientIDFile: /etc/room-broker/roomctl/client-id\n  projectIDFile:", 1)
	if _, err := Load(write(t, maku+access+"  mcp:\n    enabled: true\n    sessionStart: [Smana/a]\n")); err == nil || !strings.Contains(err.Error(), "human.mcp.sessionStart was removed: use human.mcp.start") {
		t.Fatalf("want removal error, got %v", err)
	}
	if c, err := Load(write(t, maku+"  mcp:\n    enabled: true\n")); err != nil || c.Human.MCP.SessionStart != nil {
		t.Fatalf("absent by default: %+v, %v", c.Human.MCP, err)
	}
}
