// SPDX-License-Identifier: Apache-2.0

package evidence

import (
	"bytes"
	"errors"
	"fmt"
	"slices"

	"go.yaml.in/yaml/v3"

	"github.com/Smana/maku/internal/factory/mergepolicy"
)

// Propose's refusals.
var (
	ErrInvalidPolicy = errors.New("the repository's policy file does not parse")
	ErrNoClass       = errors.New("the repository's policy file declares no such class")
	ErrNotShadow     = errors.New("the class is not in shadow")
)

// Propose is file with class's mode changed from shadow to live and nothing else: the one-line
// change a maintainer submits as their own pull request (MP1, MP14). In the binding shape the
// mode is the binding's (B5). line is the 1-based line it changes. The result is parsed again, so
// a proposal is always a valid file whose class is live.
func Propose(file []byte, class string) (out []byte, line int, err error) {
	f, err := mergepolicy.Parse(file)
	if err != nil {
		return nil, 0, fmt.Errorf("%w: %w", ErrInvalidPolicy, err)
	}
	if mode, ok := modeOf(f, class); !ok {
		return nil, 0, ErrNoClass
	} else if mode != string(mergepolicy.ModeShadow) {
		return nil, 0, ErrNotShadow
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(file, &doc); err != nil || len(doc.Content) != 1 {
		return nil, 0, ErrInvalidPolicy
	}
	var mode *yaml.Node
	if f.Bound() {
		mode = child(binding(child(doc.Content[0], "mergePolicies"), class), "mode")
	} else {
		mode = child(child(child(doc.Content[0], "classes"), class), "mode")
	}
	if mode == nil || mode.Kind != yaml.ScalarNode {
		return nil, 0, ErrNoClass
	}
	lines := bytes.SplitAfter(file, []byte("\n"))
	l := lines[mode.Line-1]
	col := mode.Column - 1
	token := []byte("shadow")
	if mode.Style&(yaml.DoubleQuotedStyle|yaml.SingleQuotedStyle) != 0 {
		col++ // the scalar's column is its opening quote's
	}
	if col+len(token) > len(l) || !bytes.Equal(l[col:col+len(token)], token) {
		return nil, 0, ErrNotShadow
	}
	lines[mode.Line-1] = slices.Concat(l[:col], []byte("live"), l[col+len(token):])
	out = bytes.Join(lines, nil)
	if g, err := mergepolicy.Parse(out); err != nil {
		return nil, 0, fmt.Errorf("%w: the proposal does not read back as live", ErrInvalidPolicy)
	} else if m, _ := modeOf(g, class); m != string(mergepolicy.ModeLive) {
		return nil, 0, fmt.Errorf("%w: the proposal does not read back as live", ErrInvalidPolicy)
	}
	return out, mode.Line, nil
}

// modeOf is class's mode in f, in either shape, and whether f declares it.
func modeOf(f *mergepolicy.File, class string) (string, bool) {
	if f.Bound() {
		for _, b := range f.MergePolicies {
			if b.Name == class {
				return b.Mode, true
			}
		}
		return "", false
	}
	p, ok := f.Classes[class]
	return string(p.Mode), ok
}

// binding is sequence node n's binding named name, nil when there is none.
func binding(n *yaml.Node, name string) *yaml.Node {
	if n == nil || n.Kind != yaml.SequenceNode {
		return nil
	}
	for _, b := range n.Content {
		if v := child(b, "name"); v != nil && v.Value == name {
			return b
		}
	}
	return nil
}

// child is mapping node n's value under key, nil when n is no mapping or has no such key.
func child(n *yaml.Node, key string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}
