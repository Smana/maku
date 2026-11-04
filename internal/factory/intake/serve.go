// SPDX-License-Identifier: Apache-2.0

package intake

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"
)

// serve serves srv on ln until ctx ends, then drains for at most drain, in the broker's shape
// (internal/app's serveHTTP), and returns only after Shutdown has. It copies
// internal/factory/api's serve rather than importing it: intake must not depend on the API
// package (audit A-1).
func serve(ctx context.Context, srv *http.Server, ln net.Listener, drain time.Duration) error {
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()
	select {
	case err := <-served:
		return fmt.Errorf("runlore: serve %s: %w", ln.Addr(), err)
	case <-ctx.Done():
	}
	dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), drain)
	defer cancel()
	shut := srv.Shutdown(dctx)
	if err := <-served; !errors.Is(err, http.ErrServerClosed) {
		shut = errors.Join(shut, err)
	}
	if shut != nil {
		return fmt.Errorf("runlore: shut down %s: %w", ln.Addr(), shut)
	}
	return nil
}
