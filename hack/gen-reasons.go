// SPDX-License-Identifier: Apache-2.0

//go:build ignore

// Command gen-reasons writes the merge-policy reason table from internal/factory/reasons.
package main

import (
	"bytes"
	"fmt"
	"os"

	"github.com/Smana/maku/internal/factory/reasons"
)

const doc = "docs/merge-policies.md"

func main() {
	raw, err := os.ReadFile(doc)
	if err != nil {
		fail(err)
	}
	startMarker, endMarker := "<!-- reasons:table:start -->", "<!-- reasons:table:end -->"
	start, end := bytes.Index(raw, []byte(startMarker)), bytes.Index(raw, []byte(endMarker))
	if start < 0 || end < start {
		fail(fmt.Errorf("%s lacks the reason table markers", doc))
	}
	table := reasons.Markdown(
		"held", "policy_unavailable", "policy_invalid", "approval_required", "shadow_would_require_approval",
		"shadow_would_arm", "no_policy_matched", "reviewer_verdict_missing", "file_not_allowed",
		"file_protected", "file_added_or_deleted", "patch_too_large", "class_mismatch", "holdout_red",
		"holdout_pending", "release_age_red", "release_age_pending", "label_missing", "label_not_adopted",
		"approval_stale", "head_unreported", "changes_requested", "policy_pending", "policy_absent",
		"gate_path", "class_paused", "auto_merge_cap",
	)
	out := append(append(append([]byte{}, raw[:start+len(startMarker)]...), []byte("\n"+table)...), raw[end:]...)
	if err := os.WriteFile(doc, out, 0o644); err != nil { //nolint:gosec // a docs file
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
