// SPDX-License-Identifier: Apache-2.0

// Command loadconfig loads each file through a component's real config loader:
// `loadconfig broker|broker-roomtools-on|broker-roomtools-off|broker-roomtools-tokenreview|broker-slackintents|run-controller FILE...`. render.sh feeds
// it the chart's rendered config.yaml, so a template edit the component would refuse — or a roomTools
// switch that disagrees with the values — fails chart:check, not a rollout.
package main

import (
	"fmt"
	"os"

	"github.com/Smana/maku/internal/config"
	"github.com/Smana/maku/internal/runctl"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: loadconfig broker|broker-roomtools-on|broker-roomtools-off|broker-roomtools-tokenreview|broker-slackintents|run-controller FILE...")
		os.Exit(2)
	}
	var load func(string) error
	switch os.Args[1] {
	case "broker":
		load = func(p string) error { _, err := config.Load(p); return err }
	case "broker-roomtools-on", "broker-roomtools-off":
		// The :8090 listener is the config's switch (I8): the rendered block must decode to what the
		// values say, not merely load.
		want := os.Args[1] == "broker-roomtools-on"
		load = func(p string) error {
			c, err := config.Load(p)
			if err != nil {
				return err
			}
			if got := c.RoomToolsEnabled(); got != want {
				return fmt.Errorf("roomTools enabled = %v, want %v", got, want)
			}
			return nil
		}
	case "broker-roomtools-tokenreview":
		// Task 6: :8090 on, verifying the runs' own tokens.
		load = func(p string) error {
			c, err := config.Load(p)
			if err != nil {
				return err
			}
			if !c.RoomToolsEnabled() || c.RoomToolsAuth() != config.RoomToolsAuthTokenReview {
				return fmt.Errorf("roomTools enabled %v auth %q, want true and tokenreview", c.RoomToolsEnabled(), c.RoomToolsAuth())
			}
			return nil
		}
	case "broker-slackintents":
		// human.slackIntents is the classifier's switch: the rendered block must decode to an on switch.
		load = func(p string) error {
			c, err := config.Load(p)
			if err != nil {
				return err
			}
			if _, on := c.SlackIntents(); !on {
				return fmt.Errorf("slackIntents is off, want on")
			}
			return nil
		}
	case "run-controller":
		load = func(p string) error { _, err := runctl.LoadConfig(p); return err }
	case "run-controller-gateway":
		// v4 step 4: runController.model.mode gateway must decode to the gateway mode, with no model Secret.
		load = func(p string) error {
			c, err := runctl.LoadConfig(p)
			if err != nil {
				return err
			}
			if !c.Gateway() || c.ModelSecret != "" {
				return fmt.Errorf("model.mode %q modelSecret %q, want gateway and none", c.Model.Mode, c.ModelSecret)
			}
			return nil
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown component %q\n", os.Args[1])
		os.Exit(2)
	}
	failed := false
	for _, p := range os.Args[2:] {
		if err := load(p); err != nil {
			fmt.Fprintf(os.Stderr, "FAIL: %s config %s: %v\n", os.Args[1], p, err)
			failed = true
		}
	}
	if failed {
		os.Exit(1)
	}
}
