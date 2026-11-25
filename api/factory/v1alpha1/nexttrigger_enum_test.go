// SPDX-License-Identifier: Apache-2.0

package v1alpha1_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// Every value the code writes to status.nextTrigger is in the CRD's enum: a value outside it makes
// the API server refuse the whole status update, silently (plan_revise on gcp-0, 2026-11-25: Revise
// was accepted by the broker and never reached the task).
func TestNextTriggerValuesAreInTheCRDEnum(t *testing.T) {
	crd, err := os.ReadFile("../../../config/crd/maku.ogenki.io_tasks.yaml")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?s)nextTrigger:\s*\n\s*description:.*?enum:\s*\n((?:\s*- [a-z_]+\n)+)`).FindSubmatch(crd)
	if m == nil {
		t.Fatal("nextTrigger enum not found in the Task CRD")
	}
	var enum []string
	for _, l := range strings.Split(strings.TrimSpace(string(m[1])), "\n") {
		enum = append(enum, strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(l), "- ")))
	}
	set := regexp.MustCompile(`NextTrigger\s*(?:=|:)\s*"([a-z_]+)"`)
	seen := 0
	err = filepath.WalkDir("../../../internal", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, v := range set.FindAllStringSubmatch(string(src), -1) {
			seen++
			if !slices.Contains(enum, v[1]) {
				t.Errorf("%s sets nextTrigger %q, not in the CRD enum %v", path, v[1], enum)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if seen == 0 {
		t.Fatal("found no nextTrigger assignment: the pattern no longer matches the code")
	}
}
