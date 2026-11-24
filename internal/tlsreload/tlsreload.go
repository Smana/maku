// SPDX-License-Identifier: Apache-2.0

// Package tlsreload serves a key pair from two mounted files and re-reads it when either
// changes, so a cert-manager renewal needs no restart (GP-18). The broker's :8443 and the
// factory's run-request API take their server config from Config (D7). It knows no path, port or
// issuer: callers pass the files.
package tlsreload

import (
	"crypto/tls"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

// retryBackoff bounds how often a failing pair with the same file stamp is re-read, so a broken
// renewal does not hot-loop disk reads while still recovering if the file is fixed in place.
const retryBackoff = time.Second

// Config serves the pair at certFile and keyFile, TLS 1.3 minimum. Each handshake compares both
// files' size and modification time with the last seen, and re-reads the pair once either
// changes. A pair that fails to load, such as one caught mid-rotation, is logged and the last
// good one stays in service. It refuses to start without a loadable pair. A nil log discards.
func Config(certFile, keyFile string, log *slog.Logger) (*tls.Config, error) {
	kp, err := newKeyPair(certFile, keyFile, log, time.Now)
	if err != nil {
		return nil, err
	}
	return &tls.Config{MinVersion: tls.VersionTLS13, GetCertificate: kp.get}, nil
}

func newKeyPair(certFile, keyFile string, log *slog.Logger, now func() time.Time) (*keyPair, error) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	if now == nil {
		now = time.Now
	}
	kp := &keyPair{certFile: filepath.Clean(certFile), keyFile: filepath.Clean(keyFile), log: log, now: now}
	stamp, err := kp.stat()
	if err != nil {
		return nil, fmt.Errorf("tlsreload: the key pair: %w", err)
	}
	cert, err := tls.LoadX509KeyPair(kp.certFile, kp.keyFile)
	if err != nil {
		return nil, fmt.Errorf("tlsreload: the key pair: %w", err)
	}
	kp.cert.Store(&cert)
	kp.seen.Store(&stamp)
	return kp, nil
}

// fileStamp is what a rewrite of a file changes. A projected Secret volume swaps a symlink;
// os.Stat follows it to the new file.
type fileStamp struct {
	mod  time.Time
	size int64
}

type keyPair struct {
	certFile, keyFile string
	log               *slog.Logger
	now               func() time.Time

	cert atomic.Pointer[tls.Certificate]
	seen atomic.Pointer[[2]fileStamp]

	mu       sync.Mutex
	failedAt time.Time
	failedSt [2]fileStamp
}

func (k *keyPair) stat() ([2]fileStamp, error) {
	var out [2]fileStamp
	for i, f := range []string{k.certFile, k.keyFile} {
		fi, err := os.Stat(f)
		if err != nil {
			return out, err
		}
		out[i] = fileStamp{mod: fi.ModTime(), size: fi.Size()}
	}
	return out, nil
}

func (k *keyPair) get(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	stamp, err := k.stat()
	if err != nil || stamp == *k.seen.Load() {
		return k.cert.Load(), nil // unchanged or missing mid-rotation: keep the last good pair
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if stamp == *k.seen.Load() {
		return k.cert.Load(), nil
	}
	now := k.now()
	if stamp == k.failedSt && now.Sub(k.failedAt) < retryBackoff {
		return k.cert.Load(), nil
	}
	cert, err := tls.LoadX509KeyPair(k.certFile, k.keyFile)
	if err != nil {
		k.failedSt, k.failedAt = stamp, now
		k.log.Error("the renewed key pair does not load; serving the previous one", "cert", k.certFile, "err", err)
		return k.cert.Load(), nil
	}
	k.seen.Store(&stamp)
	k.failedSt, k.failedAt = [2]fileStamp{}, time.Time{}
	k.cert.Store(&cert)
	k.log.Info("reloaded the key pair", "cert", k.certFile, "notAfter", cert.Leaf.NotAfter)
	return &cert, nil
}
