// SPDX-License-Identifier: Apache-2.0

package preflight

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"text/tabwriter"
)

// maxSummary is Kubernetes' termination message bound.
const maxSummary = 4096

// ErrCredential is a result whose message carries what looks like a credential: it is never stored.
var ErrCredential = errors.New("preflight: the message carries a credential")

// credentialMarkers are what a message must never hold: a DSN, a PEM key, a GitHub token, a bearer.
var credentialMarkers = []string{"postgres://", "postgresql://", "-----BEGIN", "ghs_", "ghp_", "gho_", "ghu_", "github_pat_", "Bearer "}

// Report is a run's results.
type Report struct {
	results []Result
	stream  *json.Encoder // set: each stored result is also written here, at once
}

// Add stores r, held to the catalog. A message holding a credential is refused, and the refusal
// does not echo it.
func (r *Report) Add(res Result) error {
	for _, m := range credentialMarkers {
		if strings.Contains(res.Message, m) {
			return fmt.Errorf("%w: %s", ErrCredential, res.Check)
		}
	}
	res = normalize(res)
	r.results = append(r.results, res)
	if r.stream != nil {
		_ = r.stream.Encode(res)
	}
	return nil
}

func seamRank(check string) int {
	c, ok := lookup(check)
	if !ok {
		return len(SeamOrder)
	}
	return slices.Index(SeamOrder, c.Seams[0])
}

// Results are the stored results, sorted by seam (SeamOrder), then by check id, then by component.
func (r *Report) Results() []Result {
	out := slices.Clone(r.results)
	slices.SortStableFunc(out, func(a, b Result) int {
		return cmp.Or(cmp.Compare(seamRank(a.Check), seamRank(b.Check)), cmp.Compare(a.Check, b.Check), cmp.Compare(a.Component, b.Component))
	})
	return out
}

// Exit is the runner's status: 1 on any fail, and with strict on any warn too; else 0.
func (r *Report) Exit(strict bool) int {
	for _, res := range r.results {
		if res.Outcome == Fail || (strict && res.Outcome == Warn) {
			return 1
		}
	}
	return 0
}

// WriteJSONLines writes one JSON object per result, in order.
func (r *Report) WriteJSONLines(w io.Writer) error {
	enc := json.NewEncoder(w)
	for _, res := range r.Results() {
		if err := enc.Encode(res); err != nil {
			return err
		}
	}
	return nil
}

// WriteText writes the human table, grouped by seam.
func (r *Report) WriteText(w io.Writer) error {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "SEAM\tCHECK\tWHERE\tOUTCOME\tCODE\tMESSAGE")
	for _, res := range r.Results() {
		c, _ := lookup(res.Check)
		seam := ""
		if len(c.Seams) > 0 {
			seam = string(c.Seams[0])
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", seam, res.Check, cmp.Or(res.Component, string(InPod)), res.Outcome, res.Code, res.Message)
	}
	return tw.Flush()
}

// Summary is the one line the pod writes to its termination message: the counts, then each failing
// or warning check with its code, cut to fit 4096 bytes.
func (r *Report) Summary() string {
	counts := map[Outcome]int{}
	var notes []string
	for _, res := range r.Results() {
		counts[res.Outcome]++
		if res.Outcome == Fail || res.Outcome == Warn {
			where := ""
			if res.Component != "" {
				where = "@" + res.Component
			}
			notes = append(notes, fmt.Sprintf("%s%s=%s:%s", res.Check, where, res.Outcome, res.Code))
		}
	}
	s := fmt.Sprintf("preflight: %d fail, %d warn, %d pass, %d skip", counts[Fail], counts[Warn], counts[Pass], counts[Skip])
	for i, n := range notes {
		more := fmt.Sprintf(" (+%d more)", len(notes)-i)
		if len(s)+1+len(n)+len(more) > maxSummary {
			return s + more
		}
		s += " " + n
	}
	return s
}
