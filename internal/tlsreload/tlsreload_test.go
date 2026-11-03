// SPDX-License-Identifier: Apache-2.0

package tlsreload

import (
	"crypto/tls"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Smana/maku/internal/tlsreload/tlsreloadtest"
)

// D7: TLS 1.3 minimum, the pair from the files.
func TestConfigServesThePairOverTLS13(t *testing.T) {
	dir := t.TempDir()
	tlsreloadtest.WritePair(t, dir, 1, time.Now())
	cfg, err := Config(filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MinVersion != tls.VersionTLS13 {
		t.Fatalf("MinVersion %x, want TLS 1.3", cfg.MinVersion)
	}
	if c, err := cfg.GetCertificate(nil); err != nil || c.Leaf.SerialNumber.Int64() != 1 {
		t.Fatalf("got %v, %v", c, err)
	}
}

// GP-18: cert-manager renews the pair in place; the next handshake serves it, with no restart.
func TestARenewedPairIsServed(t *testing.T) {
	dir := t.TempDir()
	tlsreloadtest.WritePair(t, dir, 1, time.Now().Add(-time.Minute))
	cfg, err := Config(filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key"), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	tlsreloadtest.WritePair(t, dir, 2, time.Now())
	if c, err := cfg.GetCertificate(nil); err != nil || c.Leaf.SerialNumber.Int64() != 2 {
		t.Fatalf("after rotation: %v, %v", c, err)
	}
}

// A pair that fails to load mid-rotation never stops the listener: the last good one stays.
func TestABrokenRenewalKeepsTheLastGoodPair(t *testing.T) {
	dir := t.TempDir()
	tlsreloadtest.WritePair(t, dir, 1, time.Now().Add(-time.Minute))
	cfg, err := Config(filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key"), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tls.key"), []byte("half-written"), 0o600); err != nil {
		t.Fatal(err)
	}
	if c, err := cfg.GetCertificate(nil); err != nil || c == nil || c.Leaf.SerialNumber.Int64() != 1 {
		t.Fatalf("got %v, %v", c, err)
	}
}

func TestConfigRefusesAMissingPair(t *testing.T) {
	dir := t.TempDir()
	if _, err := Config(filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key"), nil); err == nil {
		t.Fatal("no pair, no error")
	}
}
