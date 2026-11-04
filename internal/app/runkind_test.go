// SPDX-License-Identifier: Apache-2.0

package app

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Smana/maku/internal/runrequest"
	"github.com/Smana/maku/internal/runwatch"
)

// runRequester is the one wiring seam that takes the runs kind as a value: with the factory unset
// it renders claims of the configured kind, whatever it is.
func TestRunRequesterCarriesTheConfiguredKind(t *testing.T) {
	native := runwatch.Kind{Group: "maku.ogenki.io", Namespace: "runs", NamePrefix: "run-"}
	for name, kind := range map[string]runwatch.Kind{"cloud-native-ref's": runwatch.DefaultKind, "a native one": native} {
		t.Run(name, func(t *testing.T) {
			if got := runRequester("", "", kind); got != (runrequest.Manifest{Kind: kind}) {
				t.Fatalf("requester %#v, want a manifest renderer of %v", got, kind)
			}
		})
	}
}

// Every binary gets its runs kind from its config's `runs` (cfg.Runs, defaulted once at Load):
// the broker's manager, watch, Room controller and suspender, the factory's manager, meter, run
// client and reconciler. A production file that names runwatch.DefaultKind instead would run a
// native-runtime deployment against cloud-native-ref's group, namespace and prefix; no file
// reads it but the two places that define and compare it (the kind itself, and the factory
// config's runtime checks).
func TestProductionCodeNeverWiresTheDefaultKind(t *testing.T) {
	allowed := map[string]bool{
		"internal/runwatch/kind.go":         true, // defines it and fills a config's gaps
		"internal/factory/config/config.go": true, // compares runs.group to it (loop policy needs cloud-native-ref's AgentRun)
	}
	root := filepath.Join("..", "..")
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if n := d.Name(); n == ".git" || n == "node_modules" || n == "web" || n == "testdata" || n == ".claude" {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel := filepath.ToSlash(strings.TrimPrefix(path, root+string(filepath.Separator)))
		if allowed[rel] {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(f, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.SelectorExpr: // runwatch.DefaultKind from outside the package
				if x.Sel.Name == "DefaultKind" {
					t.Errorf("%s: names DefaultKind: use the config's runs kind", fset.Position(x.Pos()))
				}
			case *ast.Ident: // DefaultKind inside package runwatch
				if x.Name == "DefaultKind" && f.Name.Name == "runwatch" {
					t.Errorf("%s: names DefaultKind: use the config's runs kind", fset.Position(x.Pos()))
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
