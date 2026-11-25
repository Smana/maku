// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"context"
	"errors"
	"path"
	"slices"
	"strings"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/forge"
)

// ReasonOutsideDeclaredFiles escalates a split part that modified a file outside its declared
// files globs, returning the family to one part at a time (phase H3).
const ReasonOutsideDeclaredFiles = "outside_declared_files"

// normalizeGlob normalises a repository-relative path or glob pattern.
func normalizeGlob(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	trailingSlash := strings.HasSuffix(p, "/")
	p = strings.TrimPrefix(p, "./")
	p = strings.TrimPrefix(p, "/")
	if p == "" || p == "." {
		return "**"
	}
	p = path.Clean(p)
	if p == "." || p == "/" {
		return "**"
	}
	if trailingSlash && !strings.HasSuffix(p, "/**") && p != "**" {
		p += "/**"
	}
	return p
}

// splitGlobSegments splits a normalised pattern into path segments, collapsing adjacent "**".
func splitGlobSegments(p string) []string {
	raw := strings.Split(p, "/")
	out := make([]string, 0, len(raw))
	for _, s := range raw {
		if s == "" || s == "." {
			continue
		}
		if s == "**" && len(out) > 0 && out[len(out)-1] == "**" {
			continue
		}
		out = append(out, s)
	}
	return out
}

// globMatch reports whether repository-relative file matches pattern (supporting *, ?, [...] and **).
func globMatch(pattern, file string) bool {
	p := normalizeGlob(pattern)
	f := normalizeGlob(file)
	if p == "" || f == "" {
		return false
	}
	ps := splitGlobSegments(p)
	fs := splitGlobSegments(f)
	memo := make(map[[2]int]bool, (len(ps)+1)*(len(fs)+1))
	var dp func(i, j int) bool
	dp = func(i, j int) bool {
		if i == len(ps) && j == len(fs) {
			return true
		}
		key := [2]int{i, j}
		if v, ok := memo[key]; ok {
			return v
		}
		res := false
		switch {
		case i < len(ps) && ps[i] == "**":
			res = dp(i+1, j) || (j < len(fs) && dp(i, j+1))
		case i < len(ps) && j < len(fs):
			if ok, err := path.Match(ps[i], fs[j]); err == nil && ok {
				res = dp(i+1, j+1)
			}
		}
		memo[key] = res
		return res
	}
	return dp(0, 0)
}

// segmentOverlap reports whether two single path-segment glob patterns can match a common string.
func segmentOverlap(a, b string) bool {
	if a == b || a == "*" || b == "*" {
		return true
	}
	// If either contains a character class, match conservatively unless exact literals cannot meet.
	if strings.ContainsAny(a, "[]") || strings.ContainsAny(b, "[]") {
		if ok, _ := path.Match(a, b); ok {
			return true
		}
		if ok, _ := path.Match(b, a); ok {
			return true
		}
		return true
	}
	ra, rb := []rune(a), []rune(b)
	memo := make(map[[2]int]bool, (len(ra)+1)*(len(rb)+1))
	var dp func(i, j int) bool
	dp = func(i, j int) bool {
		if i == len(ra) && j == len(rb) {
			return true
		}
		key := [2]int{i, j}
		if v, ok := memo[key]; ok {
			return v
		}
		res := false
		switch {
		case i < len(ra) && ra[i] == '*':
			res = dp(i+1, j) || (j < len(rb) && dp(i, j+1))
		case j < len(rb) && rb[j] == '*':
			res = dp(i, j+1) || (i < len(ra) && dp(i+1, j))
		case i < len(ra) && j < len(rb):
			if ra[i] == '?' || rb[j] == '?' || ra[i] == rb[j] {
				res = dp(i+1, j+1)
			}
		}
		memo[key] = res
		return res
	}
	return dp(0, 0)
}

// globsOverlap reports whether two path/glob patterns could match a common file path.
func globsOverlap(p1, p2 string) bool {
	n1, n2 := normalizeGlob(p1), normalizeGlob(p2)
	if n1 == "" || n2 == "" {
		return true // undeclared / empty fails closed
	}
	s1, s2 := splitGlobSegments(n1), splitGlobSegments(n2)
	memo := make(map[[2]int]bool, (len(s1)+1)*(len(s2)+1))
	var dp func(i, j int) bool
	dp = func(i, j int) bool {
		if i == len(s1) && j == len(s2) {
			return true
		}
		key := [2]int{i, j}
		if v, ok := memo[key]; ok {
			return v
		}
		res := false
		switch {
		case i < len(s1) && s1[i] == "**":
			res = dp(i+1, j) || (j < len(s2) && dp(i, j+1))
		case j < len(s2) && s2[j] == "**":
			res = dp(i, j+1) || (i < len(s1) && dp(i+1, j))
		case i < len(s1) && j < len(s2):
			if segmentOverlap(s1[i], s2[j]) {
				res = dp(i+1, j+1)
			}
		}
		memo[key] = res
		return res
	}
	return dp(0, 0)
}

// filesDisjoint reports whether two parts declare non-empty file globs that do not overlap.
func filesDisjoint(a, b []string) bool {
	if len(a) == 0 || len(b) == 0 {
		return false
	}
	for _, pa := range a {
		for _, pb := range b {
			if globsOverlap(pa, pb) {
				return false
			}
		}
	}
	return true
}

// fileWithinDeclared reports whether file matches at least one declared pattern (or declared is empty).
func fileWithinDeclared(file string, declared []string) bool {
	if len(declared) == 0 {
		return true
	}
	for _, pat := range declared {
		if globMatch(pat, file) {
			return true
		}
	}
	return false
}

// repoDeclaredChecks returns the repository's declared checks (required, verify, contract).
func repoDeclaredChecks(rc *config.Repository) []string {
	if rc == nil {
		return nil
	}
	out := make([]string, 0, len(rc.Merge.RequiredChecks)+len(rc.Merge.VerifyChecks)+1)
	for _, c := range rc.Merge.RequiredChecks {
		if c != "" && !slices.Contains(out, c) {
			out = append(out, c)
		}
	}
	for _, c := range rc.Merge.VerifyChecks {
		if c != "" && !slices.Contains(out, c) {
			out = append(out, c)
		}
	}
	if rc.Merge.ContractCheck != "" && !slices.Contains(out, rc.Merge.ContractCheck) {
		out = append(out, rc.Merge.ContractCheck)
	}
	return out
}

// partHasValidChecks reports whether checks names at least one check, and — when the repository
// declares checks — every named check is in the repository's declared checks.
func partHasValidChecks(checks []string, rc *config.Repository) bool {
	if len(checks) == 0 {
		return false
	}
	declared := repoDeclaredChecks(rc)
	for _, c := range checks {
		if strings.TrimSpace(c) == "" {
			return false
		}
		if len(declared) > 0 && !slices.Contains(declared, c) {
			return false
		}
	}
	return true
}

// partsCanRunTogether reports whether parts a and b satisfy all parallel opening rules (phase H3):
// neither depends on the other, their declared files do not overlap, and each names valid checks.
func partsCanRunTogether(aIdx int32, a envelope.Scope, bIdx int32, b envelope.Scope, rc *config.Repository) bool {
	if slices.Contains(a.DependsOn, int(bIdx)) || slices.Contains(b.DependsOn, int(aIdx)) {
		return false
	}
	if !filesDisjoint(a.Files, b.Files) {
		return false
	}
	return partHasValidChecks(a.Checks, rc) && partHasValidChecks(b.Checks, rc)
}

// checkDeclaredFiles enforces a split part's declared files fence on its pull request (phase H3).
// A part touching any file outside spec.scope.files ends Escalated outside_declared_files.
func (r *Reconciler) checkDeclaredFiles(ctx context.Context, cfg *config.Config, t *v1alpha1.Task) (bool, error) {
	if t.Spec.Parent == nil || t.Spec.Scope == nil || len(t.Spec.Scope.Files) == 0 ||
		t.Status.PullRequest == nil || t.Status.PullRequest.Number <= 0 {
		return false, nil
	}
	if r.merger(ctx) == nil {
		return false, nil
	}
	fs, err := r.merger(ctx).PullFiles(ctx, t.Status.PullRequest.Number)
	if errors.Is(err, forge.ErrNoMerger) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	for _, fp := range fs {
		if (fp.Path != "" && !fileWithinDeclared(fp.Path, t.Spec.Scope.Files)) ||
			(fp.Previous != "" && !fileWithinDeclared(fp.Previous, t.Spec.Scope.Files)) {
			return true, r.end(ctx, cfg, t, v1alpha1.PhaseEscalated, ReasonOutsideDeclaredFiles)
		}
	}
	return false, nil
}
