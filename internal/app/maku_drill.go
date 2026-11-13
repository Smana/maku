// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"time"

	"github.com/Smana/maku/internal/maku"
)

// drill is `maku drill plant` (hardening HR38): it needs no maku config, only its flags and token
// files, so it runs from a CronJob.
func (r Maku) drill(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] != "plant" {
		return fmt.Errorf("drill plant\n%s", makuUsage)
	}
	fs := flag.NewFlagSet("drill plant", flag.ContinueOnError)
	d := maku.DrillPlant{Hooks: map[string]string{}, Out: r.Out}
	fs.StringVar(&d.Repo, "repo", "", "the drill repository, owner/name")
	fs.StringVar(&d.Factory, "factory", "", "the factory API's base URL")
	fs.StringVar(&d.StrangerTokenFile, "stranger-token-file", "", "the drill-stranger account's GitHub token")
	fs.StringVar(&d.MaintainerTokenFile, "maintainer-token-file", "", "a maintainer's GitHub token")
	fs.StringVar(&d.FactoryTokenFile, "factory-token-file", "", "the token the factory API maps to system:drill")
	fs.StringVar(&d.GitHubAPI, "github-api", "https://api.github.com", "the GitHub API base URL")
	fs.StringVar(&d.TriggerLabel, "trigger-label", "factory/ready", "the drill repository's trigger label")
	fs.StringVar(&d.ID, "id", "", "the drill's id (default drill-<UTC time>)")
	fs.DurationVar(&d.Wait, "wait", 15*time.Minute, "how long to wait for the factory to make the planted tasks")
	for _, h := range []string{"runlore", "tool_result", "room"} {
		h := h
		fs.Func(map[string]string{"runlore": "runlore-hook", "tool_result": "tool-result-hook", "room": "room-hook"}[h],
			"the executable that plants the "+h+" scenario and prints its task id", func(v string) error { d.Hooks[h] = v; return nil })
	}
	pos, err := flags(fs, args[1:])
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return errors.New("drill plant: nothing is positional")
	}
	return d.Run(ctx)
}
