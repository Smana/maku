// SPDX-License-Identifier: Apache-2.0

package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	yaml3 "go.yaml.in/yaml/v3"
	"sigs.k8s.io/yaml"

	factoryv1 "github.com/Smana/maku/api/factory/v1alpha1"
)

// setMergePaused edits a Repository manifest's spec.merge.paused and nothing else: the lines the
// key holds are replaced, inserted or removed, and every other byte (comments, key order, absent
// zero values) stays. Re-marshalling the object instead dropped the comments and wrote every zero
// field out (breaker.maxReverts: 0, revertWindow: 0s), which a maintainer merging the pause PR
// would have applied too (gcp-0 live drill, 2026-11-25).
func setMergePaused(raw []byte, pause bool, reason string) ([]byte, error) {
	var doc yaml3.Node
	if err := yaml3.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("gitops manifest: %w", err)
	}
	if len(doc.Content) != 1 || doc.Content[0].Kind != yaml3.MappingNode {
		return nil, errors.New("gitops manifest: not one YAML mapping")
	}
	specKey, spec := mapEntry(doc.Content[0], "spec")
	if spec == nil || spec.Kind != yaml3.MappingNode || spec.Style&yaml3.FlowStyle != 0 || len(spec.Content) == 0 {
		return nil, errors.New("gitops manifest: spec is not a block mapping")
	}
	lines := strings.SplitAfter(string(raw), "\n")
	quoted, _ := json.Marshal(reason) // a JSON string is a YAML double-quoted scalar
	mergeKey, merge := mapEntry(spec, "merge")
	switch {
	case merge == nil && !pause:
		return raw, nil
	case merge == nil:
		ind := strings.Repeat(" ", spec.Content[0].Column-1)
		add := ind + "merge:\n" + ind + "  paused: {reason: " + string(quoted) + "}\n"
		lines = insertAfter(lines, specKey.Line, add)
	case merge.Kind != yaml3.MappingNode || merge.Style&yaml3.FlowStyle != 0 || len(merge.Content) == 0:
		return nil, errors.New("gitops manifest: spec.merge is not a block mapping; edit it by hand")
	default:
		pausedKey, _ := mapEntry(merge, "paused")
		ind := strings.Repeat(" ", merge.Content[0].Column-1)
		line := ind + "paused: {reason: " + string(quoted) + "}\n"
		switch {
		case pausedKey == nil && !pause:
			return raw, nil
		case pausedKey == nil:
			lines = insertAfter(lines, mergeKey.Line, line)
		default:
			start, end := pausedKey.Line-1, blockEnd(lines, pausedKey.Line, pausedKey.Column)
			repl := []string{}
			if pause {
				repl = []string{line}
			}
			lines = append(lines[:start], append(repl, lines[end:]...)...)
		}
	}
	out := []byte(strings.Join(lines, ""))
	var check factoryv1.Repository
	if err := yaml.Unmarshal(out, &check); err != nil {
		return nil, fmt.Errorf("gitops manifest after the edit: %w", err)
	}
	got := check.Spec.Merge != nil && check.Spec.Merge.Paused != nil
	if got != pause || (pause && check.Spec.Merge.Paused.Reason != reason) {
		return nil, errors.New("gitops manifest: the edit did not set spec.merge.paused; edit it by hand")
	}
	return out, nil
}

// mapEntry is m's key and value nodes for key, or nils.
func mapEntry(m *yaml3.Node, key string) (*yaml3.Node, *yaml3.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i], m.Content[i+1]
		}
	}
	return nil, nil
}

// insertAfter inserts text after 1-based line n.
func insertAfter(lines []string, n int, text string) []string {
	if n > 0 && n <= len(lines) && !strings.HasSuffix(lines[n-1], "\n") {
		lines[n-1] += "\n"
	}
	return append(lines[:n], append([]string{text}, lines[n:]...)...)
}

// blockEnd is the 0-based index of the first line after 1-based line n that holds content indented at
// or left of column col: where the entry starting on line n ends. Blank and comment lines in between
// stay with the entry only when content of the entry follows them.
func blockEnd(lines []string, n, col int) int {
	end := n
	for i := n; i < len(lines); i++ {
		t := strings.TrimRight(lines[i], "\r\n")
		trim := strings.TrimLeft(t, " ")
		if trim == "" || strings.HasPrefix(trim, "#") {
			continue
		}
		if len(t)-len(trim) < col {
			break
		}
		end = i + 1
	}
	return end
}
