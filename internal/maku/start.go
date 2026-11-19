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
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Smana/maku/internal/mcpwire"
)

// IssueFiler files a confirmed draft as the developer and returns the issue's URL. GH is the one
// maku mcp uses.
type IssueFiler func(ctx context.Context, repo, title, body string) (string, error)

// Labeler puts a label on an issue as the developer. GHLabel is the one maku mcp uses.
type Labeler func(ctx context.Context, repo string, issue int, label string) error

// readyLabel is the label that starts the factory: the developer's own, never run_create.
const readyLabel = "factory/ready"

const startDescription = "Use when the plan is ready. Shows the developer a form; only their answer files the issue and starts the work."

var startEntry = map[string]any{
	"name":        mcpwire.Start,
	"description": startDescription,
	"inputSchema": json.RawMessage(`{"type":"object","additionalProperties":false,"required":["id"],` +
		`"properties":{"id":{"type":"string","minLength":1,"maxLength":128}}}`),
}

type startArgs struct {
	ID string `json:"id"`
}

// planShape is handoff_check's validated plan envelope: the plan the broker re-validated, the hash
// it recomputed over it, and whether this developer may start the factory on it.
type planShape struct {
	Plan        json.RawMessage `json:"plan"`
	Hash        string          `json:"hash"`
	CanStart    bool            `json:"canStart"`
	CanStartWhy string          `json:"canStartWhy"`
	Maintainer  *bool           `json:"maintainer"`
	Status      string          `json:"status"`
}

// planView is what the form shows of a validated plan: the repository it files into, the issue it
// files first, and the ceiling its work is charged against.
type planView struct {
	Repository string `json:"repository"`
	Issues     []struct {
		Index      int    `json:"index"`
		Title      string `json:"title"`
		Body       string `json:"body"`
		Class      string `json:"class"`
		Profile    string `json:"profile"`
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

// planLabels are the labels start applies before factory/ready, in order: class:<c>, then
// workflow:<p> when the plan pins one (P5, P7, WF9), unless the catalogue says this developer does
// not maintain the repository. None without a class.
func (g *governed) planLabels() []string {
	is := g.view.Issues[0]
	if is.Class == "" || (g.Maintainer != nil && !*g.Maintainer) {
		return nil
	}
	labels := []string{"class:" + is.Class}
	if is.Profile != "" {
		labels = append(labels, "workflow:"+is.Profile)
	}
	return labels
}

// dollars is a plan's USD ceiling as a person reads it: two decimals, from the cent up.
func dollars(usd float64) string {
	cents := int64(math.Round(usd * 100))
	return fmt.Sprintf("$%d.%02d", cents/100, cents%100)
}

// draftShape is the issue the form shows and start files as it stands.
type draftShape struct {
	Repo        string `json:"repo"`
	Title       string `json:"title"`
	Body        string `json:"body"`
	CanStart    bool   `json:"canStart"`
	CanStartWhy string `json:"canStartWhy"`
}

// The issue's repository and title, checked again here: they become gh's arguments.
var (
	repoRE  = regexp.MustCompile(`^[A-Za-z0-9-]+/[A-Za-z0-9._-]+$`)
	titleRE = regexp.MustCompile(`^[A-Za-z0-9]`)
)

const needsConfirmation = "needs_confirmation"

type handoffCheckShape struct {
	planShape
	Workflows  []string `json:"workflows"`
	Title      string   `json:"title"`
	Body       string   `json:"body"`
	Repository string   `json:"repository"`
	view       planView
	workflow   string
}

func (h *handoffCheckShape) governed() *governed {
	return &governed{planShape: h.planShape, view: h.view}
}

func (h *handoffCheckShape) draft() draftShape {
	repo := h.Repository
	if repo == "" {
		repo = h.view.Repository
	}
	title := h.Title
	if title == "" && len(h.view.Issues) > 0 {
		title = h.view.Issues[0].Title
	}
	body := h.Body
	if body == "" && len(h.view.Issues) > 0 {
		body = h.view.Issues[0].Body
	}
	return draftShape{
		Repo:        repo,
		Title:       title,
		Body:        body,
		CanStart:    h.CanStart,
		CanStartWhy: h.CanStartWhy,
	}
}

func parseRenderedWorkflow(text string) string {
	for line := range strings.SplitSeq(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if after, ok := strings.CutPrefix(trimmed, "Workflow "); ok {
			if wf := strings.TrimSpace(after); wf != "" {
				return wf
			}
		}
	}
	return ""
}

func (r *Relay) checkHandoff(ctx context.Context, id, workflow string) (*handoffCheckShape, any) {
	callArgs := map[string]any{"id": id}
	if workflow != "" {
		callArgs["workflow"] = workflow
	}
	text, sc, isErr, err := r.callBroker(ctx, "handoff_check", callArgs)
	if err != nil {
		return nil, mcpwire.ToolError(explainRelay(err))
	}
	if isErr {
		return nil, mcpwire.ToolError(text)
	}
	var h handoffCheckShape
	if json.Unmarshal(sc, &h) != nil || !present(h.Plan) || h.Hash == "" {
		return nil, mcpwire.ToolError("unavailable: the broker's handoff_check answer is unreadable")
	}
	if json.Unmarshal(h.Plan, &h.view) != nil {
		return nil, mcpwire.ToolError("unavailable: the plan handoff_check validated is unreadable")
	}
	d := h.draft()
	if !repoRE.MatchString(d.Repo) || len(h.view.Issues) == 0 || !titleRE.MatchString(d.Title) || strings.TrimSpace(d.Body) == "" {
		return nil, mcpwire.ToolError("unavailable: the plan handoff_check validated is not an issue this tool can file")
	}
	switch {
	case h.view.Issues[0].Profile != "":
		h.workflow = h.view.Issues[0].Profile
	case parseRenderedWorkflow(text) != "":
		h.workflow = parseRenderedWorkflow(text)
	case h.view.Issues[0].Suggestion != nil && h.view.Issues[0].Suggestion.Profile != "":
		h.workflow = h.view.Issues[0].Suggestion.Profile
	case len(h.Workflows) > 0:
		h.workflow = h.Workflows[0]
	default:
		h.workflow = "default"
	}
	return &h, nil
}

// startTool serves start {id}: it re-validates the stored handoff plan on the broker, shows the
// developer a confirmation form (UX3, UX14, UX15), re-validates on workflow change, and files and
// starts the work with the developer's gh only on their explicit yes.
func (r *Relay) startTool(ctx context.Context, args json.RawMessage) any {
	var a startArgs
	if mcpwire.DecodeArgs(args, &a) != nil || strings.TrimSpace(a.ID) == "" || len(a.ID) > 128 {
		return mcpwire.ToolError("invalid_arguments: id is required, and no field outside {id} is taken")
	}
	r.mu.Lock()
	elicit := r.forms
	r.mu.Unlock()

	checked, refused := r.checkHandoff(ctx, a.ID, "")
	if refused != nil {
		return refused
	}
	draft := checked.draft()
	if !elicit || r.File == nil {
		msg := needsConfirmation + ": start asks the developer to confirm, and this client cannot ask (no elicitation); " +
			"show them the issue and let them file it with gh issue create\n" + startFallbackNext(draft)
		return mcpwire.ToolError(msg)
	}

	offer := draft.CanStart && r.Label != nil
	props := map[string]any{
		"file": map[string]any{
			"type":        "boolean",
			"title":       "File this issue",
			"default":     false,
			"description": "Filed with your gh, as you",
		},
	}
	tail := "It gets no label: label it " + readyLabel + " yourself to start the work."
	if offer {
		props["start"] = map[string]any{
			"type":        "boolean",
			"title":       "Start now",
			"default":     true,
			"description": "Also labels it " + readyLabel + " with your gh, which starts the work",
		}
		tail = "Keep \"Start now\" on to also label it " + readyLabel + " with your gh, which starts the work."
	} else if why := StartWhy(draft.CanStartWhy); why != "" {
		tail = "Work cannot be started from here (" + why + "). It gets no label: label it " + readyLabel + " yourself to start the work."
	}

	defaultWF := OneLine(checked.workflow)
	if len(checked.Workflows) > 1 {
		choices := make([]string, 0, len(checked.Workflows)+1)
		if defaultWF != "" && !slices.Contains(checked.Workflows, defaultWF) {
			choices = append(choices, defaultWF)
		}
		choices = append(choices, checked.Workflows...)
		props["workflow"] = map[string]any{
			"type":    "string",
			"title":   "Workflow",
			"enum":    choices,
			"default": defaultWF,
		}
	}

	budgetLine := "Budget none"
	if checked.view.Budget.CeilingUSD > 0 {
		budgetLine = "Budget up to " + dollars(checked.view.Budget.CeilingUSD)
	}
	meta := fmt.Sprintf("Workflow %s\n%s\n\n", defaultWF, budgetLine)
	if n := len(checked.view.Issues); n > 1 {
		meta += fmt.Sprintf("The plan holds %d issues: this files the first.\n\n", n)
	}

	ans, err := r.ask(ctx, "elicitation/create", map[string]any{
		"message":         fmt.Sprintf("File this issue on %s as you, with gh? %s\n\n%s%s\n\n%s", draft.Repo, tail, meta, draft.Title, draft.Body),
		"requestedSchema": map[string]any{"type": "object", "required": []string{"file"}, "properties": props},
	})
	if errors.Is(err, context.DeadlineExceeded) {
		return mcpwire.ToolError(needsConfirmation + "; no answer to the confirmation form within " + elicitWait.String() +
			". The plan is kept: if the developer saw the form, call start again with the same id (" + a.ID +
			") and they answer it; only if their client never showed it, show them the issue and let them file it with gh issue create\n" +
			startFallbackNext(draft))
	}
	if err != nil {
		return mcpwire.ToolError("not_filed: the developer was not asked: " + err.Error() + "\n" + startFallbackNext(draft))
	}
	var res struct {
		Action  string                     `json:"action"`
		Content map[string]json.RawMessage `json:"content"`
	}
	if len(ans.Error) > 0 || json.Unmarshal(ans.Result, &res) != nil {
		return mcpwire.ToolError("not_filed: the client could not ask the developer\n" + startFallbackNext(draft))
	}
	file, _ := formBool(res.Content["file"])
	if res.Action != "accept" || !file {
		// The plan is kept: a new handoff would plan again, start with the same id asks again.
		out, _ := mcpwire.ToolResult(map[string]any{"filed": false, "why": "the developer did not confirm (" + res.Action + ")",
			"next": "nothing was filed and the plan is kept: when the developer wants it filed, call start again with the same id (" + a.ID + ")"})
		return out
	}

	if len(checked.Workflows) > 1 {
		if chosenWF := formString(res.Content["workflow"]); chosenWF != "" && chosenWF != defaultWF {
			rechecked, ref := r.checkHandoff(ctx, a.ID, chosenWF)
			if ref != nil {
				return ref
			}
			checked = rechecked
			draft = checked.draft()
			offer = draft.CanStart && r.Label != nil
		}
	}

	startNow, chosen := formBool(res.Content["start"])
	if offer && !chosen {
		r.log().Warn("start: the form answer has no usable start; treated as not started", "start", string(res.Content["start"]))
	}
	url, err := r.File(ctx, draft.Repo, draft.Title, draft.Body)
	if err != nil {
		return mcpwire.ToolError("not_filed: " + err.Error())
	}
	if !offer || !startNow {
		next := "label it " + readyLabel + " to start the work: your decision"
		if offer && !chosen {
			next = "start was not chosen (the form came back without it), so the work is not started: ask the developer whether to start it, and label it " + readyLabel + " only on their yes"
		}
		out, _ := mcpwire.ToolResult(map[string]any{"filed": true, "url": url, "started": false, "next": next})
		return out
	}
	return r.startFromHandoff(ctx, draft.Repo, url, checked.governed().planLabels())
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

func formString(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return strings.TrimSpace(s)
	}
	return ""
}

func startFallbackNext(d draftShape) string {
	start := "to start the work they add the label " + readyLabel + " themselves, or you call start again and they choose Start now in its form"
	if !d.CanStart {
		start = "work cannot be started from here; they label the issue " + readyLabel + " themselves when it can run"
	}
	return "next: show the developer this one command and let them run it; it only files the issue, and nothing is filed until they do. " +
		"Never add a label yourself: " + start + ":\n" + fallbackCommand(d)
}

func (r *Relay) startFromHandoff(ctx context.Context, repo, url string, labels []string) any {
	result := map[string]any{"filed": true, "url": url, "started": false}
	n, err := issueNumber(url, repo)
	for _, label := range labels {
		if err != nil || r.ClassLabel == nil {
			break
		}
		if cerr := r.ClassLabel(ctx, repo, n, label); cerr != nil {
			result["why"] = "filed without labels, so not started: " + label + ": " + OneLine(cerr.Error()) +
				"; label it " + strings.Join(labels, ", ") + ", then " + readyLabel + ", yourself"
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
	result["task"] = id
	result["room"] = id
	out, _ := mcpwire.ToolResult(result)
	if text, sc, isErr, err := r.callBroker(ctx, mcpwire.Task, map[string]string{"task": id}); err == nil && !isErr && text != "" {
		var meta struct {
			APIVersion string `json:"apiVersion"`
		}
		if json.Unmarshal(sc, &meta) == nil && meta.APIVersion == "task/v1" {
			out["content"] = []map[string]string{{"type": "text", "text": "Filed " + url + " · task " + id + "\n\n" + text}}
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

// cardResult rebuilds a tools/call result from what callBroker returned, for viewed.
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
	if m == nil || !strings.EqualFold(m[1], repo) {
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

// GHUnlabel removes a label from the issue with the developer's gh before GHLabel re-applies it:
// each value joined to its flag, never a shell.
func GHUnlabel(ctx context.Context, repo string, issue int, label string) error {
	ctx, cancel := context.WithTimeout(ctx, ghTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gh", "issue", "edit", strconv.Itoa(issue), "--repo="+repo, "--remove-label="+label) //nolint:gosec // gh, with a checked repository, a number and a fixed label, never through a shell
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

// startWhyFixes is the one-line fix for each canStartWhy reason that is the operator's to fix (#621).
var startWhyFixes = map[string]string{
	"repository_not_offered": "the broker does not offer start here; serve the repository from the factory catalog or list it in human.intake.plan.repositories, and keep it out of human.mcp.start.except",
	"access_unwired":         "set human.access and human.githubLink in the broker config so it can check who writes the repository",
	"sponsored":              "a sponsored caller never starts work; start it from your own session",
}

// StartWhy is a canStartWhy reason on one line, followed by its fix when there is one: what the
// start form and maku doctor show. An older broker's reason, such as off, is shown as it is.
func StartWhy(why string) string {
	why = OneLine(why)
	if fix, ok := startWhyFixes[why]; ok {
		return why + ": " + fix
	}
	return why
}
