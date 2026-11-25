// SPDX-License-Identifier: Apache-2.0

package narrate

import (
	"fmt"
	"strings"

	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/decision"
)

// MaxDecisionDetail bounds the readable decision stored on a Task and sent to the broker.
const MaxDecisionDetail = 2048

// Measurements renders the values the gate read, with their limits where the policy set one.
func Measurements(r decision.Rows) string {
	var parts []string
	if r.Counted {
		switch {
		case r.MaxFiles > 0:
			parts = append(parts, fmt.Sprintf("%d of %d files", r.Files, r.MaxFiles))
		default:
			parts = append(parts, fmt.Sprintf("%d files", r.Files))
		}
		switch {
		case r.MaxLines > 0:
			parts = append(parts, fmt.Sprintf("%d of %d lines", r.Lines, r.MaxLines))
		default:
			parts = append(parts, fmt.Sprintf("%d lines", r.Lines))
		}
	}
	if r.Reviewer != "" && r.Reviewer != "none" {
		parts = append(parts, "reviewer approved")
	}
	return strings.Join(parts, ", ")
}

// DecisionDetails is the readable form of the decision record: the matched policy's measurements,
// then every candidate that refused with its reason. classes supplies the resolved candidates' caps.
func DecisionDetails(r decision.Rows, classes map[string]config.Class) string {
	var parts []string
	if m := Measurements(r); m != "" {
		parts = append(parts, m)
	}
	for _, candidate := range strings.Split(r.Considered, ",") {
		name, reason, ok := strings.Cut(candidate, ":")
		if !ok || name == "" {
			continue
		}
		cl := classes[name]
		text := ""
		if reason == "patch_too_large" && r.Counted {
			switch {
			case cl.MaxModifiedLines > 0 && r.Lines > cl.MaxModifiedLines:
				text = fmt.Sprintf("%d lines changed, limit %d", r.Lines, cl.MaxModifiedLines)
			case cl.MaxPatchFiles > 0 && r.Files > cl.MaxPatchFiles:
				text = fmt.Sprintf("%d files changed, limit %d", r.Files, cl.MaxPatchFiles)
			}
		}
		if text == "" && reason == "class_mismatch" && cl.DiffShape == "links-only" {
			text = "not a links-only diff"
		}
		if text == "" {
			text = Reason(reason)
		}
		parts = append(parts, fmt.Sprintf("`%s`: %s", name, text))
	}
	out := strings.Join(parts, "; ")
	if len([]rune(out)) > MaxDecisionDetail {
		out = string([]rune(out)[:MaxDecisionDetail-1]) + "…"
	}
	return out
}
