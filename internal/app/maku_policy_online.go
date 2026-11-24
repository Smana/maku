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
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/Smana/maku/internal/factory/policyview"
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
		if *output == "json" {
			return writePolicyJSON(r.Out, recent)
		}
		return printRecent(r.Out, recent)
	case "explain":
		if *repository != "" || len(pos) != 1 {
			return errors.New("policy explain takes owner/name#PR")
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
		for _, step := range d.Steps {
			_, _ = fmt.Fprintf(w, "\t\t\t%s: %s\t%s\n", step.Row, step.Status, step.Detail)
		}
	}
	return w.Flush()
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
