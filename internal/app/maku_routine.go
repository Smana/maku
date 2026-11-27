// SPDX-License-Identifier: Apache-2.0

package app

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/Smana/maku/internal/maku"
	"github.com/Smana/maku/internal/roomview"
)

const routineUsage = "usage: maku routine <list|status|run|pause|resume|history> ..."

func (r Maku) routine(ctx context.Context, c maku.Client, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("%s\n%s", routineUsage, makuUsage)
	}
	verb, rest := args[0], args[1:]
	switch verb {
	case "list":
		return r.routineList(ctx, c, rest)
	case "status":
		return r.routineStatus(ctx, c, rest)
	case "run":
		return r.routineRun(ctx, c, rest)
	case "pause":
		return r.routinePause(ctx, c, rest)
	case "resume":
		return r.routineResume(ctx, c, rest)
	case "history":
		return r.routineHistory(ctx, c, rest)
	default:
		return fmt.Errorf("routine: unknown subcommand %q\n%s", verb, makuUsage)
	}
}

func writeRawJSON(out io.Writer, raw json.RawMessage) error {
	_, err := fmt.Fprintf(out, "%s\n", raw)
	return err
}

func (r Maku) routineList(ctx context.Context, c maku.Client, args []string) error {
	fs := flag.NewFlagSet("routine list", flag.ContinueOnError)
	repo := fs.String("repo", "", "only this repository, owner/name")
	asJSON := fs.Bool("json", false, "print the broker's JSON as is")
	pos, err := flags(fs, args)
	if err != nil || len(pos) != 0 {
		return fmt.Errorf("routine list [--repo owner/name] [--json]: %w", errors.Join(err, errors.New("no arguments")))
	}
	ctx, cancel := context.WithTimeout(ctx, makuTimeout)
	defer cancel()
	list, err := c.ListRoutines(ctx, *repo)
	if err != nil {
		return err
	}
	if *asJSON {
		return writeRawJSON(r.Out, list.Raw())
	}
	if len(list.Routines) == 0 {
		_, err := fmt.Fprintln(r.Out, "no routines configured")
		return err
	}
	w := tabwriter.NewWriter(r.Out, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "REPO\tROUTINE\tMODE\tSTATE\tOPEN\tMERGE RATE\t$/MERGED\tNEXT RUN")
	for _, ov := range list.Routines {
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d/%d\t%s\t%s\t%s\n",
			maku.OneLine(ov.Repository),
			maku.OneLine(ov.Routine),
			maku.OneLine(ov.Mode),
			routineStateLabel(ov),
			ov.OpenCount,
			ov.MaxOpenPRs,
			formatMergeRate(ov),
			formatUSDPerMerged(ov.USDPerMerged),
			formatNextRun(ov.NextRunAt),
		)
	}
	return w.Flush()
}

func (r Maku) routineStatus(ctx context.Context, c maku.Client, args []string) error {
	fs := flag.NewFlagSet("routine status", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print the broker's JSON as is")
	pos, err := flags(fs, args)
	if err != nil || len(pos) != 2 {
		return fmt.Errorf("routine status <repo> <name> [--json]: %w", errors.Join(err, errors.New("repository and routine name required")))
	}
	ctx, cancel := context.WithTimeout(ctx, makuTimeout)
	defer cancel()
	d, err := c.GetRoutine(ctx, pos[0], pos[1])
	if err != nil {
		return err
	}
	if *asJSON {
		return writeRawJSON(r.Out, d.Raw())
	}
	w := tabwriter.NewWriter(r.Out, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintf(w, "Repository:\t%s\n", maku.OneLine(d.Repository))
	_, _ = fmt.Fprintf(w, "Routine:\t%s\n", maku.OneLine(d.Routine))
	if d.Description != "" {
		_, _ = fmt.Fprintf(w, "Description:\t%s\n", maku.OneLine(d.Description))
	}
	_, _ = fmt.Fprintf(w, "Mode:\t%s\n", maku.OneLine(d.Mode))
	_, _ = fmt.Fprintf(w, "Workflow:\t%s\n", cmp.Or(maku.OneLine(d.Workflow), "—"))
	_, _ = fmt.Fprintf(w, "Probe:\t%s\n", cmp.Or(maku.OneLine(d.ProbeKind), "none"))
	_, _ = fmt.Fprintf(w, "Cron:\t%s\n", cmp.Or(maku.OneLine(d.Cron), "—"))
	_, _ = fmt.Fprintf(w, "State:\t%s\n", routineStateLabel(d.RoutineOverview))
	_, _ = fmt.Fprintf(w, "Breaker:\t%s\n", cmp.Or(maku.OneLine(d.BreakerState), "ok"))
	_, _ = fmt.Fprintf(w, "Open PRs:\t%d/%d\n", d.OpenCount, d.MaxOpenPRs)
	_, _ = fmt.Fprintf(w, "Max Cost:\t%s\n", roomview.Dollars(d.MaxCostUSD))
	_, _ = fmt.Fprintf(w, "Merge Rate:\t%s\n", formatMergeRate(d.RoutineOverview))
	_, _ = fmt.Fprintf(w, "$/Merged PR:\t%s\n", formatUSDPerMerged(d.USDPerMerged))
	_, _ = fmt.Fprintf(w, "Next Run:\t%s\n", formatNextRun(d.NextRunAt))
	if d.PromptSHA != "" {
		_, _ = fmt.Fprintf(w, "Prompt SHA:\t%s\n", shortSHA(d.PromptSHA))
	}
	if d.PromptEditURL != "" {
		_, _ = fmt.Fprintf(w, "Edit Prompt:\t%s\n", maku.OneLine(d.PromptEditURL))
	}
	_, _ = fmt.Fprintf(w, "Recent Runs:\t%d (%d skips)\n", len(d.Runs), len(d.Skips))
	return w.Flush()
}

func (r Maku) routineRun(ctx context.Context, c maku.Client, args []string) error {
	fs := flag.NewFlagSet("routine run", flag.ContinueOnError)
	mode := fs.String("mode", "", "mode override (pr, propose, draft)")
	idemKey := fs.String("idempotency-key", "", "idempotency key (default: random)")
	asJSON := fs.Bool("json", false, "print the broker's JSON as is")
	pos, err := flags(fs, args)
	if err != nil || len(pos) != 2 {
		return fmt.Errorf("routine run <repo> <name> [--mode pr|propose|draft] [--idempotency-key KEY] [--json]: %w", errors.Join(err, errors.New("repository and routine name required")))
	}
	ctx, cancel := context.WithTimeout(ctx, makuTimeout)
	defer cancel()
	res, err := c.RunRoutine(ctx, pos[0], pos[1], *mode, *idemKey)
	if err != nil {
		return err
	}
	if *asJSON {
		return writeRawJSON(r.Out, res.Raw())
	}
	_, err = fmt.Fprintf(r.Out, "task %s started (room %s, mode %s, requested by %s)\n",
		maku.OneLine(res.Task),
		maku.OneLine(res.Room),
		maku.OneLine(res.Mode),
		maku.OneLine(res.RequestedBy),
	)
	return err
}

func (r Maku) routinePause(ctx context.Context, c maku.Client, args []string) error {
	fs := flag.NewFlagSet("routine pause", flag.ContinueOnError)
	reason := fs.String("reason", "", "pause reason")
	asJSON := fs.Bool("json", false, "print the broker's JSON as is")
	pos, err := flags(fs, args)
	if err != nil || len(pos) != 2 {
		return fmt.Errorf("routine pause <repo> <name> [--reason TEXT] [--json]: %w", errors.Join(err, errors.New("repository and routine name required")))
	}
	ctx, cancel := context.WithTimeout(ctx, makuTimeout)
	defer cancel()
	res, err := c.PauseRoutine(ctx, pos[0], pos[1], *reason)
	if err != nil {
		return err
	}
	if *asJSON {
		return writeRawJSON(r.Out, res.Raw())
	}
	_, err = fmt.Fprintf(r.Out, "paused %s %s (reason: %s)\n",
		maku.OneLine(res.Repository),
		maku.OneLine(res.Routine),
		cmp.Or(maku.OneLine(res.PausedReason), "manual"),
	)
	return err
}

func (r Maku) routineResume(ctx context.Context, c maku.Client, args []string) error {
	fs := flag.NewFlagSet("routine resume", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print the broker's JSON as is")
	pos, err := flags(fs, args)
	if err != nil || len(pos) != 2 {
		return fmt.Errorf("routine resume <repo> <name> [--json]: %w", errors.Join(err, errors.New("repository and routine name required")))
	}
	ctx, cancel := context.WithTimeout(ctx, makuTimeout)
	defer cancel()
	res, err := c.ResumeRoutine(ctx, pos[0], pos[1])
	if err != nil {
		return err
	}
	if *asJSON {
		return writeRawJSON(r.Out, res.Raw())
	}
	if res.DeclarativePaused {
		_, err = fmt.Fprintf(r.Out, "resumed operational pause for %s %s (still paused declaratively in Repository spec)\n",
			maku.OneLine(res.Repository), maku.OneLine(res.Routine))
		return err
	}
	_, err = fmt.Fprintf(r.Out, "resumed %s %s\n", maku.OneLine(res.Repository), maku.OneLine(res.Routine))
	return err
}

func (r Maku) routineHistory(ctx context.Context, c maku.Client, args []string) error {
	fs := flag.NewFlagSet("routine history", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print the broker's JSON as is")
	pos, err := flags(fs, args)
	if err != nil || len(pos) != 2 {
		return fmt.Errorf("routine history <repo> <name> [--json]: %w", errors.Join(err, errors.New("repository and routine name required")))
	}
	ctx, cancel := context.WithTimeout(ctx, makuTimeout)
	defer cancel()
	d, err := c.GetRoutine(ctx, pos[0], pos[1])
	if err != nil {
		return err
	}
	if *asJSON {
		return writeRawJSON(r.Out, d.Raw())
	}
	if _, err := fmt.Fprintln(r.Out, "RUNS"); err != nil {
		return err
	}
	if len(d.Runs) == 0 {
		if _, err := fmt.Fprintln(r.Out, "no runs yet"); err != nil {
			return err
		}
	} else {
		w := tabwriter.NewWriter(r.Out, 0, 4, 2, ' ', 0)
		_, _ = fmt.Fprintln(w, "TASK\tPHASE\tOUTCOME\tPR\tROOM\tPROMPT SHA\tCOST\tCREATED")
		for _, run := range d.Runs {
			pr := "—"
			if run.PRNumber > 0 {
				pr = fmt.Sprintf("#%d", run.PRNumber)
			} else if run.PRURL != "" {
				pr = maku.OneLine(run.PRURL)
			}
			_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				maku.OneLine(run.Name),
				maku.OneLine(run.Phase),
				cmp.Or(maku.OneLine(run.Outcome), "—"),
				pr,
				cmp.Or(maku.OneLine(run.RoomRef), "—"),
				cmp.Or(shortSHA(run.PromptSHA), "—"),
				roomview.Dollars(run.CostUSD),
				run.CreatedAt.UTC().Format(time.RFC3339),
			)
		}
		if err := w.Flush(); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintln(r.Out, "\nSKIPS"); err != nil {
		return err
	}
	if len(d.Skips) == 0 {
		_, err := fmt.Fprintln(r.Out, "no recent skips")
		return err
	}
	w := tabwriter.NewWriter(r.Out, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "AT\tREASON\tSLOT")
	for _, sk := range d.Skips {
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\n",
			sk.At.UTC().Format(time.RFC3339),
			maku.OneLine(sk.Reason),
			cmp.Or(maku.OneLine(sk.SlotKey), "—"),
		)
	}
	return w.Flush()
}

func routineStateLabel(ov maku.RoutineOverview) string {
	switch {
	case ov.Mode == "disabled":
		return "disabled"
	case ov.Paused:
		if r := maku.OneLine(ov.PausedReason); r != "" {
			return "paused (" + r + ")"
		}
		return "paused"
	case ov.BreakerState == "half_open":
		return "half_open"
	default:
		return "active"
	}
}

func formatMergeRate(ov maku.RoutineOverview) string {
	if ov.MergeRate == nil {
		return "—"
	}
	return fmt.Sprintf("%.0f%% (%d/%d)", *ov.MergeRate*100, ov.RollingMerged, ov.RollingMerged+ov.RollingClosed)
}

func formatUSDPerMerged(v *float64) string {
	if v == nil {
		return "—"
	}
	return roomview.Dollars(*v)
}

func formatNextRun(t *time.Time) string {
	if t == nil {
		return "—"
	}
	return t.UTC().Format(time.RFC3339)
}

func shortSHA(sha string) string {
	s := strings.TrimSpace(maku.OneLine(sha))
	if len(s) > 12 {
		return s[:12]
	}
	return s
}
