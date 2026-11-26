// SPDX-License-Identifier: Apache-2.0

package browsergate

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestCheckCallURLs(t *testing.T) {
	ok := []string{"http://127.0.0.1:1313/", "http://localhost:5173/docs", "http://[::1]:3000", "http://127.0.0.1/"}
	bad := []string{"https://example.com", "http://169.254.169.254/latest", "http://room-broker.maku.svc:8443/",
		"file:///etc/passwd", "data:text/html,x", "chrome://settings", "http://127.0.0.1.nip.io/", "https://127.0.0.1:8443/",
		"http://localhost@evil.com/", "http://user:pw@127.0.0.1/", "http://0x7f000001/", "http://127.1/", "http://2130706433/",
		"http://[::ffff:127.0.0.1]/", "HTTP://LOCALHOST.:80/", "//127.0.0.1/", "", "javascript:alert(1)", "about:blank",
		"view-source:http://127.0.0.1/", "ws://127.0.0.1:1/"}
	for _, u := range ok {
		if err := CheckCall("browser_navigate", map[string]any{"url": u}); err != nil {
			t.Errorf("%s: %v", u, err)
		}
	}
	for _, u := range bad {
		if err := CheckCall("browser_navigate", map[string]any{"url": u}); !errors.Is(err, ErrURLRefused) {
			t.Errorf("%q: %v", u, err)
		}
	}
}

func TestCheckCallURLArgumentOnAnyTool(t *testing.T) {
	cases := []struct {
		name string
		args map[string]any
		want error
	}{
		{"navigate without a url is refused", map[string]any{}, ErrURLRefused},
		{"a url that is not a string is refused", map[string]any{"url": 3.0}, ErrURLRefused},
		{"a url argument on another tool is checked too", map[string]any{"action": "new", "url": "https://example.com"}, ErrURLRefused},
		{"a loopback url on another tool passes", map[string]any{"action": "new", "url": "http://localhost:3000/"}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tool := "browser_tabs"
			if c.name == "navigate without a url is refused" {
				tool = "browser_navigate"
			}
			if err := CheckCall(tool, c.args); !errors.Is(err, c.want) {
				t.Errorf("got %v, want %v", err, c.want)
			}
		})
	}
}

func TestCheckCallTools(t *testing.T) {
	for _, n := range []string{"browser_evaluate", "browser_run_code_unsafe", "browser_file_upload", "browser_drop", "browser_drag", "anything_new", ""} {
		if err := CheckCall(n, nil); !errors.Is(err, ErrToolNotAllowed) {
			t.Errorf("%s: %v", n, err)
		}
	}
	for _, n := range Allowed {
		args := map[string]any{}
		switch n {
		case "browser_navigate":
			args["url"] = "http://127.0.0.1:8080/"
		case "browser_resize":
			args["width"], args["height"] = 1280.0, 800.0
		}
		if err := CheckCall(n, args); err != nil {
			t.Errorf("%s: %v", n, err)
		}
	}
}

func TestCheckCallViewport(t *testing.T) {
	cases := []struct {
		name string
		args map[string]any
		ok   bool
	}{
		{"the ceiling itself passes", map[string]any{"width": 1920.0, "height": 1080.0}, true},
		{"a width over the ceiling is refused", map[string]any{"width": 1921.0, "height": 1080.0}, false},
		{"a height over the ceiling is refused", map[string]any{"width": 1280.0, "height": 1081.0}, false},
		{"zero is refused", map[string]any{"width": 0.0, "height": 800.0}, false},
		{"a fraction is refused", map[string]any{"width": 1280.5, "height": 800.0}, false},
		{"a string is refused", map[string]any{"width": "1280", "height": 800.0}, false},
		{"a missing height is refused", map[string]any{"width": 1280.0}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := CheckCall("browser_resize", c.args)
			if c.ok && err != nil || !c.ok && !errors.Is(err, ErrViewport) {
				t.Errorf("got %v", err)
			}
		})
	}
}

func names(tools []map[string]any) []string {
	var out []string
	for _, t := range tools {
		out = append(out, t["name"].(string))
	}
	return out
}

func TestFilterTools(t *testing.T) {
	in := []map[string]any{{"name": "browser_snapshot"}, {"name": "browser_take_screenshot"}, {"name": "browser_evaluate"},
		{"name": "browser_run_code_unsafe"}, {"name": "webmcp_page_tool"}, {"name": 3}, {}}
	if got := names(FilterTools(in)); !slices.Equal(got, []string{"browser_snapshot", "browser_take_screenshot"}) {
		t.Error(got)
	}
}

func TestCounterSurvivesRestart(t *testing.T) {
	p := filepath.Join(t.TempDir(), "count")
	c := &Counter{Path: p, Max: 2}
	if n, err := c.Take(); n != 1 || err != nil {
		t.Fatal(n, err)
	}
	c2 := &Counter{Path: p, Max: 2} // a restarted sidecar
	if n, err := c2.Take(); n != 2 || err != nil {
		t.Fatal(n, err)
	}
	if _, err := c2.Take(); !errors.Is(err, ErrCapReached) {
		t.Fatal(err)
	}
}

func TestCounterFailsClosedOnACorruptFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "count")
	if err := os.WriteFile(p, []byte("not a number"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := (&Counter{Path: p, Max: 5}).Take(); err == nil {
		t.Fatal("a corrupt count must not reset to zero")
	}
}
