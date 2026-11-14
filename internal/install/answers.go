// SPDX-License-Identifier: Apache-2.0

package install

import (
	"bytes"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"

	"sigs.k8s.io/yaml"
)

// Answers is one install's answers, by question id: a string, a bool or a list of strings, as the
// question's Kind says. An answers file (--answers) is this map in YAML.
type Answers map[string]any

// String is a string answer; "" when unset.
func (a Answers) String(id string) string { s, _ := a[id].(string); return s }

// Bool is a bool answer; false when unset.
func (a Answers) Bool(id string) bool { b, _ := a[id].(bool); return b }

// List is a list answer; nil when unset.
func (a Answers) List(id string) []string { l, _ := a[id].([]string); return l }

// DecodeAnswers reads an answers file strictly: every key is a question's id, every value its
// question's kind. Unanswered questions take their defaults later (Resolve), so CI files stay short.
func DecodeAnswers(raw []byte) (Answers, error) {
	var m map[string]any
	if err := yaml.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("answers: %w", err)
	}
	byID := questionsByID()
	a := Answers{}
	var errs []error
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		q, ok := byID[k]
		if !ok {
			errs = append(errs, fmt.Errorf("answers: unknown key %q (maku install --print --list names every question)", k))
			continue
		}
		v, err := q.Kind.coerce(m[k])
		if err != nil {
			errs = append(errs, fmt.Errorf("answers: %s: %w", k, err))
			continue
		}
		a[k] = v
	}
	return a, errors.Join(errs...)
}

// Encode writes a as an answers file, in the questions' order: what --answers reads back.
func (a Answers) Encode() []byte {
	var b bytes.Buffer
	for _, q := range Questions {
		v, ok := a[q.ID]
		if !ok {
			continue
		}
		out, _ := yaml.Marshal(map[string]any{q.ID: v})
		b.Write(out)
	}
	return b.Bytes()
}

// coerce is v (decoded YAML, or a typed line) as kind k's Go type.
func (k Kind) coerce(v any) (any, error) {
	switch k {
	case KindBool:
		switch t := v.(type) {
		case bool:
			return t, nil
		case string:
			return parseBool(t)
		}
		return nil, fmt.Errorf("want true or false, got %T", v)
	case KindList:
		switch t := v.(type) {
		case []string:
			return slices.Clone(t), nil
		case []any:
			out := make([]string, 0, len(t))
			for _, e := range t {
				s, ok := e.(string)
				if !ok {
					return nil, fmt.Errorf("want a list of strings, got a %T in it", e)
				}
				out = append(out, s)
			}
			return out, nil
		case string:
			return splitList(t), nil
		}
		return nil, fmt.Errorf("want a list of strings, got %T", v)
	default:
		switch t := v.(type) {
		case string:
			return t, nil
		case float64:
			return strconv.FormatFloat(t, 'f', -1, 64), nil
		case int, int64:
			return fmt.Sprint(t), nil
		}
		return nil, fmt.Errorf("want a string, got %T", v)
	}
}

func parseBool(s string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "y", "yes", "true", "on":
		return true, nil
	case "n", "no", "false", "off":
		return false, nil
	}
	return false, fmt.Errorf("want yes or no, got %q", s)
}

// splitList reads "a, b c" as [a b c]: commas or spaces separate.
func splitList(s string) []string {
	f := strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' })
	if f == nil {
		return []string{}
	}
	return f
}
