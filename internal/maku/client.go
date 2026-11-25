// SPDX-License-Identifier: Apache-2.0

package maku

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/coordinationview"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/factory/coordination"
	"github.com/Smana/maku/internal/factory/stages"
	"github.com/Smana/maku/internal/httpx"
	"github.com/Smana/maku/internal/mcpwire"
	"github.com/Smana/maku/internal/roomview"
	"github.com/Smana/maku/internal/wire"
)

// Bounds on what the broker sends: the room list, and one frame (a state
// frame's queue and approvals, or an event of up to 64 KiB).
const (
	maxRoomsBody = 8 << 20
	maxFrame     = 4 << 20
	maxBackoff   = 30 * time.Second
)

// Client calls one broker as the developer, through oauth2-proxy, which lets
// the bearer through (skip-jwt-bearer-tokens).
type Client struct {
	URL     string                                // https://rooms.<private domain>
	Token   func(context.Context) (string, error) // a valid access token, asked for on every call
	HC      *http.Client                          // httpx: the room list
	Stream  *http.Client                          // httpx with no whole-exchange timeout: the WebSocket
	Backoff time.Duration                         // the first wait before a reconnect; zero is a second
	Issuer  string                                // the ZITADEL issuer, named in the unlinked hint
	Err     io.Writer                             // hints for the developer; nil is stderr
}

// RoomFilter narrows the room list; the broker applies it.
type RoomFilter struct {
	Repo          string // owner/name
	Mine, NeedsMe bool
	All           bool            // list the rooms that ended more than a week ago too; the overview filters, not the query
	Format        roomview.Format // how the overview is written; the zero value is Markdown, which is not for a terminal: callers set it
	JSON          bool            // print the broker's rows unchanged; not part of the query
}

func (f RoomFilter) query() string {
	q := url.Values{}
	if f.Repo != "" {
		q.Set("repo", f.Repo)
	}
	if f.Mine {
		q.Set("mine", "1")
	}
	if f.NeedsMe {
		q.Set("needs_me", "1")
	}
	return q.Encode()
}

// Refusal is the broker refusing a request before any room data: retrying
// cannot help, the developer has to act.
type Refusal struct {
	Op     string
	Code   int
	Reason string // the broker's reason, when it names one maku explains
	// Refused is the code a JSON refusal body names ({"error":"not_found"}), when it is a bounded code.
	Refused string
	// Repository is the repository the request asked about, and Broker the broker's URL: set, a
	// not_found refusal names the repository and how to make it readable, never "no such room".
	Repository, Broker string
}

// userInactive is the broker's 401 for a ZITADEL user no longer active (#126).
const userInactive = "user_inactive"

// refusal is resp as a Refusal, reading the reason from its body.
func refusal(op string, resp *http.Response) *Refusal {
	r := &Refusal{Op: op, Code: resp.StatusCode}
	if resp.Body != nil {
		body, _ := httpx.ReadBody(resp.Body, 1<<10)
		if strings.TrimSpace(string(body)) == userInactive {
			r.Reason = userInactive
		}
		var j struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(body, &j) == nil && coordCodeRE.MatchString(j.Error) {
			r.Refused = j.Error
		}
	}
	return r
}

// repoRefusal is resp as a Refusal of a request about repository, which a not_found explains.
func (c Client) repoRefusal(op, repository string, resp *http.Response) *Refusal {
	r := refusal(op, resp)
	r.Repository, r.Broker = repository, c.URL
	return r
}

func (r *Refusal) Error() string {
	why := http.StatusText(r.Code)
	switch {
	case r.Reason == userInactive:
		why = userInactive + ": your ZITADEL user is not active; an admin must reactivate it"
	case r.Code == http.StatusUnauthorized:
		why = "the token was refused: run maku login"
	case r.Code == http.StatusForbidden:
		why = "not in an agents group, or not allowed in this room"
	case r.Repository != "" && r.Refused == "not_found":
		why = "not_found: repository " + r.Repository + " not found, or not readable by your linked GitHub account: run maku doctor, or link GitHub at " +
			strings.TrimRight(r.Broker, "/") + "/link/github"
	case r.Repository != "" && r.Refused == reasonLinkUnverified:
		why = reasonLinkUnverified + ": the factory could not check your GitHub link to " + r.Repository + ": run maku doctor, then retry"
	case r.Refused != "":
		why = r.Refused
	case r.Code == http.StatusNotFound:
		why = "no such room"
	}
	return fmt.Sprintf("%s: %d: %s", r.Op, r.Code, why)
}

// reasonLinkUnverified is the factory's 503 when it cannot check the caller's GitHub link.
const reasonLinkUnverified = "link_unverified"

// errToken is no access token to dial with: the token file is gone, or the
// issuer refused its refresh. Dialling again cannot help.
var errToken = errors.New("no access token")

func (c Client) header(ctx context.Context) (http.Header, error) {
	tok, err := c.Token(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errToken, err)
	}
	h := http.Header{}
	if tok != "" { // a local broker takes no token, and an empty bearer would be a header it must refuse
		h.Set("Authorization", "Bearer "+tok)
	}
	return h, nil
}

// roomsBody is GET /api/rooms as the broker sent it, with its X-Rooms-Access (ruling R21).
func (c Client) roomsBody(ctx context.Context, f RoomFilter) (body []byte, access string, err error) {
	h, err := c.header(ctx)
	if err != nil {
		return nil, "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL+"/api/rooms"+withQuery(f.query()), nil)
	if err != nil {
		return nil, "", err
	}
	req.Header = h
	resp, err := c.HC.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("rooms: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, "", refusal("rooms", resp)
	}
	body, err = httpx.ReadBody(resp.Body, maxRoomsBody)
	if err != nil {
		return nil, "", fmt.Errorf("rooms: %w", err)
	}
	return body, resp.Header.Get("X-Rooms-Access"), nil
}

// RoomRows is the rooms the developer may read, and X-Rooms-Access, which says why the list may be short.
func (c Client) RoomRows(ctx context.Context, f RoomFilter) ([]roomview.Row, string, error) {
	body, access, err := c.roomsBody(ctx, f)
	if err != nil {
		return nil, "", err
	}
	var rows []roomview.Row
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, "", fmt.Errorf("rooms: %w", err)
	}
	return rows, access, nil
}

// Rooms prints the overview, or with f.JSON the broker's rows as sent, then a hint on stderr
// when the broker says why the list may be short.
func (c Client) Rooms(ctx context.Context, out io.Writer, f RoomFilter) error {
	body, access, err := c.roomsBody(ctx, f)
	if err != nil {
		return err
	}
	var rows []roomview.Row
	if err := json.Unmarshal(body, &rows); err != nil { // before --json prints it: a garbled body is an error
		return fmt.Errorf("rooms: %w", err)
	}
	if f.JSON { // the broker's rows as sent: today's fields and the task's
		if _, err := fmt.Fprintln(out, string(body)); err != nil {
			return err
		}
		c.hint(access)
		return nil
	}
	if err := roomview.Overview(out, rows, roomview.Options{Format: f.Format, Now: time.Now(), All: f.All, Rooms: c.URL}); err != nil {
		return err
	}
	c.hint(access)
	return nil
}

// needsBody is GET /api/needs as the broker sent it, with its X-Rooms-Access. It is roomsBody's shape: one
// token header, one query the broker applies, and the same refusal for a caller the broker turns away.
func (c Client) needsBody(ctx context.Context, f NeedsFilter) (body []byte, access string, err error) {
	h, err := c.header(ctx)
	if err != nil {
		return nil, "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL+"/api/needs"+withQuery(f.query()), nil)
	if err != nil {
		return nil, "", err
	}
	req.Header = h
	resp, err := c.HC.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("needs: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		// A filter that matches nothing is an empty list, never a 404: the broker does not mount the route,
		// which is the operator's switch off or a broker older than needs/v1.
		return nil, "", errors.New("needs: the broker serves no /api/needs: human.needs.enabled is off, or the broker predates needs/v1 (maku rooms lists the rooms)")
	case http.StatusBadRequest:
		// The route's one 400 is a `since` it would not read, and its sentence names the format: relay the
		// broker's words rather than "Bad Request", as Summary does for access_unverified.
		msg, _ := httpx.ReadBody(resp.Body, 1<<10)
		return nil, "", fmt.Errorf("needs: %s", OneLine(string(msg)))
	default:
		return nil, "", refusal("needs", resp)
	}
	body, err = httpx.ReadBody(resp.Body, maxRoomsBody)
	if err != nil {
		return nil, "", fmt.Errorf("needs: %w", err)
	}
	return body, resp.Header.Get("X-Rooms-Access"), nil
}

// Needs reads GET /api/needs: what needs the developer across the rooms they may read, in needs/v1's one
// order (CD21, CD28). The list may be short for a reason they can fix, so the access value's hint goes to the
// error stream as the room list's does: a page is data, and stdout stays the list.
func (c Client) Needs(ctx context.Context, f NeedsFilter) (NeedsPage, error) {
	body, access, err := c.needsBody(ctx, f)
	if err != nil {
		return NeedsPage{}, err
	}
	page := NeedsPage{raw: body}
	if err := json.Unmarshal(body, &page); err != nil {
		return NeedsPage{}, fmt.Errorf("needs: %w", err)
	}
	c.hint(access)
	return page, nil
}

func withQuery(q string) string {
	if q == "" {
		return ""
	}
	return "?" + q
}

// hint says why the list may be short. It never promises a retry: an
// unverified check can be a missing configuration.
func (c Client) hint(access string) {
	var msg string
	switch access {
	case "unlinked":
		msg = "hint: your ZITADEL account has no linked GitHub identity, so no room is listed: you see a room only if GitHub lets you read its repository. " +
			"Link it once: sign in at " + printable(c.Issuer) + ", choose GitHub and \"link\", then sign in with GitHub once."
	case "unverified":
		msg = "hint: room access could not be verified (the GitHub or ZITADEL check failed, or the broker has no access check configured), so this list may be incomplete."
	default:
		return
	}
	w := c.Err
	if w == nil {
		w = os.Stderr
	}
	_, _ = fmt.Fprintln(w, msg)
}

// Summary is GET /api/rooms/{id}/summary, the broker's summary/v1 body as sent.
func (c Client) Summary(ctx context.Context, room string, after int64) (json.RawMessage, error) {
	h, err := c.header(ctx)
	if err != nil {
		return nil, err
	}
	u := c.URL + "/api/rooms/" + url.PathEscape(room) + "/summary"
	if after > 0 {
		u += "?after=" + strconv.FormatInt(after, 10)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header = h
	resp, err := c.HC.Do(req)
	if err != nil {
		return nil, fmt.Errorf("status: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, errors.New("status: no such room (or you cannot read it)")
	case http.StatusServiceUnavailable:
		body, _ := httpx.ReadBody(resp.Body, 1<<10)
		if strings.Contains(string(body), "access_unverified") {
			return nil, errors.New("status: room access cannot be verified right now")
		}
		return nil, errors.New("status: room unavailable")
	default:
		return nil, refusal("status", resp)
	}
	body, err := httpx.ReadBody(resp.Body, maxRoomsBody)
	if err != nil {
		return nil, fmt.Errorf("status: %w", err)
	}
	return body, nil
}

// toolProtocol is the MCP revision a stateless tools/call names.
const toolProtocol = "2025-06-18"

// ErrUnknownTool is the broker answering -32602 to a tool it does not serve: an older broker.
var ErrUnknownTool = errors.New("the broker serves no such tool")

// Tool calls one tool of the broker's /mcp, statelessly (no initialize), as the developer. It
// returns the result's text and structuredContent. A broker with no /mcp is mcp_off, an isError
// result an error carrying its text, and -32602 an ErrUnknownTool.
func (c Client) Tool(ctx context.Context, name string, args any) (text string, structured json.RawMessage, err error) {
	raw, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": name, "arguments": args}})
	if err != nil {
		return "", nil, err
	}
	r := &Relay{URL: c.URL, Token: c.Token, HC: c.HC, version: toolProtocol}
	res, err := r.relay(ctx, raw)
	var rel relayed
	var refused *Refusal
	switch {
	case errors.As(err, &rel):
		var e struct {
			Code    int
			Message string
		}
		_ = json.Unmarshal(rel.e, &e)
		if e.Code == mcpwire.InvalidParams && strings.HasPrefix(strings.ToLower(e.Message), "unknown tool") {
			return "", nil, fmt.Errorf("%w: %s", ErrUnknownTool, OneLine(e.Message))
		}
		return "", nil, fmt.Errorf("%s: %s", name, OneLine(e.Message))
	case errors.Is(err, errToken), errors.As(err, &refused):
		return "", nil, errors.New(explainRelay(err))
	case err != nil:
		return "", nil, err
	}
	var out struct {
		Content []struct {
			Type, Text string
		}
		Structured json.RawMessage `json:"structuredContent"`
		IsError    bool            `json:"isError"`
	}
	if err := json.Unmarshal(res, &out); err != nil {
		return "", nil, fmt.Errorf("%s: the broker's answer: %w", name, err)
	}
	var b strings.Builder
	for _, p := range out.Content {
		if p.Type == "text" {
			b.WriteString(p.Text)
		}
	}
	if out.IsError {
		return "", nil, fmt.Errorf("%s: %s", name, OneLine(b.String()))
	}
	return b.String(), out.Structured, nil
}

// Brief is GET /api/rooms/{id}/brief: the resume brief, so `maku status` can lead with it. since is
// the cursor a previous brief handed back (seq:N) or an RFC 3339 instant, and empty reads the
// broker's window. A 404 (no brief route or no such room) answers ErrUnknownTool, which the caller
// tells apart from a failure of this read.
func (c Client) Brief(ctx context.Context, room, since string) (string, error) {
	h, err := c.header(ctx)
	if err != nil {
		return "", err
	}
	u := c.URL + "/api/rooms/" + url.PathEscape(room) + "/brief"
	if since != "" {
		u += "?since=" + url.QueryEscape(since)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	req.Header = h
	resp, err := c.HC.Do(req)
	if err != nil {
		return "", fmt.Errorf("brief: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return "", ErrUnknownTool
	default:
		msg, _ := httpx.ReadBody(resp.Body, 1<<10)
		if text := OneLine(string(msg)); text != "" {
			return "", fmt.Errorf("brief: %s", text)
		}
		return "", refusal("brief", resp)
	}
	body, err := httpx.ReadBody(resp.Body, maxRoomsBody)
	if err != nil {
		return "", fmt.Errorf("brief: %w", err)
	}
	var env struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return "", fmt.Errorf("brief: %w", err)
	}
	return env.Text, nil
}

// dial opens the room's WebSocket and says hello.
func (c Client) dial(ctx context.Context, hello wire.ClientFrame) (*websocket.Conn, error) {
	h, err := c.header(ctx)
	if err != nil {
		return nil, err
	}
	conn, resp, err := websocket.Dial(ctx, c.URL+"/v1/ws?room="+url.QueryEscape(hello.RoomID),
		&websocket.DialOptions{HTTPClient: c.Stream, HTTPHeader: h})
	var refused *Refusal
	if err != nil && resp != nil && resp.StatusCode >= 400 && resp.StatusCode < 500 {
		refused = refusal("room "+hello.RoomID, resp)
	}
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		if refused != nil {
			return nil, refused
		}
		return nil, fmt.Errorf("room %s: %w", hello.RoomID, err)
	}
	conn.SetReadLimit(maxFrame)
	if err := wsjson.Write(ctx, conn, hello); err != nil {
		_ = conn.CloseNow()
		return nil, fmt.Errorf("room %s: hello: %w", hello.RoomID, err)
	}
	return conn, nil
}

// again reports whether the broker asked to be dialled again (docs/reference/api.md: 4001
// reauth, 1001 shutdown, 1013 log_unavailable, 1008 slow_consumer), or the
// connection dropped without a close frame. A refusal, no token, a frame over
// the read limit or another close code (hello first) would fail the same way again.
func again(err error) bool {
	var refused *Refusal
	if errors.As(err, &refused) || errors.Is(err, errToken) || errors.Is(err, websocket.ErrMessageTooBig) {
		return false
	}
	switch websocket.CloseStatus(err) {
	case 4001, websocket.StatusGoingAway, websocket.StatusTryAgainLater, -1:
		return true
	case websocket.StatusPolicyViolation:
		var ce websocket.CloseError
		return errors.As(err, &ce) && ce.Reason == "slow_consumer"
	}
	return false
}

// Watch prints the room's state, its last tail events, then follows it until ctx ends.
func (c Client) Watch(ctx context.Context, room string, tail int, out io.Writer) error {
	return c.Follow(ctx, room, tail, out, nil)
}

// errFollowed ends a Follow whose stop accepted an event; Follow returns nil for it.
var errFollowed = errors.New("followed to the event asked for")

// CreateRoom is POST /api/rooms: a new room the developer owns and drives, on repository
// (owner/name) and dataClass (public or internal). It returns the room's id.
func (c Client) CreateRoom(ctx context.Context, repository, dataClass string) (string, error) {
	h, err := c.header(ctx)
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(map[string]string{"repository": repository, "dataClass": dataClass})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL+"/api/rooms", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header = h
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HC.Do(req)
	if err != nil {
		return "", fmt.Errorf("create room: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusCreated:
	case http.StatusUnauthorized, http.StatusForbidden:
		return "", refusal("create room", resp)
	default:
		msg, _ := httpx.ReadBody(resp.Body, 1<<10)
		return "", fmt.Errorf("create room: %d: %s", resp.StatusCode, OneLine(string(msg)))
	}
	raw, err := httpx.ReadBody(resp.Body, 1<<10)
	if err != nil {
		return "", fmt.Errorf("create room: %w", err)
	}
	var out struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(raw, &out) != nil || out.ID == "" {
		return "", errors.New("create room: the broker's answer has no room id")
	}
	return out.ID, nil
}

// Follow is Watch until stop, called on each event it prints, reports true: it returns nil then.
// A nil stop follows until ctx ends, which is not an error. Once connected, it dials again
// wherever the broker says to, with a fresh token, from the last seq it printed.
func (c Client) Follow(ctx context.Context, room string, tail int, out io.Writer, stop func(envelope.Event) bool) error {
	w := watch{c: c, out: out, stop: stop}
	first := c.Backoff
	if first <= 0 {
		first = time.Second
	}
	wait := first
	hello := wire.ClientFrame{Type: wire.FrameHello, RoomID: room, Tail: tail}
	for {
		states := w.states
		err := w.follow(ctx, hello)
		if w.states > states {
			wait = first // that connection got through: a later drop starts the backoff again
		}
		switch {
		case errors.Is(err, errFollowed):
			return nil
		case ctx.Err() != nil:
			return nil
		case !w.connected || !again(err): // a first dial that fails is a bad URL or room, not a blip
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(wait):
		}
		wait = min(wait*2, maxBackoff)
		last := w.last
		hello = wire.ClientFrame{Type: wire.FrameHello, RoomID: room, AfterSeq: &last}
	}
}

type watch struct {
	c         Client
	out       io.Writer
	stop      func(envelope.Event) bool
	connected bool
	states    int // state frames seen, one per connection that got through
	last      int64
}

func (w *watch) follow(ctx context.Context, hello wire.ClientFrame) error {
	conn, err := w.c.dial(ctx, hello)
	if err != nil {
		return err
	}
	defer func() { _ = conn.CloseNow() }()
	for {
		var f wire.ServerFrame
		if err := wsjson.Read(ctx, conn, &f); err != nil {
			if errors.Is(err, websocket.ErrMessageTooBig) {
				return fmt.Errorf("room %s: the broker sent a frame over %d MiB, which maku does not read: %w", hello.RoomID, maxFrame>>20, err)
			}
			return err
		}
		if f.Type == wire.FrameState {
			w.states++
		}
		switch {
		case f.Type == wire.FrameState && f.Snapshot != nil && !w.connected:
			w.connected = true
			s := f.Snapshot
			_, _ = fmt.Fprintln(w.out, printable(fmt.Sprintf("# room %s · %s · driver %s · you: %s", s.RoomID, s.Phase, s.Driver, s.You.Role)))
		case f.Type == wire.FrameEvent && f.Event != nil && f.Event.Seq > w.last:
			w.last = f.Event.Seq
			if _, err := fmt.Fprintln(w.out, Line(*f.Event)); err != nil {
				return err
			}
			if w.stop != nil && w.stop(*f.Event) {
				return errFollowed
			}
		}
	}
}

// Act sends one action and returns its ack, on a connection of its own that
// replays nothing: its hello asks from past the mark, which the broker clamps.
func (c Client) Act(ctx context.Context, room string, action map[string]any) (wire.ServerFrame, error) {
	after := int64(math.MaxInt64)
	conn, err := c.dial(ctx, wire.ClientFrame{Type: wire.FrameHello, RoomID: room, AfterSeq: &after})
	if err != nil {
		return wire.ServerFrame{}, err
	}
	defer func() { _ = conn.CloseNow() }()
	raw, err := json.Marshal(action)
	if err != nil {
		return wire.ServerFrame{}, err
	}
	if err := wsjson.Write(ctx, conn, wire.ClientFrame{Type: wire.FrameAct, ClientSeq: 1, Action: raw}); err != nil {
		return wire.ServerFrame{}, fmt.Errorf("room %s: act: %w", room, err)
	}
	for {
		var f wire.ServerFrame
		if err := wsjson.Read(ctx, conn, &f); err != nil {
			return wire.ServerFrame{}, fmt.Errorf("room %s: no ack: %w", room, err)
		}
		if f.Type == wire.FrameAck && f.ClientSeq == 1 {
			_ = conn.Close(websocket.StatusNormalClosure, "")
			return f, nil
		}
	}
}

// CoordinationCheckout is GET /api/coordinations/{parent}/checkout (MR14): the family's whole
// checkout document, or an error; never a partial one.
func (c Client) CoordinationCheckout(ctx context.Context, parent string) (coordinationview.CheckoutDocument, error) {
	var d coordinationview.CheckoutDocument
	h, err := c.header(ctx)
	if err != nil {
		return d, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL+"/api/coordinations/"+url.PathEscape(parent)+"/checkout", nil)
	if err != nil {
		return d, err
	}
	req.Header = h
	resp, err := c.HC.Do(req)
	if err != nil {
		return d, fmt.Errorf("coordination checkout: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return d, errors.New("coordination checkout: no such coordination parent, or you cannot read every child")
	case http.StatusConflict:
		return d, errors.New("coordination checkout: not every child is at one commit yet (checkout_not_ready)")
	default:
		return d, refusal("coordination checkout", resp)
	}
	body, err := httpx.ReadBody(resp.Body, maxRoomsBody)
	if err != nil {
		return d, fmt.Errorf("coordination checkout: %w", err)
	}
	if err := json.Unmarshal(body, &d); err != nil {
		return d, fmt.Errorf("coordination checkout: %w", err)
	}
	if d.APIVersion != coordinationview.CheckoutAPIVersion || d.Kind != coordinationview.CheckoutKind {
		return coordinationview.CheckoutDocument{}, fmt.Errorf("coordination checkout: unknown document %q", OneLine(d.APIVersion))
	}
	return d, nil
}

// CoordinationDraft is PUT /api/coordinations/{parent}/draft (v4 MR4): a plan version stored on its
// parent as the developer, never a confirmation. The broker forwards it to the factory with the
// developer's own token.
func (c Client) CoordinationDraft(ctx context.Context, parent string, in coordination.CoordinationConfirmation) (coordination.CoordinationConfirmation, error) {
	var out coordination.CoordinationConfirmation
	h, err := c.header(ctx)
	if err != nil {
		return out, err
	}
	body, err := json.Marshal(struct {
		Plan     v1alpha1.CoordinationPlan           `json:"plan"`
		PlanHash string                              `json:"planHash"`
		Planning coordination.PlanningReconciliation `json:"planning"`
	}{in.Plan, in.PlanHash, in.Planning})
	if err != nil {
		return out, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.URL+"/api/coordinations/"+url.PathEscape(parent)+"/draft", bytes.NewReader(body))
	if err != nil {
		return out, err
	}
	req.Header = h
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HC.Do(req)
	if err != nil {
		return out, fmt.Errorf("coordination draft: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := httpx.ReadBody(resp.Body, maxRoomsBody)
	if err != nil {
		return out, fmt.Errorf("coordination draft: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		var r struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(raw, &r)
		code := "refused"
		if coordCodeRE.MatchString(r.Error) {
			code = r.Error
		}
		return out, fmt.Errorf("coordination draft: %s (%d)", code, resp.StatusCode)
	}
	return out, json.Unmarshal(raw, &out)
}

// coordCodeRE is a coordination refusal's bounded code; anything else is "refused".
var coordCodeRE = regexp.MustCompile(`^[a-z][a-z_]{0,47}$`)

// Stages reads GET /api/stages?repository=<owner/name>[&class=<c>][&last=<n>] from the broker (WR5).
func (c Client) Stages(ctx context.Context, repository, class string, last int) (stages.Report, error) {
	var out stages.Report
	h, err := c.header(ctx)
	if err != nil {
		return out, err
	}
	q := url.Values{}
	if repository != "" {
		q.Set("repository", repository)
	}
	if class != "" {
		q.Set("class", class)
	}
	if last > 0 {
		q.Set("last", strconv.Itoa(last))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL+"/api/stages"+withQuery(q.Encode()), nil)
	if err != nil {
		return out, err
	}
	req.Header = h
	resp, err := c.HC.Do(req)
	if err != nil {
		return out, fmt.Errorf("stages: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return out, c.repoRefusal("stages", repository, resp)
	}
	raw, err := httpx.ReadBody(resp.Body, maxRoomsBody)
	if err != nil {
		return out, fmt.Errorf("stages: %w", err)
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, fmt.Errorf("stages: %w", err)
	}
	return out, nil
}

// HandoffRequest is the body of POST /api/handoffs: either {text, repository?} to begin, or
// {id, answer} / {id, acceptRest: true} to continue.
type HandoffRequest struct {
	Text       string `json:"text,omitempty"`
	ID         string `json:"id,omitempty"`
	Answer     string `json:"answer,omitempty"`
	AcceptRest bool   `json:"acceptRest,omitempty"`
	Repository string `json:"repository,omitempty"`
}

// HandoffQuestion is the planner's one clarifying question, when it has one.
type HandoffQuestion struct {
	Text        string `json:"text"`
	Recommended string `json:"recommended"`
}

// HandoffResponse is the broker's answer to POST /api/handoffs.
type HandoffResponse struct {
	APIVersion string           `json:"apiVersion"`
	ID         string           `json:"id"`
	Text       string           `json:"text"`
	Question   *HandoffQuestion `json:"question"`
	Ready      bool             `json:"ready"`
	Workflow   string           `json:"workflow"`
	Budget     float64          `json:"budget"`
	Repository string           `json:"repository,omitempty"`
	Title      string           `json:"title,omitempty"`
	Body       string           `json:"body,omitempty"`
}

// HandoffWorkflowChoice describes one workflow offered by the catalogue for a handoff plan.
type HandoffWorkflowChoice struct {
	Name             string   `json:"name"`
	Description      string   `json:"description,omitempty"`
	CeilingUSD       float64  `json:"ceilingUSD,omitempty"`
	StepUpCeilingUSD float64  `json:"stepUpCeilingUSD,omitempty"`
	Path             []string `json:"path,omitempty"`
}

// HandoffCheckRequest is the body of POST /api/handoffs/{id}/check.
type HandoffCheckRequest struct {
	Workflow *string `json:"workflow,omitempty"`
	Outcome  *string `json:"outcome,omitempty"`
}

// HandoffCheckResponse is the broker's answer to POST /api/handoffs/{id}/check.
type HandoffCheckResponse struct {
	ID              string                  `json:"id"`
	Plan            json.RawMessage         `json:"plan"`
	Hash            string                  `json:"hash"`
	CanStart        bool                    `json:"canStart"`
	CanStartWhy     string                  `json:"canStartWhy,omitempty"`
	Maintainer      *bool                   `json:"maintainer,omitempty"`
	Workflow        string                  `json:"workflow"`
	Text            string                  `json:"text"`
	Linked          bool                    `json:"linked"`
	Workflows       []string                `json:"workflows,omitempty"`
	WorkflowChoices []HandoffWorkflowChoice `json:"workflowChoices,omitempty"`
	DefaultChoice   *HandoffWorkflowChoice  `json:"defaultChoice,omitempty"`
	ActiveChoice    *HandoffWorkflowChoice  `json:"activeChoice,omitempty"`
	Title           string                  `json:"title"`
	Body            string                  `json:"body"`
	Repository      string                  `json:"repository"`
}

// HandoffStartRequest is the body of POST /api/handoffs/{id}/start.
type HandoffStartRequest struct {
	Hash     string  `json:"hash,omitempty"`
	Workflow *string `json:"workflow,omitempty"`
	Outcome  *string `json:"outcome,omitempty"`
}

// HandoffStartResponse is the broker's answer to POST /api/handoffs/{id}/start.
type HandoffStartResponse struct {
	Repository string   `json:"repository"`
	Issue      int      `json:"issue"`
	URL        string   `json:"url"`
	Labels     []string `json:"labels"`
	Viewer     string   `json:"viewer"`
}

// HandoffRefusal is a structured refusal from one of the three /api/handoffs routes.
type HandoffRefusal struct {
	Op      string
	Code    int
	Reason  string
	Message string
}

func (r *HandoffRefusal) Error() string {
	if r.Message != "" {
		return r.Message
	}
	if r.Reason != "" {
		return fmt.Sprintf("%s: %s (%d)", r.Op, r.Reason, r.Code)
	}
	return fmt.Sprintf("%s: HTTP %d", r.Op, r.Code)
}

func (c Client) postHandoff(ctx context.Context, op, path string, in, out any) error {
	h, err := c.header(ctx)
	if err != nil {
		return err
	}
	body, err := json.Marshal(in)
	if err != nil {
		return fmt.Errorf("%s: %w", op, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL+path, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("%s: %w", op, err)
	}
	req.Header = h
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HC.Do(req)
	if err != nil {
		return fmt.Errorf("%s: %w", op, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusUnauthorized {
		return refusal(op, resp)
	}
	raw, err := httpx.ReadBody(resp.Body, maxRoomsBody)
	if err != nil {
		return fmt.Errorf("%s: %w", op, err)
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		var r struct {
			Error   string `json:"error"`
			Message string `json:"message"`
		}
		if json.Unmarshal(raw, &r) == nil && (r.Error != "" || r.Message != "") {
			return &HandoffRefusal{Op: op, Code: resp.StatusCode, Reason: r.Error, Message: OneLine(r.Message)}
		}
		if text := OneLine(string(raw)); text != "" {
			return &HandoffRefusal{Op: op, Code: resp.StatusCode, Message: text}
		}
		return &HandoffRefusal{Op: op, Code: resp.StatusCode}
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("%s: %w", op, err)
	}
	return nil
}

// Handoff is POST /api/handoffs: begin or continue a stored handoff plan.
func (c Client) Handoff(ctx context.Context, req HandoffRequest) (HandoffResponse, error) {
	var out HandoffResponse
	err := c.postHandoff(ctx, "start", "/api/handoffs", req, &out)
	return out, err
}

// HandoffCheck is POST /api/handoffs/{id}/check: re-validate a stored handoff plan with optional
// workflow pin and outcome.
func (c Client) HandoffCheck(ctx context.Context, id string, req HandoffCheckRequest) (HandoffCheckResponse, error) {
	var out HandoffCheckResponse
	err := c.postHandoff(ctx, "start", "/api/handoffs/"+url.PathEscape(id)+"/check", req, &out)
	return out, err
}

// HandoffStart is POST /api/handoffs/{id}/start: file the checked handoff's issue on GitHub as the
// linked viewer and apply its labels.
func (c Client) HandoffStart(ctx context.Context, id string, req HandoffStartRequest) (HandoffStartResponse, error) {
	var out HandoffStartResponse
	err := c.postHandoff(ctx, "start", "/api/handoffs/"+url.PathEscape(id)+"/start", req, &out)
	return out, err
}

// HandoffCard builds the workflow card for a checked handoff response.
func HandoffCard(checked HandoffCheckResponse) (roomview.WorkflowCard, string) {
	var view planView
	_ = json.Unmarshal(checked.Plan, &view)
	wf := checked.Workflow
	source := "repository"
	suggested := ""
	var roles []string
	var outcome, outcomeNote string
	var outcomeInferred bool
	if len(view.Issues) > 0 {
		is := view.Issues[0]
		outcome, outcomeInferred, outcomeNote = is.Outcome, is.OutcomeInferred, is.OutcomeNote
		if is.Profile != "" {
			wf = is.Profile
			source = "label"
		}
		if is.Suggestion != nil {
			roles = is.Suggestion.Roles
			if is.Suggestion.Profile != "" && is.Suggestion.Profile != wf {
				suggested = is.Suggestion.Profile
			}
		}
	}
	if wf == "" {
		wf = "default"
	}
	repo := checked.Repository
	if repo == "" {
		repo = view.Repository
	}
	var path []string
	var stepUpUSD float64
	ceiling := view.Budget.CeilingUSD
	for _, ch := range []*HandoffWorkflowChoice{checked.ActiveChoice, checked.DefaultChoice} {
		if ch != nil && ch.Name == wf {
			path = ch.Path
			stepUpUSD = ch.StepUpCeilingUSD
			if ceiling == 0 {
				ceiling = ch.CeilingUSD
			}
			break
		}
	}
	if len(path) == 0 && len(roles) > 0 {
		path = append(append([]string(nil), roles...), "CI", "merge gate")
	}
	return roomview.WorkflowCard{
		Repository:      repo,
		Outcome:         outcome,
		OutcomeInferred: outcomeInferred,
		Workflow:        wf,
		Reason:          roomview.Reason(source, "", suggested, "", nil),
		Path:            path,
		CeilingUSD:      ceiling,
		StepUpUSD:       stepUpUSD,
	}, outcomeNote
}

// HandoffLabels returns the non-ready labels (workflow:<name>, outcome) to apply before factory/ready.
func HandoffLabels(checked HandoffCheckResponse) []string {
	var view planView
	if json.Unmarshal(checked.Plan, &view) != nil || len(view.Issues) == 0 {
		return nil
	}
	g := &governed{planShape: planShape{Maintainer: checked.Maintainer}, view: view}
	return g.planLabels()
}

// IssueNumber parses the issue number from a GitHub issue URL belonging to repo.
func IssueNumber(issueURL, repo string) (int, error) {
	return issueNumber(issueURL, repo)
}

// WorkflowDryRun posts POST /api/workflows/dry-run to the broker (WR11).
func (c Client) WorkflowDryRun(ctx context.Context, in stages.DryRunRequest) (stages.DryRunResult, error) {
	var out stages.DryRunResult
	h, err := c.header(ctx)
	if err != nil {
		return out, err
	}
	body, err := json.Marshal(in)
	if err != nil {
		return out, fmt.Errorf("workflow dry-run: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL+"/api/workflows/dry-run", bytes.NewReader(body))
	if err != nil {
		return out, err
	}
	h.Set("Content-Type", "application/json")
	req.Header = h
	resp, err := c.HC.Do(req)
	if err != nil {
		return out, fmt.Errorf("workflow dry-run: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return out, c.repoRefusal("workflow dry-run", in.Repository, resp)
	}
	raw, err := httpx.ReadBody(resp.Body, maxRoomsBody)
	if err != nil {
		return out, fmt.Errorf("workflow dry-run: %w", err)
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, fmt.Errorf("workflow dry-run: %w", err)
	}
	return out, nil
}
