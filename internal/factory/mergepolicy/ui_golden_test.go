// SPDX-License-Identifier: Apache-2.0

package mergepolicy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// The settings UI's expert view writes testdata/ui/*.yaml (web/test/policy-yaml.test.ts): each
// one must pass Parse, and full.yaml must name every field of File, so a field added here and
// not to the UI's key order (web/src/policy-yaml.ts) fails one side or the other.
func TestUIGoldensParse(t *testing.T) {
	files, err := filepath.Glob("testdata/ui/*.yaml")
	if err != nil || len(files) == 0 {
		t.Fatalf("no UI goldens: %v", err)
	}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Parse(raw); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}
}

func TestUIGoldenNamesEveryField(t *testing.T) {
	raw, err := os.ReadFile("testdata/ui/full.yaml")
	if err != nil {
		t.Fatal(err)
	}
	j, err := yaml.YAMLToJSONStrict(raw)
	if err != nil {
		t.Fatal(err)
	}
	var tree any
	if err := json.Unmarshal(j, &tree); err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	treePaths(tree, "", have)
	var want []string
	typePaths(reflect.TypeFor[File](), "", &want)
	for _, p := range want {
		// The binding shape (B15) is written by the settings pages' binding editor, which comes with
		// its own golden (reusable merge policies, plan Task 22): full.yaml is the class shape, and
		// a file holds one shape or the other.
		if strings.HasPrefix(p, ".mergePolicies") {
			continue
		}
		if !have[leafPath(p)] {
			t.Errorf("testdata/ui/full.yaml lacks %s: set it in fullPolicy (web/test/policy-yaml.test.ts) and give it its place in web/src/policy-yaml.ts", p)
		}
	}
}

// leafPath reads a leaf of an any-branch as one of all: both are a Leaf, set once in the golden.
func leafPath(p string) string {
	return strings.Replace(p, ".conditions.any[][]", ".conditions.all[]", 1)
}

func typePaths(t reflect.Type, at string, out *[]string) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Map:
		typePaths(t.Elem(), at+".*", out)
	case reflect.Slice:
		if t.Elem().Kind() != reflect.String {
			typePaths(t.Elem(), at+"[]", out)
		}
	case reflect.Struct:
		for f := range t.Fields() {
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			if name == "" || name == "-" {
				continue
			}
			*out = append(*out, at+"."+name)
			typePaths(f.Type, at+"."+name, out)
		}
	}
}

func treePaths(v any, at string, out map[string]bool) {
	switch v := v.(type) {
	case map[string]any:
		for k, x := range v {
			p := at + "." + k
			if at == ".classes" {
				p = at + ".*"
			}
			out[leafPath(p)] = true
			treePaths(x, p, out)
		}
	case []any:
		for _, e := range v {
			treePaths(e, at+"[]", out)
		}
	}
}
