// SPDX-License-Identifier: Apache-2.0

package install

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
)

// The charts' own values schemas, copied by `task install:schemas` and checked unchanged by `task
// chart:check`, so the wizard refuses what the chart refuses (IN7).
//
//go:embed schema/*.json
var schemaFS embed.FS

// subcharts maps an umbrella values key to the subchart whose own schema validates it, as Helm does.
var subcharts = map[string]string{
	"maku":         "maku",
	"maku-factory": "maku-factory",
	"maku-gateway": "maku-gateway",
	"slack":        "maku-slack",
}

type schemas struct {
	compiler *jsonschema.Compiler
	docs     map[string]any // chart -> its decoded schema document
}

var (
	loadOnce sync.Once
	loaded   schemas
	loadErr  error
)

func schemaSet() (schemas, error) {
	loadOnce.Do(func() {
		c := jsonschema.NewCompiler()
		docs := map[string]any{}
		for _, chart := range []string{"agent-factory", "maku", "maku-factory", "maku-gateway", "maku-slack"} {
			raw, err := schemaFS.ReadFile("schema/" + chart + ".json")
			if err != nil {
				loadErr = err
				return
			}
			doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
			if err != nil {
				loadErr = fmt.Errorf("schema %s: %w", chart, err)
				return
			}
			if err := c.AddResource(schemaURL(chart), doc); err != nil {
				loadErr = err
				return
			}
			docs[chart] = doc
		}
		loaded = schemas{compiler: c, docs: docs}
	})
	return loaded, loadErr
}

func schemaURL(chart string) string { return "file:///" + chart + ".json" }

// target is one schema a values path is checked against: the chart and the path inside its values.
type target struct {
	chart string
	path  []string
}

// targets is every schema that judges path: the umbrella's, and the subchart's for its own keys.
func targets(path string) []target {
	parts := strings.Split(path, ".")
	ts := []target{{"agent-factory", parts}}
	if sub, ok := subcharts[parts[0]]; ok && len(parts) > 1 {
		ts = append(ts, target{sub, parts[1:]})
	}
	return ts
}

// node walks the schema document along path: the JSON pointer of the node that judges the value,
// whether the walk got there (false: an additionalProperties: false parent refuses the key), and
// whether the node is free (a parent with no properties of its own: anything goes, as Helm does).
func node(doc any, path []string) (pointer string, known, free bool) {
	cur, ptr := doc, ""
	for _, key := range path {
		cur, ptr = deref(doc, cur, ptr)
		m, _ := cur.(map[string]any)
		props, _ := m["properties"].(map[string]any)
		if p, ok := props[key]; ok {
			cur, ptr = p, ptr+"/properties/"+escape(key)
			continue
		}
		switch ap := m["additionalProperties"].(type) {
		case map[string]any:
			cur, ptr = ap, ptr+"/additionalProperties"
			continue
		case bool:
			if !ap {
				return "", false, false
			}
		}
		return ptr, true, true
	}
	_, ptr = deref(doc, cur, ptr)
	return ptr, true, false
}

// deref follows a node's local $ref ("#/definitions/x"), once or more.
func deref(doc, cur any, ptr string) (any, string) {
	for range 8 {
		m, _ := cur.(map[string]any)
		ref, _ := m["$ref"].(string)
		if !strings.HasPrefix(ref, "#/") {
			return cur, ptr
		}
		next := doc
		for _, seg := range strings.Split(ref[2:], "/") {
			nm, _ := next.(map[string]any)
			next = nm[seg]
		}
		cur, ptr = next, ref[1:]
	}
	return cur, ptr
}

func escape(k string) string { return strings.ReplaceAll(strings.ReplaceAll(k, "~", "~0"), "/", "~1") }

// Known reports whether the charts' schemas let a values file set path.
func Known(path string) (bool, error) {
	s, err := schemaSet()
	if err != nil {
		return false, err
	}
	for _, t := range targets(path) {
		if _, ok, _ := node(s.docs[t.chart], t.path); !ok {
			return false, nil
		}
	}
	return true, nil
}

// ValidateValue checks v, the value a question writes at path, against every schema that judges
// it. The error is the schema's own, as `helm template` would print it.
func ValidateValue(path string, v any) error {
	s, err := schemaSet()
	if err != nil {
		return err
	}
	inst, err := instance(v)
	if err != nil {
		return err
	}
	for _, t := range targets(path) {
		ptr, ok, free := node(s.docs[t.chart], t.path)
		if !ok {
			return fmt.Errorf("%s: the %s chart's schema has no such key", path, t.chart)
		}
		if free {
			continue
		}
		sch, err := s.compiler.Compile(schemaURL(t.chart) + "#" + ptr)
		if err != nil {
			return err
		}
		if err := sch.Validate(inst); err != nil {
			return fmt.Errorf("%s: %s", path, oneLine(err))
		}
	}
	return nil
}

// instance is v as the validator reads JSON: numbers as json.Number.
func instance(v any) (any, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return jsonschema.UnmarshalJSON(bytes.NewReader(raw))
}

// oneLine is a validation error's leaf causes, joined: the validator's tree, without its header.
func oneLine(err error) string {
	var ve *jsonschema.ValidationError
	if !errors.As(err, &ve) {
		return err.Error()
	}
	printer := message.NewPrinter(language.English)
	var leaves []string
	var walk func(e *jsonschema.ValidationError)
	walk = func(e *jsonschema.ValidationError) {
		if len(e.Causes) == 0 {
			leaves = append(leaves, e.ErrorKind.LocalizedString(printer))
			return
		}
		for _, c := range e.Causes {
			walk(c)
		}
	}
	walk(ve)
	return strings.Join(leaves, "; ")
}
