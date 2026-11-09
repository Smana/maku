// SPDX-License-Identifier: Apache-2.0

package app

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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
		ns := fs.String("namespace", "agent-system", "the namespace of the manifests")
		residual := fs.String("residual", "", "write the config file without the migrated keys here")
		if _, err := flags(fs, args); err != nil {
			return err
		}
		if *from == "" {
			return errors.New("config migrate: --from is required")
		}
		file, err := os.ReadFile(filepath.Clean(*from))
		if err != nil {
			return fmt.Errorf("config migrate: %w", err)
		}
		manifests, rest, err := resources.Migrate(file, *ns)
		if err != nil {
			return fmt.Errorf("config migrate: %w", err)
		}
		if *residual != "" {
			if err := writeIn(filepath.Clean(*residual), rest); err != nil {
				return fmt.Errorf("config migrate: %w", err)
			}
		}
		_, err = r.Out.Write(manifests)
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
		docs := make([]string, 0, len(pos))
		for _, p := range pos {
			raw, err := os.ReadFile(filepath.Clean(p))
			if err != nil {
				return fmt.Errorf("config validate: %w", err)
			}
			docs = append(docs, string(raw))
		}
		objs, err := resources.Decode([]byte(strings.Join(docs, "---\n")))
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
		for _, f := range res.Refused {
			if _, err := fmt.Fprintf(r.Out, "%s/%s: %s: %s\n", f.Kind, f.Name, f.Reason, f.Message); err != nil {
				return err
			}
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
