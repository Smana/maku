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

// A transient LoadX509KeyPair failure must not permanently suppress reloading, even if the repaired
// files end up with the exact same mtime and size as the failed read.
func TestReloadRecoversAfterTransientFailureWithSameStamp(t *testing.T) {
	dir := t.TempDir()
	t0 := time.Now().Truncate(time.Second).Add(-2 * time.Minute)
	tlsreloadtest.WritePair(t, dir, 1, t0)

	now := t0
	kp, err := newKeyPair(filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key"), nil, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}

	// Prepare a valid renewed pair in a staging dir so we know its exact size.
	stage := t.TempDir()
	t1 := t0.Add(time.Minute)
	tlsreloadtest.WritePair(t, stage, 2, t1)
	goodCert, err := os.ReadFile(filepath.Join(stage, "tls.crt"))
	if err != nil {
		t.Fatal(err)
	}
	goodKey, err := os.ReadFile(filepath.Join(stage, "tls.key"))
	if err != nil {
		t.Fatal(err)
	}

	// Write a corrupted key of the exact same size and mtime as goodKey.
	badKey := make([]byte, len(goodKey))
	for i := range badKey {
		badKey[i] = 'x'
	}
	certPath := filepath.Join(dir, "tls.crt")
	keyPath := filepath.Join(dir, "tls.key")
	if err := os.WriteFile(certPath, goodCert, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, badKey, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(certPath, t1, t1); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(keyPath, t1, t1); err != nil {
		t.Fatal(err)
	}

	// First handshake fails to load and keeps serial 1.
	if c, err := kp.get(nil); err != nil || c.Leaf.SerialNumber.Int64() != 1 {
		t.Fatalf("during broken renewal: %v, %v", c, err)
	}

	// Fix the key in place with the exact same mtime and size.
	if err := os.WriteFile(keyPath, goodKey, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(keyPath, t1, t1); err != nil {
		t.Fatal(err)
	}

	// Within retryBackoff, the failed stamp is not re-read yet (no hot-loop).
	if c, err := kp.get(nil); err != nil || c.Leaf.SerialNumber.Int64() != 1 {
		t.Fatalf("within backoff: %v, %v", c, err)
	}

	// Once retryBackoff elapses, the same stamp is retried and serial 2 loads cleanly.
	now = now.Add(retryBackoff)
	if c, err := kp.get(nil); err != nil || c.Leaf.SerialNumber.Int64() != 2 {
		t.Fatalf("after backoff: got serial %v, err %v; want 2", c, err)
	}
}
