# Audit T6: Factory API TLS Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close S-1: human ZITADEL bearer tokens stop crossing to the factory's run-request API (`:8082`) in plaintext. The factory serves TLS 1.3 from an operator-brought pair, the broker trusts a configured CA, and the broker refuses an `http://` `factoryURL` unless the operator opts out with `factoryInsecure: true`.

**Architecture:** The broker's reloading key-pair loader moves out of `internal/bridgeapi` into a new leaf package `internal/tlsreload` (D7), which the broker's wiring and the factory's `api.Server` both call. The factory config gains `api.tls: {certFile, keyFile}` (both or neither); set, `Server.listen` wraps its listener in `tls.NewListener`. The broker config gains `factoryCAFile` (read at every call into the run-request client's root pool) and `factoryInsecure`, and `Validate` enforces D6. Both charts expose it: the factory chart mounts the pair and renders a cert-manager `Certificate` in `broker.tls`'s shape; the maku chart mounts the CA, writes both keys, and refuses an `http://` `factoryURL` at render.

**Tech Stack:** Go 1.27.3 (`crypto/tls`, `crypto/x509`), stdlib `testing`, Helm 3 with cert-manager `cert-manager.io/v1`, `yq` v4 assertions in `render.sh`.

**Spec:** `docs/superpowers/specs/2026-11-01-audit-fixes-design.md` (theme T6, decisions D0, D6, D7, and the chart paragraph under T6); finding S-1 in `docs/superpowers/specs/2026-11-01-project-audit.md`.

**Branch:** `feat/audit-t6-factory-tls`, one PR. T7 waits on this PR.

## Spec deviations

| # | Spec says | Code shows | This plan |
|---|---|---|---|
| 1 | T6 has no "After" dependency | T5's F-7 fix rewrites `api.Server.Start` into `listen` + `serve` (`docs/superpowers/plans/2026-11-01-audit-t5-shutdown-plan.md`, Task 1); this theme's `Start` change touches the same lines | Task 3 edits T5's `Server.listen`. Start this branch from `main` after T5 merges, or rebase onto it before Task 3; Tasks 1, 2 and 4-7 do not depend on it |
| 2 | "Start serves `ListenAndServeTLS`" | After T5, `Start` serves on a listener it opened (`srv.Serve(ln)`), so `ListenAndServeTLS` would bypass the drain | `listen` returns `tls.NewListener(ln, cfg)`; `serve` is unchanged. `net/http` treats the `*tls.Conn` as it would under `ServeTLS`: handshake deadlines, `r.TLS`, a plain request answered 400 |
| 3 | The api test uses "an `httptest`-issued cert" | `tlsreload.Config` reads files; `httptest`'s built-in certificate is the same for every server, so it cannot stand for "another CA" in the runrequest test either | A test-only package, `internal/tlsreload/tlsreloadtest` (as `forge/forgetest`), writes a self-signed pair per test; the bridgeapi, tlsreload, api and runrequest tests share it |
| 4 | "The factory chart gains `api.tls: {secretName, issuerRef}` with `broker.tls`'s shape" | `broker.tls.secretName` defaults to `room-broker-tls` because `:8443` is TLS only; the factory's TLS is optional (D6) | `api.tls.secretName` defaults to `""` (plaintext, today's behaviour); the chart also gains `clusterDomain` (default `cluster.local`) for the Certificate's names, as the maku chart has |

## Global Constraints

- D0: one PR; each component one commit, its pinning test or `render.sh` assertion written and seen failing first; `task check` exits 0 on the PR, `chart:check` included.
- D6 verbatim: "TLS on the run-request API is optional, the operator brings the certificate, and the broker refuses an `http://` `factoryURL` unless `factoryInsecure: true`."
- D7 verbatim: "The reloading certificate loader moves to `internal/tlsreload` and both servers import it." The factory never imports `internal/bridgeapi`.
- TLS 1.3 minimum on every server this plan touches. Never `InsecureSkipVerify`, tests included: trust a generated pair through a root pool.
- Strict config: `Load` → defaults → `Validate`; a half-set `api.tls` and an `http://` `factoryURL` without `factoryInsecure` fail startup and name the key.
- Secrets read at use: `factoryCAFile` is read on every run request; the key pair is re-read when its files change. No secret on argv.
- `internal/httpx` is the only egress client; tests use `httpx.New(timeout, roots)`.
- Seams: `internal/tlsreload` holds no path, port, issuer or name: callers pass the files.
- New `.go` files start with `// SPDX-License-Identifier: Apache-2.0`; every new package has a `// Package …` contract comment and a row in `AGENTS.md`'s package map.
- Chart assertions use `yq` through `render.sh`'s `ykind` / `ynone` helpers.
- Commit messages: Conventional Commits in English, the repo's style, no `Co-Authored-By` and no "Generated with" line.

## File Structure

| File | Change | Responsibility |
|---|---|---|
| `internal/tlsreload/tlsreload.go` | Create | `Config(certFile, keyFile, log)`: TLS 1.3, `GetCertificate` re-reading the pair on change |
| `internal/tlsreload/tlsreload_test.go` | Create | The tests moved from bridgeapi, plus a rotation test |
| `internal/tlsreload/tlsreloadtest/pair.go` | Create | `WritePair`: a throwaway self-signed pair for tests |
| `internal/bridgeapi/tls.go`, `tls_test.go` | Modify | Lose the loader; keep the `:8443` server and its test |
| `internal/app/broker.go` | Modify :155, :225 | `tlsreload.Config`; the sponsored-task client takes `factoryCAFile` |
| `internal/factory/config/config.go` | Modify `API` (:640), `Validate` (:1358) | `api.tls`, both or neither |
| `internal/factory/api/server.go` | Modify `listen` (T5) | TLS when `api.tls` is set |
| `internal/config/config.go` | Modify `Config` (:225), `Validate` (:424) | `factoryCAFile`, `factoryInsecure`, D6 |
| `internal/runrequest/runrequest.go`, `tasks.go` | Modify | `Factory.CAFile`, read at use |
| `internal/app/rooms.go` | Modify `runRequester` (:93-100) | Passes the CA file |
| `charts/maku-factory/{values.yaml,templates/*,tests/render.sh,README.md}` | Modify / create `templates/certificate.yaml` | `api.tls`, `clusterDomain` |
| `charts/maku/{values.yaml,templates/*,tests/render.sh,README.md}` | Modify | `broker.factoryCA.secretName`, `broker.factoryInsecure` |
| `AGENTS.md`, `docs/{integration,operations,architecture,security,factory,roadmap}.md`, `internal/app/factory.go:79` | Modify | Package map, config keys, upgrade step, the roadmap row closed |

---

### Task 1: D7, the reloading key pair moves to `internal/tlsreload`

**Files:**
- Create: `internal/tlsreload/tlsreload.go`, `internal/tlsreload/tlsreload_test.go`, `internal/tlsreload/tlsreloadtest/pair.go`
- Modify: `internal/bridgeapi/tls.go:1-95`, `internal/bridgeapi/tls_test.go`, `internal/app/broker.go:155`, `AGENTS.md:170`

**Interfaces:**
- Consumes: nothing new.
- Produces:
  - `func tlsreload.Config(certFile, keyFile string, log *slog.Logger) (*tls.Config, error)` — `MinVersion: tls.VersionTLS13`, `GetCertificate` re-reads on a size or mtime change, keeps the last good pair on a failed load, refuses a missing pair; nil `log` discards.
  - `func tlsreloadtest.WritePair(t testing.TB, dir string, serial int64, mod time.Time) *x509.Certificate` — writes `dir/tls.crt`, `dir/tls.key`, self-signed (its own CA), valid for `127.0.0.1` and `localhost`, both files dated `mod`.
  - `bridgeapi.TLSConfig` is deleted.

- [ ] **Step 1: Write the failing tests**

Create `internal/tlsreload/tlsreload_test.go`:

```go
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
```

- [ ] **Step 2: Run them**

Run: `go test -race -count=1 ./internal/tlsreload/...`
Expected: FAIL: `no required module provides package …/internal/tlsreload/tlsreloadtest` / `undefined: Config`. The package does not exist yet.

- [ ] **Step 3: Write `tlsreloadtest`**

Create `internal/tlsreload/tlsreloadtest/pair.go`:

```go
// SPDX-License-Identifier: Apache-2.0

// Package tlsreloadtest writes throwaway key pairs for the tests of tlsreload and its callers.
// Test code only: nothing outside a _test.go file imports it.
package tlsreloadtest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// WritePair writes a self-signed pair for 127.0.0.1 and localhost to dir/tls.crt and
// dir/tls.key with the given serial, both files dated mod so a rotation is visible whatever the
// filesystem's timestamp granularity, and returns the certificate. It is its own CA: a test
// trusts it by adding it to a pool, or by naming dir/tls.crt as a CA file.
func WritePair(t testing.TB, dir string, serial int64, mod time.Time) *x509.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: "tlsreloadtest"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1)}, DNSNames: []string{"localhost"},
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true, IsCA: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	for name, block := range map[string]*pem.Block{
		"tls.crt": {Type: "CERTIFICATE", Bytes: der},
		"tls.key": {Type: "EC PRIVATE KEY", Bytes: keyDER},
	} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, pem.EncodeToMemory(block), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, mod, mod); err != nil {
			t.Fatal(err)
		}
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}
```

- [ ] **Step 4: Write `tlsreload`**

Create `internal/tlsreload/tlsreload.go` (the body is `bridgeapi.TLSConfig`'s, moved, with its messages no longer naming `:8443`):

```go
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
```

- [ ] **Step 5: Run them**

Run: `go test -race -count=1 ./internal/tlsreload/...`
Expected: PASS.

- [ ] **Step 6: bridgeapi and the broker use it**

In `internal/bridgeapi/tls.go`: delete everything from `// TLSConfig serves the pair at certFile and keyFile (GP-18: cert-manager's` through the closing brace of `func (k *keyPair) get`; replace the import block with:

```go
import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)
```

and change the comment line `// ListenAndServeTLS serves the API on addr with cfg (TLSConfig's) until ctx ends,` to `// ListenAndServeTLS serves the API on addr with cfg (tlsreload.Config's) until ctx ends,`.

In `internal/bridgeapi/tls_test.go`: delete `writePair`, `TestABrokenRenewalKeepsTheLastGoodPair` and `TestTLSConfigRefusesAMissingPair` (both now live in tlsreload); replace the import block with:

```go
import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Smana/maku/internal/httpx"
	"github.com/Smana/maku/internal/tlsreload"
	"github.com/Smana/maku/internal/tlsreload/tlsreloadtest"
)
```

and in `TestPlainHTTPIsRefused` make three replacements:

```go
	first := tlsreloadtest.WritePair(t, dir, 1, time.Now().Add(-time.Minute))
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg, err := tlsreload.Config(filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key"), quiet)
```

```go
	second := tlsreloadtest.WritePair(t, dir, 2, time.Now())
```

In `internal/app/broker.go:155` replace `tlsCfg, err := bridgeapi.TLSConfig(cfg.TLS.CertFile, cfg.TLS.KeyFile, log)` with:

```go
	tlsCfg, err := tlsreload.Config(cfg.TLS.CertFile, cfg.TLS.KeyFile, log)
```

and add `"github.com/Smana/maku/internal/tlsreload"` to its imports.

In `AGENTS.md`, under the `internal/bridgeapi` row (line 170), add:

```markdown
| API | `internal/tlsreload` | a server key pair from two mounted files, TLS 1.3 minimum, re-read when cert-manager renews it: `:8443` and the factory's run-request API serve it (D7); `tlsreload/tlsreloadtest`, its test-only pair writer |
```

- [ ] **Step 7: Run the callers**

Run: `go build ./... && go test -race -count=1 ./internal/tlsreload/... ./internal/bridgeapi ./internal/app && git grep -n 'bridgeapi.TLSConfig' -- '*.go'`
Expected: tests PASS; the grep prints nothing.

- [ ] **Step 8: Commit**

```bash
git add internal/tlsreload internal/bridgeapi/tls.go internal/bridgeapi/tls_test.go internal/app/broker.go AGENTS.md
git commit -m "refactor(tls): the reloading key pair moves to internal/tlsreload (D7)"
```

---

### Task 2: The factory config's `api.tls`, both files or neither

**Files:**
- Modify: `internal/factory/config/config.go:640-669` (`API`), `:1358-1364` (`Validate`), `docs/factory.md:1340`
- Test: `internal/factory/config/config_test.go`

**Interfaces:**
- Consumes: `Parse([]byte) (*Config, error)`, the `good` fixture (its api block holds `  listen: ":8443"` once).
- Produces: `type ServerTLS struct { CertFile string \`json:"certFile"\`; KeyFile string \`json:"keyFile"\` }`; `API.TLS *ServerTLS \`json:"tls,omitempty"\``; the error `api.tls needs certFile and keyFile both`.

- [ ] **Step 1: Write the failing tests**

In `internal/factory/config/config_test.go`, add to `TestBadConfigsFail`'s map (beside the other api rows, or at its end):

```go
		// S-1, D6: TLS on the run-request API is the whole pair or nothing.
		"api tls without its key": {`  listen: ":8443"` + "\n", `  listen: ":8443"` + "\n  tls: {certFile: /etc/agent-factory-api-tls/tls.crt}\n",
			"api.tls needs certFile and keyFile both"},
		"api tls without its cert": {`  listen: ":8443"` + "\n", `  listen: ":8443"` + "\n  tls: {keyFile: /etc/agent-factory-api-tls/tls.key}\n",
			"api.tls needs certFile and keyFile both"},
		"an empty api tls": {`  listen: ":8443"` + "\n", `  listen: ":8443"` + "\n  tls: {}\n", "api.tls needs certFile and keyFile both"},
```

and append:

```go
// S-1, D6: api.tls is optional; set, it carries the pair the API serves.
func TestAPITLSIsOptional(t *testing.T) {
	c, err := Parse([]byte(good))
	if err != nil || c.API.TLS != nil {
		t.Fatalf("absent: %+v %v", c.API.TLS, err)
	}
	raw := strings.Replace(good, `  listen: ":8443"`+"\n",
		`  listen: ":8443"`+"\n  tls: {certFile: /etc/agent-factory-api-tls/tls.crt, keyFile: /etc/agent-factory-api-tls/tls.key}\n", 1)
	c, err = Parse([]byte(raw))
	if err != nil || c.API.TLS == nil || c.API.TLS.CertFile != "/etc/agent-factory-api-tls/tls.crt" ||
		c.API.TLS.KeyFile != "/etc/agent-factory-api-tls/tls.key" {
		t.Fatalf("set: %+v %v", c.API.TLS, err)
	}
}
```

- [ ] **Step 2: Run them**

Run: `go test -race -count=1 ./internal/factory/config -run 'TestBadConfigsFail|TestAPITLSIsOptional'`
Expected: FAIL to build: `c.API.TLS undefined`. (Without the field, the three new `TestBadConfigsFail` rows would fail on `unknown field "tls"`, not the wanted message.)

- [ ] **Step 3: Write the field and its check**

In `internal/factory/config/config.go`, in `type API struct`, after `Listen       string   \`json:"listen"\``:

```go
	// TLS serves the API over TLS 1.3 from this pair, re-read when it is renewed (S-1, D6): the
	// broker forwards each human's bearer token here. Absent, the API serves plaintext.
	TLS *ServerTLS `json:"tls,omitempty"`
```

and after the `API` struct:

```go
// ServerTLS is a listener's key pair: two mounted files, re-read on renewal. Both are set, or the
// block is absent.
type ServerTLS struct {
	CertFile string `json:"certFile"`
	KeyFile  string `json:"keyFile"`
}
```

In `Validate`, right after the loop that requires `api.listen`, `api.humanIssuer` and `api.humanJWKS`:

```go
	if t := c.API.TLS; t != nil && (t.CertFile == "" || t.KeyFile == "") {
		bad("api.tls needs certFile and keyFile both: set both, or drop the block to serve plaintext")
	}
```

In `docs/factory.md:1340` (the `api` row of the config table), insert before `` `projectIDFile` and `sponsored` ``:

```markdown
`tls: {certFile, keyFile}`, both or neither: the API serves TLS 1.3 from that pair, re-read when it is renewed, and the broker's `factoryURL` becomes `https://` (the chart's `api.tls` writes it); absent, plaintext;
```

- [ ] **Step 4: Run the package**

Run: `go test -race -count=1 ./internal/factory/config`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/factory/config/config.go internal/factory/config/config_test.go docs/factory.md
git commit -m "feat(factory): api.tls names the run-request API's key pair, both files or neither"
```

---

### Task 3: The run-request API serves TLS when `api.tls` is set

**Prerequisite:** T5's Task 1 is on this branch (`Server.listen`, `Server.httpServer`, `serve` in `internal/factory/api`). Check: `grep -n 'func (s \*Server) listen' internal/factory/api/server.go` prints a line.

**Files:**
- Modify: `internal/factory/api/server.go` (`listen`), `internal/app/factory.go:79`
- Test: `internal/factory/api/start_test.go`

**Interfaces:**
- Consumes: `tlsreload.Config` (Task 1), `config.ServerTLS` / `API.TLS` (Task 2), `tlsreloadtest.WritePair` (Task 1), T5's `serve`, `Server.listen`, `Server.httpServer`; the test double `byToken` (`server_test.go`).
- Produces: `Server.listen` returns a TLS listener when `s.Cfg.API.TLS != nil`; its error names `api.tls`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/factory/api/start_test.go` (add `"crypto/tls"`, `"crypto/x509"`, `"path/filepath"`, `"strings"` and `"github.com/Smana/maku/internal/tlsreload/tlsreloadtest"` to its imports):

```go
// postStatus POSTs an empty JSON body to url and returns the response, its body drained.
func postStatus(t *testing.T, c *http.Client, url string) (*http.Response, error) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, url, strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	return resp, nil
}

// startOn opens s's listener and serves until the test ends; it returns the bound address.
func startOn(t *testing.T, s *Server) string {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	ln, err := s.listen(ctx)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- serve(ctx, s.httpServer(), ln, time.Second) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("drain: %v", err)
		}
	})
	return ln.Addr().String()
}

// S-1, D6: with api.tls the API speaks TLS 1.3 only, from the mounted pair, and a plain request
// never reaches a handler.
func TestTheAPIServesTLSWhenConfigured(t *testing.T) {
	dir := t.TempDir()
	cert := tlsreloadtest.WritePair(t, dir, 1, time.Now())
	addr := startOn(t, &Server{Auth: byToken{}, Cfg: &config.Config{API: config.API{Listen: "127.0.0.1:0",
		TLS: &config.ServerTLS{CertFile: filepath.Join(dir, "tls.crt"), KeyFile: filepath.Join(dir, "tls.key")}}}})
	roots := x509.NewCertPool()
	roots.AddCert(cert)

	resp, err := postStatus(t, httpx.New(5*time.Second, roots), "https://"+addr+"/v1/runs")
	if err != nil {
		t.Fatalf("https: %v", err)
	}
	if resp.StatusCode != http.StatusUnauthorized || resp.TLS == nil || resp.TLS.Version != tls.VersionTLS13 {
		t.Fatalf("https: %d, TLS %+v", resp.StatusCode, resp.TLS)
	}
	if resp, err := postStatus(t, httpx.New(5*time.Second, nil), "http://"+addr+"/v1/runs"); err == nil && resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("plain http reached the API: %d", resp.StatusCode)
	}
	d := tls.Dialer{Config: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12, MaxVersion: tls.VersionTLS12}}
	if c, err := d.DialContext(t.Context(), "tcp", addr); err == nil {
		_ = c.Close()
		t.Fatal("a TLS 1.2 handshake succeeded: the API's minimum is 1.3")
	}
}

// Without api.tls the API serves plaintext, as every deployment before S-1 (D6: TLS is optional).
func TestTheAPIServesPlaintextWithoutTLS(t *testing.T) {
	addr := startOn(t, &Server{Auth: byToken{}, Cfg: &config.Config{API: config.API{Listen: "127.0.0.1:0"}}})
	resp, err := postStatus(t, httpx.New(5*time.Second, nil), "http://"+addr+"/v1/runs")
	if err != nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("plaintext: %v %v", resp, err)
	}
}

// A pair that does not load fails the start, naming api.tls: the pod crash-loops with the reason
// rather than serving plaintext.
func TestAMissingAPIPairFailsTheStart(t *testing.T) {
	dir := t.TempDir()
	s := &Server{Cfg: &config.Config{API: config.API{Listen: "127.0.0.1:0",
		TLS: &config.ServerTLS{CertFile: filepath.Join(dir, "tls.crt"), KeyFile: filepath.Join(dir, "tls.key")}}}}
	if ln, err := s.listen(t.Context()); err == nil || !strings.Contains(err.Error(), "api.tls") {
		if ln != nil {
			_ = ln.Close()
		}
		t.Fatalf("err = %v, want one naming api.tls", err)
	}
}
```

- [ ] **Step 2: Run them**

Run: `go test -race -count=1 ./internal/factory/api -run 'TestTheAPIServes|TestAMissingAPIPairFailsTheStart'`
Expected: `TestTheAPIServesPlaintextWithoutTLS` PASSes; `TestTheAPIServesTLSWhenConfigured` FAILs with `https: … http: server gave HTTP response to HTTPS client`; `TestAMissingAPIPairFailsTheStart` FAILs with `err = <nil>`: `listen` ignores `api.tls`.

- [ ] **Step 3: Write the fix**

In `internal/factory/api/server.go`, replace T5's `listen` with:

```go
// listen binds api.listen. With api.tls it serves TLS 1.3 from that pair, re-read when
// cert-manager renews it (S-1, D6): the broker forwards each human's bearer token here, so it
// never crosses the cluster network in the clear. A pair that does not load fails the start.
func (s *Server) listen(ctx context.Context) (net.Listener, error) {
	var tlsCfg *tls.Config
	if t := s.Cfg.API.TLS; t != nil {
		c, err := tlsreload.Config(t.CertFile, t.KeyFile, s.Log)
		if err != nil {
			return nil, fmt.Errorf("api: api.tls: %w", err)
		}
		tlsCfg = c
	}
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", s.Cfg.API.Listen)
	if err != nil {
		return nil, fmt.Errorf("api: listen %s: %w", s.Cfg.API.Listen, err)
	}
	if tlsCfg != nil {
		return tls.NewListener(ln, tlsCfg), nil
	}
	return ln, nil
}
```

Add `"crypto/tls"` and `"github.com/Smana/maku/internal/tlsreload"` to `server.go`'s imports.

In `internal/app/factory.go:79`, replace `the run-request API on :8082 (plaintext inside the mesh; the broker's :8443 is the TLS listener)` with `the run-request API on :8082 (TLS when api.tls is set)`.

- [ ] **Step 4: Run the package**

Run: `go test -race -count=1 ./internal/factory/api && go list -deps ./internal/factory/api | grep -c 'internal/bridgeapi$' || true`
Expected: PASS; the count prints `0` (D7: the factory never imports bridgeapi).

- [ ] **Step 5: Commit**

```bash
git add internal/factory/api/server.go internal/factory/api/start_test.go internal/app/factory.go
git commit -m "feat(factory): the run-request API serves TLS 1.3 when api.tls is set (S-1)"
```

---

### Task 4: D6 in the broker config, `factoryCAFile` and `factoryInsecure`

**Files:**
- Modify: `internal/config/config.go:225` (`Config`), `:424-426` (`Validate`), `docs/integration.md:125,200`
- Test: `internal/config/config_test.go`, `internal/config/sponsored_test.go`

**Interfaces:**
- Consumes: `Load(path)`, the `valid` fixture, `write(t, body)`.
- Produces: `Config.FactoryCAFile string \`json:"factoryCAFile,omitempty"\``, `Config.FactoryInsecure bool \`json:"factoryInsecure,omitempty"\``; `func checkFactoryURL(raw string, insecure bool) []error`.

- [ ] **Step 1: Move the existing fixtures to https, then write the failing test**

First, since an `http://` `factoryURL` alone is about to be refused, move the four existing fixtures (`config_test.go:548,559`, `sponsored_test.go:16,45`) to https:

```bash
sed -i 's#factoryURL: http://#factoryURL: https://#' internal/config/config_test.go internal/config/sponsored_test.go
git diff --stat   # 2 files, 4 lines
```

Then append to `internal/config/config_test.go` (after the `sed`, so its `http://` rows stay):

```go
// D6: the broker forwards each human's bearer token to factoryURL, so it is https, or http under
// an explicit factoryInsecure. A broker given http:// alone refuses to start, naming the key.
func TestFactoryURLIsHTTPSUnlessInsecure(t *testing.T) {
	for name, c := range map[string]struct{ body, err string }{
		"unset":                  {"", ""},
		"https":                  {"factoryURL: https://agent-factory.agent-system.svc:8082\n", ""},
		"https with its CA":      {"factoryURL: https://agent-factory.agent-system.svc:8082\nfactoryCAFile: /etc/room-broker/factory-ca/ca.crt\n", ""},
		"http":                   {"factoryURL: http://agent-factory.agent-system.svc:8082\n", "factoryURL: http:// sends human tokens in the clear"},
		"http, insecure opt-out": {"factoryURL: http://agent-factory.agent-system.svc:8082\nfactoryInsecure: true\n", ""},
		"another scheme":         {"factoryURL: ftp://agent-factory.agent-system.svc:8082\n", `factoryURL: scheme "ftp" is not https`},
		"no host":                {"factoryURL: https:///v1\n", "factoryURL: must be an absolute https URL"},
		"userinfo":               {"factoryURL: https://u:p@agent-factory.agent-system.svc:8082\n", "factoryURL: must be an absolute https URL"},
	} {
		t.Run(name, func(t *testing.T) {
			cfg, err := Load(write(t, valid+c.body))
			switch {
			case c.err == "" && err != nil:
				t.Fatalf("refused: %v", err)
			case c.err != "" && (err == nil || !strings.Contains(err.Error(), c.err)):
				t.Fatalf("err = %v, want one naming %q", err, c.err)
			case name == "https with its CA" && cfg.FactoryCAFile != "/etc/room-broker/factory-ca/ca.crt":
				t.Fatalf("factoryCAFile = %q", cfg.FactoryCAFile)
			case name == "http, insecure opt-out" && !cfg.FactoryInsecure:
				t.Fatal("factoryInsecure not read")
			}
		})
	}
}
```

Check: `grep -c 'factoryURL: http://agent' internal/config/config_test.go` prints `2` (the new test's `http` rows).

- [ ] **Step 2: Run it**

Run: `go test -race -count=1 ./internal/config -run 'TestFactoryURLIsHTTPSUnlessInsecure|TestLoadSlackLink|TestLoadMCPSponsored'`
Expected: `TestFactoryURLIsHTTPSUnlessInsecure` FAILs: `https with its CA` and `http, insecure opt-out` are refused with `unknown field "factoryCAFile"` / `"factoryInsecure"`, and `http`, `another scheme`, `no host`, `userinfo` are accepted (`err = <nil>`). The other two still PASS.

- [ ] **Step 3: Write the fields and the check**

In `internal/config/config.go`, in `type Config struct`, after `FactoryURL`:

```go
	// FactoryCAFile is the PEM CA that signs the factory's api certificate, read on every run
	// request so a rotation needs no restart (S-1). Empty: the system roots.
	FactoryCAFile string `json:"factoryCAFile,omitempty"`
	// FactoryInsecure admits an http:// factoryURL. The broker forwards each human's bearer token
	// there, so plaintext is an explicit opt-out, never a default (D6).
	FactoryInsecure bool `json:"factoryInsecure,omitempty"`
```

In `Validate`, after the `human.mcp.sponsored needs factoryURL` check:

```go
	errs = append(errs, checkFactoryURL(c.FactoryURL, c.FactoryInsecure)...)
```

and add, after `Validate`:

```go
// checkFactoryURL is D6: the broker forwards each human's bearer token to the factory, so its URL
// is https, or http under an explicit factoryInsecure, as the bridge refuses a non-https
// BROKER_URL. Unset is fine: start_run then renders a manifest.
func checkFactoryURL(raw string, insecure bool) []error {
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	switch {
	case err != nil || u.Host == "" || u.User != nil:
		return []error{errors.New("factoryURL: must be an absolute https URL with no userinfo")}
	case u.Scheme == "https", u.Scheme == "http" && insecure:
		return nil
	case u.Scheme == "http":
		return []error{errors.New("factoryURL: http:// sends human tokens in the clear: serve the factory's api.tls and use https, or set factoryInsecure: true")}
	}
	return []error{fmt.Errorf("factoryURL: scheme %q is not https", u.Scheme)}
}
```

In `docs/integration.md`, replace the `factoryURL` row (line 125) with:

```markdown
| `factoryURL` | The factory's run API on the factory Service's `api` port: `https://agent-factory.agent-system.svc:8082` once the factory serves `api.tls`. The broker forwards each human's bearer token there, so an `http://` URL is refused at start unless `factoryInsecure` is `true`. It moves with the factory chart's port and its `api.listen`. Unset: `start_run` returns a manifest for the owner to apply |
| `factoryCAFile` | PEM file of the CA that signs the factory's `api` certificate, read on every run request, so a rotation needs no restart. Unset: the system roots. The maku chart mounts `broker.factoryCA.secretName` (key `ca.crt`) at `/etc/room-broker/factory-ca/ca.crt` |
| `factoryInsecure` | `true` admits an `http://` `factoryURL`, sending human tokens to the factory in the clear: for a cluster whose CNI encrypts pod traffic, or a test. Default `false` |
```

and in the `Run requests` row (line 200) replace ``on the factory's `:8082` (plaintext)`` with ``on the factory's `:8082` (TLS, or plaintext under `factoryInsecure`)``.

- [ ] **Step 4: Run the package**

Run: `go test -race -count=1 ./internal/config`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go internal/config/sponsored_test.go docs/integration.md
git commit -m "feat(broker): refuse an http factoryURL unless factoryInsecure, and trust factoryCAFile (D6)"
```

---

### Task 5: The run-request client trusts `factoryCAFile`, read at use

**Files:**
- Modify: `internal/runrequest/runrequest.go:99-135`, `internal/runrequest/tasks.go:84-87`, `internal/app/rooms.go:29-32,49,93-100`, `internal/app/broker.go:224-226`
- Create: `internal/runrequest/ca_test.go`
- Test: `internal/app/rooms_test.go:216-224`

**Interfaces:**
- Consumes: `Config.FactoryCAFile` (Task 4), `tlsreloadtest.WritePair` (Task 1), `httpx.New`.
- Produces: `runrequest.Factory{URL, CAFile string; HC *http.Client}`; `func (f Factory) client() (*http.Client, error)`; `func runRequester(factoryURL, caFile string) runrequest.Requester` (package `app`).

- [ ] **Step 1: Write the failing tests**

Create `internal/runrequest/ca_test.go`:

```go
// SPDX-License-Identifier: Apache-2.0

package runrequest

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Smana/maku/internal/tlsreload/tlsreloadtest"
)

// S-1: a run request trusts the CA in factoryCAFile, read at each call: a factory it signs is
// reached, one another CA signs is refused, and a rotated file applies with no restart.
func TestFactoryTrustsTheCAFileReadAtUse(t *testing.T) {
	served, other := t.TempDir(), t.TempDir()
	tlsreloadtest.WritePair(t, served, 1, time.Now())
	tlsreloadtest.WritePair(t, other, 2, time.Now())
	pair, err := tls.LoadX509KeyPair(filepath.Join(served, "tls.crt"), filepath.Join(served, "tls.key"))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		if r.URL.Path == "/v1/tasks" {
			_, _ = w.Write([]byte(`{"task":"abcd2345","room":"abcd2345","issue":42,"duplicate":false,"sponsor":"smana"}`))
			return
		}
		_, _ = w.Write([]byte(`{"runId":"3kq7x2ma"}`))
	}))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS13}
	srv.StartTLS()
	defer srv.Close()

	ca := filepath.Join(t.TempDir(), "ca.crt")
	f := Factory{URL: srv.URL, CAFile: ca}
	req := Request{Role: "implementer", AccessToken: "human-token", IdempotencyKey: "3kq7x2ma:web:1"}
	copyCA := func(from string) {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(from, "tls.crt"))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(ca, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.Request(t.Context(), req); err == nil {
		t.Fatal("a missing factoryCAFile reached the factory")
	}
	copyCA(other)
	if _, err := f.Request(t.Context(), req); err == nil {
		t.Fatal("a factory signed by another CA was trusted")
	}
	copyCA(served) // the rotation: same path, new content, same Factory value
	if res, err := f.Request(t.Context(), req); err != nil || res.RunID != "3kq7x2ma" || res.Via != "factory" {
		t.Fatalf("the factory's own CA: %+v %v", res, err)
	}
	task, err := f.CreateTask(t.Context(), TaskRequest{Repository: "Smana/x", Text: "t", DataClass: "public", MaxCostUSD: 2,
		AccessToken: "tok", IdempotencyKey: "k1"})
	if err != nil || task.Task != "abcd2345" {
		t.Fatalf("a sponsored task over the same CA: %+v %v", task, err)
	}
}
```

In `internal/app/rooms_test.go`, replace `TestRunRequester` with:

```go
func TestRunRequester(t *testing.T) {
	if _, ok := runRequester("", "").(runrequest.Manifest); !ok {
		t.Fatal("before SP3 the claim is rendered")
	}
	f, ok := runRequester("https://factory.example", "/etc/room-broker/factory-ca/ca.crt").(runrequest.Factory)
	if !ok || f.URL != "https://factory.example" || f.CAFile != "/etc/room-broker/factory-ca/ca.crt" || f.HC != nil {
		t.Fatalf("the CA is read at each call, so no client is built at start: %+v", f)
	}
}
```

- [ ] **Step 2: Run them**

Run: `go test -race -count=1 ./internal/runrequest ./internal/app -run 'TestFactoryTrustsTheCAFileReadAtUse|TestRunRequester'`
Expected: FAIL to build: `unknown field CAFile in struct literal of type Factory`, `too many arguments in call to runRequester`.

- [ ] **Step 3: Write the client**

In `internal/runrequest/runrequest.go`, replace the `Factory` type and its comment with:

```go
// Factory is SP3's run API at URL. HC, when set, is the client (tests). Otherwise each call
// builds the audited egress client, trusting the PEM CA in CAFile, read at that call so a rotated
// CA needs no restart (S-1), or the system roots when CAFile is empty.
type Factory struct {
	URL    string
	CAFile string
	HC     *http.Client
}

// client is f's HTTP client for one call.
func (f Factory) client() (*http.Client, error) {
	if f.HC != nil {
		return f.HC, nil
	}
	if f.CAFile == "" {
		return httpx.New(factoryTimeout, nil), nil
	}
	raw, err := os.ReadFile(filepath.Clean(f.CAFile))
	if err != nil {
		return nil, fmt.Errorf("runrequest: factoryCAFile: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(raw) {
		return nil, fmt.Errorf("runrequest: factoryCAFile %s holds no PEM certificate", f.CAFile)
	}
	return httpx.New(factoryTimeout, roots), nil
}
```

Add `"crypto/x509"`, `"os"` and `"path/filepath"` to `runrequest.go`'s imports. In `Factory.Request`, replace

```go
	hc := f.HC
	if hc == nil {
		hc = httpx.New(factoryTimeout, nil)
	}
	resp, err := hc.Do(req)
```

with

```go
	hc, err := f.client()
	if err != nil {
		return Result{}, err
	}
	if f.HC == nil {
		defer hc.CloseIdleConnections() // a client per call: its connection goes with it
	}
	resp, err := hc.Do(req)
```

In `internal/runrequest/tasks.go`, make the same replacement in `CreateTask`, returning `TaskResult{}, err`:

```go
	hc, err := f.client()
	if err != nil {
		return TaskResult{}, err
	}
	if f.HC == nil {
		defer hc.CloseIdleConnections() // a client per call: its connection goes with it
	}
	resp, err := hc.Do(req)
```

In `internal/app/rooms.go`: delete the `factoryTimeout = 15 * time.Second` line from the const block (no other user remains); change line 49's call to `runRequester(cfg.FactoryURL, cfg.FactoryCAFile)`; replace `runRequester` with:

```go
// runRequester is SP3's factory at factoryURL, trusting caFile's CA (read at each call), or
// before SP3 (unset) the manifest renderer (ruling P14).
func runRequester(factoryURL, caFile string) runrequest.Requester {
	if factoryURL == "" {
		return runrequest.Manifest{}
	}
	return runrequest.Factory{URL: factoryURL, CAFile: caFile}
}
```

In `internal/app/broker.go:225` replace the sponsored-task client with:

```go
		humans.Tasks = runrequest.Factory{URL: cfg.FactoryURL, CAFile: cfg.FactoryCAFile}
```

and remove `"github.com/Smana/maku/internal/httpx"` from `broker.go`'s imports if `go build` reports it unused.

- [ ] **Step 4: Run them**

Run: `go build ./... && go test -race -count=1 ./internal/runrequest ./internal/app`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/runrequest internal/app/rooms.go internal/app/rooms_test.go internal/app/broker.go
git commit -m "feat(broker): run requests trust factoryCAFile, read at each call (S-1)"
```

---

### Task 6: The factory chart's `api.tls`

**Files:**
- Create: `charts/maku-factory/templates/certificate.yaml`
- Modify: `charts/maku-factory/values.yaml`, `templates/configmap.yaml`, `templates/deployment.yaml`, `templates/service.yaml`, `tests/render.sh`, `README.md`

**Interfaces:**
- Consumes: `config.api.tls` (Task 2), the `ykind` / `ynone` / `vol` / `mnt` helpers of `charts/maku-factory/tests/render.sh`.
- Produces: values `api.tls.secretName` (default `""`), `api.tls.issuerRef: {kind: ClusterIssuer, name: ""}`, `clusterDomain` (default `cluster.local`); mount path `/etc/agent-factory-api-tls`; Certificate `agent-factory-api-tls`; the Service port `https` (8082) when `api.tls.secretName` is set. Task 7's docs name the Secret `agent-factory-api-tls`.

- [ ] **Step 1: Write the failing assertions**

In `charts/maku-factory/tests/render.sh`, insert before the line `for bad in "" "v0.0.0" "latest"; do`:

```bash
# S-1, D6: TLS on the run-request API is off by default: no Certificate, no pair, no config.api.tls
ynone 'select(.kind == "Certificate")' "no Certificate unless api.tls.issuerRef.name"
ynone 'select(.kind == "Deployment") | .spec.template.spec.volumes[] | select(.name == "api-tls")' "no API pair unless api.tls.secretName"
ynone 'select(.kind == "ConfigMap") | .data."config.yaml" | from_yaml | .api.tls | select(. != null)' "plaintext by default: no config.api.tls"
# api.tls on: the pair mounted, config.api.tls pointed at it, the Certificate, the port named https
base="$out"
out="$(helm template agent-factory . --namespace agent-system --set image.tag="$digest" --set config.repository=Smana/cloud-native-ref \
  --set api.tls.secretName=agent-factory-api-tls --set api.tls.issuerRef.name=openbao)"
ykind Certificate '.spec.secretName == "agent-factory-api-tls"' "the Certificate writes api.tls.secretName"
ykind Certificate '.spec.dnsNames | contains(["agent-factory.agent-system.svc", "agent-factory.agent-system.svc.cluster.local"])' "the Certificate names the factory Service"
ykind Certificate '(.spec.issuerRef.name == "openbao" and .spec.issuerRef.kind == "ClusterIssuer")' "the Certificate's issuer is api.tls.issuerRef"
ykind Deployment "$(vol api-tls).secret | (.secretName == \"agent-factory-api-tls\" and .defaultMode == \"0440\")" "the API's pair, readable through fsGroup"
ykind Deployment "$(mnt api-tls) | (.mountPath == \"/etc/agent-factory-api-tls\" and .readOnly == true)" "the pair mounted read-only"
ykind ConfigMap '.data."config.yaml" | from_yaml | (.api.tls.certFile == "/etc/agent-factory-api-tls/tls.crt" and .api.tls.keyFile == "/etc/agent-factory-api-tls/tls.key")' "config.api.tls reads the mounted pair"
ykind ConfigMap '.data."config.yaml" | from_yaml | .repository == "Smana/cloud-native-ref"' "the rest of the config is still rendered verbatim"
ykind Service '.spec.ports[] | select(.port == 8082) | .name == "https"' "the API's Service port is named https"
out="$base"
# refuse 'message' --set ...: the render must fail, naming message
refuse() {
  local want="$1" err; shift
  if err="$(helm template agent-factory . --namespace agent-system --set image.tag="$digest" --set config.repository=Smana/cloud-native-ref "$@" 2>&1 >/dev/null)"; then
    echo "FAIL: $* must not render" >&2; fail=1
  elif ! grep -qF -- "$want" <<<"$err"; then
    echo "FAIL: $* failed, but not with \"$want\": $err" >&2; fail=1
  fi
}
refuse 'api.tls.issuerRef.name needs api.tls.secretName' --set api.tls.issuerRef.name=openbao
refuse 'config.api.tls and api.tls.secretName' --set api.tls.secretName=agent-factory-api-tls --set config.api.tls.certFile=/c
```

- [ ] **Step 2: Run it**

Run: `bash charts/maku-factory/tests/render.sh`
Expected: exit 1; the three `ynone` lines pass, and `FAIL:` lines for every `ykind` of the TLS render (no Certificate, no `api-tls` volume, no `config.api.tls`, port named `api`) and both `refuse` cases (`must not render`).

- [ ] **Step 3: Write the templates**

`charts/maku-factory/values.yaml`, after `roomsAudience: rooms-system`:

```yaml
# The cluster's DNS domain, for the run-request API certificate's names.
clusterDomain: cluster.local
# TLS on the run-request API (S-1, D6). secretName set: the Secret (tls.crt, tls.key) is mounted
# at /etc/agent-factory-api-tls, config.api.tls is written to read it, and the Service port is
# named https; point the broker's factoryURL at https://agent-factory.<namespace>.svc:8082 and its
# broker.factoryCA at the issuing CA. A cert-manager Certificate is rendered for it only when
# issuerRef.name is set; otherwise bring the Secret yourself. Empty: plaintext, and the broker
# needs factoryInsecure: true to call it.
api:
  tls:
    secretName: ""
    issuerRef: {kind: ClusterIssuer, name: ""}
```

Create `charts/maku-factory/templates/certificate.yaml`:

```yaml
{{- $t := .Values.api.tls -}}
{{- if and $t.issuerRef.name (not $t.secretName) -}}
{{- fail "api.tls.issuerRef.name needs api.tls.secretName: the Secret the Certificate writes and the pod mounts" -}}
{{- end -}}
{{- if $t.issuerRef.name }}
# The run-request API serves TLS from this pair; the factory re-reads it on renewal, so no restart.
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: agent-factory-api-tls
  labels: {{- include "agent-factory.labels" . | nindent 4 }}
spec:
  secretName: {{ $t.secretName }}
  duration: 720h # 30d
  renewBefore: 240h # 10d
  commonName: agent-factory.{{ .Release.Namespace }}.svc.{{ .Values.clusterDomain }}
  dnsNames:
    - agent-factory.{{ .Release.Namespace }}.svc
    - agent-factory.{{ .Release.Namespace }}.svc.{{ .Values.clusterDomain }}
  issuerRef:
    group: cert-manager.io
    {{- toYaml $t.issuerRef | nindent 4 }}
{{- end }}
```

Replace `charts/maku-factory/templates/configmap.yaml` with:

```yaml
{{- $cfg := deepCopy .Values.config -}}
{{- if .Values.api.tls.secretName -}}
{{- $api := deepCopy (get $cfg "api" | default dict) -}}
{{- if hasKey $api "tls" -}}
{{- fail "config.api.tls and api.tls.secretName: set api.tls.secretName alone; the chart writes config.api.tls to read the mounted pair" -}}
{{- end -}}
{{- $_ := set $api "tls" (dict "certFile" "/etc/agent-factory-api-tls/tls.crt" "keyFile" "/etc/agent-factory-api-tls/tls.key") -}}
{{- $_ := set $cfg "api" $api -}}
{{- end -}}
apiVersion: v1
kind: ConfigMap
metadata:
  name: agent-factory-config
  labels: {{- include "agent-factory.labels" . | nindent 4 }}
data:
  config.yaml: |
    {{- toYaml $cfg | nindent 4 }}
```

In `templates/deployment.yaml`, after the `sponsored` volumeMount line:

```yaml
            {{- if .Values.api.tls.secretName }}
            - {name: api-tls, mountPath: /etc/agent-factory-api-tls, readOnly: true}
            {{- end }}
```

and after the `sponsored` volume (the last one):

```yaml
        {{- if .Values.api.tls.secretName }}
        - name: api-tls
          # S-1: the run-request API's pair, re-read on renewal; 0440 with fsGroup, as the other secrets.
          secret: {secretName: {{ .Values.api.tls.secretName }}, defaultMode: 0440, items: [{key: tls.crt, path: tls.crt}, {key: tls.key, path: tls.key}]}
        {{- end }}
```

In `templates/service.yaml`, replace the `api` port line with:

```yaml
    - {name: {{ if .Values.api.tls.secretName }}https{{ else }}api{{ end }}, port: 8082, targetPort: api, protocol: TCP}
```

In `charts/maku-factory/README.md`, add to the Values table before the `config` row:

```markdown
| `api.tls.secretName` | `""` | The run-request API's key pair (`tls.crt`, `tls.key`), mounted at `/etc/agent-factory-api-tls`; the chart writes `config.api.tls` to read it and names the Service port `https`. Empty: plaintext, and the broker needs `factoryInsecure: true` |
| `api.tls.issuerRef` | `{kind: ClusterIssuer, name: ""}` | `name` set: a cert-manager `Certificate` `agent-factory-api-tls` for `agent-factory.<ns>.svc[.<clusterDomain>]` writes `api.tls.secretName`; empty: bring the Secret yourself |
| `clusterDomain` | `cluster.local` | The cluster's DNS domain, for the Certificate's names |
```

and replace the Ports table's `api` row with:

```markdown
| `api` 8082 (`https` with `api.tls.secretName`) | `api` | `POST /v1/runs` and `POST /v1/tasks`; TLS 1.3 when `api.tls.secretName` is set, plaintext otherwise; the broker's `factoryURL` points here. Before this chart moved it, the API sat on 8443 |
```

- [ ] **Step 4: Run it**

Run: `helm lint --strict charts/maku-factory --set image.tag='v0.0.0@sha256:0000' && bash charts/maku-factory/tests/render.sh`
Expected: lint passes; `PASS`, exit 0.

- [ ] **Step 5: Commit**

```bash
git add charts/maku-factory
git commit -m "feat(chart): the factory chart serves the run-request API over TLS with api.tls"
```

---

### Task 7: The maku chart's `broker.factoryCA` and `broker.factoryInsecure`

**Files:**
- Modify: `charts/maku/values.yaml:43`, `templates/broker-config.yaml:36-38`, `templates/broker-deployment.yaml` (volumeMounts, volumes), `templates/_helpers.tpl` (`ap.validate`), `tests/render.sh`, `README.md`

**Interfaces:**
- Consumes: `factoryCAFile`, `factoryInsecure` (Task 4); `render.sh`'s `ykind`, `ynone`, `bad`, `imgs`, the `tmp` loadconfig block; `tests/loadconfig broker`.
- Produces: values `broker.factoryInsecure` (default `false`), `broker.factoryCA.secretName` (default `""`); mount `/etc/room-broker/factory-ca/ca.crt`; the render refuses `broker.factoryURL=http://…` without `broker.factoryInsecure`.

- [ ] **Step 1: Write the failing assertions**

In `charts/maku/tests/render.sh`, insert after the existing `bad 'broker.publicURL "http://0.0.0.0:8080" in local mode' …` line:

```bash
# S-1, D6: the factory's CA and the insecure opt-out reach the broker's config; http alone is refused
fca="$(helm template ap . -n x -f values-quickstart.yaml "${imgs[@]}" \
  --set broker.factoryURL=https://agent-factory.x.svc:8082 --set broker.factoryCA.secretName=agent-factory-api-tls)"
out="$fca"
ykind ConfigMap 'select(.metadata.name=="room-broker-config") | .data."config.yaml" | from_yaml | (.factoryURL == "https://agent-factory.x.svc:8082" and .factoryCAFile == "/etc/room-broker/factory-ca/ca.crt" and (has("factoryInsecure") | not))' 'factoryCA.secretName writes factoryCAFile'
ykind Deployment 'select(.metadata.name=="room-broker") | .spec.template.spec.volumes[] | select(.name=="factory-ca") | (.secret.secretName == "agent-factory-api-tls" and .secret.items[0].key == "ca.crt")' 'the factory CA Secret, ca.crt only'
ykind Deployment 'select(.metadata.name=="room-broker") | .spec.template.spec.containers[0].volumeMounts[] | select(.name=="factory-ca") | (.mountPath == "/etc/room-broker/factory-ca" and .readOnly == true)' 'the factory CA mounted where factoryCAFile reads it'
fins="$(helm template ap . -n x -f values-quickstart.yaml "${imgs[@]}" \
  --set broker.factoryURL=http://agent-factory.x.svc:8082 --set broker.factoryInsecure=true)"
out="$fins"
ykind ConfigMap 'select(.metadata.name=="room-broker-config") | .data."config.yaml" | from_yaml | (.factoryInsecure == true and (has("factoryCAFile") | not))' 'factoryInsecure reaches the config'
ynone 'select(.kind=="Deployment" and .metadata.name=="room-broker") | .spec.template.spec.volumes[] | select(.name=="factory-ca")' 'no factory CA Secret, no mount'
bad 'broker.factoryURL "http://agent-factory.x.svc:8082" is http' --set broker.factoryURL=http://agent-factory.x.svc:8082
```

and in the loadconfig block, after the `yq … <<<"$fwd" >"$tmp/forwarded.yaml"` line:

```bash
yq 'select(.metadata.name=="room-broker-config") | .data."config.yaml"' - <<<"$fca" >"$tmp/factory-ca.yaml"
yq 'select(.metadata.name=="room-broker-config") | .data."config.yaml"' - <<<"$fins" >"$tmp/factory-insecure.yaml"
```

and extend the broker loader's call to

```bash
go run ./tests/loadconfig broker "$tmp/quickstart.yaml" "$tmp/production.yaml" "$tmp/forwarded.yaml" "$tmp/factory-ca.yaml" "$tmp/factory-insecure.yaml" || fail=1
```

- [ ] **Step 2: Run it**

Run: `bash charts/maku/tests/render.sh`
Expected: exit 1, with `FAIL:` on `factoryCA.secretName writes factoryCAFile`, the two `factory-ca` Deployment checks, `factoryInsecure reaches the config`, and `broker.factoryURL=http://agent-factory.x.svc:8082 must not render`.

- [ ] **Step 3: Write the templates**

`charts/maku/values.yaml`, replace `  factoryURL: ""` with:

```yaml
  # The factory's run API: https://agent-factory.<namespace>.svc:8082 once the factory chart sets
  # api.tls.secretName. The broker forwards each human's bearer token there, so an http:// URL is
  # refused, here and at start, unless factoryInsecure is true (S-1, D6).
  factoryURL: ""
  factoryInsecure: false
  # The Secret (key ca.crt) of the CA that signs the factory's api certificate, mounted at
  # /etc/room-broker/factory-ca and read on every run request. Empty: the system roots.
  factoryCA:
    secretName: ""
```

`templates/broker-config.yaml`, after the `factoryURL` block (`{{- with $b.factoryURL }} … {{- end }}`):

```yaml
    {{- if $b.factoryInsecure }}
    factoryInsecure: true
    {{- end }}
    {{- if $b.factoryCA.secretName }}
    factoryCAFile: /etc/room-broker/factory-ca/ca.crt
    {{- end }}
```

`templates/broker-deployment.yaml`, after the `tmp` volumeMount line:

```yaml
            {{- if $b.factoryCA.secretName }}
            - {name: factory-ca, mountPath: /etc/room-broker/factory-ca, readOnly: true}
            {{- end }}
```

and after the `tmp` volume (`emptyDir: {}`):

```yaml
        {{- if $b.factoryCA.secretName }}
        - name: factory-ca
          secret: {secretName: {{ $b.factoryCA.secretName }}, items: [{key: ca.crt, path: ca.crt}]}
        {{- end }}
```

`templates/_helpers.tpl`, in `ap.validate`, before its final `{{- end -}}`:

```
{{- if and $b.factoryURL (hasPrefix "http://" $b.factoryURL) (not $b.factoryInsecure) -}}
{{- fail (printf "broker.factoryURL %q is http: the broker forwards human tokens to it; serve the factory chart's api.tls and use https, or set broker.factoryInsecure: true" $b.factoryURL) -}}
{{- end -}}
```

`charts/maku/README.md`, after the `broker.tls.issuerRef.name` row:

```markdown
| `broker.factoryURL` | The factory's run API, `https://agent-factory.<ns>.svc:8082` once the factory chart sets `api.tls.secretName`. An `http://` URL is refused unless `broker.factoryInsecure` |
| `broker.factoryInsecure` | `true` admits an `http://` `factoryURL`: human tokens cross to the factory in the clear. Default `false` |
| `broker.factoryCA.secretName` | A Secret with `ca.crt`, the CA that signs the factory's certificate; mounted at `/etc/room-broker/factory-ca` and written as `factoryCAFile`. Empty: the system roots |
```

- [ ] **Step 4: Run it**

Run: `helm lint --strict charts/maku -f charts/maku/values-quickstart.yaml --set broker.image.tag='v0.0.0@sha256:0000' --set runController.image.tag='v0.0.0@sha256:0000' --set runController.images.harness.tag='v0.0.0@sha256:0000' --set runController.images.bridge.tag='v0.0.0@sha256:0000' && bash charts/maku/tests/render.sh`
Expected: lint passes; `PASS`, exit 0 (both new rendered configs load through `config.Load`).

- [ ] **Step 5: Commit**

```bash
git add charts/maku
git commit -m "feat(chart): the maku chart's broker trusts the factory's CA, and refuses an http factoryURL"
```

---

### Task 8: The upgrade step, the docs, the roadmap row, and the gate

**Files:**
- Modify: `docs/operations.md:195`, `docs/architecture.md:16,297`, `docs/security.md` (after the "TLS on :8443" table, :132), `docs/roadmap.md:14`, `AGENTS.md:199,241-243`

**Interfaces:**
- Consumes: every name above.
- Produces: nothing new.

- [ ] **Step 1: Write the docs**

`docs/operations.md:195`, replace the `Factory run-request API port` row with two rows:

```markdown
| Factory run-request API port | The chart serves `POST /v1/runs` on `:8082` (Service port `api`, or `https` with `api.tls.secretName`; container port `api`); the RunLore intake stays on `:8080`. Releases before it served the API on `:8443`: move the factory config's `api.listen` and the broker's `factoryURL` to `:8082` in the same change as the chart, or the broker's `start_run` calls fail until they do |
| Factory run-request API TLS (S-1) | A broker of this release refuses an `http://` `factoryURL` at start, naming `factoryURL`, and the maku chart refuses to render one. Before rolling it, either (a) serve TLS on the factory: `api.tls.secretName` (plus `api.tls.issuerRef.name` for a cert-manager `Certificate`) in the factory chart, then the broker's `factoryURL: https://agent-factory.<ns>.svc:8082` and `broker.factoryCA.secretName` (a Secret with the issuing CA's `ca.crt`); roll the factory first, since a broker on `https://` cannot reach a plaintext factory; or (b) keep plaintext explicitly with `broker.factoryInsecure: true` (`factoryInsecure: true` in a hand-written config) |
```

`docs/architecture.md:16`, replace ``answers `POST /v1/runs` on `:8082` (plaintext inside the mesh)`` with ``answers `POST /v1/runs` on `:8082` (TLS 1.3 with `api.tls`)``; `docs/architecture.md:297`, replace ``| `POST /v1/runs`, plaintext inside the mesh: the broker's `start_run` |`` with ``| `POST /v1/runs`, TLS 1.3 when `api.tls` is set (`internal/tlsreload`): the broker's `start_run` |``.

`docs/security.md`, after the `CA distribution` row of the "TLS on :8443" table (line 132), add a blank line and:

```markdown
The broker forwards each human's ZITADEL bearer token to the factory's `:8082` on a run request,
so that hop is TLS too once the factory chart sets `api.tls.secretName`: the same reloading loader
(`internal/tlsreload`), TLS 1.3 minimum, the broker trusting `factoryCAFile`. The broker refuses
an `http://` `factoryURL` at start unless `factoryInsecure: true` says the operator accepts
plaintext, the same rule as the bridge's `BROKER_URL`.
```

`docs/roadmap.md`: delete the row `| TLS on the factory's run-request API, which serves plaintext on `:8082` inside the mesh | … |`.

`AGENTS.md:199`, replace ``the run-request API, `POST /v1/runs` (the chart's `api` port, `:8082`)`` with ``the run-request API, `POST /v1/runs` (the chart's `api` port, `:8082`; TLS 1.3 with `api.tls`)``; and replace the security rule at lines 241-243 with:

```markdown
- **TLS on `:8443`.** The broker serves it with a reloading `GetCertificate` (`internal/tlsreload`);
  the bridge verifies against `BROKER_CA_FILE` and refuses a non-`https://` `BROKER_URL`. The
  factory's `:8082` serves the same loader when `api.tls` is set, and the broker refuses an
  `http://` `factoryURL` unless `factoryInsecure: true`. Never `InsecureSkipVerify`, tests
  included (use `httptest.NewTLSServer`'s client, or `tlsreloadtest.WritePair` and a root pool).
```

- [ ] **Step 2: Check the docs gate**

Run: `task docs`
Expected: exit 0 (no dead link or anchor; the roadmap row's link went with it).

- [ ] **Step 3: Commit**

```bash
git add docs/operations.md docs/architecture.md docs/security.md docs/roadmap.md AGENTS.md
git commit -m "docs: TLS on the factory's run-request API, its upgrade step, and the roadmap row closed"
```

- [ ] **Step 4: Run the full gate (Docker running)**

Run: `task check`
Expected: exit 0, `spdx` (the three new Go files), `lint`, `test`, `chart:check` (both `render.sh`) and `docs` included. A finding is fixed with `git commit --amend` when it belongs to the last commit, otherwise in one more commit scoped to the component.

- [ ] **Step 5: Confirm the spec's verification rows**

Run: `go list -deps ./internal/factory/api | grep -x 'github.com/Smana/maku/internal/bridgeapi' ; git grep -n 'TLSConfig(' -- 'internal/bridgeapi/*.go'`
Expected: both print nothing. Then `git log --oneline main..` shows the T6 commits (8 with T5's merged first), none with an attribution line. The PR description states the upgrade step from `docs/operations.md` verbatim, since the release notes are generated from PR titles.

## Self-review

| Spec item | Task |
|---|---|
| D7: `tlsreload.Config(certFile, keyFile, log)`; bridgeapi calls it; the moved tests | 1 |
| Factory `api.tls: {certFile, keyFile}`, both or neither; half-set refused | 2 |
| Set, `Start` serves TLS 1.3; a TLS round trip | 3 (deviation 2: `tls.NewListener` under T5's `serve`) |
| Broker `factoryCAFile`, `factoryInsecure`; `http` refused, `http` + insecure accepted, `https` accepted | 4 |
| `factoryCAFile` read at use into the run-request client's roots; signed reached, other CA refused | 5 |
| Factory chart `api.tls: {secretName, issuerRef: {kind, name}}`, Certificate, read-only mount, `config.api.tls`, port `https` | 6 |
| Maku chart `broker.factoryCA.secretName`, `broker.factoryInsecure` | 7 |
| `docs/integration.md` and the release notes state the upgrade step; roadmap row closes | 4, 8 |
| "A broker started with `factoryURL: http://…` and no `factoryInsecure` exits with the key named" | 4 (`config.Load` fails naming `factoryURL`; `serveBroker` returns it) |
| `render.sh` asserts the T6 Certificate | 6 |
| D0, `task check` | 1-8 |
