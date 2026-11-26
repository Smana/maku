// SPDX-License-Identifier: Apache-2.0

// Package browsergate is the loopback gate in front of a run's Playwright MCP server: the one place
// that decides which browser tools the harness may call, which URLs the agent may open, how many
// screenshots a run takes and how large each one is, and that keeps a copy of every screenshot for
// the room. Everything it allows is an exact match against a short list; anything else is refused.
//
// The gate is one of three layers that keep the browser on loopback. It refuses the agent's own
// requests; Chromium's proxy and resolver flags in the maku-browser image refuse what a page asks
// for (redirects, subresources). Neither stops a renderer exploit from reaching what the pod's
// network policy allows, which is the harness's own trust domain: no credential lives in the
// browser sidecar.
package browsergate

import (
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
)

// Allowed is the tool allow-list, @playwright/mcp 0.0.85's names. Anything else, a tool a later
// release adds or one a page registers, is neither listed nor served. browser_evaluate,
// browser_run_code_unsafe, browser_file_upload, browser_drop and browser_drag are left out on
// purpose: each runs page script or moves files.
var Allowed = []string{
	"browser_navigate", "browser_navigate_back", "browser_snapshot", "browser_take_screenshot",
	"browser_click", "browser_hover", "browser_type", "browser_fill_form", "browser_select_option",
	"browser_press_key", "browser_resize", "browser_emulate_media", "browser_handle_dialog",
	"browser_wait_for", "browser_find", "browser_console_messages", "browser_network_requests",
	"browser_tabs", "browser_close",
}

// ScreenshotTool is the one allowed tool whose answer carries an image.
const ScreenshotTool = "browser_take_screenshot"

// The viewport ceiling browser_resize may ask for.
const (
	MaxViewportWidth  = 1920
	MaxViewportHeight = 1080
)

// The refusals CheckCall and Counter report. Each one's text is the code the model reads.
var (
	ErrToolNotAllowed = errors.New("tool_not_allowed")
	ErrURLRefused     = errors.New("url_refused")
	ErrViewport       = errors.New("viewport")
	ErrCapReached     = errors.New("screenshot_cap_reached")
)

// loopbackHosts are the only hostnames a URL may name, compared as literals: 127.1, 0x7f000001
// and *.nip.io resolve to loopback too, and are refused because they do not match.
var loopbackHosts = []string{"127.0.0.1", "localhost", "::1"}

// CheckURL accepts an http URL to a loopback host named literally, with no user info. It is the
// rule for every URL a tool argument carries.
func CheckURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%w: not a URL", ErrURLRefused)
	}
	if u.Scheme != "http" || u.User != nil || u.Opaque != "" || !slices.Contains(loopbackHosts, u.Hostname()) {
		return fmt.Errorf("%w: only http://127.0.0.1, http://localhost and http://[::1] are reachable", ErrURLRefused)
	}
	return nil
}

// CheckCall refuses a tools/call the gate must not forward: a tool off the allow-list, a url
// argument that is not literal loopback (on any tool, browser_navigate requiring one), or a
// browser_resize past the viewport ceiling.
func CheckCall(name string, args map[string]any) error {
	if !slices.Contains(Allowed, name) {
		return fmt.Errorf("%w: %q is not served by this gate", ErrToolNotAllowed, name)
	}
	raw, has := args["url"]
	if name == "browser_navigate" && !has {
		return fmt.Errorf("%w: browser_navigate needs a url", ErrURLRefused)
	}
	if has {
		s, ok := raw.(string)
		if !ok {
			return fmt.Errorf("%w: url is not a string", ErrURLRefused)
		}
		if err := CheckURL(s); err != nil {
			return err
		}
	}
	if name == "browser_resize" {
		if !dimension(args["width"], MaxViewportWidth) || !dimension(args["height"], MaxViewportHeight) {
			return fmt.Errorf("%w: width and height must be whole numbers up to %dx%d", ErrViewport, MaxViewportWidth, MaxViewportHeight)
		}
	}
	return nil
}

// dimension accepts a JSON number that is a whole number in [1, limit].
func dimension(v any, limit int) bool {
	var f float64
	switch n := v.(type) {
	case float64:
		f = n
	case interface{ Float64() (float64, error) }: // json.Number, under a UseNumber decoder
		var err error
		if f, err = n.Float64(); err != nil {
			return false
		}
	default:
		return false
	}
	return f >= 1 && f <= float64(limit) && f == math.Trunc(f)
}

// FilterTools keeps the tools/list entries whose name is on the allow-list, in their order.
// browser_take_screenshot stays listed whatever the run's model: without vision the gate keeps the
// image for the room and answers the model with text.
func FilterTools(tools []map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		if n, ok := t["name"].(string); ok && slices.Contains(Allowed, n) {
			out = append(out, t)
		}
	}
	return out
}

// Counter is a run's screenshot count, persisted in Path as a decimal so a restarted sidecar
// cannot hand out the cap again. Max is the run's cap.
type Counter struct {
	Path string
	Max  int

	mu sync.Mutex
}

// Take reserves the next screenshot and returns its number, from 1, or ErrCapReached once Max are
// taken. A count file it cannot read fails closed.
func (c *Counter) Take() (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	b, err := os.ReadFile(c.Path)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return 0, fmt.Errorf("read screenshot count: %w", err)
	default:
		if n, err = strconv.Atoi(strings.TrimSpace(string(b))); err != nil || n < 0 {
			return 0, fmt.Errorf("screenshot count %q is not a count", b)
		}
	}
	if n >= c.Max {
		return 0, fmt.Errorf("%w: this run's %d screenshots are taken", ErrCapReached, c.Max)
	}
	n++
	if err := writeAtomic(c.Path, []byte(strconv.Itoa(n)+"\n")); err != nil {
		return 0, fmt.Errorf("write screenshot count: %w", err)
	}
	return n, nil
}

// writeAtomic writes b to path through a temporary file in the same directory and a rename, so a
// reader never sees half a file.
func writeAtomic(path string, b []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, werr := f.Write(b)
	cerr := f.Close()
	if err := errors.Join(werr, cerr); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
