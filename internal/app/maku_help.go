// SPDX-License-Identifier: Apache-2.0

package app

import (
	"fmt"
	"slices"
	"strings"

	"github.com/Smana/maku/internal/maku"
)

// adminCommands are the commands a platform team runs to install, operate and test Maku: `maku help
// admin` lists them, and `maku help` leaves them out (developer review gap 10). Every other command
// of makuUsage is everyday.
var adminCommands = []string{"install", "db", "preflight", "repo", "demo", "config", "policy", "drill", "fork", "run",
	"stages", "workflow", "routine"}

const (
	usageHead = "usage: maku <command>"
	helpFoot  = "Run maku help <command> for one command, and maku help admin for the platform team's commands."
)

// usageBlocks are makuUsage's command entries, each with its continuation lines: a block starts at a
// line indented by two spaces exactly.
func usageBlocks(usage string) []string {
	var blocks []string
	for _, line := range strings.Split(usage, "\n") {
		indent := len(line) - len(strings.TrimLeft(line, " "))
		switch {
		case indent == 2:
			blocks = append(blocks, line)
		case indent > 2 && len(blocks) > 0:
			blocks[len(blocks)-1] += "\n" + line
		}
	}
	return blocks
}

// commandOf is a block's command, its first word.
func commandOf(block string) string { return strings.Fields(block)[0] }

func blocksWhere(keep func(cmd string) bool) string {
	var out []string
	for _, b := range usageBlocks(makuUsage) {
		if keep(commandOf(b)) {
			out = append(out, b)
		}
	}
	return strings.Join(out, "\n")
}

// everydayUsage is `maku help`: the commands a developer uses, and where the rest are.
func everydayUsage() string {
	lines := strings.Split(strings.TrimRight(makuUsage, "\n"), "\n")
	return usageHead + "\n" + blocksWhere(func(c string) bool { return !slices.Contains(adminCommands, c) }) + "\n" +
		lines[len(lines)-1] + "\n" + helpFoot
}

// adminUsage is `maku help admin`.
func adminUsage() string {
	return "usage: maku <command>, for the platform team\n" + blocksWhere(func(c string) bool { return slices.Contains(adminCommands, c) })
}

// commandHelp is the usage of the command words name: every block whose synopsis starts with them
// ("repo" gives repo add and repo check), false when none does.
func commandHelp(name []string) (string, bool) {
	var out []string
	for _, b := range usageBlocks(makuUsage) {
		words := strings.Fields(b)
		match := len(words) >= len(name)
		for i := 0; match && i < len(name); i++ {
			match = words[i] == name[i] || slices.Contains(strings.Split(words[i], "|"), name[i])
		}
		if match {
			out = append(out, b)
		}
	}
	return strings.Join(out, "\n"), len(out) > 0
}

// help answers `maku help [admin | <command>...]`.
func (r Maku) help(args []string) error {
	text := everydayUsage()
	switch {
	case len(args) == 1 && args[0] == "admin":
		text = adminUsage()
	case len(args) > 0:
		h, ok := commandHelp(args)
		if !ok {
			return &maku.ExitError{Code: 2, Err: fmt.Errorf("no command %q\n%s", strings.Join(args, " "), everydayUsage())}
		}
		text = h
	}
	_, err := fmt.Fprintln(r.Out, text)
	return err
}
