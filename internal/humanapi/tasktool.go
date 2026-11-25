// SPDX-License-Identifier: Apache-2.0

package humanapi

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Smana/maku/api/v1alpha1"
	"github.com/Smana/maku/internal/authn"
	"github.com/Smana/maku/internal/coordinationview"
	"github.com/Smana/maku/internal/envelope"
	reasonwords "github.com/Smana/maku/internal/factory/reasons"
	"github.com/Smana/maku/internal/mcpwire"
	"github.com/Smana/maku/internal/policy"
	"github.com/Smana/maku/internal/roomctrl"
	"github.com/Smana/maku/internal/roomview"
	"github.com/Smana/maku/internal/store"
	"github.com/Smana/maku/internal/summary"
)

const (
	taskVersion     = "task/v1"
	taskDescription = "Use to see where one task stands, what it needs and what changed since you last asked."
	taskMaxText     = 160
	taskMaxNotes    = 5
	taskLabelWidth  = 10
)

var (
	taskInputSchema  = json.RawMessage(`{"type":"object","additionalProperties":false,"required":["task"],"properties":{"task":{"type":"string","pattern":"^[a-z2-7]{8}$"},"since":{"type":"string","pattern":"^(seq:[0-9]{1,18}|[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\\.[0-9]+)?(Z|[+-][0-9]{2}:[0-9]{2}))$"}}}`)
	taskOutputSchema = json.RawMessage(`{"type":"object","required":["apiVersion","room","text","cursor","status","canStart","untrusted"],"properties":{"apiVersion":{"type":"string"},"room":{"type":"string"},"url":{"type":"string"},"since":{"type":"string"},"text":{"type":"string"},"cursor":{"type":"string"},"status":{"type":"object","required":["phase"]},"runs":{"type":"array","items":{"type":"object","required":["id","role"]}},"needsYou":{"type":"array","items":{"type":"object"}},"coordination":{"type":"object"},"items":{"type":"array","items":{"type":"object","required":["seq","at","kind"]}},"more":{"type":"integer"},"question":{"type":"object","required":["text","recommended"],"properties":{"text":{"type":"string"},"recommended":{"type":"string"}}},"canStart":{"type":"boolean"},"canStartWhy":{"type":"string"},"untrusted":{"type":"boolean"}}}`)
	taskRoomPathRE   = regexp.MustCompile(`^/r/[a-z0-9]{1,32}(#[A-Za-z0-9_-]{1,64})?$`)
)

type taskArgs struct {
	Task  string `json:"task"`
	Since string `json:"since"`
}

type taskEnvelope struct {
	APIVersion   string               `json:"apiVersion"`
	Room         string               `json:"room"`
	URL          string               `json:"url,omitempty"`
	Since        time.Time            `json:"since,omitzero"`
	Text         string               `json:"text"`
	Cursor       string               `json:"cursor"`
	Status       any                  `json:"status"`
	Runs         []envelope.RunFact   `json:"runs,omitempty"`
	NeedsYou     []summary.Need       `json:"needsYou,omitempty"`
	Coordination json.RawMessage      `json:"coordination,omitempty"`
	Question     *briefQuestion       `json:"question,omitempty"`
	Items        []roomview.Milestone `json:"items,omitempty"`
	More         int                  `json:"more,omitempty"`
	CanStart     bool                 `json:"canStart"`
	CanStartWhy  string               `json:"canStartWhy,omitempty"`
	Untrusted    bool                 `json:"untrusted"`
}

func (s *Server) taskToolEntry() mcpTool {
	return mcpTool{
		name:        "task",
		call:        s.taskTool,
		description: taskDescription,
		schema:      taskInputSchema,
		output:      taskOutputSchema,
	}
}

func (s *Server) taskTool(ctx context.Context, p authn.Principal, args json.RawMessage) (any, error) {
	var a taskArgs
	bad := toolError("invalid_arguments: task is a room id; " + briefSinceForms)
	if mcpwire.DecodeArgs(args, &a) != nil || !envelope.ValidID(a.Task) {
		return nil, bad
	}
	since, err := parseBriefSince(a.Since, s.now())
	if err != nil {
		return nil, bad
	}
	env, err := s.readTask(ctx, a.Task, p, since)
	if err != nil {
		return nil, err
	}
	return viewResult{text: env.Text, data: env}, nil
}

func (s *Server) readTask(ctx context.Context, id string, p authn.Principal, since briefSince) (taskEnvelope, error) {
	room, row, sub, err := s.readRoom(ctx, id, p)
	if err != nil {
		return taskEnvelope{}, err
	}
	fold, err := s.foldOf(ctx, room, row)
	if err != nil {
		return taskEnvelope{}, err
	}
	now := s.now()
	// Compute standing and coordination once per readTask call (PERF-M8).
	standing := s.standing(ctx, room, p)
	coord := s.coordinationOnce(ctx, id, p)
	sum := s.summaryFromFold(ctx, id, room, fold, sub, p, standing, coord, now)

	scope, err := s.lastScope(ctx, room, row)
	if err != nil {
		return taskEnvelope{}, err
	}
	var question *briefQuestion
	if scope != nil && scope.State == envelope.ScopeQuestion {
		question = &briefQuestion{Text: scope.Question}
		if len(scope.Assumptions) > 0 {
			question.Recommended = scope.Assumptions[0]
		}
	}

	changed, cursor, at, err := s.taskChanges(ctx, room, row, since)
	if err != nil {
		return taskEnvelope{}, err
	}

	web := sub
	web.WebUI = true
	live, _ := s.liveApprovals(ctx, room, web)
	needs, _ := s.needsFor(ctx, room, p, sub, false, needsRead{facts: s.taskFacts(ctx, room), live: live, coord: coord, standing: &standing})
	if len(needs) == 0 && len(sum.NeedsYou) > 0 {
		needs = sum.NeedsYou
	}
	var blocker *summary.Need
	if len(needs) > 0 {
		blocker = &needs[0]
	}

	cause, _ := summary.Cause(fold.Facts, fold.Notes)
	repo := taskRepository(room, fold.Facts)
	canStart, canStartWhy := s.canStart(ctx, p, repo)

	text := renderTaskText(taskTextIn{
		Sum:       sum,
		Repo:      repo,
		Scope:     scope,
		Needs:     needs,
		Blocker:   blocker,
		Cause:     cause,
		Changed:   changed,
		Since:     at,
		Now:       now,
		PublicURL: s.PublicURL,
	})

	sinceAt := at
	if sinceAt.IsZero() {
		sinceAt = changed.Since
	}

	return taskEnvelope{
		APIVersion:   taskVersion,
		Room:         id,
		URL:          sum.URL,
		Since:        sinceAt,
		Text:         text,
		Cursor:       cursor,
		Status:       sum.Status,
		Runs:         sum.Runs,
		NeedsYou:     sum.NeedsYou,
		Coordination: sum.Coordination,
		Question:     question,
		Items:        changed.Items,
		More:         changed.More,
		CanStart:     canStart,
		CanStartWhy:  canStartWhy,
		Untrusted:    true,
	}, nil
}

func (s *Server) summaryFromFold(ctx context.Context, id string, room *v1alpha1.Room, fold summary.State, sub policy.Subject, p authn.Principal, standing summary.GitHubStanding, coord coordFn, now time.Time) summary.Summary {
	view := summary.View(fold, id, s.PublicURL+"/r/"+id, sub, standing, 0, now)
	if fold.Sealed == "" {
		if q, err := s.Log.Queue(ctx, id); err != nil {
			s.log().Warn("task: queue unreadable", "room", id, "err", err)
		} else if len(q) > 0 {
			view.Queued.Untrusted = true
			for _, n := range q {
				view.Queued.Items = append(view.Queued.Items, summary.QueuedNote{Ref: n.Ref, Author: n.Author, Text: n.Text})
			}
		}
	}
	s.warnNoRepository(view, fold.Facts, id, sub)
	taskPhase, phase := "", room.Status.Phase
	if fold.Facts != nil {
		taskPhase = fold.Facts.Phase
	}
	if fold.Sealed != "" {
		phase = "Closed"
	}
	view.Status.State = summary.Coarse(taskPhase, phase, s.nextRun(id))
	if fold.Sealed == "" {
		web := sub
		web.WebUI = true
		view.Status.Approvals = fold.CountApprovals(web, room.Spec.Approvals.FourEyes, now)
	}
	if v, ok := coord(); ok {
		coordinationview.Attach(&view, v)
		if fold.Sealed == "" {
			in := summary.NeedsIn{
				Room: id, URL: s.PublicURL + "/r/" + id, FactsRead: true, Now: now,
				GitHub: standing, Caller: summary.Caller{WebUI: true},
				Coordination: coordinationview.NeedsOf(v), CoordinationStanding: s.childStanding(ctx, v, p),
			}
			view.NeedsYou = summary.Ordered(append(view.NeedsYou, summary.Items(in)...))
		}
	}
	return view
}

func (s *Server) taskChanges(ctx context.Context, room *v1alpha1.Room, row store.RoomState, since briefSince) (roomview.ChangeSet, string, time.Time, error) {
	fork, cutoff, at := roomctrl.ForkSeq(room), s.now().Add(-briefWindow), since.at
	from := max(row.LastSeq-tailScan, fork)
	baseSeq := from
	if since.hasSeq {
		baseSeq = min(max(since.seq, fork), row.LastSeq)
		from = max(baseSeq-1, fork)
	}
	var events []envelope.Event
	cursor := from
	for scanned := 0; scanned < tailScan && cursor < row.LastSeq; {
		n := int(min(int64(pageSize), int64(tailScan-scanned), row.LastSeq-cursor))
		page, err := s.Log.Range(ctx, room.Name, cursor, n)
		if err != nil {
			s.log().Warn("task: log unreadable", "room", room.Name, "err", err)
			return roomview.ChangeSet{}, "", time.Time{}, errLogUnreadable
		}
		for _, e := range page {
			cursor = e.Seq
			if e.RoomID != room.Name {
				continue
			}
			switch {
			case since.hasSeq && e.Seq <= since.seq:
				at = e.TS
			case since.hasSeq:
				events = append(events, e)
			case since.at.IsZero() && e.TS.Before(cutoff):
				if len(events) == 0 {
					baseSeq = e.Seq
				}
			case !since.at.IsZero() && !e.TS.After(since.at):
				baseSeq = e.Seq
			default:
				events = append(events, e)
			}
		}
		scanned += len(page)
		if len(page) < n {
			break
		}
	}
	var base *envelope.TaskFacts
	if baseSeq > fork {
		raw, err := s.Log.TaskStateAt(ctx, room.Name, fork, baseSeq)
		if err != nil {
			s.log().Warn("task: task facts unreadable", "room", room.Name, "err", err)
			return roomview.ChangeSet{}, "", time.Time{}, errLogUnreadable
		}
		if raw != nil {
			var f envelope.TaskFacts
			if json.Unmarshal(raw, &f) == nil {
				base = &f
			}
		}
	}
	set := roomview.Milestones(roomview.MilestoneInput{
		Room:   room.Name,
		URL:    s.PublicURL + "/r/" + room.Name,
		Base:   base,
		Events: events,
		Limit:  briefLimit,
	})
	if cursor < row.LastSeq && set.More == 0 {
		set.More = 1
	}
	if set.Cursor == "" {
		set.Cursor = "seq:" + strconv.FormatInt(cursor, 10)
	}
	return set, set.Cursor, at, nil
}

func taskRepository(room *v1alpha1.Room, facts *envelope.TaskFacts) string {
	if room != nil && room.Spec.Repository != "" {
		return room.Spec.Repository
	}
	return summary.Repo(facts)
}

type taskTextIn struct {
	Sum       summary.Summary
	Repo      string
	Scope     *envelope.Scope
	Needs     []summary.Need
	Blocker   *summary.Need
	Cause     string
	Changed   roomview.ChangeSet
	Since     time.Time
	Now       time.Time
	PublicURL string
}

func renderTaskText(in taskTextIn) string {
	var b strings.Builder
	row := func(label, val string) {
		pad := max(1, taskLabelWidth-utf8.RuneCountInString(label))
		b.WriteString(label + strings.Repeat(" ", pad) + val + "\n")
	}

	left := taskClean(in.Sum.Room)
	if i := in.Sum.Status.Issue; i != nil && i.Number > 0 {
		left = "#" + strconv.Itoa(i.Number)
		if title := taskClean(i.Title); title != "" {
			left += " " + title
		}
	}
	shortRepo := in.Repo
	if _, name, ok := strings.Cut(in.Repo, "/"); ok && name != "" {
		shortRepo = name
	}
	if shortRepo != "" {
		pad := max(2, 60-utf8.RuneCountInString(left))
		b.WriteString(left + strings.Repeat(" ", pad) + taskClean(shortRepo) + "\n")
	} else {
		b.WriteString(left + "\n")
	}

	if p := in.Sum.Status.Profile; p != nil {
		f := envelope.TaskFacts{
			Phase:   in.Sum.Status.Phase,
			Reason:  in.Sum.Status.Reason,
			Issue:   in.Sum.Status.Issue,
			PR:      in.Sum.Status.PR,
			Spend:   in.Sum.Status.Spend,
			Profile: p,
		}
		if in.Sum.Flow != nil {
			f.Plan = in.Sum.Flow.Plan
		}
		card := roomview.CardOf(&f, "")
		wf := taskClean(card.Workflow)
		if card.Reason != "" {
			wf += " · " + taskClean(card.Reason)
		}
		row("Workflow", wf)
		if len(card.Path) > 0 {
			row("Will run", strings.Join(card.Path, " → "))
		}
		if cost := roomview.CostLine(card); cost != "" {
			row("Cost", cost)
		}
		if strip := summary.Strip(in.Sum.Flow); strip != "" {
			row("Path", strip)
		}
	}

	row("State", taskStateWords(in.Sum))
	row("Needs you", taskNeedsText(in.Needs))
	if budget := taskBudgetText(in.Sum); budget != "" {
		row("Budget", budget)
	}
	if merge := taskMergeText(in.Sum); merge != "" {
		row("Merge", merge)
	}
	if checks := taskChecksText(in.Scope); checks != "" {
		row("Checks", checks)
	}
	if fix := summary.Fix(in.Cause); fix != "" {
		row("Fix", taskClean(fix))
	}
	if next := taskNextText(in.Blocker, in.PublicURL); next != "" {
		row("Next", next)
	}

	if len(in.Changed.Items) > 0 {
		ref := in.Since
		if ref.IsZero() {
			ref = in.Changed.Since
		}
		head := "Since you last looked"
		if !ref.IsZero() && in.Now.After(ref) {
			head += " (" + summary.Span(in.Now.Sub(ref)) + ")"
		}
		b.WriteString(head + "\n")
		for _, m := range in.Changed.Items {
			b.WriteString("  " + taskClock(m.At, ref) + "  " + taskMilestoneText(m, in.PublicURL) + "\n")
		}
		if in.Changed.More > 0 {
			b.WriteString("  and " + strconv.Itoa(in.Changed.More) + " more\n")
		}
	}

	if len(in.Sum.Notes.Items) > 0 && !taskHasNoteMilestone(in.Changed.Items) {
		notes := in.Sum.Notes.Items
		if len(notes) > taskMaxNotes {
			notes = notes[len(notes)-taskMaxNotes:]
		}
		b.WriteString("Notes\n")
		for _, n := range notes {
			b.WriteString("  " + taskClock(n.At, time.Time{}) + "  " + taskQuote(n.Text) + "\n")
		}
	}

	if taskAllowedURL(in.Sum.URL, in.PublicURL) {
		row("Room", in.Sum.URL)
	}
	return b.String()
}

var taskPhases = map[string]string{
	"Received": "received", "Triaged": "triaged", "Queued": "queued for a run", "Split": "split into parts",
	"Implementing": "an agent is coding", "Reviewing": "an agent is reviewing", "AwaitingCI": "waiting for CI",
	"AutoMerging": "auto-merging", "Merged": "merged, verifying", "Verifying": "verifying the merge",
	"AwaitingHuman": "waiting for a human", "Escalated": "handed to a maintainer",
	"Done": "done", "NoOp": "nothing to change", "Rejected": "rejected", "Reverted": "merge reverted",
	"Stopped": "stopped", "Closed": "closed",
	"Open": "open", "Active": "an agent is working", "Idle": "idle", "Sealed": "sealed, its log is full",
}

func taskStateWords(s summary.Summary) string {
	if w, ok := taskPhases[s.Status.Phase]; ok {
		return w
	}
	switch s.Status.State {
	case summary.StateWaiting:
		return "waiting for a human"
	case summary.StateRunning:
		return "running"
	case summary.StateQueued:
		return "queued"
	case summary.StateDone:
		return "done"
	case summary.StateFailed:
		return "failed"
	case summary.StateStopped:
		return "stopped"
	}
	if p := taskClean(s.Status.Phase); p != "" {
		return p
	}
	return "open"
}

func taskNeedsText(needs []summary.Need) string {
	if len(needs) == 0 {
		return "nothing"
	}
	n := needs[0]
	var head string
	switch n.Kind {
	case "approval":
		head = "approve " + taskQuote(n.What)
	case "merge":
		if n.Approve {
			head = "approve PR #" + taskClean(n.ID)
		} else {
			head = "merge PR #" + taskClean(n.ID)
		}
	case summary.NeedQuestion:
		head = "answer the question on issue #" + taskClean(n.ID)
	case "hold":
		head = taskBrokerText(n.What)
	default:
		if lbl := taskBrokerText(n.Action.Label); lbl != "" {
			head = lbl
		} else {
			head = taskQuote(n.What)
		}
	}
	if len(needs) > 1 {
		head += fmt.Sprintf(" (+%d more)", len(needs)-1)
	}
	return head
}

func taskBudgetText(s summary.Summary) string {
	sp, b := s.Status.Spend, s.Status.Budget
	sealed := s.Status.Phase == "Closed" || s.Status.Phase == "Sealed"
	settling := !s.Status.SpendFinal && !sealed
	if sp != nil && (sp.UsedMicroUSD > 0 || sp.LimitMicroUSD > 0) {
		var out string
		if sp.LimitMicroUSD > 0 {
			out = fmt.Sprintf("%s of %s (%d %%)", taskDollars(sp.UsedMicroUSD), taskDollars(sp.LimitMicroUSD), taskPercent(sp.UsedMicroUSD, sp.LimitMicroUSD))
		} else {
			out = taskDollars(sp.UsedMicroUSD)
		}
		if settling {
			out += " so far"
		}
		return out
	}
	if b != nil && (b.UsedTokens > 0 || b.LimitTokens > 0) {
		var out string
		if b.LimitTokens > 0 {
			out = fmt.Sprintf("%s of %s tokens (%d %%)", taskTokens(b.UsedTokens), taskTokens(b.LimitTokens), taskPercent(b.UsedTokens, b.LimitTokens))
		} else {
			out = taskTokens(b.UsedTokens) + " tokens"
		}
		if settling {
			out += " so far"
		}
		return out
	}
	return ""
}

func taskChecksText(scope *envelope.Scope) string {
	if scope == nil {
		return ""
	}
	var checks []string
	for _, a := range scope.Acceptance {
		if c := taskClean(a.Check); c != "" {
			checks = append(checks, taskQuote(c))
		}
	}
	return strings.Join(checks, ", ")
}

func taskPolicyName(p *summary.PolicyView) string {
	if p == nil {
		return ""
	}
	for _, name := range []string{p.Name, p.Class} {
		if name != "" && name != "review" && name != "gate" && envelope.ValidClassName(name) {
			return name
		}
	}
	return ""
}

func taskHoldReason(code, policyName string) string {
	switch code {
	case "shadow_would_arm":
		if policyName != "" {
			return "policy " + policyName + " is in shadow here"
		}
		return "it would auto-merge, and the merge gate is in shadow"
	case "class_paused":
		if policyName != "" {
			return "policy " + policyName + " is paused"
		}
		return "the merge policy is paused"
	}
	if _, known := reasonwords.Longs(code)[code]; !known {
		return ""
	}
	return reasonwords.For(code).Short
}

func taskMergeText(s summary.Summary) string {
	p := s.Status.Policy
	if p == nil || s.Status.PR == nil {
		return ""
	}
	switch p.MergedHow {
	case "class":
		if name := taskPolicyName(p); name != "" {
			return "Merged automatically by policy " + name
		}
		return "Merged automatically"
	case "approved":
		who := "a maintainer's"
		if envelope.ValidLogin(s.Status.PR.ApprovedBy) {
			who = "@" + s.Status.PR.ApprovedBy + "'s"
		}
		return "Merged on " + who + " approval"
	}
	if s.Status.Phase != "AwaitingHuman" {
		return ""
	}
	if why := taskHoldReason(s.Status.ReasonCode, taskPolicyName(p)); why != "" {
		return "Waits for a human: " + why
	}
	return ""
}

func taskNextText(n *summary.Need, publicURL string) string {
	if n == nil {
		return ""
	}
	label, u := taskBrokerText(n.Action.Label), n.Action.URL
	if u == "" {
		u = n.URL
	}
	if !taskAllowedURL(u, publicURL) {
		u = ""
	}
	switch {
	case label != "" && u != "":
		return label + " → " + u
	case u != "":
		return u
	default:
		return label
	}
}

func taskMilestoneText(m roomview.Milestone, publicURL string) string {
	switch m.Kind {
	case "note":
		return "note " + taskQuote(m.Text)
	case "handoff":
		s := "handoff " + taskClean(m.Role)
		if m.Text != "" {
			s += ": " + taskQuote(m.Text)
		}
		return s
	case "approval_requested":
		s := "approval requested: " + taskQuote(m.Text)
		if taskAllowedURL(m.URL, publicURL) {
			s += " → " + m.URL
		}
		return s
	case "approval_decided":
		return "approval decided: " + taskBrokerText(m.Text)
	case "judge":
		return "completion judge: " + taskClean(m.Verdict) + " " + taskQuote(m.Text)
	case "verdict":
		return "review verdict: " + taskBrokerText(m.Text)
	case "activity":
		who := "the room"
		if m.Run != "" {
			who = taskClean(m.Run)
		}
		parts := []string{taskPlural(m.Turns, "model turn", "model turns"), taskPlural(m.Tools, "tool call", "tool calls")}
		if m.Pushes > 0 {
			parts = append(parts, taskPlural(m.Pushes, "push", "pushes"))
		}
		return who + ": " + strings.Join(parts, ", ")
	case "run_started":
		if m.Role == "reviewer" && !strings.HasPrefix(m.Text, "changes were requested") {
			return "review started"
		}
		return taskBrokerText(m.Text)
	default:
		return taskBrokerText(m.Text)
	}
}

func taskHasNoteMilestone(items []roomview.Milestone) bool {
	for _, m := range items {
		if m.Kind == "note" {
			return true
		}
	}
	return false
}

func taskClock(t, since time.Time) string {
	t = t.UTC()
	if !since.IsZero() && t.Format("2006-01-02") != since.UTC().Format("2006-01-02") {
		return t.Format("01-02 15:04")
	}
	return t.Format("15:04")
}

func taskPlural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

func taskStrip(s string) string {
	s = strings.Map(func(r rune) rune {
		if envelope.Control(r) {
			return -1
		}
		return r
	}, s)
	return strings.Join(strings.Fields(s), " ")
}

func taskClean(s string) string {
	s = taskStrip(s)
	if r := []rune(s); len(r) > taskMaxText {
		return string(r[:taskMaxText]) + "…"
	}
	return s
}

func taskBrokerText(s string) string {
	return strings.ReplaceAll(taskStrip(s), "`", "")
}

func taskQuote(s string) string {
	s = strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(taskClean(s))
	return `"` + s + `"`
}

func taskDollars(micro int64) string {
	if micro < 0 {
		micro = 0
	}
	cents := (micro + 5000) / 10000
	return fmt.Sprintf("$%d.%02d", cents/100, cents%100)
}

func taskPercent(used, limit int64) int64 {
	return (100*used + limit/2) / limit
}

func taskTokens(n int64) string {
	switch {
	case n < 1000:
		return strconv.FormatInt(n, 10)
	case n < 999_500:
		return strconv.FormatInt((n+500)/1000, 10) + " k"
	default:
		return strconv.FormatFloat(float64((n+50_000)/100_000)/10, 'f', -1, 64) + " M"
	}
}

func taskAllowedURL(u, publicURL string) bool {
	if envelope.GitHubURL(u) {
		return true
	}
	base := strings.TrimRight(publicURL, "/")
	if base == "" || !strings.HasPrefix(u, base) {
		return false
	}
	return taskRoomPathRE.MatchString(strings.TrimPrefix(u, base))
}
