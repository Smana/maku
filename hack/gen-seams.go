// SPDX-License-Identifier: Apache-2.0

//go:build ignore

// Command gen-seams writes the umbrella's and the maku chart's _seams.tpl and docs/install-secrets.md's table from
// internal/install's registry (v4 D14). Run it with `task chart:seams`; chart:check fails on drift.
package main

import (
	"bytes"
	"fmt"
	"os"

	"github.com/Smana/maku/internal/install"
)

func main() {
	for path, gen := range map[string]func() (string, error){
		"charts/agent-factory/templates/_seams.tpl": install.Template,
		"charts/maku/templates/_seams.tpl":          install.MakuTemplate,
	} {
		tpl, err := gen()
		if err != nil {
			fail(err)
		}
		if err := os.WriteFile(path, []byte(tpl), 0o644); err != nil { //nolint:gosec // a chart file
			fail(err)
		}
	}
	const doc = "docs/install-secrets.md"
	raw, err := os.ReadFile(doc)
	if err != nil {
		fail(err)
	}
	start, end := bytes.Index(raw, []byte(install.TableStart)), bytes.Index(raw, []byte(install.TableEnd))
	if start < 0 || end < start {
		fail(fmt.Errorf("%s lacks the table markers", doc))
	}
	out := append(append(append([]byte{}, raw[:start+len(install.TableStart)]...), []byte("\n"+install.Table()+"\n")...), raw[end:]...)
	if err := os.WriteFile(doc, out, 0o644); err != nil { //nolint:gosec // a docs file
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "gen-seams:", err)
	os.Exit(1)
}
