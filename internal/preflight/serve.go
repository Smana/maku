// SPDX-License-Identifier: Apache-2.0

package preflight

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// The component side's bounds (IN3): one check runs at most CheckTimeout, all of them at most
// AllTimeout, and an answer is served from cache for CacheFor.
const (
	CheckTimeout = 10 * time.Second
	AllTimeout   = 30 * time.Second
	CacheFor     = 30 * time.Second
)

// Source runs a component's checks once. Each result names a catalog check; the handler holds them
// to the catalog and refuses a message that carries a credential.
type Source func(ctx context.Context) []Result

// Bounded runs one check within timeout: a check that does not return in time is a fail undecided
// (it keeps running in the background until its own context ends). The result's Check is check's,
// and Seconds its duration.
func Bounded(ctx context.Context, timeout time.Duration, check string, run func(ctx context.Context) Result) Result {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	start := time.Now()
	done := make(chan Result, 1)
	go func() { done <- run(ctx) }()
	var r Result
	select {
	case r = <-done:
	case <-ctx.Done():
		r = Result{Outcome: Fail, Code: Undecided, Message: fmt.Sprintf("the check did not finish in %s", timeout)}
	}
	r.Check, r.Seconds = check, time.Since(start).Seconds()
	return r
}

// Handler serves GET /preflight on a component's ops port: its checks, run on demand under
// AllTimeout and cached CacheFor, so a scrape storm costs nothing. It is unauthenticated, like
// /metrics on the same port, so it carries nothing a metric would not: a result whose message holds
// a credential is replaced by a fail undecided naming the check alone.
func Handler(component, version string, src Source, now func() time.Time) http.Handler {
	if now == nil {
		now = time.Now
	}
	var (
		mu    sync.Mutex
		at    time.Time
		cache []byte
	)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock() // one round at a time: concurrent callers wait for it, then read its answer
		defer mu.Unlock()
		if cache == nil || now().Sub(at) >= CacheFor {
			ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), AllTimeout)
			results := src(ctx)
			cancel()
			var rep Report
			for _, res := range results {
				if err := rep.Add(res); err != nil {
					_ = rep.Add(Result{Check: res.Check, Outcome: Fail, Code: Undecided, Message: "the result was withheld: its message carried a credential"})
				}
			}
			body, err := json.Marshal(Component{Component: component, Version: version, Checks: rep.Results()})
			if err != nil {
				http.Error(w, "preflight: encode", http.StatusInternalServerError)
				return
			}
			cache, at = body, now()
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(cache)
	})
}
