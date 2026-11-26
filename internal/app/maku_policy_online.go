// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/Smana/maku/internal/factory/evidence"
	"github.com/Smana/maku/internal/factory/meaning"
	"github.com/Smana/maku/internal/factory/policyview"
	"github.com/Smana/maku/internal/factory/reasons"
	makucli "github.com/Smana/maku/internal/maku"
)

func (r Maku) policyOnline(ctx context.Context, verb string, args []string) error {
	fs := flag.NewFlagSet("policy "+verb, flag.ContinueOnError)
	repository := fs.String("repository", "", "owner/name")
	since := fs.String("since", "", "merged since a YYYY-MM-DD or RFC3339 date")
	output := fs.String("output", "", "json")
	pos, err := flags(fs, args)
	if err != nil {
		return err
	}
	client, err := r.policyClient(ctx)
	if err != nil {
		return err
	}
	switch verb {
	case "list":
		if *repository == "" || len(pos) != 0 {
			return errors.New("policy list needs --repository owner/name")
		}
		recent, err := client.PolicyRecent(ctx, *repository, *since)
		if err != nil {
			return err
		}
		// Each policy first (correctness signals CS7): what checks it and how far it is. A broker
		// that cannot answer leaves the decisions alone, as before.
		rep, rerr := client.PolicyRepository(ctx, *repository)
		if rerr != nil {
			_, _ = fmt.Fprintf(r.Err, "maku: the policies of %s are unavailable (%v): only the decisions follow\n", *repository, rerr)
		}
		rows := policyRows(rep)
		if *output == "json" {
			return writePolicyJSON(r.Out, struct {
				policyview.Recent
				Policies []policyRow `json:"policies,omitempty"`
			}{recent, rows})
		}
		if len(rows) > 0 {
			if err := printPolicies(r.Out, rows); err != nil {
				return err
			}
			_, _ = fmt.Fprintln(r.Out)
		}
		return printRecent(r.Out, recent)
	case "explain":
		if *repository == "" && len(pos) == 2 {
			return r.explainPolicy(ctx, client, pos[0], pos[1], *output)
		}
		if *repository != "" || len(pos) != 1 {
			return errors.New("policy explain takes owner/name#PR, or owner/name and a policy name")
		}
		repo, pr, err := policyPR(pos[0])
		if err != nil {
			return err
		}
		timeline, err := client.PolicyTimeline(ctx, repo, pr)
		if err != nil {
			return err
		}
		if *output == "json" {
			return writePolicyJSON(r.Out, timeline)
		}
		return printTimeline(r.Out, timeline)
	case "backtest":
		if *repository == "" || len(pos) != 1 {
			return errors.New("policy backtest takes a policy file and --repository owner/name")
		}
		raw, err := os.ReadFile(filepath.Clean(pos[0]))
		if err != nil {
			return err
		}
		bt, err := client.PolicyBacktest(ctx, *repository, string(raw), *since)
		if err != nil {
			return err
		}
		if *output == "json" {
			return writePolicyJSON(r.Out, bt)
		}
		return printBacktest(r.Out, bt)
	case "promote":
		if *repository == "" || len(pos) != 1 {
			return errors.New("policy promote takes a policy name and --repository owner/name")
		}
		result, err := client.PolicyPromote(ctx, *repository, pos[0])
		if err != nil {
			return err
		}
		if *output == "json" {
			return writePolicyJSON(r.Out, result)
		}
		if result.PullRequestURL != "" {
			_, err = fmt.Fprintln(r.Out, result.PullRequestURL)
		} else {
			_, err = fmt.Fprintln(r.Out, result.Command)
		}
		return err
	case "drill":
		if *repository != "" || len(pos) != 1 {
			return errors.New("policy drill takes owner/name#PR")
		}
		repo, pr, err := policyPR(pos[0])
		if err != nil {
			return err
		}
		result, err := client.PolicyDrill(ctx, repo, pr)
		if err != nil {
			return err
		}
		if *output == "json" {
			return writePolicyJSON(r.Out, result)
		}
		_, err = fmt.Fprintln(r.Out, result.URL)
		return err
	}
	return fmt.Errorf("policy: unknown online verb %q", verb)
}

func (r Maku) policyClient(_ context.Context) (makucli.Client, error) {
	var cfg makucli.Config
	if err := makucli.LoadJSON(filepath.Join(r.Dir, "config.json"), &cfg); err != nil {
		return makucli.Client{}, errors.New("not configured: run maku configure --url https://rooms.<your private domain>")
	}
	if err := cfg.Validate(); err != nil {
		return makucli.Client{}, fmt.Errorf("maku config: %w", err)
	}
	tokPath := filepath.Join(r.Dir, "token.json")
	return makucli.Client{URL: cfg.URL, HC: r.HC, Stream: r.Stream, Issuer: cfg.Issuer, Err: r.Err,
		Token: func(ctx context.Context) (string, error) { return makucli.Token(ctx, r.HC, cfg, tokPath) }}, nil
}

func policyPR(value string) (string, int, error) {
	repo, raw, ok := strings.Cut(value, "#")
	parts := strings.Split(repo, "/")
	n, err := strconv.Atoi(raw)
	if !ok || len(parts) != 2 || parts[0] == "" || parts[1] == "" || err != nil || n < 1 {
		return "", 0, fmt.Errorf("policy explain: %q is not owner/name#PR", value)
	}
	return repo, n, nil
}

func writePolicyJSON(out interface{ Write([]byte) (int, error) }, value any) error {
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func printRecent(out interface{ Write([]byte) (int, error) }, recent policyview.Recent) error {
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "PR\tOUTCOME\tPOLICY\tMEASUREMENT\tDECIDED")
	for _, d := range recent.Decisions {
		_, _ = fmt.Fprintf(w, "#%d\t%s\t%s\t%s\t%s\n", d.PullRequest, d.Outcome+":"+d.Reason, d.Policy, recentMeasurement(d.Rows), d.At.Format("2006-01-02 15:04 UTC"))
	}
	return w.Flush()
}

func recentMeasurement(rows policyview.DecisionRows) string {
	if !rows.Counted {
		return "not read"
	}
	return fmt.Sprintf("%d/%d files, %d/%d lines", rows.Files, rows.MaxFiles, rows.Lines, rows.MaxLines)
}

func printTimeline(out interface{ Write([]byte) (int, error) }, timeline policyview.Timeline) error {
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "WHEN\tHEAD\tPOLICY\tDECISION\tDETAIL")
	for _, d := range timeline.Decisions {
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s:%s\t%s\n", d.At.Format("2006-01-02 15:04 UTC"), d.Head, d.Class, d.Verdict, d.Reason, d.CommentURL)
		for _, line := range explainLines(d, timeline.Meanings) {
			_, _ = fmt.Fprintf(w, "\t\t\t\t%s\n", line)
		}
		for _, step := range d.Steps {
			_, _ = fmt.Fprintf(w, "\t\t\t%s: %s\t%s\n", step.Row, step.Status, step.Detail)
		}
	}
	return w.Flush()
}

// explainLines is one decision in words (correctness signals CS7, W8): whether the policy held,
// what checked the meaning, how to add a signal when nothing does, and what was recorded.
func explainLines(d policyview.Decision, meanings map[string]meaning.Sentence) []string {
	held := (d.Verdict == "arm" && d.Reason == "armed") || d.Reason == "shadow_would_arm"
	var out []string
	if held {
		out = append(out, "holds")
	} else {
		out = append(out, "does not hold: "+reasons.For(d.Reason).Long)
	}
	if m, ok := meanings[d.Class]; ok {
		out = append(out, "meaning checked by: "+m.CheckedBy)
		if m.WayOut != "" {
			out = append(out, strings.Replace(m.WayOut, ", ", ": ", 1))
		}
	}
	switch {
	case d.Reason == "shadow_would_arm":
		out = append(out, "recorded as: would have merged. Merge it unchanged if you agree: that is an agreed decision")
	case held:
		out = append(out, "recorded as: merged by the policy")
	}
	return out
}

// policyRow is one policy of a repository on `maku policy list`.
type policyRow struct {
	Policy     string `json:"policy"`
	Mode       string `json:"mode"`
	CheckedBy  string `json:"checkedBy"`
	Progress   string `json:"progress"`
	WrongCalls int    `json:"wrongCalls"`
}

func countsOf(e *evidence.Evidence) meaning.Counts {
	if e == nil {
		return meaning.Counts{}
	}
	return meaning.Counts{Agreed: e.Clean, Minimum: e.Minimum, Changed: e.Shadow.Changed, WrongCalls: e.Bad,
		Merges: e.Outcomes.Merges, Quiet: max(0, e.Outcomes.Conclusive-e.Outcomes.Bad), Drill: e.Drill}
}

func checkedByWord(m *meaning.Sentence) string {
	switch {
	case m == nil:
		return "-"
	case m.Signal != meaning.None:
		return string(m.Signal)
	case m.Prose:
		return "nothing (prose)"
	}
	return "nothing"
}

func policyRows(rep evidence.Report) []policyRow {
	var out []policyRow
	for _, c := range rep.Classes {
		row := policyRow{Policy: c.Class, Mode: c.Mode, CheckedBy: checkedByWord(c.Meaning), Progress: "-"}
		if e := c.Evidence; e != nil {
			k := countsOf(e)
			row.WrongCalls = e.Bad
			switch c.Mode {
			case "live":
				row.Progress = fmt.Sprintf("%d merged alone, %d quiet", k.Merges, k.Quiet)
			case "shadow":
				row.Progress = fmt.Sprintf("%d of %d agreed", k.Agreed, k.Minimum)
			}
		}
		out = append(out, row)
	}
	return out
}

func printPolicies(out interface{ Write([]byte) (int, error) }, rows []policyRow) error {
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "POLICY\tMODE\tCHECKED BY\tPROGRESS\tWRONG CALLS")
	for _, r := range rows {
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\n", r.Policy, r.Mode, r.CheckedBy, r.Progress, r.WrongCalls)
	}
	return w.Flush()
}

// explainPolicy is one policy of a repository: its sentence, its progress and what blocks its
// promotion with the next step, the settings card in a terminal (correctness signals CS7).
func (r Maku) explainPolicy(ctx context.Context, client makucli.Client, repository, policy, output string) error {
	if strings.Count(repository, "/") != 1 {
		return fmt.Errorf("policy explain: %q is not owner/name", repository)
	}
	rep, err := client.PolicyRepository(ctx, repository)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(rep.Classes, func(c evidence.ClassReport) bool { return c.Class == policy })
	if i < 0 {
		return fmt.Errorf("policy explain: %s has no policy %q", repository, policy)
	}
	c := rep.Classes[i]
	if output == "json" {
		return writePolicyJSON(r.Out, c)
	}
	var lines []string
	if c.Meaning != nil {
		lines = append(lines, c.Meaning.Plain())
	} else {
		lines = append(lines, "This factory does not say what checks this policy's meaning.")
	}
	if e := c.Evidence; e != nil && c.Meaning != nil {
		lines = append(lines, meaning.Progress(c.Mode, *c.Meaning, countsOf(e)))
		for _, code := range e.Blockers {
			says, next := meaning.Blocker(code, countsOf(e), meaning.InForce)
			if next != "" {
				says += " " + next
			}
			lines = append(lines, "Blocks promotion: "+says)
		}
	}
	lines = append(lines, "The words: https://github.com/Smana/maku/blob/main/docs/merge-policies.md#holds-agreed-signal-checked")
	_, err = fmt.Fprintln(r.Out, strings.Join(lines, "\n"))
	return err
}

func printBacktest(out interface{ Write([]byte) (int, error) }, bt policyview.Backtest) error {
	if !bt.Valid {
		return fmt.Errorf("policy backtest: %s", strings.Join(bt.Errors, "; "))
	}
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "PR\tPOLICY\tBEFORE\tAFTER\tCHANGE")
	for _, row := range bt.Rows {
		_, _ = fmt.Fprintf(w, "#%d\t%s\t%s\t%s\t%s\n", row.PullRequest, row.Policy, show(row.Before), show(row.After), row.Change)
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if bt.Counts.Matched == 0 && bt.Counts.Considered > 0 {
		_, _ = fmt.Fprintln(out, "warning: the edited rule matched none of these pull requests")
	}
	_, err := fmt.Fprintf(out, "%d considered: %d changed, %d unchanged, %d unknown\n", bt.Counts.Considered, bt.Counts.Changed, bt.Counts.Unchanged, bt.Counts.Unknown)
	return err
}
