// SPDX-License-Identifier: Apache-2.0

package intake

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/factory/forge/forgetest"
	"github.com/Smana/maku/internal/ghidentity"
)

const factoryBot = "ogenki-agent-factory[bot]"

type subLogins map[string]string

func (l subLogins) Login(_ context.Context, sub, _ string) (string, error) {
	switch sub {
	case "zinactive":
		return "", ghidentity.ErrInactive
	case "zdown":
		return "", errors.New("zitadel down")
	}
	return l[sub], nil
}

// startRecords answers the broker's web start records by issue.
func startRecords(recs ...HandoffStart) HandoffStarts {
	return func(_ context.Context, _ string, issue int) (HandoffStart, bool, error) {
		for _, h := range recs {
			if h.Issue == issue {
				return h, true, nil
			}
		}
		return HandoffStart{}, false, nil
	}
}

// webStart is the broker's record of issue n's web start for sub, a few seconds after the App's label.
func webStart(n int, sub string) HandoffStart {
	return HandoffStart{Handoff: "abcdefghijklmnopqrstuvwx23", Repository: "smana/cloud-native-ref", Issue: n, Sub: sub,
		Labels: []string{"workflow:careful", "plan-first", "factory/ready"}, StartedAt: t0.Add(3 * time.Second)}
}

// appFiled is issue n as the broker filed it: the factory App applied factory/ready and the pins.
func appFiled(f *forgetest.Fake, n int, actors ...string) {
	if len(actors) == 0 {
		actors = []string{factoryBot}
	}
	labelled(f, n, actors...)
	f.SetIssue(forge.Issue{Number: n, Title: "Fix the link", Body: "docs/a.md links to a moved page.", Author: factoryBot,
		Labels: []string{"workflow:careful", "plan-first", "factory/ready"}})
}

// A web New task start: the App's factory/ready on the issue the broker recorded it filed for a
// maintainer starts that maintainer's task, with the App's workflow pin theirs.
func TestABrokerConfirmedHandoffStartsTheHumansTask(t *testing.T) {
	f := forgetest.NewFake()
	appFiled(f, 512)
	p, c := poller(t, f)
	p.HandoffStarts, p.Logins, p.Now = startRecords(webStart(512, "zsmana")), subLogins{"zsmana": "Smana"}, func() time.Time { return t0.Add(time.Minute) }
	if err := p.Poll(t.Context()); err != nil {
		t.Fatal(err)
	}
	got := tasks(t, c)
	if len(got) != 1 || len(f.Comments(512)) != 0 {
		t.Fatalf("tasks %+v comments %q", got, f.Comments(512))
	}
	tk := got[0]
	if tk.Spec.Source.RequestedBy != "github:Smana" || tk.Spec.Intake == nil || tk.Spec.Intake.Login != "Smana" ||
		tk.Spec.Intake.Profile != "careful" || tk.Spec.Outcome != "plan-first" || tk.Spec.IssueAuthor != factoryBot {
		t.Fatalf("spec %+v intake %+v", tk.Spec.Source, tk.Spec.Intake)
	}
	if v1alpha1.Person("Smana") != "github:smana" {
		t.Fatal("the ledger key is the human's")
	}
}

// Every other App or non-maintainer label is refused as before: no record, a record for a
// non-maintainer, an inactive or unlinked human, another issue's record, a later label, a record
// far from the label.
func TestAnUnconfirmedAppLabelIsRefused(t *testing.T) {
	later := func(f *forgetest.Fake, n int) { appFiled(f, n, factoryBot, factoryBot) }
	far := webStart(512, "zsmana")
	far.StartedAt = t0.Add(time.Hour)
	for name, c := range map[string]struct {
		recs   []HandoffStart
		setup  func(*forgetest.Fake, int)
		logins subLogins
	}{
		"no record":              {},
		"a non-maintainer's":     {recs: []HandoffStart{webStart(512, "zsomeone")}},
		"an unlinked human":      {recs: []HandoffStart{webStart(512, "znobody")}},
		"an inactive human":      {recs: []HandoffStart{webStart(512, "zinactive")}},
		"another issue's record": {recs: []HandoffStart{webStart(511, "zsmana")}},
		"a second App label":     {recs: []HandoffStart{webStart(512, "zsmana")}, setup: later},
		"a record far from it":   {recs: []HandoffStart{far}},
		"another labeller's":     {recs: []HandoffStart{webStart(512, "zsmana")}, setup: func(f *forgetest.Fake, n int) { appFiled(f, n, "someone") }},
	} {
		t.Run(name, func(t *testing.T) {
			f := forgetest.NewFake()
			if c.setup != nil {
				c.setup(f, 512)
			} else {
				appFiled(f, 512)
			}
			p, cl := poller(t, f)
			p.HandoffStarts, p.Logins = startRecords(c.recs...), subLogins{"zsmana": "Smana", "zsomeone": "someone"}
			p.Now = func() time.Time { return t0.Add(time.Hour) }
			if err := p.Poll(t.Context()); err != nil {
				t.Fatal(err)
			}
			if n := len(tasks(t, cl)); n != 0 || len(f.Comments(512)) != 1 || len(f.Removed(512)) != 1 {
				t.Fatalf("tasks %d comments %q removed %q", n, f.Comments(512), f.Removed(512))
			}
		})
	}
}

// A record that names another issue than the one asked for confirms nothing: a replay.
func TestAReplayedRecordIsRefused(t *testing.T) {
	f := forgetest.NewFake()
	appFiled(f, 513)
	p, c := poller(t, f)
	replay := webStart(512, "zsmana")
	p.HandoffStarts = func(context.Context, string, int) (HandoffStart, bool, error) { return replay, true, nil }
	p.Logins, p.Now = subLogins{"zsmana": "Smana"}, func() time.Time { return t0.Add(time.Minute) }
	if err := p.Poll(t.Context()); err != nil {
		t.Fatal(err)
	}
	if n := len(tasks(t, c)); n != 0 || len(f.Comments(513)) != 1 {
		t.Fatalf("tasks %d comments %q", n, f.Comments(513))
	}
}

// A non-maintainer's label never waits for, or reads, a broker record: it is refused at once.
func TestAnotherLabellerNeverWaits(t *testing.T) {
	f := forgetest.NewFake()
	appFiled(f, 512, "someone")
	p, c := poller(t, f)
	p.HandoffStarts = func(context.Context, string, int) (HandoffStart, bool, error) {
		t.Error("the broker was asked about another labeller's label")
		return HandoffStart{}, false, nil
	}
	p.Logins, p.Now = subLogins{}, func() time.Time { return t0.Add(time.Second) }
	if err := p.Poll(t.Context()); err != nil {
		t.Fatal(err)
	}
	if n := len(tasks(t, c)); n != 0 || len(f.Comments(512)) != 1 {
		t.Fatalf("tasks %d comments %q", n, f.Comments(512))
	}
}

// The App's label waits for a record still in flight, and for a check that went unanswered; it is
// never refused for either.
func TestAnAppLabelWaitsForTheRecord(t *testing.T) {
	for name, c := range map[string]struct {
		starts HandoffStarts
		sub    string
	}{
		"the record not yet written": {starts: startRecords()},
		"the broker down": {starts: func(context.Context, string, int) (HandoffStart, bool, error) {
			return HandoffStart{}, false, errors.New("broker down")
		}},
		"the link unanswered": {starts: startRecords(webStart(512, "zdown"))},
	} {
		t.Run(name, func(t *testing.T) {
			f := forgetest.NewFake()
			appFiled(f, 512)
			p, cl := poller(t, f)
			p.HandoffStarts, p.Logins, p.Now = c.starts, subLogins{}, func() time.Time { return t0.Add(30 * time.Second) }
			_ = p.Poll(t.Context())
			if n := len(tasks(t, cl)); n != 0 || len(f.Comments(512)) != 0 || len(f.Removed(512)) != 0 {
				t.Fatalf("tasks %d comments %q removed %q", n, f.Comments(512), f.Removed(512))
			}
		})
	}
}

// Without the broker's records wired, the App's label is refused, as every non-maintainer's.
func TestNoHandoffReaderRefusesTheAppLabel(t *testing.T) {
	f := forgetest.NewFake()
	appFiled(f, 512)
	p, c := poller(t, f)
	p.Logins = subLogins{"zsmana": "Smana"}
	if err := p.Poll(t.Context()); err != nil {
		t.Fatal(err)
	}
	if n := len(tasks(t, c)); n != 0 || len(f.Comments(512)) != 1 {
		t.Fatalf("tasks %d comments %q", n, f.Comments(512))
	}
}

// A broker that will never serve the records (403, or no route) is not waited on past the grace,
// and an unanswered check is not waited on past handoffGiveUp: both refuse as handoff_unconfirmed,
// so the issue is never left waiting for ever.
func TestAnUnconfirmableStartIsRefusedInTime(t *testing.T) {
	unserved := func(context.Context, string, int) (HandoffStart, bool, error) {
		return HandoffStart{}, false, fmt.Errorf("%w: broker: 403 not_permitted", ErrUnconfirmable)
	}
	down := func(context.Context, string, int) (HandoffStart, bool, error) {
		return HandoffStart{}, false, errors.New("broker down")
	}
	for name, c := range map[string]struct {
		starts HandoffStarts
		sub    string
		at     time.Duration
		wait   bool
	}{
		"unserved, in the grace":        {starts: unserved, at: 30 * time.Second, wait: true},
		"unserved, past the grace":      {starts: unserved, at: 3 * time.Minute},
		"the broker down, for a while":  {starts: down, at: 10 * time.Minute, wait: true},
		"the broker down, too long":     {starts: down, at: 16 * time.Minute},
		"the link unanswered, too long": {starts: startRecords(webStart(512, "zdown")), at: 16 * time.Minute},
	} {
		t.Run(name, func(t *testing.T) {
			f := forgetest.NewFake()
			appFiled(f, 512)
			p, cl := poller(t, f)
			p.HandoffStarts, p.Logins, p.Now = c.starts, subLogins{}, func() time.Time { return t0.Add(c.at) }
			err := p.Poll(t.Context())
			if c.wait {
				if len(f.Comments(512)) != 0 || len(f.Removed(512)) != 0 {
					t.Fatalf("refused while waiting: %q", f.Comments(512))
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			cs := f.Comments(512)
			if n := len(tasks(t, cl)); n != 0 || len(cs) != 1 || !strings.Contains(cs[0], "could not be confirmed") || len(f.Removed(512)) != 1 {
				t.Fatalf("tasks %d comments %q removed %q", n, cs, f.Removed(512))
			}
		})
	}
}
