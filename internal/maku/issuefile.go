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
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Smana/maku/internal/mcpwire"
	"github.com/Smana/maku/internal/roomview"
)

// issueFileTool is maku mcp's own tool: the broker never files an issue (F3).
const issueFileTool = "issue_file"

// IssueFiler files a confirmed draft as the developer and returns the issue's URL. GH is the one
// maku mcp uses.
type IssueFiler func(ctx context.Context, repo, title, body string) (string, error)

// Labeler puts a label on an issue as the developer. GHLabel is the one maku mcp uses.
type Labeler func(ctx context.Context, repo string, issue int, label string) error

// readyLabel is the label that starts the factory: the developer's own, never run_create.
const readyLabel = "factory/ready"

// issueFileEntry is issue_file's tools/list entry. The plan and its hash are properties, never
// required: a client that has no plan sends the three it always sent, and an older broker refuses a
// call that names no plan_check only if one was named (CD3, CD27).
var issueFileEntry = map[string]any{
	"name": issueFileTool,
	"description": "File a factory issue as the developer, with their gh, once they confirm it in a prompt this tool shows them. " +
		"When the developer was shown a plan, pass it with the hash intake_plan answered: plan_check re-validates both, the form shows the plan's issue and its ceiling, and the issue filed is the plan's as it was validated. " +
		"Without a plan the draft is checked by issue_draft. It labels nothing itself: the form offers to start the factory only when plan_check (or, with no plan, issue_draft) says canStart, and the label is the developer's, applied with their gh.",
	"inputSchema": json.RawMessage(`{"type":"object","additionalProperties":false,"required":["repo","title","body"],` +
		`"properties":{"repo":{"type":"string"},"title":{"type":"string"},"body":{"type":"string"},` +
		`"plan":{"type":"object","description":"the plan/v2 intake_plan answered with"},` +
		`"hash":{"type":"string","pattern":"^sha256:[0-9a-f]{64}$","description":"the hash intake_plan answered with; plan_check recomputes it over the plan"}}}`),
}

// planShape is plan_check's answer: the plan the broker re-validated, the hash it recomputed over it,
// and whether this developer may start the factory on it. It is the relay's second source of truth,
// and never a second planner (CD1, CD3, CD27).
type planShape struct {
	Plan        json.RawMessage `json:"plan"`
	Hash        string          `json:"hash"`
	CanStart    bool            `json:"canStart"`    // whether this developer may start the factory
	CanStartWhy string          `json:"canStartWhy"` // why not, when it is false
	// Maintainer is the caller's flag from the repository's catalogue; absent when the broker read
	// none. A false one means a class: label of theirs would not count (P5).
	Maintainer *bool `json:"maintainer"`
	// Status is "replanned" when the plan was a plan/v1 (P14): the plan here is the plan/v2 to
	// confirm, and nothing may be filed from this answer.
	Status string `json:"status"`
}

// planView is what the form shows of a validated plan: the repository it files into, the issue it
// files first, and the ceiling its work is charged against. All three are display: the hash binds the
// plan, and plan_check is what re-validated it.
type planView struct {
	Repository string `json:"repository"`
	Issues     []struct {
		Index      int    `json:"index"`
		Title      string `json:"title"`
		Body       string `json:"body"`
		Class      string `json:"class"`
		Suggestion *struct {
			Profile string   `json:"profile"`
			Roles   []string `json:"roles"`
			Merge   struct {
				Outcome string `json:"outcome"`
			} `json:"merge"`
			Why string `json:"why"`
		} `json:"suggestion"`
	} `json:"issues"`
	Budget struct {
		CeilingUSD float64 `json:"ceilingUSD"`
		Source     string  `json:"source"`
	} `json:"budget"`
}

// governed is a plan the broker re-validated and the view of it the form shows.
type governed struct {
	planShape
	view planView
}

// draft is the plan's first issue as plan_check validated it: what the form shows and the only thing
// that is filed. The plan wins over the arguments on purpose — a caller's own copy of the text is not
// a second source of truth for what was validated (CD27).
func (g *governed) draft() draftShape {
	return draftShape{Repo: g.view.Repository, Title: g.view.Issues[0].Title, Body: g.view.Issues[0].Body,
		CanStart: g.CanStart, CanStartWhy: g.CanStartWhy}
}

// classLabel is the class:<c> label start applies before factory/ready: the plan's class, unless
// the catalogue says this developer does not maintain the repository (the factory would not count
// it). Empty without one.
func (g *governed) classLabel() string {
	if g.view.Issues[0].Class == "" || (g.Maintainer != nil && !*g.Maintainer) {
		return ""
	}
	return "class:" + g.view.Issues[0].Class
}

// suggestion is the form's lines about the class and the profile (O1): the class, the factory's
// choice of profile and the merge outcome in the broker's words, then the planner's suggested profile
// with its why, folded to one inert line. Empty for a plan without a class.
func (g *governed) suggestion() string {
	is := g.view.Issues[0]
	if is.Class == "" {
		return ""
	}
	line := "Class " + OneLine(is.Class)
	if s := is.Suggestion; s != nil {
		line += " · profile: the factory's choice · " + roomview.MergeWords(s.Merge.Outcome)
		if s.Profile != "" {
			line += "\nSuggested profile " + OneLine(s.Profile)
			if len(s.Roles) > 0 {
				line += " (" + OneLine(strings.Join(s.Roles, ", ")) + ")"
			}
			if why := OneLine(s.Why); why != "" {
				line += "; the planner's reading: " + why
			}
		}
	}
	if g.Maintainer != nil && !*g.Maintainer {
		line += "\nThe class:" + OneLine(is.Class) + " label is skipped: you do not maintain this repository, so the factory would not count it."
	}
	return line + "\n\n"
}

// ceiling is the money this plan's work is charged against, as the form says it: the number and
// where it came from, both from config through the broker (CD17). Empty when the plan carries none.
func (g *governed) ceiling() string {
	if g.view.Budget.CeilingUSD <= 0 {
		return ""
	}
	s := " (" + dollars(g.view.Budget.CeilingUSD) + " ceiling"
	if g.view.Budget.Source != "" {
		s += ", from " + OneLine(g.view.Budget.Source)
	}
	return s + ")"
}

// dollars is a plan's USD ceiling as a person reads it: two decimals, from the cent up.
func dollars(usd float64) string {
	cents := int64(math.Round(usd * 100))
	return fmt.Sprintf("$%d.%02d", cents/100, cents%100)
}

// draftShape is the draft the form shows and issue_file files as it stands: the broker's issue_draft
// result, or the plan's issue as plan_check re-validated it.
type draftShape struct {
	Repo        string `json:"repo"`
	Title       string `json:"title"`
	Body        string `json:"body"`
	CanStart    bool   `json:"canStart"`    // absent from an older broker: false
	CanStartWhy string `json:"canStartWhy"` // why not, when it is false
}

// The draft's repository and title, checked again here: they become gh's arguments.
var (
	repoRE  = regexp.MustCompile(`^[A-Za-z0-9-]+/[A-Za-z0-9._-]+$`)
	titleRE = regexp.MustCompile(`^[A-Za-z0-9]`)
)

const needsConfirmation = "needs_confirmation"

// issueFile is issue_file: the broker's plan_check re-validates the plan and its hash when the call
// carries them (issue_draft, when it does not), the client asks the developer through an elicitation,
// and only an accepted draft is filed. A client that cannot ask is refused: nothing else stands for
// the developer's yes.
func (r *Relay) issueFile(ctx context.Context, args json.RawMessage) any {
	r.mu.Lock()
	elicit := r.forms
	r.mu.Unlock()
	// A plan is re-validated before anything else the tool does, whatever this client can ask: the
	// form shows what plan_check validated and files nothing else, and a plan that no longer holds is
	// refused before a form is shown (CD3, CD27, offline proof 4).
	plan, refused := r.planned(ctx, args)
	if refused != nil {
		return refused
	}
	draft, draftRefusal := draftShape{}, any(nil)
	if plan != nil {
		draft = plan.draft()
	} else {
		draft, draftRefusal = r.draft(ctx, args)
	}
	if !elicit || r.File == nil {
		msg := needsConfirmation + ": issue_file asks the developer to confirm, and this client cannot ask (no elicitation); " +
			"show them the draft and let them file it with gh issue create"
		if draftRefusal == nil {
			msg += "\n" + fallbackNext(draft)
		}
		return mcpwire.ToolError(msg)
	}
	if draftRefusal != nil {
		return draftRefusal
	}
	offer := draft.CanStart && r.Label != nil
	// A default, or Claude Code shows the required toggle as unset with "This field is required"
	// until it is toggled; false keeps filing an explicit yes.
	props := map[string]any{"file": map[string]any{"type": "boolean", "title": "File this issue", "default": false, "description": "Filed with your gh, as you"}}
	tail := "It gets no label: label it " + readyLabel + " yourself to start the factory."
	if offer {
		props["start"] = map[string]any{"type": "boolean", "title": "Start the factory now", "default": false,
			"description": "Also labels it " + readyLabel + " with your gh, which starts the factory"}
		tail = "Choose \"start\" to also label it " + readyLabel + " with your gh, which starts the factory."
	} else if why := OneLine(draft.CanStartWhy); why != "" {
		tail = "The factory cannot be started from here (" + why + "). It gets no label: label it " + readyLabel + " yourself to start the factory."
	}
	ceiling, more, class := "", "", ""
	if plan != nil {
		ceiling, class = plan.ceiling(), plan.classLabel()
		more = plan.suggestion()
		if n := len(plan.view.Issues); n > 1 { // one call files one issue; the form says which (CD19)
			more += fmt.Sprintf("The plan holds %d issues: this files the first. ", n)
		}
	}
	ans, err := r.ask(ctx, "elicitation/create", map[string]any{
		"message":         fmt.Sprintf("File this issue on %s%s as you, with gh? %s\n\n%s%s\n\n%s", draft.Repo, ceiling, tail, more, draft.Title, draft.Body),
		"requestedSchema": map[string]any{"type": "object", "required": []string{"file"}, "properties": props},
	})
	if errors.Is(err, context.DeadlineExceeded) {
		// The client declared elicitation and never answered: it did not show the form. Same result as
		// a client that cannot ask, so the agent shows the draft and the developer files it with gh.
		return mcpwire.ToolError(needsConfirmation + "; the client did not show the confirmation form (no answer within " +
			elicitWait.String() + "), so show them the draft and let them file it with gh issue create\n" + fallbackNext(draft))
	}
	if err != nil {
		return mcpwire.ToolError("not_filed: the developer was not asked: " + err.Error() + "\n" + fallbackNext(draft))
	}
	var res struct {
		Action  string                     `json:"action"`
		Content map[string]json.RawMessage `json:"content"` // a client may return it partial
	}
	if len(ans.Error) > 0 || json.Unmarshal(ans.Result, &res) != nil {
		return mcpwire.ToolError("not_filed: the client could not ask the developer\n" + fallbackNext(draft))
	}
	file, _ := formBool(res.Content["file"])
	if res.Action != "accept" || !file {
		out, _ := mcpwire.ToolResult(map[string]any{"filed": false, "why": "the developer did not confirm (" + res.Action + ")"})
		return out
	}
	start, chosen := formBool(res.Content["start"])
	if offer && !chosen {
		r.log().Warn("issue_file: the form answer has no usable start; treated as not started", "start", string(res.Content["start"]))
	}
	url, err := r.File(ctx, draft.Repo, draft.Title, draft.Body)
	if err != nil {
		return mcpwire.ToolError("not_filed: " + err.Error())
	}
	if !offer || !start { // a start the form never offered is ignored
		next := "label it " + readyLabel + " to start the factory: your decision"
		if offer && !chosen {
			next = "start was not chosen (the form came back without it), so the factory is not started: ask the developer whether to start it, and label it " + readyLabel + " only on their yes"
		}
		out, _ := mcpwire.ToolResult(map[string]any{"filed": true, "url": url, "started": false, "next": next})
		return out
	}
	return r.start(ctx, draft.Repo, url, class)
}

// fallbackNext is the hint for a form that could not be shown or answered (OpenCode cuts a tool at
// its execution timeout): one command for the developer to run, nothing filed until they do. It never
// labels the issue: starting the factory is the developer's own act.
func fallbackNext(d draftShape) string {
	start := "to start the factory they add the label " + readyLabel + " themselves, or you call issue_file again and they choose start in its form"
	if !d.CanStart {
		start = "the factory cannot be started from here; they label the issue " + readyLabel + " themselves when it can run"
	}
	return "next: show the developer this one command and let them run it; it only files the issue, and nothing is filed until they do. " +
		"Never add a label yourself: " + start + ", and that is what starts the factory:\n" + fallbackCommand(d)
}

// fallbackCommand is one shell command that files the draft with gh and nothing else. Every value is
// single-quoted and the body rides a quoted heredoc whose end never occurs in it.
func fallbackCommand(d draftShape) string {
	eof := "BODY_EOF"
	for i := 1; strings.Contains(d.Body, "\n"+eof+"\n") || strings.HasPrefix(d.Body, eof+"\n") || strings.HasSuffix(d.Body, "\n"+eof) || d.Body == eof; i++ {
		eof = fmt.Sprintf("BODY_EOF_%d", i)
	}
	return fmt.Sprintf("gh issue create --repo=%s --title=%s --body-file=- <<'%s'\n%s\n%s\n", d.Repo, shQuote(d.Title), eof, strings.TrimRight(d.Body, "\n"), eof)
}

// shQuote single-quotes s for a POSIX shell.
func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// formBool reads a form field: a JSON boolean, or the strings "true" and "false" some clients send.
// ok is false when the field is absent, null or neither, which callers treat as false.
func formBool(raw json.RawMessage) (v, ok bool) {
	var b bool
	if json.Unmarshal(raw, &b) == nil && len(raw) > 0 && string(raw) != "null" {
		return b, true
	}
	var str string
	if json.Unmarshal(raw, &str) == nil {
		switch strings.ToLower(strings.TrimSpace(str)) {
		case "true":
			return true, true
		case "false":
			return false, true
		}
	}
	return false, false
}

// start labels the issue just filed and waits for its room. A failure files without the label and
// says so: it is never retried, since the developer can label the issue themselves.
func (r *Relay) start(ctx context.Context, repo, url, class string) any {
	result := map[string]any{"filed": true, "url": url, "started": false}
	n, err := issueNumber(url, repo)
	if err == nil && class != "" && r.ClassLabel != nil {
		// The class first (P7): factory/ready starts triage, which reads the labels it finds then.
		if cerr := r.ClassLabel(ctx, repo, n, class); cerr != nil {
			result["why"] = "filed without labels, so not started: " + class + ": " + OneLine(cerr.Error()) +
				"; label it " + class + ", then " + readyLabel + ", yourself"
			out, _ := mcpwire.ToolResult(result)
			return out
		}
	}
	if err == nil {
		err = r.Label(ctx, repo, n, readyLabel)
	}
	if err != nil {
		result["why"] = "filed without the label, so not started: " + OneLine(err.Error()) + "; label it " + readyLabel + " yourself"
		out, _ := mcpwire.ToolResult(result)
		return out
	}
	result["started"] = true
	id := r.awaitRoom(ctx, repo, n)
	if id == "" {
		result["why"] = "not started yet: the factory's comment on the issue says why"
		out, _ := mcpwire.ToolResult(result)
		return out
	}
	result["room"] = id
	out, _ := mcpwire.ToolResult(result)
	if text, sc, isErr, err := r.callBroker(ctx, "room_summary", map[string]string{"room": id}); err == nil && !isErr {
		if card, ok := cardText(viewed("room_summary", cardResult(text, sc), r.URL, time.Now())); ok {
			out["content"] = []map[string]string{{"type": "text", "text": card}}
		}
	}
	return out
}

// awaitRoom polls rooms_list every 5 s for the room of the issue, up to RoomWait; "" if none came.
func (r *Relay) awaitRoom(ctx context.Context, repo string, issue int) string {
	wait := r.RoomWait
	if wait <= 0 {
		wait = roomWait
	}
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	for {
		if _, sc, isErr, err := r.callBroker(ctx, "rooms_list", map[string]any{}); err == nil && !isErr {
			var list struct {
				Rooms []struct {
					ID         string `json:"id"`
					Repository string `json:"repository"`
					Issue      int    `json:"issue"`
				} `json:"rooms"`
			}
			if json.Unmarshal(sc, &list) == nil {
				for _, row := range list.Rooms {
					if strings.EqualFold(row.Repository, repo) && row.Issue == issue && row.ID != "" {
						return row.ID
					}
				}
			}
		}
		select {
		case <-ctx.Done():
			return ""
		case <-r.after(defaultPoll):
		}
	}
}

// cardResult rebuilds a room_summary tools/call result from what callBroker returned, for viewed.
func cardResult(text string, structured json.RawMessage) json.RawMessage {
	b, _ := json.Marshal(map[string]any{"content": []map[string]string{{"type": "text", "text": text}}, "structuredContent": structured})
	return b
}

// cardText is the first text of a tools/call result.
func cardText(res json.RawMessage) (string, bool) {
	var out struct {
		Content []struct{ Text string } `json:"content"`
	}
	if json.Unmarshal(res, &out) != nil || len(out.Content) == 0 || out.Content[0].Text == "" {
		return "", false
	}
	return out.Content[0].Text, true
}

// issueURL is the shape of the URL gh prints for an issue.
var issueURL = regexp.MustCompile(`^https://github\.com/([A-Za-z0-9-]+/[A-Za-z0-9._-]+)/issues/([1-9][0-9]{0,8})$`)

// issueNumber is the number in https://github.com/<repo>/issues/<n>; any other shape, or another
// repository, is refused: the number becomes gh's argument.
func issueNumber(url, repo string) (int, error) {
	m := issueURL.FindStringSubmatch(strings.TrimSpace(url))
	if m == nil || !strings.EqualFold(m[1], repo) { // gh prints the canonical case
		return 0, fmt.Errorf("gh printed %q, not an issue of %s", OneLine(url), repo)
	}
	return strconv.Atoi(m[2])
}

// GHLabel labels the issue with the developer's gh: each value joined to its flag, never a shell.
func GHLabel(ctx context.Context, repo string, issue int, label string) error {
	ctx, cancel := context.WithTimeout(ctx, ghTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gh", "issue", "edit", strconv.Itoa(issue), "--repo="+repo, "--add-label="+label) //nolint:gosec // gh, with a checked repository, a number and a fixed label, never through a shell
	var errOut bytes.Buffer
	cmd.Stderr = &capped{b: &errOut}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("gh issue edit: %w: %s", err, OneLine(errOut.String()))
	}
	return nil
}

// GHAddLabel adds a label with the developer's gh through the REST route, which creates a label the
// repository does not have yet (G5; gh issue edit --add-label refuses one): each value its own
// argument, never a shell.
func GHAddLabel(ctx context.Context, repo string, issue int, label string) error {
	ctx, cancel := context.WithTimeout(ctx, ghTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gh", "api", "-X", "POST", "repos/"+repo+"/issues/"+strconv.Itoa(issue)+"/labels", "-f", "labels[]="+label) //nolint:gosec // gh, with a checked repository, a number and the plan's class, never through a shell
	var errOut bytes.Buffer
	cmd.Stderr = &capped{b: &errOut}
	cmd.Stdout = io.Discard
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("gh api labels: %w: %s", err, OneLine(errOut.String()))
	}
	return nil
}

// planned re-validates the plan the call carries through the broker's plan_check, before anything
// else the tool does (CD3, CD27): the plan a developer was shown is the one that gets filed, and a
// plan edited after it was shown is refused plan_changed, its text as the broker wrote it. A call
// with no plan, or a null one, gets nil and issue_file behaves as it always has.
func (r *Relay) planned(ctx context.Context, args json.RawMessage) (*governed, any) {
	var a struct {
		Plan json.RawMessage `json:"plan"`
		Hash string          `json:"hash"`
	}
	_ = json.Unmarshal(args, &a) // the arguments are issue_draft's too: these two are this check's
	if !present(a.Plan) {
		return nil, nil
	}
	text, sc, isErr, err := r.callBroker(ctx, mcpwire.PlanCheck, map[string]any{"plan": a.Plan, "hash": a.Hash})
	if err != nil {
		return nil, mcpwire.ToolError(explainRelay(err))
	}
	if isErr {
		return nil, mcpwire.ToolError(text) // the broker's refusal, as the model would read plan_check
	}
	var p planShape
	if json.Unmarshal(sc, &p) != nil || !present(p.Plan) || p.Hash == "" {
		return nil, mcpwire.ToolError("unavailable: the broker's plan_check answer is unreadable")
	}
	if p.Status == "replanned" {
		// P14, review B1: the plan was never checked as final, so nothing is filed from it. The
		// developer is shown the plan/v2 and confirms that one, through issue_file again.
		out, _ := mcpwire.ToolResult(map[string]any{"filed": false, "status": "replanned", "plan": p.Plan, "hash": p.Hash,
			"next": "the plan was made before profiles, so nothing was filed: show the developer this plan/v2 and, on their yes, call issue_file again with it and this hash"})
		return nil, out
	}
	var v planView
	// The plan_check answer is what gh's arguments come from: a plan that does not read as a
	// repository and an issue is one this tool cannot file, whatever the broker validated.
	if json.Unmarshal(p.Plan, &v) != nil || !repoRE.MatchString(v.Repository) || len(v.Issues) == 0 ||
		!titleRE.MatchString(v.Issues[0].Title) || strings.TrimSpace(v.Issues[0].Body) == "" {
		return nil, mcpwire.ToolError("unavailable: the plan plan_check validated is not a draft this tool can file")
	}
	return &governed{planShape: p, view: v}, nil
}

// draft asks the broker's issue_draft for the draft to file, or returns the refusal to answer with.
func (r *Relay) draft(ctx context.Context, args json.RawMessage) (draftShape, any) {
	raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": "maku-draft", "method": "tools/call",
		"params": map[string]any{"name": "issue_draft", "arguments": args}})
	res, err := r.relay(ctx, raw)
	if err != nil {
		return draftShape{}, mcpwire.ToolError(explainRelay(err))
	}
	var out struct {
		IsError    bool            `json:"isError"`
		Structured json.RawMessage `json:"structuredContent"`
	}
	var d draftShape
	if json.Unmarshal(res, &out) != nil {
		return d, mcpwire.ToolError("unavailable: the broker's draft is unreadable")
	}
	if out.IsError {
		return d, res // the broker's refusal, as the model would read it from issue_draft
	}
	if json.Unmarshal(out.Structured, &d) != nil || !repoRE.MatchString(d.Repo) || !titleRE.MatchString(d.Title) || strings.TrimSpace(d.Body) == "" {
		return d, mcpwire.ToolError("unavailable: the broker's draft is unreadable")
	}
	return d, nil
}

// ghTimeout bounds one gh call.
var ghTimeout = 60 * time.Second

// maxGHOutput bounds what gh's stdout and stderr keep.
const maxGHOutput = 4 << 10

// GH files the issue with the developer's gh: their identity, the body on stdin, never a label.
func GH(ctx context.Context, repo, title, body string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, ghTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gh", ghArgs(repo, title)...) //nolint:gosec // gh, with a checked repository and title, never through a shell
	cmd.Stdin = strings.NewReader(body)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &capped{b: &out}, &capped{b: &errOut}
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("gh issue create: %w: %s", err, OneLine(errOut.String()))
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	return strings.TrimSpace(lines[len(lines)-1]), nil
}

// ghArgs are gh's arguments: each value joined to its flag, so none reads as a flag of its own.
func ghArgs(repo, title string) []string {
	return []string{"issue", "create", "--repo=" + repo, "--title=" + title, "--body-file=-"}
}

// capped keeps the first maxGHOutput bytes written to it and drops the rest.
type capped struct{ b *bytes.Buffer }

func (c *capped) Write(p []byte) (int, error) {
	if room := maxGHOutput - c.b.Len(); room > 0 {
		c.b.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}
