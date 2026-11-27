// SPDX-License-Identifier: Apache-2.0

package app

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/Smana/maku/internal/maku"
)

func runHelp(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := (Maku{Dir: t.TempDir(), Out: &out, Err: &out}).Run(t.Context(), args)
	return out.String(), err
}

// Developer review gap 10: maku help is the everyday commands; the platform team's are one more word away.
func TestHelpIsEverydayByDefault(t *testing.T) {
	for _, args := range [][]string{{"help"}, {"--help"}, {"-h"}} {
		out, err := runHelp(t, args...)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"login", "status <room>", "checkout <room>", "start [--repository", "maku help admin", "maku help <command>"} {
			if !strings.Contains(out, want) {
				t.Errorf("%v lacks %q:\n%s", args, want, out)
			}
		}
		for _, admin := range []string{"db bootstrap", "preflight [--namespace", "install --print", "drill plant", "config migrate", "policy list"} {
			if strings.Contains(out, admin) {
				t.Errorf("%v shows the admin command %q", args, admin)
			}
		}
	}
}

func TestHelpAdmin(t *testing.T) {
	out, err := runHelp(t, "help", "admin")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"db bootstrap", "preflight", "install --print", "repo add", "repo check", "drill plant", "policy list"} {
		if !strings.Contains(out, want) {
			t.Errorf("admin lacks %q", want)
		}
	}
	if strings.Contains(out, "checkout <room>") {
		t.Error("admin shows an everyday command")
	}
}

// Every command of the full usage is in exactly one of the two.
func TestEveryCommandIsEverydayOrAdmin(t *testing.T) {
	for _, b := range usageBlocks(makuUsage) {
		e, a := strings.Contains(everydayUsage(), b), strings.Contains(adminUsage(), b)
		if e == a {
			t.Errorf("%q: everyday %v, admin %v", strings.SplitN(b, "\n", 2)[0], e, a)
		}
	}
}

func TestHelpForOneCommand(t *testing.T) {
	for args, want := range map[string]string{
		"help checkout":   "  checkout <room>",
		"help repo check": "  repo check <owner/name>",
		"help plan":       "  plan approve|split <task>",
	} {
		out, err := runHelp(t, strings.Fields(args)...)
		if err != nil || !strings.HasPrefix(out, want) {
			t.Errorf("%s: %v\n%s", args, err, out)
		}
	}
	out, _ := runHelp(t, "help", "repo")
	if !strings.Contains(out, "repo add") || !strings.Contains(out, "repo check") {
		t.Errorf("help repo shows both:\n%s", out)
	}
	// A command's own --help stays the command's: scripts probe a binary with it (hack/demo/lib.sh,
	// hack/install-e2e/upgrade.sh), and a flag error names that command's lines, not the whole wall.
	if _, err := runHelp(t, "repo", "check", "--bogus"); err == nil || !strings.Contains(err.Error(), "  repo check <owner/name>") || strings.Contains(err.Error(), "db bootstrap") {
		t.Errorf("a flag error: %v", err)
	}
	var ee *maku.ExitError
	if _, err := runHelp(t, "help", "nope"); !errors.As(err, &ee) || ee.Code != 2 {
		t.Errorf("an unknown command: %v", err)
	}
}

// A mistyped command answers with the everyday help, not the whole wall.
func TestAnUnknownCommandShowsTheEverydayHelp(t *testing.T) {
	_, err := runHelp(t, "chekout")
	if err == nil || strings.Contains(err.Error(), "db bootstrap") || !strings.Contains(err.Error(), "maku help admin") {
		t.Errorf("%v", err)
	}
}
