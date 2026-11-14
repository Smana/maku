// SPDX-License-Identifier: Apache-2.0

package install

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"sigs.k8s.io/yaml"
)

// Tree is the wizard's output, by path.
type Tree map[string][]byte

// Paths is the tree's paths, sorted.
func (t Tree) Paths() []string {
	ps := make([]string, 0, len(t))
	for p := range t {
		ps = append(ps, p)
	}
	sort.Strings(ps)
	return ps
}

// Render writes the install's files from its inputs (IN6): values.yaml, the CRD chart's values, the
// factory's objects, the merge gate's ruleset, the Secrets and the GitOps files, and a README with
// the commands to run, in order. It runs nothing.
func Render(in Inputs) (Tree, error) {
	vals, err := Values(in)
	if err != nil {
		return nil, err
	}
	_, manifests, err := FactoryObjects(in)
	if err != nil {
		return nil, err
	}
	t := Tree{}
	head := "# Written by maku install --print " + in.Answers.String("version") + ": the production defaults, every seam answered.\n" +
		"# It holds no secret value: the Secrets are under secrets/ (git-ignored), or ExternalSecrets.\n"
	if t["values.yaml"], err = yamlDoc(head, vals); err != nil {
		return nil, err
	}
	if t["crds/values.yaml"], err = yamlDoc("# agent-factory-crds: keep the CRDs, and every Room and Task, on uninstall.\n", map[string]any{"keep": true}); err != nil {
		return nil, err
	}
	files, err := splitManifests(manifests)
	if err != nil {
		return nil, err
	}
	for name, data := range files {
		t["factory/"+name] = data
	}
	rs, err := Ruleset(in.FactoryAppID())
	if err != nil {
		return nil, err
	}
	t["github/ruleset-merge-gate.json"] = rs
	t["github/apply-ruleset.sh"] = applyRuleset(in)
	for name, data := range secretFiles(in) {
		t["secrets/"+name] = data
	}
	t["secrets/.gitignore"] = []byte("*\n")
	if t["gitops/flux.yaml"], err = fluxFiles(in, vals); err != nil {
		return nil, err
	}
	if t["gitops/argocd.yaml"], err = argoFiles(in, vals); err != nil {
		return nil, err
	}
	t["README.md"] = readme(in, t)
	return t, nil
}

func yamlDoc(head string, v any) ([]byte, error) {
	b, err := yaml.Marshal(v)
	if err != nil {
		return nil, err
	}
	return append([]byte(head), b...), nil
}

// applyRuleset is the gh commands that create the gate's ruleset on each repository: the wizard
// never runs them.
func applyRuleset(in Inputs) []byte {
	var b bytes.Buffer
	b.WriteString("#!/usr/bin/env bash\n# The merge gate's ruleset on each repository (docs/factory.md#branch-protection-for-the-merge-gate).\n")
	b.WriteString("# Review it, then run it with a token that administers the repositories. maku never runs it.\nset -euo pipefail\ncd \"$(dirname \"$0\")\"\n")
	host := ""
	if g := in.ghes(); g != "" {
		host = " --hostname " + shellQuote(strings.TrimPrefix(g, "https://"))
	}
	for _, r := range repoNames(in.Answers) {
		fmt.Fprintf(&b, "gh api%s %s --method POST --input ruleset-merge-gate.json\n", host, shellQuote("repos/"+r+"/rulesets"))
	}
	return b.Bytes()
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// Write lays t out under dir: refused when dir holds anything unless force, and secrets/ is never
// overwritten. secrets/ is 0700 and its files 0600 (R3); the rest is 0644, the scripts 0755.
func Write(dir string, t Tree, force bool) error {
	if err := CheckOut(dir, force); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	for _, p := range t.Paths() {
		full := filepath.Join(dir, filepath.FromSlash(p))
		dirMode, mode := os.FileMode(0o750), os.FileMode(0o644)
		if strings.HasPrefix(p, "secrets/") {
			dirMode, mode = 0o700, 0o600
		} else if strings.HasSuffix(p, ".sh") {
			mode = 0o755
		}
		if err := os.MkdirAll(filepath.Dir(full), dirMode); err != nil {
			return err
		}
		if err := os.Chmod(filepath.Dir(full), dirMode); err != nil {
			return err
		}
		if err := writeFile(full, t[p], mode); err != nil {
			return err
		}
	}
	return nil
}

// CheckOut refuses an output directory before anything is asked or created (IN6): a non-empty one
// without force, and one holding secrets/, which is never overwritten. The command calls it first,
// since the Apps' keys exist only once, in memory, until they are written.
func CheckOut(dir string, force bool) error {
	entries, err := os.ReadDir(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return err
	case len(entries) > 0 && !force:
		return fmt.Errorf("%s is not empty: write into an empty directory, or pass --force (secrets/ is never overwritten)", dir)
	}
	if _, err := os.Stat(filepath.Join(dir, "secrets")); err == nil {
		return fmt.Errorf("%s already exists: maku never overwrites secrets/; move it away, then run again", filepath.Join(dir, "secrets"))
	}
	return nil
}

// writeFile writes data at path with mode, through a temporary file and a rename: a reader never
// sees half a file, and a symlink at path is replaced rather than followed.
func writeFile(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".maku-install-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
