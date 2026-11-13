// SPDX-License-Identifier: Apache-2.0

package app

import (
	"cmp"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"text/tabwriter"

	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/mergepolicy"
	"github.com/Smana/maku/internal/factory/policyview"
	"github.com/Smana/maku/internal/factory/resources"
)

// config is `maku config`: offline, it splits a factory config file into the Factory, Repository
// and MergePolicy manifests (migrate) and checks manifests against a config file the way the
// factory would (validate).
func (r Maku) config(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("config migrate or validate\n%s", makuUsage)
	}
	verb, args := args[0], args[1:]
	fs := flag.NewFlagSet("config "+verb, flag.ContinueOnError)
	switch verb {
	case "migrate":
		from := fs.String("from", "", "the factory config file")
		cfgPath := fs.String("config", "", "with --manifests: the config file the manifests are served over")
		var manifests manifestList
		fs.Var(&manifests, "manifests", "per-repository MergePolicy manifests (and their Factory and Repositories) to convert; repeatable")
		ns := fs.String("namespace", "agent-system", "the namespace of the manifests")
		residual := fs.String("residual", "", "write the config file without the migrated keys here")
		if _, err := flags(fs, args); err != nil {
			return err
		}
		if (*from == "") == (len(manifests) == 0) {
			return errors.New("config migrate: --from, or --config with --manifests, is required")
		}
		out, before, file, err := r.migrateFrom(*from, *cfgPath, manifests, *ns, *residual)
		if err != nil {
			return fmt.Errorf("config migrate: %w", err)
		}
		objs, err := resources.Decode(out)
		if err != nil {
			return fmt.Errorf("config migrate: %w", err)
		}
		after, err := resources.Assemble(file, objs)
		if err != nil {
			return fmt.Errorf("config migrate: the converted objects: %w", err)
		}
		if err := r.printRefusals(after); err != nil {
			return err
		}
		if len(after.Refused) > 0 {
			return fmt.Errorf("config migrate: the factory would refuse %d converted objects", len(after.Refused))
		}
		// B17: the converter proves it, or writes nothing.
		if d := resources.Equal(before, after.Config); len(d) > 0 {
			for _, line := range d {
				if _, err := fmt.Fprintf(r.Out, "changed: %s\n", line); err != nil {
					return err
				}
			}
			return fmt.Errorf("config migrate: the conversion would change %d resolved settings: nothing written", len(d))
		}
		_, err = r.Out.Write(out)
		return err
	case "validate":
		cfgPath := fs.String("config", "", "the factory config file")
		var base manifestList
		fs.Var(&base, "base", "the manifests before the change (e.g. origin/main's), for each changed policy's consumers; repeatable")
		resolved := fs.Bool("resolved", false, "print each repository's resolved classes as JSON")
		unchanged := fs.Bool("expect-unchanged", false, "with --base: fail when any resolved class differs from the base's")
		pos, err := flags(fs, args)
		if err != nil {
			return err
		}
		if *cfgPath == "" || len(pos) == 0 {
			return errors.New("config validate: --config and at least one manifest file are required")
		}
		file, err := os.ReadFile(filepath.Clean(*cfgPath))
		if err != nil {
			return fmt.Errorf("config validate: %w", err)
		}
		objs, err := readManifests(pos)
		if err != nil {
			return fmt.Errorf("config validate: %w", err)
		}
		// An error means no servable config; its refusals are still printed first.
		res, assembleErr := resources.Assemble(file, objs)
		for _, w := range res.Warnings {
			if _, err := fmt.Fprintf(r.Out, "warning: %s\n", w); err != nil {
				return err
			}
		}
		if err := r.printRefusals(res); err != nil {
			return err
		}
		if assembleErr != nil {
			return fmt.Errorf("config validate: %w", assembleErr)
		}
		if len(res.Refused) > 0 {
			return fmt.Errorf("config validate: %d refused", len(res.Refused))
		}
		if *unchanged && len(base) == 0 {
			return errors.New("config validate: --expect-unchanged compares with --base")
		}
		if len(base) > 0 {
			if err := r.blastRadius(file, base, res, *unchanged); err != nil {
				return err
			}
		}
		if over, err := r.guardrailLines(res); err != nil || over > 0 {
			return cmp.Or(err, fmt.Errorf("config validate: %d bindings over a guardrail", over))
		}
		if *resolved {
			return r.printResolved(res.Config)
		}
		_, err = fmt.Fprintf(r.Out, "ok: %d accepted\n", len(res.Accepted))
		return err
	}
	return fmt.Errorf("config: unknown verb %q\n%s", verb, makuUsage)
}

// migrateFrom converts a config file (--from) or per-repository manifests over a config file
// (--config with --manifests) into the binding shape: the manifests, the config served before,
// and the file the manifests are served over. A residual, written only for --from, is the file.
func (r Maku) migrateFrom(from, cfgPath string, manifests []string, ns, residual string) (out []byte, before *config.Config, file []byte, err error) {
	if from != "" {
		raw, err := os.ReadFile(filepath.Clean(from))
		if err != nil {
			return nil, nil, nil, err
		}
		if before, err = config.Parse(raw); err != nil {
			return nil, nil, nil, err
		}
		if out, file, err = resources.Migrate(raw, ns); err != nil {
			return nil, nil, nil, err
		}
		if residual != "" {
			if err := writeIn(filepath.Clean(residual), file); err != nil {
				return nil, nil, nil, err
			}
		}
		return out, before, file, nil
	}
	if cfgPath == "" {
		return nil, nil, nil, errors.New("--manifests needs --config, the file they are served over")
	}
	if file, err = os.ReadFile(filepath.Clean(cfgPath)); err != nil {
		return nil, nil, nil, err
	}
	objs, err := readManifests(manifests)
	if err != nil {
		return nil, nil, nil, err
	}
	res, err := resources.Assemble(file, objs)
	if err != nil {
		return nil, nil, nil, err
	}
	if err := r.printRefusals(res); err != nil {
		return nil, nil, nil, err
	}
	if len(res.Refused) > 0 {
		return nil, nil, nil, fmt.Errorf("the factory refuses %d of the objects as they are: fix them first", len(res.Refused))
	}
	split, err := resources.Split(objs, ns)
	if err != nil {
		return nil, nil, nil, err
	}
	if out, err = resources.Encode(split); err != nil {
		return nil, nil, nil, err
	}
	return out, res.Config, file, nil
}

// readManifests decodes manifest files, strictly, into one object list.
func readManifests(paths []string) ([]resources.Object, error) {
	docs := make([]string, 0, len(paths))
	for _, p := range paths {
		raw, err := os.ReadFile(filepath.Clean(p))
		if err != nil {
			return nil, err
		}
		docs = append(docs, string(raw))
	}
	return resources.Decode([]byte(strings.Join(docs, "---\n")))
}

// printRefusals prints each refused object as validate does.
func (r Maku) printRefusals(res resources.Result) error {
	for _, f := range res.Refused {
		if _, err := fmt.Fprintf(r.Out, "%s/%s: %s: %s\n", f.Kind, f.Name, f.Reason, f.Message); err != nil {
			return err
		}
	}
	return nil
}

// blastRadius prints each changed policy's consumers against the base manifests (B11, Q5): live
// consumers first, each relaxation of a live one marked LIVE. With unchanged, any resolved class
// that differs from the base's is an error.
func (r Maku) blastRadius(file []byte, paths []string, next resources.Result, unchanged bool) error {
	objs, err := readManifests(paths)
	if err != nil {
		return fmt.Errorf("config validate: --base: %w", err)
	}
	base, err := resources.Assemble(file, objs)
	if err != nil {
		return fmt.Errorf("config validate: --base: %w", err)
	}
	w := tabwriter.NewWriter(r.Out, 0, 4, 2, ' ', 0) // buffered: Flush returns the write error
	for _, p := range policyview.Impact(base, next) {
		gen := ""
		if p.BaseGeneration != 0 || p.Generation != 0 {
			gen = fmt.Sprintf(" (generation %d -> %d)", p.BaseGeneration, p.Generation)
		}
		_, _ = fmt.Fprintf(w, "MergePolicy %s%s:\n", p.Policy, gen)
		for _, c := range p.Consumers {
			mark, change, result := "", c.Change, "resolves"
			if len(c.Why) > 0 {
				change += ": " + strings.Join(c.Why, "; ")
			}
			if c.Mode == config.ModeLive && c.Change == mergepolicy.KindRelaxation {
				mark = "LIVE "
			}
			if !c.Resolves {
				result = "refused: " + c.Reason
			}
			_, _ = fmt.Fprintf(w, "  %s%s\t%s\t%s\t%s\n", mark, c.Repository, c.Mode, change, result)
		}
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if !unchanged {
		return nil
	}
	if base.Config == nil || next.Config == nil {
		return errors.New("config validate: --expect-unchanged: no config to compare")
	}
	d := resources.Equal(base.Config, next.Config)
	for _, line := range d {
		if _, err := fmt.Fprintf(r.Out, "changed: %s\n", line); err != nil {
			return err
		}
	}
	if len(d) > 0 {
		return fmt.Errorf("config validate: %d resolved settings differ from --base", len(d))
	}
	return nil
}

// guardrailLines prints one line per binding over a guardrail (Q10, PR10) and, with guardrails
// set and none over, how many bindings are within. It returns how many are over.
func (r Maku) guardrailLines(res resources.Result) (int, error) {
	over, within := 0, 0
	for _, b := range res.Bindings {
		switch {
		case b.Reason == config.ReasonGuardrailMismatch:
			over++
			msg := strings.TrimPrefix(b.Message, "class "+b.Name+": ")
			if _, err := fmt.Fprintf(r.Out, "guardrail: Repository/%s %s: %s\n", b.Repository, b.Name, msg); err != nil {
				return over, err
			}
		case b.Accepted:
			within++
		}
	}
	if over == 0 && res.Config != nil && !reflect.ValueOf(res.Config.MergeGuardrails).IsZero() {
		_, err := fmt.Fprintf(r.Out, "guardrails: %d bindings within\n", within)
		return 0, err
	}
	return over, nil
}

// printResolved prints each repository's resolved classes as JSON, sorted by repository and
// class: the snapshot a migration pull request keeps unchanged.
func (r Maku) printResolved(c *config.Config) error {
	out := map[string]map[string]config.Class{}
	for _, rc := range c.Repos() {
		out[rc.Name] = rc.Classes
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(r.Out, "%s\n", b)
	return err
}

// writeIn writes b to p, 0600, through an os.Root on p's directory: the file stays in the
// directory the developer named.
func writeIn(p string, b []byte) error {
	root, err := os.OpenRoot(filepath.Dir(p))
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	return root.WriteFile(filepath.Base(p), b, 0o600)
}
