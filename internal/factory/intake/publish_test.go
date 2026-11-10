// SPDX-License-Identifier: Apache-2.0

package intake

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/factory/forge/forgetest"
	"github.com/Smana/maku/internal/factory/taskid"
	"github.com/Smana/maku/internal/redact"
)

var (
	redactorOnce sync.Once
	redactorVal  *redact.Redactor
	redactorErr  error
)

func testRedactor(t *testing.T) *redact.Redactor {
	t.Helper()
	redactorOnce.Do(func() { redactorVal, redactorErr = redact.New() })
	if redactorErr != nil {
		t.Fatal(redactorErr)
	}
	return redactorVal
}

const proposalOK = "docs(alerts): the crash-loop rule fires on a completed Job\n" +
	"## What is wrong\nThe rule counts Job pods.\n## What it should be\nIt skips them.\n## Acceptance check\nThe alert stays quiet."

// investigated is a Done investigate task of issue 7, with a proposal.
func investigated(name string) *v1alpha1.Task {
	return &v1alpha1.Task{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "agent-system", CreationTimestamp: metav1.NewTime(t0),
		Labels: map[string]string{v1alpha1.LabelIssue: "7"}},
		Spec: v1alpha1.TaskSpec{Source: v1alpha1.Source{Kind: "api", RequestedBy: "system:runlore"}, Repository: "Smana/cloud-native-ref",
			Issue: 7, Template: "investigate", DataClass: "internal"},
		Status: v1alpha1.TaskStatus{Phase: v1alpha1.PhaseDone}}
}

// publishing is a poller whose proposal reader answers text, with 7 labelled factory/publish by actors.
func publishing(t *testing.T, text string, tk *v1alpha1.Task, actors ...string) (*IssuePoller, *forgetest.Fake, client.Client) {
	t.Helper()
	f := forgetest.NewFake()
	var evs []forge.LabelEvent
	for i, a := range actors {
		evs = append(evs, forge.LabelEvent{Actor: a, Label: LabelPublish, At: t0.Add(time.Duration(i) * time.Minute)})
	}
	f.SetEvents(7, evs...)
	f.SetLabeled(LabelPublish, forge.Item{Number: 7})
	f.SetIssue(forge.Issue{Number: 7, Title: "RunLore: A on r", Body: "stub"})
	p, c := poller(t, f, tk)
	p.Cfg.Current().Maintainers = []string{"Smana"}
	p.Now = func() time.Time { return t0.Add(time.Hour) }
	p.Redact = testRedactor(t)
	p.Proposal = func(context.Context, *v1alpha1.Task) (string, error) {
		if text == "" {
			return "", ErrNoProposal
		}
		return text, nil
	}
	return p, f, c
}

func getTask(t *testing.T, c client.Client, name string) v1alpha1.Task {
	t.Helper()
	var tk v1alpha1.Task
	if err := c.Get(t.Context(), types.NamespacedName{Namespace: "agent-system", Name: name}, &tk); err != nil {
		t.Fatal(err)
	}
	return tk
}

// RA9, R7, offline proof 3: a maintainer's factory/publish files the proposal once, unlabelled,
// with a footer, records it on the task and answers on the stub.
func TestPublishFilesTheProposalOnce(t *testing.T) {
	p, f, c := publishing(t, proposalOK, investigated("3buqdlot"), "Smana")
	if err := p.Poll(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := f.Created(); len(got) != 1 || !strings.HasPrefix(got[0], "docs(alerts): the crash-loop rule fires on a completed Job\n") ||
		!strings.Contains(got[0], "\n\n---\nPublished by @Smana from agent factory task `3buqdlot`.\nlabels: ") || !strings.HasSuffix(got[0], "labels: factory/published-from:3buqdlot,class:review") {
		t.Fatalf("the draft's title, a footer and only the marker that makes a retry find it, never factory/ready: %q", got)
	}
	tk := getTask(t, c, "3buqdlot")
	if pub := tk.Status.Published; pub == nil || pub.Issue != 501 || pub.By != "smana" || !pub.At.Time.Equal(t0.Add(time.Hour)) ||
		tk.Labels[v1alpha1.LabelPublishedIssue] != "501" {
		t.Fatalf("%+v %v", tk.Status.Published, tk.Labels)
	}
	if cs := strings.Join(f.Comments(7), "\n"); !strings.Contains(cs, "Published as #501 by @smana") {
		t.Fatalf("%q", cs)
	}
	if !slices.Contains(f.Removed(7), LabelPublish) {
		t.Fatalf("the label stays: %v", f.Removed(7))
	}
	// the same label again: a pointer, no second issue
	f.SetLabeled(LabelPublish, forge.Item{Number: 7})
	if err := p.Poll(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(f.Created()) != 1 || len(f.Removed(7)) != 2 {
		t.Fatalf("%d issues, removed %v", len(f.Created()), f.Removed(7))
	}
}

// A replay on a task already published: a pointer to the issue, nothing created.
func TestPublishReplayNamesTheIssue(t *testing.T) {
	tk := investigated("3buqdlot")
	tk.Status.Published = &v1alpha1.Published{Issue: 42, By: "smana", At: metav1.NewTime(t0)}
	p, f, _ := publishing(t, proposalOK, tk, "Smana")
	if err := p.Poll(t.Context()); err != nil {
		t.Fatal(err)
	}
	if cs := strings.Join(f.Comments(7), "\n"); len(f.Created()) != 0 || !strings.Contains(cs, "#42") {
		t.Fatalf("%v %q", f.Created(), cs)
	}
}

// The refusals each answer once, consume the label and create nothing.
func TestPublishRefusals(t *testing.T) {
	aws := "AKIA" + "Z3QPLR7MXN2KWT5V" // an access key id, built from halves so no literal sits in the source
	for name, c := range map[string]struct {
		actors []string
		tk     func(*v1alpha1.Task)
		text   string
		want   string
	}{
		"a stranger":                   {[]string{"mallory"}, nil, proposalOK, "only a maintainer's factory/publish"},
		"a maintainer then a stranger": {[]string{"Smana", "mallory"}, nil, proposalOK, "only a maintainer's factory/publish"},
		"a task still running":         {[]string{"Smana"}, func(t *v1alpha1.Task) { t.Status.Phase = v1alpha1.PhaseImplementing }, proposalOK, "still running"},
		"a stopped task":               {[]string{"Smana"}, func(t *v1alpha1.Task) { t.Status.Phase = v1alpha1.PhaseStopped }, proposalOK, "made no proposal"},
		"a task of another template":   {[]string{"Smana"}, func(t *v1alpha1.Task) { t.Spec.Template = "pair" }, proposalOK, "made no proposal"},
		"no proposal":                  {[]string{"Smana"}, nil, "", "made no proposal"},
		"a bad title":                  {[]string{"Smana"}, nil, "Fix it\n## What is wrong\nx\n## What it should be\ny\n## Acceptance check\nz", "issue template"},
		"no sections":                  {[]string{"Smana"}, nil, "docs(a): b\njust text", "issue template"},
		"a secret":                     {[]string{"Smana"}, nil, strings.Replace(proposalOK, "The rule counts Job pods.", "key "+aws+" and secret_access_key = wJalrXUtnFEMI/K7MDENG/bPxRfiCYzZ3qLpTnVb", 1), "looks like a secret"},
	} {
		t.Run(name, func(t *testing.T) {
			tk := investigated("3buqdlot")
			if c.tk != nil {
				c.tk(tk)
			}
			p, f, cl := publishing(t, c.text, tk, c.actors...)
			if err := p.Poll(t.Context()); err != nil {
				t.Fatal(err)
			}
			cs := strings.Join(f.Comments(7), "\n")
			if len(f.Created()) != 0 || !strings.Contains(cs, "did not publish #7's proposal") || !strings.Contains(cs, c.want) ||
				!slices.Contains(f.Removed(7), LabelPublish) {
				t.Fatalf("%d issues, comments %q, removed %v", len(f.Created()), cs, f.Removed(7))
			}
			if got := getTask(t, cl, "3buqdlot"); got.Status.Published != nil {
				t.Fatalf("a refusal records nothing: %+v", got.Status.Published)
			}
		})
	}
}

// The last gate is GitHub's own reading of the final title and body: whatever it renders live, or
// cannot render, publishes nothing. The refusal answers once and consumes the label, as invalid_draft
// and redacted do; the caller's footer, a deliberate mention, is added after it and never sent.
func TestPublishRenderGate(t *testing.T) {
	const title = "docs(alerts): the crash-loop rule fires on a completed Job"
	mention := `<p dir="auto"><span class="user-mention">@x</span></p>`
	for name, c := range map[string]struct {
		render func(string) (string, error)
		want   string
	}{
		"a link in the body": {func(s string) (string, error) {
			if s == title {
				return "<p>ok</p>", nil
			}
			return `<p><a href="https://evil.example">x</a></p>`, nil
		}, "cannot make inert"},
		"a link in the title": {func(s string) (string, error) {
			if s == title {
				return `<p><A HREF="https://evil.example">x</A></p>`, nil
			}
			return "<p>ok</p>", nil
		}, "cannot make inert"},
		"a mention's class": {func(string) (string, error) { return mention, nil }, "cannot make inert"},
		"an issue link's class": {func(string) (string, error) {
			return `<p><span class="x issue-link">#1</span></p>`, nil
		}, "cannot make inert"},
		"a commit link's class": {func(string) (string, error) {
			return `<p><span class="commit-link">18e264e</span></p>`, nil
		}, "cannot make inert"},
	} {
		t.Run(name, func(t *testing.T) {
			p, f, cl := publishing(t, proposalOK, investigated("3buqdlot"), "Smana")
			f.SetRender(c.render)
			if err := p.Poll(t.Context()); err != nil {
				t.Fatal(err)
			}
			cs := strings.Join(f.Comments(7), "\n")
			if len(f.Created()) != 0 || !strings.Contains(cs, "did not publish #7's proposal") || !strings.Contains(cs, c.want) ||
				!slices.Contains(f.Removed(7), LabelPublish) {
				t.Fatalf("%d issues, comments %q, removed %v", len(f.Created()), cs, f.Removed(7))
			}
			if got := getTask(t, cl, "3buqdlot"); got.Status.Published != nil {
				t.Fatalf("a refusal records nothing: %+v", got.Status.Published)
			}
		})
	}

	t.Run("an inert page publishes, the footer never rendered", func(t *testing.T) {
		p, f, _ := publishing(t, proposalOK, investigated("3buqdlot"), "Smana")
		// Text in a page is no markup: GitHub escapes it, so a class name in a code span is harmless.
		f.SetRender(func(string) (string, error) {
			return `<h2 dir="auto">What is wrong</h2><p><code>class=&quot;user-mention&quot; issue-link &lt;a href</code></p>`, nil
		})
		if err := p.Poll(t.Context()); err != nil {
			t.Fatal(err)
		}
		if len(f.Created()) != 1 || !strings.Contains(f.Created()[0], "Published by @Smana") {
			t.Fatalf("%q", f.Created())
		}
		got := f.Rendered()
		if len(got) != 2 || got[0] != title || !strings.HasPrefix(got[1], "## What is wrong") {
			t.Fatalf("rendered %q", got)
		}
		if strings.Contains(strings.Join(got, ""), "Published by") {
			t.Fatalf("the footer was rendered: %q", got)
		}
	})

	t.Run("a secret is never sent to be rendered", func(t *testing.T) {
		aws := "AKIA" + "Z3QPLR7MXN2KWT5V"
		text := strings.Replace(proposalOK, "The rule counts Job pods.", "key "+aws+" and secret_access_key = wJalrXUtnFEMI/K7MDENG/bPxRfiCYzZ3qLpTnVb", 1)
		p, f, _ := publishing(t, text, investigated("3buqdlot"), "Smana")
		if err := p.Poll(t.Context()); err != nil {
			t.Fatal(err)
		}
		if len(f.Rendered()) != 0 || !strings.Contains(strings.Join(f.Comments(7), ""), "looks like a secret") {
			t.Fatalf("rendered %q, comments %q", f.Rendered(), f.Comments(7))
		}
	})

	t.Run("what Public withholds is refused for what it is", func(t *testing.T) {
		text := strings.Replace(proposalOK, "The rule counts Job pods.", "> | a |\n|---|\n| x | @Smana |", 1)
		p, f, _ := publishing(t, text, investigated("3buqdlot"), "Smana")
		if err := p.Poll(t.Context()); err != nil {
			t.Fatal(err)
		}
		if len(f.Created()) != 0 || len(f.Rendered()) != 0 || !strings.Contains(strings.Join(f.Comments(7), ""), "cannot make inert") {
			t.Fatalf("created %q, rendered %q, comments %q", f.Created(), f.Rendered(), f.Comments(7))
		}
	})
}

// A render that fails is no verdict on the proposal: as with a write check that fails, nothing is
// filed or said, the label waits, and a later poll publishes once GitHub answers. Every poll of an
// outage logs it.
func TestPublishWaitsOutARenderOutage(t *testing.T) {
	for name, cause := range map[string]string{"an error": "forge: 502", "a rate limit": "forge: secondary rate limit"} {
		t.Run(name, func(t *testing.T) {
			p, f, _ := publishing(t, proposalOK, investigated("3buqdlot"), "Smana")
			var logged bytes.Buffer
			p.Log = slog.New(slog.NewTextHandler(&logged, nil))
			f.SetRender(func(string) (string, error) { return "", errors.New(cause) })
			for poll := 1; poll <= 3; poll++ {
				if err := p.Poll(t.Context()); err == nil {
					t.Fatalf("poll %d: the outage was swallowed", poll)
				}
				if len(f.Created()) != 0 || len(f.Comments(7)) != 0 || len(f.Removed(7)) != 0 {
					t.Fatalf("poll %d: created %q, comments %q, removed %v", poll, f.Created(), f.Comments(7), f.Removed(7))
				}
				if got := strings.Count(logged.String(), "could not be rendered"); got != poll {
					t.Fatalf("poll %d: logged %d times: %s", poll, got, logged.String())
				}
			}
			f.SetRender(nil)
			if err := p.Poll(t.Context()); err != nil {
				t.Fatal(err)
			}
			if len(f.Created()) != 1 || !slices.Contains(f.Removed(7), LabelPublish) {
				t.Fatalf("recovered: created %q, removed %v", f.Created(), f.Removed(7))
			}
		})
	}
}

// Public's code spans break the redactor's rules: a token split by them matches none. So the raw
// draft is scanned first, and a secret is refused with nothing sent to be rendered. Each token is
// built here, from halves, so no literal sits in the source.
func TestPublishScansTheDraftBeforeItIsMadeInert(t *testing.T) {
	fold := func(s string) string { // fullwidth: no rule reads it until Text folds it
		var b strings.Builder
		for _, r := range s {
			if r >= '!' && r <= '~' {
				r += 0xFEE0
			}
			b.WriteRune(r)
		}
		return b.String()
	}
	hex36 := strings.Repeat("4f9a1c7e", 5)[:36]
	slackBot := "xox" + "b-2846193750-3917264058-" + "aB3dE6gH9jK2mN5pQ8sT1vW4"
	for name, c := range map[string]struct{ title, secret string }{
		"a slack bot token":  {"", slackBot},
		"a slack user token": {"", "xox" + "p-2846193750-3917264058-3917264058-" + hex36[:32]},
		"a github pat":       {"", "gh" + "p_" + hex36},
		"a github oauth":     {"", "gh" + "o_" + hex36},
		"a github app token": {"", "gh" + "s_" + hex36},
		"an npm token":       {"", "np" + "m_" + hex36},
		"in the title":       {slackBot, ""},
		"folded by Text":     {"", fold(slackBot)},
	} {
		t.Run(name, func(t *testing.T) {
			text := proposalOK
			if c.title != "" {
				text = strings.Replace(text, "completed Job\n", "completed "+c.title+"\n", 1)
			}
			if c.secret != "" {
				text = strings.Replace(text, "The rule counts Job pods.", "see "+c.secret+" here", 1)
			}
			p, f, cl := publishing(t, text, investigated("3buqdlot"), "Smana")
			if err := p.Poll(t.Context()); err != nil {
				t.Fatal(err)
			}
			cs := strings.Join(f.Comments(7), "\n")
			if len(f.Created()) != 0 || !strings.Contains(cs, "looks like a secret") || !slices.Contains(f.Removed(7), LabelPublish) {
				t.Fatalf("%d issues, comments %q", len(f.Created()), cs)
			}
			if got := f.Rendered(); len(got) != 0 {
				t.Fatal("the draft was sent to be rendered")
			}
			if got := getTask(t, cl, "3buqdlot"); got.Status.Published != nil {
				t.Fatalf("a refusal records nothing: %+v", got.Status.Published)
			}
		})
	}
}

// A write check that fails leaves the label for the next poll, as factory/ready does.
func TestPublishNeedsTheWriteCheck(t *testing.T) {
	p, f, _ := publishing(t, proposalOK, investigated("3buqdlot"), "Smana")
	p.CanWrite = func(context.Context, string) (bool, error) { return false, nil }
	if err := p.Poll(t.Context()); err != nil || len(f.Created()) != 0 || !strings.Contains(strings.Join(f.Comments(7), ""), "only a maintainer") {
		t.Fatalf("%v %v", err, f.Created())
	}
	p, f, _ = publishing(t, proposalOK, investigated("3buqdlot"), "Smana")
	p.CanWrite = func(context.Context, string) (bool, error) { return false, errors.New("github down") }
	if err := p.Poll(t.Context()); err == nil || len(f.Created()) != 0 || len(f.Comments(7)) != 0 || len(f.Removed(7)) != 0 {
		t.Fatalf("no answer is no refusal: %v", err)
	}
}

// R8, offline proof 3: a hostile proposal is filed inert: no link, no raw HTML, no bidi character.
func TestPublishFilesAnInertBody(t *testing.T) {
	hostile := "docs(alerts): fix [the rule](https://evil.example/t)\n## What is wrong\nsee [x](https://evil.example/x) www.evil.example <img src=x onerror=1> \u202eevil\n" +
		"## What it should be\nhttps://evil.example/y\n## Acceptance check\nz"
	p, f, _ := publishing(t, hostile, investigated("3buqdlot"), "Smana")
	if err := p.Poll(t.Context()); err != nil {
		t.Fatal(err)
	}
	created := f.Created()
	if len(created) != 1 {
		t.Fatalf("%v", created)
	}
	if strings.ContainsRune(created[0], '\u202e') || strings.Contains(created[0], "](") || strings.Contains(created[0], "<img") ||
		strings.Contains(created[0], " https://evil.example/y") || strings.Contains(created[0], " www.evil.example") {
		t.Fatalf("%q", created[0])
	}
	if !strings.Contains(created[0], "`https://evil.example/y`") {
		t.Fatalf("%q", created[0])
	}
}

// RA9, R9: a fix task's origin is the factory's record, never what the issue says.
func TestOriginOfAStartedIssue(t *testing.T) {
	published := func(name, labelled string, recorded int) *v1alpha1.Task {
		tk := investigated(name)
		tk.Labels[v1alpha1.LabelPublishedIssue] = labelled
		tk.Status.Published = &v1alpha1.Published{Issue: recorded, By: "smana", At: metav1.NewTime(t0)}
		return tk
	}
	for name, c := range map[string]struct {
		issue int
		body  string
		objs  []client.Object
		want  string
	}{
		"the published issue":              {42, "fix it", []client.Object{published("3buqdlot", "42", 42)}, "3buqdlot"},
		"an issue that names an origin":    {43, "origin: 3buqdlot\nFollows agent factory task 3buqdlot", []client.Object{published("3buqdlot", "42", 42)}, ""},
		"a label the record does not back": {42, "fix it", []client.Object{published("3buqdlot", "42", 41)}, ""},
		"a label with no record": {42, "fix it", func() []client.Object {
			tk := investigated("3buqdlot")
			tk.Labels[v1alpha1.LabelPublishedIssue] = "42"
			return []client.Object{tk}
		}(), ""},
		"a task of another repository's issue": {42, "fix it", func() []client.Object {
			tk := published("3buqdlot", "42", 42)
			tk.Spec.Repository = "Smana/other"
			return []client.Object{tk}
		}(), ""},
	} {
		t.Run(name, func(t *testing.T) {
			f := forgetest.NewFake()
			labelled(f, c.issue, "Smana")
			f.SetIssue(forge.Issue{Number: c.issue, Title: "docs(alerts): x", Body: c.body, Author: "ogenki-agent-factory[bot]"})
			p, cl := poller(t, f, c.objs...)
			p.Cfg.Current().Repositories = []config.Repository{{Name: "Smana/cloud-native-ref", Maintainers: []string{"Smana"}, TriggerLabel: "factory/ready"},
				{Name: "Smana/other", Maintainers: []string{"Smana"}, TriggerLabel: "factory/ready"}}
			p.Repository = "Smana/cloud-native-ref"
			if err := p.Poll(t.Context()); err != nil {
				t.Fatal(err)
			}
			var l v1alpha1.TaskList
			if err := cl.List(t.Context(), &l, client.MatchingLabels{v1alpha1.LabelIssue: strconv.Itoa(c.issue)}); err != nil {
				t.Fatal(err)
			}
			var started *v1alpha1.Task
			for i := range l.Items {
				if l.Items[i].Spec.Source.Kind == "issue" {
					started = &l.Items[i]
				}
			}
			if started == nil || started.Spec.Origin != c.want {
				t.Fatalf("%+v", started)
			}
		})
	}
}

// A crash between CreateIssue and the record, or a record that failed, leaves an issue the label
// still asks for: the retry reuses it, and files no second one.
func TestPublishReusesTheIssueAnEarlierAttemptFiled(t *testing.T) {
	p, f, c := publishing(t, proposalOK, investigated("3buqdlot"), "Smana")
	f.SetIssue(forge.Issue{Number: 88, Title: "docs(alerts): x", Author: "ogenki-agent-factory[bot]", Labels: []string{"factory/published-from:3buqdlot"}})
	f.SetIssue(forge.Issue{Number: 89, Title: "another task's", Labels: []string{"factory/published-from:other000"}})
	if err := p.Poll(t.Context()); err != nil {
		t.Fatal(err)
	}
	tk := getTask(t, c, "3buqdlot")
	if len(f.Created()) != 0 || tk.Status.Published == nil || tk.Status.Published.Issue != 88 || tk.Labels[v1alpha1.LabelPublishedIssue] != "88" {
		t.Fatalf("%v %+v %v", f.Created(), tk.Status.Published, tk.Labels)
	}
	if cs := strings.Join(f.Comments(7), "\n"); !strings.Contains(cs, "Published as #88") || !slices.Contains(f.Removed(7), LabelPublish) {
		t.Fatalf("%q %v", cs, f.Removed(7))
	}
}

// The kill switch pauses publishing, as it pauses factory/ready: the label waits.
func TestPublishWaitsOutTheKillSwitch(t *testing.T) {
	p, f, _ := publishing(t, proposalOK, investigated("3buqdlot"), "Smana")
	p.Stopped = func(context.Context) bool { return true }
	if err := p.Poll(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(f.Created()) != 0 || len(f.Comments(7)) != 0 || len(f.Removed(7)) != 0 {
		t.Fatalf("%v %v %v", f.Created(), f.Comments(7), f.Removed(7))
	}
}

// Without a redactor nothing is published: no scan, no publication.
func TestPublishWithoutARedactorRefuses(t *testing.T) {
	p, f, _ := publishing(t, proposalOK, investigated("3buqdlot"), "Smana")
	p.Redact = nil
	if err := p.Poll(t.Context()); err != nil {
		t.Fatal(err)
	}
	if cs := strings.Join(f.Comments(7), "\n"); len(f.Created()) != 0 || !strings.Contains(cs, "looks like a secret") {
		t.Fatalf("%v %q", f.Created(), cs)
	}
}

// A marker label anyone can apply by hand is no record: only an issue the factory's own App filed
// is reused, so nobody can point a publish at an issue of their choosing.
func TestPublishIgnoresAMarkerFiledBySomeoneElse(t *testing.T) {
	p, f, c := publishing(t, proposalOK, investigated("3buqdlot"), "Smana")
	f.SetIssue(forge.Issue{Number: 88, Title: "x", Author: "mallory", Labels: []string{"factory/published-from:3buqdlot"}})
	if err := p.Poll(t.Context()); err != nil {
		t.Fatal(err)
	}
	tk := getTask(t, c, "3buqdlot")
	if len(f.Created()) != 1 || tk.Status.Published == nil || tk.Status.Published.Issue == 88 {
		t.Fatalf("%d filed, %+v", len(f.Created()), tk.Status.Published)
	}
}

// gcp-0, 2026-10-28, task 2d5ch27g: RunLore's task was rejected on the daily task cap before any
// triage, so it has no template, and factory/publish answered "the triager's run made no proposal".
// The refusal names the rejection and its reason.
func TestPublishOnARejectedTaskNamesTheRejection(t *testing.T) {
	tk := investigated("2d5ch27g")
	tk.Spec.Template = ""
	tk.Status.Phase, tk.Status.Reason = v1alpha1.PhaseRejected, "daily_task_cap"
	p, f, _ := publishing(t, "", tk, "Smana")
	if err := p.Poll(t.Context()); err != nil {
		t.Fatal(err)
	}
	cs := strings.Join(f.Comments(7), "\n")
	want := "The agent factory did not publish #7's proposal: its task `2d5ch27g` was not accepted, so no run made one: " +
		"the factory has reached its daily task cap."
	if len(f.Created()) != 0 || !strings.Contains(cs, want) || strings.Contains(cs, "made no proposal to publish") ||
		!slices.Contains(f.Removed(7), LabelPublish) {
		t.Fatalf("%d issues, comments %q, removed %v", len(f.Created()), cs, f.Removed(7))
	}
}

func TestProposalPaths(t *testing.T) {
	for name, c := range map[string]struct {
		body string
		want []string
	}{
		"listed": {"## Acceptance check\nquiet\n## Files\n- `observability/a.yaml`\n- `observability/b.yml`\n",
			[]string{"observability/a.yaml", "observability/b.yml"}},
		"next heading": {"## Files\n- `a.yaml`\n## Notes\n- `b.yaml`\n", []string{"a.yaml"}},
		"footer":       {"## Files\n- `a.yaml`\n\n---\nPublished by @x from agent factory task `y`.", []string{"a.yaml"}},
		"no section":   {"## Acceptance check\nquiet\n", nil},
		"dotdot":       {"## Files\n- `../etc/passwd`\n", nil},
		"dotdot inner": {"## Files\n- `a/../b.yaml`\n", nil},
		"absolute":     {"## Files\n- `/abs/x`\n", nil},
		"no code span": {"## Files\n- a.yaml\n", nil},
		"whitespace":   {"## Files\n- `a b.yaml`\n", nil},
		"too long":     {"## Files\n- `" + strings.Repeat("a", 201) + "`\n", nil},
		"one bad line": {"## Files\n- `a.yaml`\nnot a path\n", nil},
		"eleven":       {"## Files\n" + strings.Repeat("- `a.yaml`\n", 11), nil},
		"ten":          {"## Files\n" + strings.Repeat("- `a.yaml`\n", 10), slices.Repeat([]string{"a.yaml"}, 10)},
	} {
		t.Run(name, func(t *testing.T) {
			if got := ProposalPaths(c.body); !slices.Equal(got, c.want) || (c.want == nil) != (got == nil) {
				t.Fatalf("ProposalPaths = %q, want %q", got, c.want)
			}
		})
	}
}

// hostClasses are the host's classes as Task 4's inference reads them.
func hostClasses() map[string]config.Class {
	deps := `^(apps|flux|infrastructure|observability|security|tooling)/.+\.ya?ml$`
	adopt := &config.Author{Adopt: &config.Adopt{}}
	return map[string]config.Class{
		"alert-rules": {AllowedFiles: []string{`^observability/.+\.ya?ml$`}},
		"deps-patch":  {AllowedFiles: []string{deps}, Author: adopt},
		"deps-minor":  {AllowedFiles: []string{deps}, Author: adopt},
		"dead-links":  {AllowedFiles: []string{`^(docs|website/content)/.+\.md$`}, Oracle: &config.Oracle{}},
		"docs-links":  {}, "docs": {}, "review": {}, "revert": {},
	}
}

const proposalWithFiles = proposalOK + "\n## Files\n- `observability/base/rules/apps.yaml`"

// startsPublishing is publishing with the host's classes and runlore an internal principal whose
// publishStarts is starts.
func startsPublishing(t *testing.T, starts bool, text string, tk *v1alpha1.Task) (*IssuePoller, *forgetest.Fake, client.Client) {
	t.Helper()
	p, f, c := publishing(t, text, tk, "Smana")
	p.Cfg.Current().Classes = hostClasses()
	p.Cfg.Current().API.InternalPrincipals = []config.InternalPrincipal{{Sponsored: config.Sponsored{Name: "runlore"}, PublishStarts: starts}}
	return p, f, c
}

func fixName(gen int) string {
	return taskid.Name(taskid.IssueKey("Smana/cloud-native-ref", 501, gen))
}

// failTaskCreateOnce makes the poller's first Task create fail.
func failTaskCreateOnce(p *IssuePoller, c client.Client) {
	failed := false
	p.Client = interceptor.NewClient(c.(client.WithWatch), interceptor.Funcs{Create: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
		if _, ok := obj.(*v1alpha1.Task); ok && !failed {
			failed = true
			return errors.New("the API server refused")
		}
		return cl.Create(ctx, obj, opts...)
	}})
}

func commentsOf(f *forgetest.Fake, n int) string { return strings.Join(f.Comments(n), "\n") }

// K5 to K9, K13, K14, offline proof 2: factory/publish on an internal principal's stub with
// publishStarts files the issue with the inferred class and starts the fix, once; anything else
// only publishes.
func TestPublishStarts(t *testing.T) {
	t.Run("starts the fix and records the class", func(t *testing.T) {
		p, f, c := startsPublishing(t, true, proposalWithFiles, investigated("3buqdlot"))
		if err := p.Poll(t.Context()); err != nil {
			t.Fatal(err)
		}
		if got := f.Created(); len(got) != 1 || !strings.HasSuffix(got[0], "labels: factory/published-from:3buqdlot,class:alert-rules") {
			t.Fatalf("%q", got)
		}
		fix := getTask(t, c, fixName(0))
		if fix.Spec.Source.Kind != "issue" || fix.Spec.Source.RequestedBy != "github:Smana" || fix.Payer() != "github:smana" ||
			fix.Spec.Issue != 501 || fix.Spec.Origin != "3buqdlot" || fix.Spec.PredictedClass != "alert-rules" || fix.Spec.DataClass != "public" {
			t.Fatalf("%+v", fix.Spec)
		}
		if got := getTask(t, c, "3buqdlot"); got.Status.Published == nil || got.Status.Published.Class != "alert-rules" {
			t.Fatalf("%+v", got.Status.Published)
		}
		if cs := commentsOf(f, 7); !strings.Contains(cs, "#501") || !strings.Contains(cs, fixName(0)) || !strings.Contains(cs, "alert-rules") {
			t.Fatalf("%q", cs)
		}
		f.SetLabeled(LabelPublish, forge.Item{Number: 7}) // the same label again
		if err := p.Poll(t.Context()); err != nil {
			t.Fatal(err)
		}
		if len(f.Created()) != 1 || len(tasks(t, c)) != 2 {
			t.Fatalf("%d issues, %d tasks", len(f.Created()), len(tasks(t, c)))
		}
	})

	t.Run("a proposal with no files is review", func(t *testing.T) {
		p, f, c := startsPublishing(t, true, proposalOK, investigated("3buqdlot"))
		if err := p.Poll(t.Context()); err != nil {
			t.Fatal(err)
		}
		fix := getTask(t, c, fixName(0))
		if got := f.Created(); len(got) != 1 || !strings.HasSuffix(got[0], "class:review") || fix.Spec.PredictedClass != "review" {
			t.Fatalf("%q %+v", got, fix.Spec)
		}
	})

	for name, c := range map[string]struct {
		starts bool
		tk     func(*v1alpha1.Task)
	}{
		"publishStarts off":       {false, nil},
		"a runlore-kind task":     {true, func(t *v1alpha1.Task) { t.Spec.Source.Kind = "runlore" }},
		"a sponsored principal's": {true, func(t *v1alpha1.Task) { t.Spec.Source.RequestedBy = "system:slack-app" }},
	} {
		t.Run("publishes only: "+name, func(t *testing.T) {
			tk := investigated("3buqdlot")
			if c.tk != nil {
				c.tk(tk)
			}
			p, f, cl := startsPublishing(t, c.starts, proposalWithFiles, tk)
			if err := p.Poll(t.Context()); err != nil {
				t.Fatal(err)
			}
			if len(f.Created()) != 1 || len(tasks(t, cl)) != 1 || !strings.Contains(commentsOf(f, 7), "factory/ready") {
				t.Fatalf("%d issues, %d tasks, %q", len(f.Created()), len(tasks(t, cl)), commentsOf(f, 7))
			}
		})
	}

	t.Run("a failed create retries to one issue, one Task and the same class (K14)", func(t *testing.T) {
		p, f, c := startsPublishing(t, true, proposalWithFiles, investigated("3buqdlot"))
		failTaskCreateOnce(p, c)
		if err := p.Poll(t.Context()); err == nil || len(f.Removed(7)) != 0 {
			t.Fatalf("the poll errs and the label stays: %v %v", err, f.Removed(7))
		}
		if err := p.Poll(t.Context()); err != nil {
			t.Fatal(err)
		}
		fix := getTask(t, c, fixName(0))
		got := getTask(t, c, "3buqdlot")
		created := f.Created()
		if len(created) != 1 || !strings.HasSuffix(created[0], "class:alert-rules") || slices.Contains(f.Added(501), "class:review") ||
			len(tasks(t, c)) != 2 || fix.Spec.PredictedClass != "alert-rules" || got.Status.Published == nil || got.Status.Published.Class != "alert-rules" {
			t.Fatalf("%q %v %d %+v %+v", created, f.Added(501), len(tasks(t, c)), fix.Spec, got.Status.Published)
		}
	})

	t.Run("a generation-1 Task opened between a failure and the retry starts nothing (K13)", func(t *testing.T) {
		p, f, c := startsPublishing(t, true, proposalWithFiles, investigated("3buqdlot"))
		failTaskCreateOnce(p, c)
		if err := p.Poll(t.Context()); err == nil {
			t.Fatal("the first poll errs")
		}
		f.SetEvents(501, forge.LabelEvent{Actor: "Smana", Label: "factory/ready", At: t0})
		if err := p.one(t.Context(), p.Cfg.Current(), 501); err != nil { // a poll that ran factory/ready on the filed issue
			t.Fatal(err)
		}
		if err := p.Poll(t.Context()); err != nil {
			t.Fatal(err)
		}
		var open []string
		for _, tk := range tasks(t, c) {
			if tk.Spec.Issue == 501 {
				open = append(open, tk.Name)
			}
		}
		if !slices.Equal(open, []string{fixName(1)}) || !strings.Contains(commentsOf(f, 7), "factory/ready") {
			t.Fatalf("%v %q", open, commentsOf(f, 7))
		}
	})

	for _, hide := range []bool{false, true} {
		name := ""
		if hide {
			name = " with a lagging List"
		}
		t.Run("factory/ready seen with the retry: the start wins, one Task and one charge (K13)"+name, func(t *testing.T) {
			p, f, c := startsPublishing(t, true, proposalWithFiles, investigated("3buqdlot"))
			failTaskCreateOnce(p, c)
			if err := p.Poll(t.Context()); err == nil {
				t.Fatal("the first poll errs")
			}
			f.SetEvents(501, forge.LabelEvent{Actor: "Smana", Label: "factory/ready", At: t0})
			f.SetLabeled("factory/ready", forge.Item{Number: 501})
			f.SetLabeled(LabelPublish, forge.Item{Number: 7})
			if hide { // the informer has not yet seen the Task the retry creates
				base := p.Client
				created := false
				p.Client = interceptor.NewClient(base.(client.WithWatch), interceptor.Funcs{
					Create: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
						if _, ok := obj.(*v1alpha1.Task); ok {
							created = true
						}
						return cl.Create(ctx, obj, opts...)
					},
					List: func(ctx context.Context, cl client.WithWatch, l client.ObjectList, opts ...client.ListOption) error {
						err := cl.List(ctx, l, opts...)
						if tl, ok := l.(*v1alpha1.TaskList); ok && created {
							tl.Items = slices.DeleteFunc(tl.Items, func(t v1alpha1.Task) bool { return t.Spec.Issue == 501 })
						}
						return err
					}})
			}
			if err := p.Poll(t.Context()); err != nil {
				t.Fatal(err)
			}
			var on501 []v1alpha1.Task
			for _, tk := range tasks(t, c) {
				if tk.Spec.Issue == 501 {
					on501 = append(on501, tk)
				}
			}
			if len(on501) != 1 || on501[0].Name != fixName(0) || on501[0].Payer() != "github:smana" {
				t.Fatalf("%+v", on501)
			}
			p.Client = c // the cache has caught up: the next poll refuses the label
			if err := p.Poll(t.Context()); err != nil {
				t.Fatal(err)
			}
			if got := commentsOf(f, 501); !strings.Contains(got, "is still running") {
				t.Fatalf("task_active expected: %q", got)
			}
		})
	}

	t.Run("a filed issue closed before the retry starts nothing", func(t *testing.T) {
		p, f, c := startsPublishing(t, true, proposalWithFiles, investigated("3buqdlot"))
		f.SetIssue(forge.Issue{Number: 88, Title: "docs(alerts): x", Body: proposalWithFiles, Author: "ogenki-agent-factory[bot]", State: "CLOSED",
			Labels: []string{"factory/published-from:3buqdlot", "class:alert-rules"}})
		if err := p.Poll(t.Context()); err != nil {
			t.Fatal(err)
		}
		if len(f.Created()) != 0 || len(tasks(t, c)) != 1 || !strings.Contains(commentsOf(f, 7), "factory/ready") {
			t.Fatalf("%v %d %q", f.Created(), len(tasks(t, c)), commentsOf(f, 7))
		}
	})

	t.Run("an edited marker issue starts nothing (K7)", func(t *testing.T) {
		p, f, c := startsPublishing(t, true, proposalWithFiles, investigated("3buqdlot"))
		f.SetIssue(forge.Issue{Number: 88, Title: "docs(alerts): x", Body: proposalWithFiles, Author: "ogenki-agent-factory[bot]", State: "OPEN",
			Labels: []string{"factory/published-from:3buqdlot", "class:alert-rules"}, LastEditedAt: t0.Add(time.Minute)})
		if err := p.Poll(t.Context()); err != nil {
			t.Fatal(err)
		}
		if len(f.Created()) != 0 || len(tasks(t, c)) != 1 || !strings.Contains(commentsOf(f, 7), "factory/ready") {
			t.Fatalf("%v %d %q", f.Created(), len(tasks(t, c)), commentsOf(f, 7))
		}
	})
}

// Review Focus 3: a maintainer's factory/ready after a published start is generation 1, carrying
// the origin and the class.
func TestFactoryReadyAfterAPublishedStartIsGenerationOne(t *testing.T) {
	p, f, c := startsPublishing(t, true, proposalWithFiles, investigated("3buqdlot"))
	if err := p.Poll(t.Context()); err != nil {
		t.Fatal(err)
	}
	gen0 := getTask(t, c, fixName(0))
	gen0.Status.Phase = v1alpha1.PhaseDone
	if err := c.Status().Update(t.Context(), &gen0); err != nil {
		t.Fatal(err)
	}
	labelled(f, 501, "Smana")
	if err := p.Poll(t.Context()); err != nil {
		t.Fatal(err)
	}
	fix := getTask(t, c, fixName(1))
	if fix.Spec.Origin != "3buqdlot" || fix.Spec.PredictedClass != "alert-rules" {
		t.Fatalf("%+v", fix.Spec)
	}
}

// A crash or a failed comment after the Task is created: the retry announces the start that
// happened, never "label it factory/ready" beside a running fix.
func TestPublishRetryAnnouncesTheStartThatHappened(t *testing.T) {
	t.Run("the record is missing", func(t *testing.T) {
		p, f, c := startsPublishing(t, true, proposalWithFiles, investigated("3buqdlot"))
		p.Client = interceptor.NewClient(c.(client.WithWatch), interceptor.Funcs{SubResourcePatch: func(context.Context, client.Client, string, client.Object, client.Patch, ...client.SubResourcePatchOption) error {
			return errors.New("no record") // the record fails; the Task is already created
		}})
		if err := p.Poll(t.Context()); err != nil {
			t.Fatal(err)
		}
		f.SetLabeled(LabelPublish, forge.Item{Number: 7})
		p.Client = c
		if err := p.Poll(t.Context()); err != nil {
			t.Fatal(err)
		}
		if cs := commentsOf(f, 7); !strings.Contains(cs, "The fix started at once") || len(tasks(t, c)) != 2 {
			t.Fatalf("%d tasks, %q", len(tasks(t, c)), cs)
		}
	})
	t.Run("the comment failed after the record", func(t *testing.T) {
		p, f, c := startsPublishing(t, true, proposalWithFiles, investigated("3buqdlot"))
		if err := p.Poll(t.Context()); err != nil {
			t.Fatal(err)
		}
		f.SetComments(7) // the comment never landed
		f.SetLabeled(LabelPublish, forge.Item{Number: 7})
		if err := p.Poll(t.Context()); err != nil {
			t.Fatal(err)
		}
		if cs := commentsOf(f, 7); !strings.Contains(cs, "The fix started at once") || strings.Contains(cs, "label it `factory/ready`") || len(tasks(t, c)) != 2 {
			t.Fatalf("%d tasks, %q", len(tasks(t, c)), cs)
		}
	})
}
