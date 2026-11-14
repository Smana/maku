// SPDX-License-Identifier: Apache-2.0

package install

import (
	"bytes"
	"errors"
	"fmt"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
	sigsyaml "sigs.k8s.io/yaml"

	"github.com/Smana/maku/internal/factory/fence"
	"github.com/Smana/maku/internal/factory/resources"
)

// RepoInputs is what one repository's onboarding files are written from (workflows review WF11).
type RepoInputs struct {
	Repository     string // owner/name
	Factory        string // the Factory's name; the fence's value
	Namespace      string
	Maintainers    []string
	RequiredChecks []string // the default branch's required checks
	LeakScanCheck  string
	Private        bool
	Acknowledged   bool   // spec.privateCode.acknowledged, required when Private
	FactoryAppID   string // the ruleset's integration
	FactoryLogin   string // the factory App's bot login: merge.mergerLogin
	// NoMergeDefaults writes the merge settings every repository shares (mergerLogin, verifyFor,
	// revertWindow, breaker) into the Repository, for a Factory with no spec.mergeDefaults: the
	// factory refuses a Repository that lacks them and has no default to fill them. By default they
	// are left to the Factory's spec.mergeDefaults, which maku install --print writes (Q15).
	NoMergeDefaults bool
	// The two Apps' installation links, for the README; empty leaves the line out.
	FactoryInstallURL, AgentsInstallURL string
}

// ValidRepository reports whether s is a GitHub owner/name.
func ValidRepository(s string) bool { return repoRE.MatchString(s) }

func (in RepoInputs) validate() error {
	var errs []error
	bad := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }
	if !repoRE.MatchString(in.Repository) {
		bad("%q is not owner/name", in.Repository)
	}
	if !fence.ValidName(in.Factory) {
		bad("the factory's name %q is not a DNS label", in.Factory)
	}
	if !dnsLabelRE.MatchString(in.Namespace) {
		bad("the namespace %q is not a DNS label", in.Namespace)
	}
	if len(in.Maintainers) == 0 {
		bad("no maintainer: pass --maintainer LOGIN")
	}
	for _, m := range in.Maintainers {
		if !loginRE.MatchString(m) {
			bad("the maintainer %q is not a GitHub login", m)
		}
	}
	if len(in.RequiredChecks) == 0 {
		bad("no required check: the factory merges on one: require a check on the default branch, or pass --check NAME")
	} else if !slices.Contains(in.RequiredChecks, in.LeakScanCheck) {
		bad("the leak scan check %q is not a required check (R42): pass --leak-scan-check with one of %s", in.LeakScanCheck, strings.Join(in.RequiredChecks, ", "))
	}
	if !strings.HasSuffix(in.FactoryLogin, "[bot]") {
		bad("the factory App's login %q is not a bot login", in.FactoryLogin)
	}
	if _, err := jsonNumber(in.FactoryAppID); err != nil {
		errs = append(errs, err)
	}
	if in.Private && !in.Acknowledged {
		bad("%s is private: the factory refuses its new tasks until spec.privateCode.acknowledged is set (D3); pass --acknowledge-private-code once its owner agrees its code goes to the model provider", in.Repository)
	}
	return errors.Join(errs...)
}

// RepoTree is the files `maku repo add` writes: factory/repository-<name>.yaml (no binding: the
// catalog is evaluated in shadow without one), repo/.github/agent-factory.yaml (the fence),
// github/ruleset-merge-gate.json and github/apply-ruleset.sh (install's), and a README.md.
func RepoTree(in RepoInputs) (Tree, error) {
	if err := in.validate(); err != nil {
		return nil, err
	}
	name := resources.ObjectName(in.Repository, nil)
	repo, err := repositoryYAML(name, in)
	if err != nil {
		return nil, err
	}
	rs, err := Ruleset(in.FactoryAppID)
	if err != nil {
		return nil, err
	}
	t := Tree{
		"factory/repository-" + name + ".yaml": repo,
		"repo/" + fence.Path:                   []byte("factory: " + in.Factory + "\n"),
		"github/ruleset-merge-gate.json":       rs,
		"github/apply-ruleset.sh":              rulesetScript("", []string{in.Repository}),
	}
	t["README.md"] = repoReadme(in, name)
	return t, nil
}

// repositoryYAML is the Repository in the spec's compact form: flow-style metadata and lists, keys
// in the order a reader expects them. The shared merge settings are install's (mergeDefaults).
func repositoryYAML(name string, in RepoInputs) ([]byte, error) {
	var b bytes.Buffer
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(repositoryNode(name, in)); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func repositoryNode(name string, in RepoInputs) *yaml.Node {
	merge := mapping(false, "requiredChecks", flowList(in.RequiredChecks), "verifyChecks", flowList(in.RequiredChecks),
		"leakScanCheck", in.LeakScanCheck)
	if in.NoMergeDefaults {
		merge.Content = append(merge.Content, mapping(false, "mergerLogin", in.FactoryLogin, "verifyFor", defaultVerifyFor,
			"revertWindow", defaultRevertWindow, "breaker", mapping(true, "window", intNode(defaultBreakerWindow),
				"maxReverts", intNode(defaultBreakerMaxReverts))).Content...)
	}
	spec := mapping(false, "factoryRef", in.Factory, "repository", in.Repository, "maintainers", flowList(in.Maintainers),
		"triggerLabel", "factory/ready", "merge", merge)
	if in.Private {
		spec.Content = append(spec.Content, strNode("privateCode"),
			mapping(true, "acknowledged", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: "true"}))
	}
	return mapping(false, "apiVersion", "maku.ogenki.io/v1alpha1", "kind", string(resources.KindRepository),
		"metadata", mapping(true, "name", name, "namespace", in.Namespace), "spec", spec)
}

// strNode is s as a YAML string, quoted when a YAML 1.1 reader (sigs.k8s.io/yaml, the factory's)
// would read it as anything else: yes, on, 1.0, a leading '*'.
func strNode(s string) *yaml.Node {
	n := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: s}
	var v any
	if err := sigsyaml.Unmarshal([]byte(s), &v); err != nil || v != s {
		n.Style = yaml.DoubleQuotedStyle
	}
	return n
}

func intNode(i int) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: fmt.Sprint(i)}
}

func flowList(ss []string) *yaml.Node {
	n := &yaml.Node{Kind: yaml.SequenceNode, Style: yaml.FlowStyle}
	for _, s := range ss {
		n.Content = append(n.Content, strNode(s))
	}
	return n
}

// mapping is kv's keys and values in order: a value is a *yaml.Node or a string.
func mapping(flow bool, kv ...any) *yaml.Node {
	n := &yaml.Node{Kind: yaml.MappingNode}
	if flow {
		n.Style = yaml.FlowStyle
	}
	for i := 0; i+1 < len(kv); i += 2 {
		v, ok := kv[i+1].(*yaml.Node)
		if !ok {
			v = strNode(kv[i+1].(string))
		}
		n.Content = append(n.Content, strNode(kv[i].(string)), v)
	}
	return n
}

func repoReadme(in RepoInputs, name string) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "# Onboarding %s to %s\n\nWritten by `maku repo add`. Nothing here has been applied.\n\n", in.Repository, in.Factory)
	b.WriteString("| Path | What it is |\n|---|---|\n")
	fmt.Fprintf(&b, "| `factory/repository-%s.yaml` | the `Repository`: commit it where your GitOps applies the factory's objects |\n", name)
	fmt.Fprintf(&b, "| `repo/%s` | the fence: commit it to %s's default branch |\n", fence.Path, in.Repository)
	b.WriteString("| `github/ruleset-merge-gate.json`, `github/apply-ruleset.sh` | the merge gate's ruleset and the `gh` command that creates it |\n\n")
	b.WriteString("In order:\n\n")
	fmt.Fprintf(&b, "1. Commit `repo/%s` to %s's default branch.\n", fence.Path, in.Repository)
	if in.FactoryInstallURL != "" && in.AgentsInstallURL != "" {
		fmt.Fprintf(&b, "2. Install both Apps on %s:\n   - %s\n   - %s\n", in.Repository, in.FactoryInstallURL, in.AgentsInstallURL)
	} else {
		fmt.Fprintf(&b, "2. Install the factory App and the agents App on %s.\n", in.Repository)
	}
	b.WriteString("3. Review `github/apply-ruleset.sh`, then run it with a token that administers the repository.\n")
	fmt.Fprintf(&b, "4. Commit `factory/repository-%s.yaml` to your GitOps repository.\n", name)
	fmt.Fprintf(&b, "5. Run `maku repo check %s` once the factory has applied it.\n", in.Repository)
	return []byte(b.String())
}
