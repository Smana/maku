// SPDX-License-Identifier: Apache-2.0

// Command maku-run-controller reconciles maku.ogenki.io AgentRuns into gVisor pods (adoption 3a),
// on clusters without cloud-native-ref's Crossplane runtime. Its wiring is app.RunController.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/Smana/maku/internal/app"
	"github.com/Smana/maku/internal/logging"
)

func main() { os.Exit(run()) }

func run() int {
	log, err := logging.New(os.Stdout, os.Getenv("LOG_FORMAT"), os.Getenv("LOG_LEVEL"))
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "maku-run-controller:", err)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	// No argument runs the controller; the same image serves the kubernetes network mode's egress proxy.
	cmd, name := app.RunController, "maku-run-controller"
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "egress-proxy":
			cmd, name = app.EgressProxy, "egress-proxy"
		case "netprobe": // the preflight's probe pod: no logger, its result goes to the termination log
			return app.Netprobe(ctx, os.Args[2:], os.Stderr)
		case "preflight": // the install's preflight (v4 D24): the JSON lines to stdout, the summary to the termination log
			return app.Preflight(ctx, os.Args[2:], os.Stdout, os.Stderr)
		default:
			_, _ = fmt.Fprintf(os.Stderr, "maku-run-controller: unknown subcommand %q (egress-proxy, netprobe, preflight)\n", os.Args[1])
			return 2
		}
	}
	if err := cmd(ctx, log, os.Getenv); err != nil {
		log.Error(name+" stopped", "err", err)
		return 1
	}
	return 0
}
