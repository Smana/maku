// SPDX-License-Identifier: Apache-2.0

package install

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/Smana/maku/internal/version"
)

// defaultVersion is this maku's own release, the version it installs unless told otherwise.
func defaultVersion() string {
	v := version.Version
	if v == "" || v == "dev" || !strings.Contains(v, ".") {
		return "v0.0.0"
	}
	if !strings.HasPrefix(v, "v") {
		v = "v" + v
	}
	return v
}

func sortedKeys(m map[string]any) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// Resolve completes a: each applicable question takes its default when unanswered, and every answer
// is validated. Answers to questions that do not apply are refused, so a file cannot carry dead
// keys. Warnings (F4) are returned for the caller to print.
func Resolve(a Answers) (Answers, []string, error) {
	out := Answers{}
	var warns []string
	var errs []error
	asked := map[string]bool{}
	for _, q := range Questions {
		if !q.Asked(out) {
			continue
		}
		asked[q.ID] = true
		v, ok := a[q.ID]
		if !ok {
			v = q.defaultValue(out)
			if v == nil {
				errs = append(errs, fmt.Errorf("%s: required (%s)", q.ID, q.Prompt))
				continue
			}
		}
		w, err := q.Validate(v, out)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", q.ID, err))
			continue
		}
		if w != "" {
			warns = append(warns, q.ID+": "+w)
		}
		out[q.ID] = v
	}
	for _, id := range sortedKeys(a) {
		if !asked[id] {
			errs = append(errs, fmt.Errorf("%s: answered, but the answers before it leave it out", id))
		}
	}
	return out, warns, errors.Join(errs...)
}

// Ask asks each applicable question on out, reading one line from in for each, and re-asks an answer
// the validators refuse (up to three times). An empty line takes the default.
func Ask(in io.Reader, out io.Writer) (Answers, error) {
	r := bufio.NewReader(in)
	a := Answers{}
	for _, q := range Questions {
		if !q.Asked(a) {
			continue
		}
		def := q.defaultValue(a)
		var err error
		for try := 0; try < 3; try++ {
			_, _ = fmt.Fprintf(out, "%s%s: ", q.Prompt, shown(def))
			line, rerr := r.ReadString('\n')
			if rerr != nil && line == "" {
				return nil, fmt.Errorf("%s: no answer (%w)", q.ID, rerr)
			}
			line = strings.TrimSpace(line)
			var v any = def
			if line != "" {
				if v, err = q.Kind.coerce(line); err != nil {
					_, _ = fmt.Fprintf(out, "  %v\n", err)
					continue
				}
			}
			if v == nil {
				err = errors.New("an answer is required")
				_, _ = fmt.Fprintf(out, "  %v\n", err)
				continue
			}
			w, verr := q.Validate(v, a)
			if err = verr; err != nil {
				_, _ = fmt.Fprintf(out, "  %v\n", err)
				continue
			}
			if w != "" {
				_, _ = fmt.Fprintf(out, "  warning: %s\n", w)
			}
			a[q.ID] = v
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", q.ID, err)
		}
	}
	return a, nil
}

func shown(def any) string {
	switch d := def.(type) {
	case nil:
		return ""
	case bool:
		if d {
			return " [Y/n]"
		}
		return " [y/N]"
	case []string:
		return " [" + strings.Join(d, ", ") + "]"
	case string:
		if d == "" {
			return " []"
		}
		return " [" + d + "]"
	}
	return ""
}
