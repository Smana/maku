// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/Smana/maku/internal/httpx"
)

const (
	maxJSONLine        = 4 << 20
	maxHTTPResponse    = 8 << 20
	defaultCallTimeout = 60 * time.Second
	mcpProtocol        = "2025-06-18"
)

// Client is one scripted MCP session, either directly against a broker's /mcp or through maku mcp
// on stdio. Calls are serialized because a stdio elicitation belongs to the call waiting on it.
type Client struct {
	BrokerURL  string
	Token      string
	HTTPClient *http.Client
	Command    []string
	Stderr     io.Writer
	Answer     string
	// BeforeAnswer runs after need_answer's form arrives and before its deterministic answer. The
	// conflict scenario uses it to change the fixture; the form answer itself remains unchanged.
	BeforeAnswer func(context.Context) error
	Timeout      time.Duration

	mu          sync.Mutex
	initialized bool
	protocol    string
	nextID      int64
	cmd         *exec.Cmd
	stdin       io.WriteCloser
	lines       chan stdioLine
	closed      bool
}

type stdioLine struct {
	raw []byte
	err error
}

type wireMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   json.RawMessage `json:"error,omitempty"`
}

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("MCP error %d: %s", e.Code, e.Message) }

// ToolError is a successful JSON-RPC tools/call whose result carries isError.
type ToolError struct{ Message string }

func (e *ToolError) Error() string { return e.Message }

func (c *Client) timeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return defaultCallTimeout
}

func (c *Client) requestID() int64 {
	c.nextID++
	return c.nextID
}

// Call invokes one tool and returns its structuredContent. It initializes the transport once,
// rejects tool and JSON-RPC refusals, and answers the two local relay forms deterministically.
func (c *Client) Call(ctx context.Context, name string, args any) (json.RawMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, errors.New("MCP client is closed")
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout())
	defer cancel()
	if !c.initialized {
		if err := c.initialize(ctx); err != nil {
			return nil, fmt.Errorf("initialize MCP: %w", err)
		}
	}
	arguments, err := marshalArguments(args)
	if err != nil {
		return nil, err
	}
	result, err := c.roundTrip(ctx, "tools/call", map[string]any{"name": name, "arguments": arguments}, name)
	if err != nil {
		return nil, err
	}
	var out struct {
		IsError bool `json:"isError"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Structured json.RawMessage `json:"structuredContent"`
	}
	if err := json.Unmarshal(result, &out); err != nil {
		return nil, fmt.Errorf("decode %s result: %w", name, err)
	}
	if out.IsError {
		message := name + " refused"
		if len(out.Content) > 0 {
			message = out.Content[0].Text
		}
		return nil, &ToolError{Message: message}
	}
	if len(bytes.TrimSpace(out.Structured)) == 0 || string(bytes.TrimSpace(out.Structured)) == "null" {
		return nil, fmt.Errorf("%s result has no structuredContent", name)
	}
	return append(json.RawMessage(nil), out.Structured...), nil
}

func marshalArguments(args any) (json.RawMessage, error) {
	if args == nil {
		return json.RawMessage(`{}`), nil
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return nil, fmt.Errorf("marshal tool arguments: %w", err)
	}
	if len(raw) == 0 || raw[0] != '{' {
		return nil, errors.New("tool arguments must be one JSON object")
	}
	return raw, nil
}

func (c *Client) initialize(ctx context.Context) error {
	if len(c.Command) > 0 {
		if err := c.startStdio(ctx); err != nil {
			return err
		}
	}
	result, err := c.roundTrip(ctx, "initialize", map[string]any{
		"protocolVersion": mcpProtocol,
		"capabilities":    map[string]any{"elicitation": map[string]any{"form": map[string]any{}}, "prompts": map[string]any{}},
		"clientInfo":      map[string]string{"name": "maku-e2e", "version": "1"},
	}, "")
	if err != nil {
		return err
	}
	var answered struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if err := json.Unmarshal(result, &answered); err != nil {
		return fmt.Errorf("invalid initialize result: %w", err)
	}
	if answered.ProtocolVersion == "" {
		return errors.New("initialize result has no protocolVersion")
	}
	c.protocol = answered.ProtocolVersion
	if len(c.Command) > 0 {
		if err := c.writeStdio(ctx, map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"}); err != nil {
			return err
		}
	} else if err := c.httpNotify(ctx, "notifications/initialized"); err != nil {
		return err
	}
	c.initialized = true
	return nil
}

func (c *Client) roundTrip(ctx context.Context, method string, params any, tool string) (json.RawMessage, error) {
	id := c.requestID()
	request := map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}
	if len(c.Command) > 0 {
		return c.stdioRoundTrip(ctx, request, id, tool)
	}
	return c.httpRoundTrip(ctx, request)
}

func decodeResponse(raw []byte, id int64) (json.RawMessage, error) {
	var m wireMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("decode MCP response: %w", err)
	}
	if m.JSONRPC != "2.0" {
		return nil, errors.New("MCP response is not JSON-RPC 2.0")
	}
	if id >= 0 {
		var got int64
		if err := json.Unmarshal(m.ID, &got); err != nil || got != id {
			return nil, fmt.Errorf("MCP response id %s does not match %d", m.ID, id)
		}
	}
	if len(m.Error) > 0 && string(m.Error) != "null" {
		var rpcErr rpcError
		if err := json.Unmarshal(m.Error, &rpcErr); err != nil {
			return nil, fmt.Errorf("decode MCP error: %w", err)
		}
		return nil, &rpcErr
	}
	if len(m.Result) == 0 {
		return nil, errors.New("MCP response has neither result nor error")
	}
	return m.Result, nil
}

func (c *Client) mcpURL() string {
	base := strings.TrimRight(c.BrokerURL, "/")
	if strings.HasSuffix(base, "/mcp") {
		return base
	}
	return base + "/mcp"
}

func (c *Client) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return httpx.New(c.timeout(), nil)
}

func (c *Client) newHTTPRequest(ctx context.Context, body []byte) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.mcpURL(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	if c.protocol != "" {
		req.Header.Set("MCP-Protocol-Version", c.protocol)
	}
	return req, nil
}

func (c *Client) httpRoundTrip(ctx context.Context, request any) (json.RawMessage, error) {
	body, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	if len(body) > maxJSONLine {
		return nil, errors.New("MCP request exceeds the JSON line bound")
	}
	req, err := c.newHTTPRequest(ctx, body)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("POST /mcp: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := httpx.ReadBody(resp.Body, maxHTTPResponse)
	if err != nil {
		return nil, fmt.Errorf("read /mcp response: %w", err)
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusBadRequest {
		return nil, fmt.Errorf("POST /mcp: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		raw = lastSSEData(raw)
	}
	id := int64(-1)
	if requestMap, ok := request.(map[string]any); ok {
		if value, ok := requestMap["id"].(int64); ok {
			id = value
		}
	}
	return decodeResponse(raw, id)
}

func lastSSEData(raw []byte) []byte {
	var last []byte
	for _, line := range bytes.Split(raw, []byte("\n")) {
		if data, ok := bytes.CutPrefix(bytes.TrimSuffix(line, []byte("\r")), []byte("data: ")); ok {
			last = data
		}
	}
	return last
}

func (c *Client) httpNotify(ctx context.Context, method string) error {
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method})
	if err != nil {
		return err
	}
	req, err := c.newHTTPRequest(ctx, body)
	if err != nil {
		return err
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("POST /mcp notification: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusAccepted {
		raw, readErr := httpx.ReadBody(resp.Body, 4<<10)
		if readErr != nil {
			return readErr
		}
		return fmt.Errorf("POST /mcp notification: HTTP %d: %s", resp.StatusCode, raw)
	}
	return nil
}

func (c *Client) startStdio(ctx context.Context) error {
	if len(c.Command) == 0 || c.Command[0] == "" {
		return errors.New("empty maku mcp command")
	}
	// The process belongs to the client, not to the first call's deadline. Close owns its teardown.
	cmd := exec.CommandContext(context.WithoutCancel(ctx), c.Command[0], c.Command[1:]...) //nolint:gosec // the scenario supplies one fixed maku mcp argv, never a shell
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = c.Stderr
	if cmd.Stderr == nil {
		cmd.Stderr = io.Discard
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start maku mcp: %w", err)
	}
	c.cmd, c.stdin, c.lines = cmd, stdin, make(chan stdioLine, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 64<<10), maxJSONLine)
		for scanner.Scan() {
			c.lines <- stdioLine{raw: append([]byte(nil), scanner.Bytes()...)}
		}
		c.lines <- stdioLine{err: scanner.Err()}
		close(c.lines)
	}()
	return nil
}

func (c *Client) writeStdio(ctx context.Context, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(raw) > maxJSONLine {
		return errors.New("MCP request exceeds the JSON line bound")
	}
	done := make(chan error, 1)
	go func() {
		_, err := c.stdin.Write(append(raw, '\n'))
		done <- err
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-done:
		return err
	}
}

func (c *Client) stdioRoundTrip(ctx context.Context, request any, id int64, tool string) (json.RawMessage, error) {
	if err := c.writeStdio(ctx, request); err != nil {
		return nil, fmt.Errorf("write maku mcp: %w", err)
	}
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case line, ok := <-c.lines:
			if !ok {
				return nil, io.EOF
			}
			if line.err != nil {
				return nil, fmt.Errorf("read maku mcp: %w", line.err)
			}
			var m wireMessage
			if err := json.Unmarshal(line.raw, &m); err != nil {
				return nil, fmt.Errorf("decode maku mcp line: %w", err)
			}
			if m.Method == "elicitation/create" {
				if err := c.answerElicitation(ctx, tool, m); err != nil {
					return nil, err
				}
				continue
			}
			if m.Method != "" {
				continue // the scenario declares no roots and expects no other server requests
			}
			return decodeResponse(line.raw, id)
		}
	}
}

func (c *Client) answerElicitation(ctx context.Context, tool string, request wireMessage) error {
	var content map[string]any
	switch tool {
	case "issue_file":
		content = map[string]any{"file": true, "start": false}
	case "need_answer":
		if c.BeforeAnswer != nil {
			if err := c.BeforeAnswer(ctx); err != nil {
				return fmt.Errorf("change the fixture before need_answer: %w", err)
			}
		}
		content = map[string]any{"answer": c.Answer, "send": true}
	default:
		return fmt.Errorf("unexpected elicitation during %s", tool)
	}
	return c.writeStdio(ctx, map[string]any{"jsonrpc": "2.0", "id": request.ID,
		"result": map[string]any{"action": "accept", "content": content}})
}

// Close terminates a stdio relay. An HTTP client owns no process and needs no close.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	if c.stdin != nil {
		_ = c.stdin.Close()
	}
	if c.cmd != nil && c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
		_ = c.cmd.Wait()
	}
	return nil
}
