// SPDX-License-Identifier: Apache-2.0

package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"text/tabwriter"
	"time"

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
		residual := fs.String("residual", "", "write the config file without the migrated keys here, once the conversion is proved")
		force := fs.Bool("force", false, "replace an existing --residual file")
		if _, err := flags(fs, args); err != nil {
			return err
		}
		if (*from == "") == (len(manifests) == 0) {
			return errors.New("config migrate: --from, or --config with --manifests, is required")
		}
		if *residual != "" && *from != "" && samePath(*residual, *from) {
			return errors.New("config migrate: --residual is the --from file: write the residual beside it")
		}
		if *residual != "" && !*force {
			if _, err := os.Stat(*residual); err == nil {
				return fmt.Errorf("config migrate: %s exists: --force replaces it", *residual)
			}
		}
		out, before, file, err := r.migrateFrom(*from, *cfgPath, manifests, *ns)
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
		if err := printRefusals(r.Out, after); err != nil {
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
		if *residual != "" && *from != "" {
			if err := writeAtomic(filepath.Clean(*residual), file); err != nil {
				return fmt.Errorf("config migrate: %w", err)
			}
		}
		_, err = r.Out.Write(out)
		return err
	case "validate":
		cfgPath := fs.String("config", "", "the factory config file")
		var base manifestList
		fs.Var(&base, "base", "the manifests before the change, for each changed policy's consumers; repeatable, with --base-config")
		baseConfig := fs.String("base-config", "", "with --base: the config file the base manifests are served over")
		baseRef := fs.String("base-ref", "", "a git ref (e.g. origin/main): the base is --config and each manifest as committed there")
		resolved := fs.Bool("resolved", false, "print each repository's resolved classes as JSON on stdout, and everything else on stderr")
		unchanged := fs.Bool("expect-unchanged", false, "with a base: fail when any resolved class differs from the base's")
		allowRefused := fs.Bool("allow-refused-bindings", false, "print a binding that does not resolve as a warning instead of failing")
		pos, err := flags(fs, args)
		if err != nil {
			return err
		}
		if *cfgPath == "" || len(pos) == 0 {
			return errors.New("config validate: --config and at least one manifest file are required")
		}
		switch {
		case len(base) > 0 && *baseRef != "":
			return errors.New("config validate: --base or --base-ref, not both")
		case len(base) > 0 && *baseConfig == "":
			return errors.New("config validate: --base needs --base-config, the file the base is served over: a catalogue change shows only against it")
		case *unchanged && len(base) == 0 && *baseRef == "":
			return errors.New("config validate: --expect-unchanged compares with --base or --base-ref")
		}
		info := r.Out // with --resolved, stdout is the JSON alone
		if *resolved {
			info = r.Err
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
			if _, err := fmt.Fprintf(info, "warning: %s\n", w); err != nil {
				return err
			}
		}
		if err := printRefusals(info, res); err != nil {
			return err
		}
		if assembleErr != nil {
			return fmt.Errorf("config validate: %w", assembleErr)
		}
		if len(res.Refused) > 0 {
			return fmt.Errorf("config validate: %d refused", len(res.Refused))
		}
		if len(base) > 0 || *baseRef != "" {
			baseFile, baseObjs, err := readBase(*baseConfig, base, *baseRef, *cfgPath, pos)
			if err != nil {
				return fmt.Errorf("config validate: the base: %w", err)
			}
			if err := blastRadius(info, baseFile, baseObjs, res, *unchanged); err != nil {
				return err
			}
		}
		over, err := guardrailLines(info, res)
		if err != nil {
			return err
		}
		refused, err := refusedBindings(info, res, *allowRefused)
		if err != nil {
			return err
		}
		switch {
		case over > 0 && refused > 0:
			return fmt.Errorf("config validate: %d bindings over a guardrail, %d refused", over, refused)
		case over > 0:
			return fmt.Errorf("config validate: %d bindings over a guardrail", over)
		case refused == 1:
			return errors.New("config validate: 1 binding refused")
		case refused > 0:
			return fmt.Errorf("config validate: %d bindings refused", refused)
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
// and the file the manifests are served over: for --from, the residual. It writes nothing.
func (r Maku) migrateFrom(from, cfgPath string, manifests []string, ns string) (out []byte, before *config.Config, file []byte, err error) {
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
	if err := printRefusals(r.Out, res); err != nil {
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
func printRefusals(out io.Writer, res resources.Result) error {
	for _, f := range res.Refused {
		if _, err := fmt.Fprintf(out, "%s/%s: %s: %s\n", f.Kind, f.Name, f.Reason, f.Message); err != nil {
			return err
		}
	}
	return nil
}

// blastRadius prints each changed policy's consumers against the base manifests (B11, Q5): live
// consumers first, each relaxation of a live one marked LIVE. With unchanged, any resolved class
// that differs from the base's is an error.
func blastRadius(out io.Writer, file []byte, objs []resources.Object, next resources.Result, unchanged bool) error {
	base, err := resources.Assemble(file, objs)
	if err != nil {
		return fmt.Errorf("config validate: --base: %w", err)
	}
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0) // buffered: Flush returns the write error
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
		if _, err := fmt.Fprintf(out, "changed: %s\n", line); err != nil {
			return err
		}
	}
	if len(d) > 0 {
		return fmt.Errorf("config validate: %d resolved settings differ from the base", len(d))
	}
	return nil
}

// readBase is the base a change is compared with: --base-config and --base, or, with ref, the
// config file and each manifest as committed at ref (git show), so CI compares with origin/main
// without copying it.
func readBase(cfgPath string, paths []string, ref, nextCfg string, nextPaths []string) ([]byte, []resources.Object, error) {
	if ref == "" {
		file, err := os.ReadFile(filepath.Clean(cfgPath))
		if err != nil {
			return nil, nil, err
		}
		objs, err := readManifests(paths)
		return file, objs, err
	}
	file, err := gitShow(ref, nextCfg)
	if err != nil {
		return nil, nil, err
	}
	docs := make([]string, 0, len(nextPaths))
	for _, p := range nextPaths {
		b, err := gitShow(ref, p)
		if err != nil {
			return nil, nil, err
		}
		docs = append(docs, string(b))
	}
	objs, err := resources.Decode([]byte(strings.Join(docs, "---\n")))
	return file, objs, err
}

// gitShow is path's content at ref, read from the repository path is in.
func gitShow(ref, path string) ([]byte, error) {
	if strings.HasPrefix(ref, "-") {
		return nil, fmt.Errorf("--base-ref %q is not a ref", ref)
	}
	p := filepath.Clean(path)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", filepath.Dir(p), "show", ref+":./"+filepath.Base(p)) //nolint:gosec // a ref and a path the developer named, no shell
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git show %s:%s: %w: %s", ref, path, err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// guardrailLines prints one line per binding over a guardrail (Q10, PR10) and, with guardrails
// set and none over, how many bindings are within. It returns how many are over.
func guardrailLines(out io.Writer, res resources.Result) (int, error) {
	over, within := 0, 0
	for _, b := range res.Bindings {
		switch {
		case b.Reason == config.ReasonGuardrailMismatch:
			over++
			msg := strings.TrimPrefix(b.Message, "class "+b.Name+": ")
			if _, err := fmt.Fprintf(out, "guardrail: Repository/%s %s: %s\n", b.Repository, b.Name, msg); err != nil {
				return over, err
			}
		case b.Accepted:
			within++
		}
	}
	if over == 0 && res.Config != nil && !reflect.ValueOf(res.Config.MergeGuardrails).IsZero() {
		_, err := fmt.Fprintf(out, "guardrails: %d bindings within\n", within)
		return 0, err
	}
	return over, nil
}

// refusedBindings prints each binding that does not resolve, other than one over a guardrail
// (guardrailLines prints those), with its reason: such a binding drops its class without refusing an
// object (B9), so the objects alone would read as accepted. It returns how many fail the run: none
// with allow, which prints each as a warning.
func refusedBindings(out io.Writer, res resources.Result, allow bool) (int, error) {
	n := 0
	for _, b := range res.Bindings {
		if b.Accepted || b.Reason == config.ReasonGuardrailMismatch {
			continue
		}
		prefix := "binding: "
		if allow {
			prefix = "warning: binding: "
		} else {
			n++
		}
		if _, err := fmt.Fprintf(out, "%sRepository/%s %s: %s: %s\n", prefix, b.Repository, b.Name, b.Reason, b.Message); err != nil {
			return n, err
		}
	}
	return n, nil
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

// writeAtomic writes b to p, 0600, through an os.Root on p's directory, so the file stays in the
// directory the developer named: a temporary file beside p, renamed over it, so a failed write
// never leaves p half written.
func writeAtomic(p string, b []byte) error {
	root, err := os.OpenRoot(filepath.Dir(p))
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	tmp := fmt.Sprintf(".%s.%d.tmp", filepath.Base(p), os.Getpid())
	if err := root.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := root.Rename(tmp, filepath.Base(p)); err != nil {
		_ = root.Remove(tmp)
		return err
	}
	return nil
}

// samePath reports whether a and b name one file: the same cleaned absolute path, or the same file
// on disk.
func samePath(a, b string) bool {
	x, err1 := filepath.Abs(a)
	y, err2 := filepath.Abs(b)
	if err1 == nil && err2 == nil && x == y {
		return true
	}
	sa, err1 := os.Stat(a)
	sb, err2 := os.Stat(b)
	return err1 == nil && err2 == nil && os.SameFile(sa, sb)
}
