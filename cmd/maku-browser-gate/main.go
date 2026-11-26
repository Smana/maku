// SPDX-License-Identifier: Apache-2.0

// Command maku-browser-gate is the loopback MCP gate in front of a run's Playwright MCP server, in
// the maku-browser sidecar: the tool allow-list, the loopback URL check, the screenshot and image
// caps, and the screenshot keep the room-bridge relays. Its wiring is app.RunBrowserGate.
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
		_, _ = fmt.Fprintln(os.Stderr, "maku-browser-gate:", err)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	if err := app.RunBrowserGate(ctx, log, os.Args[1:], os.Getenv); err != nil {
		log.Error("maku-browser-gate stopped", "err", err)
		return 1
	}
	return 0
}
