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
	"time"
)

// Config serves the pair at certFile and keyFile, TLS 1.3 minimum. Each handshake compares both
// files' size and modification time with the last seen, and re-reads the pair once either
// changes. A pair that fails to load, such as one caught mid-rotation, is logged and the last
// good one stays in service. It refuses to start without a loadable pair. A nil log discards.
func Config(certFile, keyFile string, log *slog.Logger) (*tls.Config, error) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	kp := &keyPair{certFile: filepath.Clean(certFile), keyFile: filepath.Clean(keyFile), log: log}
	stamp, err := kp.stat()
	if err != nil {
		return nil, fmt.Errorf("tlsreload: the key pair: %w", err)
	}
	cert, err := tls.LoadX509KeyPair(kp.certFile, kp.keyFile)
	if err != nil {
		return nil, fmt.Errorf("tlsreload: the key pair: %w", err)
	}
	kp.cert, kp.seen = &cert, stamp
	return &tls.Config{MinVersion: tls.VersionTLS13, GetCertificate: kp.get}, nil
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

	mu   sync.Mutex
	cert *tls.Certificate
	seen [2]fileStamp
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
	k.mu.Lock()
	defer k.mu.Unlock()
	if err != nil || stamp == k.seen {
		return k.cert, nil // a file missing mid-rotation: keep the last good pair
	}
	// Remembered even when the load fails: the next attempt waits for the next change instead
	// of re-reading a broken pair on every handshake.
	k.seen = stamp
	cert, err := tls.LoadX509KeyPair(k.certFile, k.keyFile)
	if err != nil {
		k.log.Error("the renewed key pair does not load; serving the previous one", "cert", k.certFile, "err", err)
		return k.cert, nil
	}
	k.cert = &cert
	k.log.Info("reloaded the key pair", "cert", k.certFile, "notAfter", cert.Leaf.NotAfter)
	return k.cert, nil
}
