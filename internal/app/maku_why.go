// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"strings"

	"github.com/Smana/maku/internal/factory/reasons"
	"github.com/Smana/maku/internal/maku"
	"github.com/Smana/maku/internal/summary"
)

// whyEvidence is how many of the completion judge's evidence lines maku why prints.
const whyEvidence = 5

// why runs `maku why <room>` (developer review gap 8): why the task stopped, in words, the one thing
// to do next, how its last run ended, the judge's last evidence, and where to look. It reads the
// room's summary only; every line is the factory's words or the run's, one line each.
func (r Maku) why(ctx context.Context, c maku.Client, args []string) error {
	fs := flag.NewFlagSet("why", flag.ContinueOnError)
	pos, err := flags(fs, args)
	if err != nil || len(pos) != 1 {
		return fmt.Errorf("why <room>: %w", errors.Join(err, errors.New("one room")))
	}
	raw, err := c.Summary(ctx, pos[0], 0)
	if err != nil {
		return err
	}
	var sum summary.Summary
	if err := json.Unmarshal(raw, &sum); err != nil {
		return fmt.Errorf("why: the broker's answer: %w", err)
	}
	st := sum.Status
	var b strings.Builder
	line := func(format string, a ...any) { b.WriteString(maku.OneLine(fmt.Sprintf(format, a...)) + "\n") }
	switch {
	case st.ReasonCode == "" && st.Reason == "":
		line("%s: nothing is stuck. %s", st.Phase, st.Next)
	default:
		line("%s: %s (%s)", st.Phase, st.Reason, st.ReasonCode)
		next := reasons.For(st.ReasonCode).Next
		if next == "" {
			next = st.Next
		}
		line("What to do: %s", next)
	}
	if n := len(sum.Runs); n > 0 {
		last := sum.Runs[n-1]
		out := last.Outcome
		if out == "" {
			out = "still running"
		}
		line("Last run: %s, %s, %s", last.ID, last.Role, out)
	}
	if j := st.LastJudge; j != nil {
		line("The judge: %s, %s", j.Verdict, j.Reason)
		for i, e := range j.Evidence {
			if i == whyEvidence {
				break
			}
			line("  %s", e)
		}
	}
	if pr := st.PR; pr != nil && pr.URL != "" {
		line("Pull request: %s", pr.URL)
	}
	if sum.URL != "" {
		line("Room: %s", sum.URL)
	}
	_, err = fmt.Fprint(r.Out, b.String())
	return err
}
