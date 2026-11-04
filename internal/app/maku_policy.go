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
	"github.com/Smana/maku/internal/factory/mergepolicy"
)

// policy is `maku policy`: offline, against a factory config file, for the migration's export
// and for a repository's own CI.
func (r Maku) policy(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("policy export or policy check\n%s", makuUsage)
	}
	verb, args := args[0], args[1:]
	fs := flag.NewFlagSet("policy "+verb, flag.ContinueOnError)
	cfgPath := fs.String("config", "", "the factory config file")
	repository := fs.String("repository", "", "owner/name")
	pos, err := flags(fs, args)
	if err != nil {
		return err
	}
	if *cfgPath == "" || *repository == "" {
		return errors.New("policy: --config and --repository are required")
	}
	cfg, err := config.Load(*cfgPath)
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
			return err
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
		raw, err := os.ReadFile(filepath.Clean(pos[0]))
		if err != nil {
			return err
		}
		f, err := mergepolicy.Parse(raw)
		if err != nil {
			return err
		}
		if err := mergepolicy.Validate(f, cfg); err != nil {
			return err
		}
		if d := mergepolicy.Compare(rc, f, cfg); len(d) > 0 {
			return fmt.Errorf("policy: not equivalent to the central config:\n%s", strings.Join(d, "\n"))
		}
		_, err = fmt.Fprintln(r.Out, "equivalent")
		return err
	}
	return fmt.Errorf("policy: unknown verb %q", verb)
}
