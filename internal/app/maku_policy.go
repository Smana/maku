// SPDX-License-Identifier: Apache-2.0

package app

import (
	"cmp"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/mergepolicy"
	"github.com/Smana/maku/internal/factory/reconciler"
	"github.com/Smana/maku/internal/factory/resources"
)

// policy is `maku policy`: offline, against a factory config file, for the migration's export,
// for a repository's own CI, and (behind policyManagement.simulation) to simulate a policy on
// decision fixtures and to tell a tightening from a relaxation.
func (r Maku) policy(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("policy export, check, simulate or diff\n%s", makuUsage)
	}
	verb, args := args[0], args[1:]
	fs := flag.NewFlagSet("policy "+verb, flag.ContinueOnError)
	cfgPath := fs.String("config", "", "the factory config file")
	repository := fs.String("repository", "", "owner/name")
	var manifests manifestList
	fs.Var(&manifests, "manifests", "a Factory, Repository and MergePolicy manifest file the factory serves, over the config file; repeatable")
	pos, err := flags(fs, args)
	if err != nil {
		return err
	}
	if *cfgPath == "" || *repository == "" {
		return errors.New("policy: --config and --repository are required")
	}
	cfg, err := loadServed(*cfgPath, manifests)
	if err != nil {
		return err
	}
	var rc *config.Repository
	for _, c := range cfg.Repos() {
		if strings.EqualFold(c.Name, *repository) {
			rc = &c
		}
	}
	if rc == nil {
		return fmt.Errorf("policy: %s is not listed in %s", *repository, *cfgPath)
	}
	switch verb {
	case "export":
		f, err := mergepolicy.Export(rc, cfg)
		if err != nil {
			return fmt.Errorf("policy export: %s: %w", rc.Name, err)
		}
		raw, err := mergepolicy.Marshal(f)
		if err != nil {
			return err
		}
		_, err = r.Out.Write(raw)
		return err
	case "check":
		if len(pos) != 1 {
			return errors.New("policy check takes one policy file")
		}
		f, rep, err := readPolicy(pos[0], cfg, rc)
		if err != nil {
			return err
		}
		if err := r.printReport(f, rep); err != nil {
			return err
		}
		if d := mergepolicy.Compare(rc, f, cfg); len(d) > 0 {
			return fmt.Errorf("policy: not equivalent to the central config:\n%s", strings.Join(d, "\n"))
		}
		_, err = fmt.Fprintln(r.Out, "equivalent")
		return err
	case "simulate", "diff":
		if !cfg.PolicyManagement.Simulation {
			return fmt.Errorf("policy %s: policyManagement.simulation is off in %s", verb, *cfgPath)
		}
		if verb == "diff" {
			return r.policyDiff(pos, cfg, rc)
		}
		return r.policySimulate(pos, cfg, rc)
	}
	return fmt.Errorf("policy: unknown verb %q", verb)
}

// manifestList is a repeated --manifests.
type manifestList []string

func (m *manifestList) String() string     { return strings.Join(*m, ",") }
func (m *manifestList) Set(v string) error { *m = append(*m, v); return nil }

// loadServed is the config the factory would serve: the file alone, or the file with the manifests
// over it, as `maku config validate` assembles them. A refused manifest is an error: the policy
// would be read against a config the factory does not serve.
func loadServed(cfgPath string, manifests []string) (*config.Config, error) {
	if len(manifests) == 0 {
		return config.Load(cfgPath)
	}
	file, err := os.ReadFile(filepath.Clean(cfgPath))
	if err != nil {
		return nil, err
	}
	objs, err := readManifests(manifests)
	if err != nil {
		return nil, fmt.Errorf("policy: %w", err)
	}
	res, err := resources.Assemble(file, objs)
	if err != nil {
		return nil, fmt.Errorf("policy: %w", err)
	}
	if len(res.Refused) > 0 {
		var lines []string
		for _, f := range res.Refused {
			lines = append(lines, fmt.Sprintf("%s/%s: %s: %s", f.Kind, f.Name, f.Reason, f.Message))
		}
		return nil, fmt.Errorf("policy: the factory would refuse:\n%s", strings.Join(lines, "\n"))
	}
	return res.Config, nil
}

// readPolicy reads a policy file as check does: strictly, naming this config's factory, valid
// against it for rc.
func readPolicy(path string, cfg *config.Config, rc *config.Repository) (*mergepolicy.File, mergepolicy.Report, error) {
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, mergepolicy.Report{}, err
	}
	f, err := mergepolicy.Parse(raw)
	if err != nil {
		return nil, mergepolicy.Report{}, err
	}
	if f.Factory != cfg.Factory {
		return nil, mergepolicy.Report{}, fmt.Errorf("policy: the file names factory %q, this config is factory %q", f.Factory, cfg.Factory)
	}
	rep, err := mergepolicy.ValidateFile(f, rc, cfg)
	if err != nil {
		return nil, rep, err
	}
	return f, rep, nil
}

// printReport prints the bindings that do not resolve, in file order: the file is valid, and each
// line is what the factory would log.
func (r Maku) printReport(f *mergepolicy.File, rep mergepolicy.Report) error {
	for _, b := range f.MergePolicies {
		if e, ok := rep.BindingErrors[b.Name]; ok {
			if _, err := fmt.Fprintf(r.Out, "binding %s: %s: %s\n", b.Name, e.Reason, e.Message); err != nil {
				return err
			}
		}
	}
	return nil
}

// policySimulate decides each fixture under the central config and under the policy file, with
// the gate's own evaluator (MP16), and prints the change. A fixture recorded under another config
// shows the decision it recorded beside this config's.
func (r Maku) policySimulate(pos []string, cfg *config.Config, rc *config.Repository) error {
	if len(pos) < 2 {
		return errors.New("policy simulate takes a policy file and one or more fixtures")
	}
	_, rep, err := readPolicy(pos[0], cfg, rc)
	if err != nil {
		return err
	}
	policy := rep.Classes
	var cases []reconciler.Case
	for _, p := range pos[1:] {
		raw, err := os.ReadFile(filepath.Clean(p))
		if err != nil {
			return err
		}
		c, err := reconciler.ReadCase(raw)
		if err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		c.Inputs.Task.Spec.Repository = rc.Name // the simulation is of this repository
		cases = append(cases, c)
	}
	w := tabwriter.NewWriter(r.Out, 0, 4, 2, ' ', 0) // buffered: Flush returns the write error
	_, _ = fmt.Fprintln(w, "CASE\tCLASS\tCENTRAL\tREPOSITORY\tCHANGE")
	for _, row := range reconciler.Simulate(cfg, policy, cases) {
		change := row.Change
		if !row.Conforms {
			change += " (the fixture recorded another central decision)"
		}
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", row.Name, row.Class, show(row.Central), show(row.Repository), change)
	}
	return w.Flush()
}

func show(o reconciler.Outcome) string { return o.Verdict + ":" + cmp.Or(o.Reason, "armed") }

// policyDiff prints each class's structural change from the first file to the second (MP8).
func (r Maku) policyDiff(pos []string, cfg *config.Config, rc *config.Repository) error {
	if len(pos) != 2 {
		return errors.New("policy diff takes the old policy file and the new one")
	}
	old, _, err := readPolicy(pos[0], cfg, rc)
	if err != nil {
		return err
	}
	next, _, err := readPolicy(pos[1], cfg, rc)
	if err != nil {
		return err
	}
	changes, err := mergepolicy.Structural(old, next, rc, cfg)
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(r.Out, 0, 4, 2, ' ', 0) // buffered: Flush returns the write error
	_, _ = fmt.Fprintln(w, "CLASS\tCHANGE\tWHY")
	for _, c := range changes {
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\n", c.Class, c.Kind, strings.Join(c.Why, "; "))
	}
	return w.Flush()
}
