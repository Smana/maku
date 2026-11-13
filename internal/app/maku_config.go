// SPDX-License-Identifier: Apache-2.0

package app

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Smana/maku/internal/factory/config"
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
