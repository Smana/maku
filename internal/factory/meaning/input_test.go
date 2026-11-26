// SPDX-License-Identifier: Apache-2.0

package meaning_test

import (
	"testing"
	"time"

	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/meaning"
)

func TestFor(t *testing.T) {
	rc := &config.Repository{Name: "o/n", Classes: map[string]config.Class{"docs-links": {Live: true}, "docs": {AllowedFiles: []string{`^.+\.md$`}}},
		Merge: config.Merge{VerifyFor: config.Duration{Duration: 30 * time.Minute}, RevertWindow: config.Duration{Duration: 168 * time.Hour}}}
	in, ok := meaning.For(rc, "docs-links")
	if !ok || in.Mode != config.ModeLive || in.VerifyFor != 30*time.Minute || in.RevertWindow != 168*time.Hour {
		t.Fatalf("%+v", in)
	}
	if s := meaning.OfRepository(rc, "docs"); s == nil || s.Lead != "Merges nothing here:" {
		t.Fatalf("an unmarked class is off: %+v", s)
	}
	if meaning.OfRepository(rc, "nope") != nil {
		t.Fatal("an undeclared class has no sentence")
	}
	if s := meaning.OfPolicy("docs", config.PolicySpec{MergeClass: config.MergeClass{AllowedFiles: []string{`^.+\.md$`}}}); s.Lead != "Would merge without you when" || !s.Prose {
		t.Fatalf("%+v", s)
	}
}
