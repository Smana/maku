// SPDX-License-Identifier: Apache-2.0

package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Smana/maku/internal/httpx"
	"github.com/Smana/maku/internal/wire"
)

const (
	// defaultShotEvery is how often the relay asks the gate for new screenshots.
	defaultShotEvery = 2 * time.Second
	// gateTimeout bounds one call to the browser gate, on loopback.
	gateTimeout = 10 * time.Second
	// maxShotList bounds the gate's list: 25 shots of a few hundred bytes each.
	maxShotList = 64 << 10
	// maxShotBytes is the largest image relayed, the broker's cap (store.MaxScreenshot): the gate
	// keeps at most 512 KiB, so a larger one is not the gate's and is dropped.
	maxShotBytes = 1 << 20
)

// Shot is a screenshot the browser gate kept, as GET {gate}/shots lists it.
type Shot struct {
	N       int       `json:"n"`
	Mime    string    `json:"mime"`
	Bytes   int       `json:"bytes"`
	Width   int       `json:"width"`
	Height  int       `json:"height"`
	URL     string    `json:"url"`
	TakenAt time.Time `json:"takenAt"`
	Vision  bool      `json:"vision"`
}

// UploadScreenshot posts img, shot n of run, to the broker's screenshot route (harness screenshots
// phase B). The broker re-encodes it; the query carries only what it cannot read from the image.
// A refusal is a Reply, not an error: 200 and 201 are the broker keeping it.
func (b *Broker) UploadScreenshot(ctx context.Context, run string, s Shot, img []byte) (Reply, error) {
	q := url.Values{"run": {run}, "n": {strconv.Itoa(s.N)}, "vision": {"0"}}
	if s.Vision {
		q.Set("vision", "1")
	}
	if s.URL != "" {
		q.Set("url", s.URL)
	}
	path := "/v1/bridge/screenshots?" + q.Encode()
	req, err := b.request(ctx, http.MethodPost, path, img)
	if err != nil {
		return Reply{}, err
	}
	req.Header.Set("Content-Type", s.Mime)
	resp, err := b.hc.Do(req)
	if err != nil {
		return Reply{}, fmt.Errorf("broker POST /v1/bridge/screenshots: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	rep := Reply{Code: resp.StatusCode, RetryAfter: resp.Header.Get("Retry-After")}
	raw, err := httpx.ReadBody(resp.Body, maxReplyBytes)
	if err != nil {
		return rep, fmt.Errorf("broker POST /v1/bridge/screenshots: %w", err)
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		var e wire.Error
		if json.Unmarshal(raw, &e) == nil {
			rep.Reason = e.Reason
		}
	}
	return rep, nil
}

// shotUploader is the broker side of the relay; *Broker implements it.
type shotUploader interface {
	UploadScreenshot(ctx context.Context, run string, s Shot, img []byte) (Reply, error)
}

// ValidGateURL refuses a gate URL that is not http on a loopback host: the browser gate shares the
// pod's network namespace, and the relay never fetches an image from anywhere else.
func ValidGateURL(gate string) error {
	u, err := url.Parse(gate)
	if err != nil || u.Scheme != "http" || u.User != nil || u.Opaque != "" || u.Host == "" {
		return fmt.Errorf("bridge: BROWSER_GATE_URL %q must be http://<loopback>:<port>", gate)
	}
	if h := u.Hostname(); h != "localhost" {
		if ip := net.ParseIP(h); ip == nil || !ip.IsLoopback() {
			return fmt.Errorf("bridge: BROWSER_GATE_URL %q must be on loopback", gate)
		}
	}
	return nil
}

// ShotRelay copies the screenshots the browser gate kept into the run's room (C12): the bridge's
// run token, which never enters the harness container, is the only credential that uploads one.
// It polls the gate every Every, uploads each new shot once and in order, retries a retryable
// refusal after a backoff, and drops a permanent one with one log line.
type ShotRelay struct {
	Gate   string
	Broker shotUploader
	RunID  string
	// Every is the poll period; zero means 2 s.
	Every time.Duration
	// MinBackoff and MaxBackoff bound the retry of a failed poll or upload; zero means 250 ms and
	// 30 s.
	MinBackoff, MaxBackoff time.Duration
	// Client calls the gate; nil means an httpx client.
	Client *http.Client
	Logger *slog.Logger

	once  sync.Once
	turn  chan struct{} // one pass at a time: the loop's and a Flush's
	last  int           // the highest n uploaded or dropped
	retry backoff
	done  string // why the relay stopped for good, "" while it runs
}

func (r *ShotRelay) init() {
	r.once.Do(func() {
		r.turn = make(chan struct{}, 1)
		if r.Every <= 0 {
			r.Every = defaultShotEvery
		}
		if r.Client == nil {
			r.Client = httpx.New(gateTimeout, nil)
		}
		if r.Logger == nil {
			r.Logger = slog.New(slog.DiscardHandler)
		}
		r.Gate = strings.TrimRight(r.Gate, "/")
	})
}

func (r *ShotRelay) backoffs() (lo, hi time.Duration) {
	lo, hi = r.MinBackoff, r.MaxBackoff
	if lo <= 0 {
		lo = defaultMinBackoff
	}
	if hi <= 0 {
		hi = defaultMaxBackoff
	}
	return lo, max(lo, hi)
}

// Run relays until ctx ends. It fails at once on a gate URL that is not loopback.
func (r *ShotRelay) Run(ctx context.Context) error {
	if err := ValidGateURL(r.Gate); err != nil {
		return err
	}
	r.init()
	for {
		wait, _ := r.pass(ctx)
		if pause(ctx, wait) != nil {
			return nil
		}
	}
}

// Flush uploads what the gate holds now, at the end of the run, retrying a retryable failure at
// most every drainWait until ctx ends. It returns ctx's error if it ended first.
func (r *ShotRelay) Flush(ctx context.Context) error {
	if err := ValidGateURL(r.Gate); err != nil {
		return err
	}
	r.init()
	for {
		wait, again := r.pass(ctx)
		if !again {
			return nil
		}
		if err := pause(ctx, min(wait, drainWait)); err != nil {
			return err
		}
	}
}

// pass lists the gate's new shots and uploads them in order. again is a retryable failure; wait is
// how long to wait before the next pass.
func (r *ShotRelay) pass(ctx context.Context) (wait time.Duration, again bool) {
	select {
	case r.turn <- struct{}{}:
	case <-ctx.Done():
		return 0, false
	}
	defer func() { <-r.turn }()
	if r.done != "" {
		return r.Every, false
	}
	shots, err := r.list(ctx)
	if err != nil {
		if ctx.Err() == nil {
			r.Logger.Debug("the browser gate did not list its screenshots", "err", err)
		}
		return r.retry.next(r.backoffs()), ctx.Err() == nil
	}
	for _, s := range shots {
		if s.N <= r.last {
			continue
		}
		rep, err := r.relay(ctx, s)
		switch verdict := judge(rep, err); verdict {
		case shotKept:
			r.last = s.N
		case shotDropped:
			r.Logger.Warn("screenshot dropped: the broker refuses it", "n", s.N, "code", rep.Code, "reason", rep.Reason, "err", errString(err))
			r.last = s.N
		case shotStop:
			r.done = rep.Reason
			r.Logger.Warn("the broker takes no more screenshots for this run", "code", rep.Code, "reason", rep.Reason)
			return r.Every, false
		default:
			if ctx.Err() != nil {
				return 0, false
			}
			r.Logger.Debug("screenshot not relayed; retrying", "n", s.N, "code", rep.Code, "reason", rep.Reason, "err", errString(err))
			return retryDelay(rep.RetryAfter, time.Now(), r.retry.next(r.backoffs())), true
		}
	}
	r.retry.reset()
	return r.Every, false
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// errGone is an image the gate no longer serves, or one that is not the gate's: never retried.
var errGone = errors.New("the gate does not serve that screenshot")

// relay fetches shot s's bytes from the gate and uploads them.
func (r *ShotRelay) relay(ctx context.Context, s Shot) (Reply, error) {
	img, err := r.get(ctx, "/shots/"+strconv.Itoa(s.N), maxShotBytes)
	if err != nil {
		return Reply{}, err
	}
	if s.Mime == "" {
		s.Mime = "application/octet-stream"
	}
	return r.Broker.UploadScreenshot(ctx, r.RunID, s, img)
}

func (r *ShotRelay) list(ctx context.Context) ([]Shot, error) {
	raw, err := r.get(ctx, "/shots?after="+strconv.Itoa(r.last), maxShotList)
	if err != nil {
		return nil, err
	}
	var body struct {
		Shots []Shot `json:"shots"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, fmt.Errorf("the gate's screenshot list: %w", err)
	}
	slices.SortFunc(body.Shots, func(a, b Shot) int { return a.N - b.N })
	return body.Shots, nil
}

// get reads path from the gate, at most limit bytes. A 404 or an oversize body is errGone.
func (r *ShotRelay) get(ctx context.Context, path string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.Gate+path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := r.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gate GET %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("gate GET %s: %w", path, errGone)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("gate GET %s: status %d", path, resp.StatusCode)
	}
	b, err := httpx.ReadBody(resp.Body, limit)
	if errors.Is(err, httpx.ErrBodyTooLarge) {
		return nil, fmt.Errorf("gate GET %s: %w: over %d bytes", path, errGone, limit)
	}
	return b, err
}

// What the relay does with one shot after an upload.
type shotVerdict int

const (
	shotRetry   shotVerdict = iota // a failure that may pass: the network, the gate, the broker's 5xx, 429, 401
	shotKept                       // 200 or 201
	shotDropped                    // a refusal of this image no retry changes
	shotStop                       // a refusal of every image: a sealed room, screenshots off, no route
)

func judge(rep Reply, err error) shotVerdict {
	switch {
	case errors.Is(err, errGone):
		return shotDropped
	case err != nil:
		return shotRetry
	}
	switch rep.Code {
	case http.StatusOK, http.StatusCreated:
		return shotKept
	case http.StatusBadRequest, http.StatusRequestEntityTooLarge, http.StatusUnprocessableEntity:
		return shotDropped
	case http.StatusConflict:
		if rep.Reason == wire.ReasonLeaseLost {
			return shotRetry // the bridge takes the lease back with hello
		}
		return shotDropped
	case http.StatusForbidden:
		if rep.Reason == wire.ReasonRunNotLive {
			return shotRetry // the broker's run watch may not have seen the run yet
		}
		return shotStop
	case http.StatusGone, http.StatusNotImplemented:
		return shotStop
	case http.StatusNotFound:
		if rep.Reason == wire.ReasonNoRoom {
			return shotRetry
		}
		return shotStop // a broker without the route
	}
	return shotRetry
}
