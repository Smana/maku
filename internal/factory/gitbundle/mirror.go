// SPDX-License-Identifier: Apache-2.0

// Package gitbundle inspects a run's git bundle outside the sandbox and pushes it on the run's
// behalf (HR24 to HR26). A bundle is hostile input: every git call runs in a bare, blobless mirror
// with fsck on, no hooks, no worktree, an allow-list of transports, a timeout and an output cap, and
// the token reaches git only through GIT_CONFIG_* environment variables.
package gitbundle

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Bounds of one git call.
const (
	defaultTimeout = 60 * time.Second
	maxStdout      = 1 << 20 // bytes of captured stdout; more is an error
	maxStderr      = 4 << 10 // bytes of stderr kept, the tail
	maxDetail      = 200     // bytes of a refusal detail or an error's git line
)

// ErrLeaseLost is a push whose lease the remote refused: the branch moved since OldHead was read.
var ErrLeaseLost = errors.New("gitbundle: the branch moved since it was read")

// errMirrorBroken marks a failure of the mirror itself: withMirror re-creates it once.
var errMirrorBroken = errors.New("gitbundle: mirror broken")

// errOutputCap is a git call whose stdout passed maxStdout.
var errOutputCap = errors.New("gitbundle: git output over the cap")

// Cache holds one bare, blobless mirror per repository under Root (an emptyDir, MAKU_GIT_CACHE in
// the factory).
type Cache struct {
	Root string
	// Remote is a repository's URL; nil means "https://github.com/" + repo + ".git".
	Remote func(repo string) string
	// Token is the token git sends for repo (the publisher's), only through GIT_CONFIG_COUNT/KEY/VALUE
	// env as http.extraHeader; never in argv. nil sends none (tests on file://).
	Token func(ctx context.Context, repo string) (string, error)
	// Git is the git binary, "git" (from PATH) when empty.
	Git string
	// Protocols are the allowed transport protocols, ["https"] when empty.
	Protocols []string
	// Timeout bounds each git call, 60s when zero.
	Timeout time.Duration
	// MaxBytes bounds one mirror on disk, DefaultMaxBytes when zero: a mirror past it is re-created
	// before the next inspection, so pushed objects never accumulate past the cache's emptyDir.
	MaxBytes int64

	mu      sync.Mutex
	mirrors map[string]*Mirror
}

// Mirror is one repository's bare, blobless mirror. Inspect and Push hold its lock.
type Mirror struct {
	repo  string
	dir   string
	cache *Cache
	lock  sync.Mutex
}

// Mirror is cache's mirror of repo (one per lower-cased repo, created lazily on disk by the first
// operation; its dir is Root/<owner>/<name>.git, sanitised).
func (c *Cache) Mirror(repo string) *Mirror {
	key := strings.ToLower(repo)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.mirrors == nil {
		c.mirrors = map[string]*Mirror{}
	}
	if m, ok := c.mirrors[key]; ok {
		return m
	}
	owner, name, _ := strings.Cut(key, "/")
	m := &Mirror{repo: repo, cache: c, dir: filepath.Join(c.Root, sanitise(owner), sanitise(name)+".git")}
	c.mirrors[key] = m
	return m
}

// sanitise keeps one path segment within [a-z0-9._-], never "", "." or "..".
func sanitise(s string) string {
	b := []byte(s)
	for i, ch := range b {
		ok := ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9' || ch == '.' || ch == '_' || ch == '-'
		if !ok {
			b[i] = '_'
		}
	}
	out := string(b)
	if out == "" || strings.Trim(out, ".") == "" {
		out = "_" + out
	}
	return out
}

func (c *Cache) remote(repo string) string {
	if c.Remote != nil {
		return c.Remote(repo)
	}
	return "https://github.com/" + repo + ".git"
}

func (c *Cache) gitBin() string {
	if c.Git != "" {
		return c.Git
	}
	return "git"
}

func (c *Cache) timeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return defaultTimeout
}

// token is the operation's token, "" when Cache.Token is nil.
func (c *Cache) token(ctx context.Context, repo string) (string, error) {
	if c.Token == nil {
		return "", nil
	}
	tok, err := c.Token(ctx, repo)
	if err != nil {
		return "", fmt.Errorf("gitbundle: token for %s: %w", repo, err)
	}
	return tok, nil
}

// gitError is a git call that failed: its one bounded, redacted line of stderr.
type gitError struct {
	op   string
	line string
	all  string // the stderr tail, redacted, for classification only
	code int    // the exit code, -1 when git did not exit
}

func (e *gitError) Error() string { return "gitbundle: git " + e.op + ": " + e.line }

// repoBroken reports whether err is a repository-level failure that a fresh mirror would cure.
func repoBroken(err error) bool {
	if errors.Is(err, errMirrorBroken) {
		return true
	}
	var ge *gitError
	if !errors.As(err, &ge) {
		return false
	}
	s := strings.ToLower(ge.all)
	for _, p := range []string{
		"not a git repository", "corrupt", "unable to read sha1 file", "bad object",
		"invalid object", "broken link", "fatal: bad tree", "packfile", "index file",
		"unknown repository format", "bad config", "missing object",
	} {
		if strings.Contains(s, p) {
			return true
		}
	}
	return false
}

// run is one git call with the mirror as GIT_DIR, the hardened -c options, a minimal environment,
// the call timeout and the stdout cap. tok, when set, reaches git only as the http.extraHeader of
// GIT_CONFIG_COUNT env.
func (m *Mirror) run(ctx context.Context, tok string, extraEnv []string, args ...string) ([]byte, error) {
	return m.runCfg(ctx, tok, extraEnv, nil, args...)
}

// runCfg is run with extra -c options, after the hardened ones.
func (m *Mirror) runCfg(ctx context.Context, tok string, extraEnv, cfg []string, args ...string) ([]byte, error) {
	return m.runIn(ctx, tok, extraEnv, cfg, nil, args...)
}

// runIn is runCfg with stdin.
func (m *Mirror) runIn(ctx context.Context, tok string, extraEnv, cfg []string, stdin []byte, args ...string) ([]byte, error) {
	c := m.cache
	ctx, cancel := context.WithTimeout(ctx, c.timeout())
	defer cancel()

	argv := []string{
		"-c", "transfer.fsckObjects=true",
		"-c", "fetch.fsckObjects=true",
		"-c", "receive.fsckObjects=true",
		// fsck only warns on these by default; a pushed tree must not carry them.
		"-c", "fetch.fsck.hasDotgit=error",
		"-c", "fetch.fsck.hasDotdot=error",
		"-c", "fetch.fsck.fullPathname=error",
		"-c", "core.hooksPath=/dev/null",
		// A blob past it is never loaded whole into memory, nor delta-compressed: a bundle's
		// highly compressible blob cannot balloon a call (sizes are refused before any read too).
		"-c", "core.bigFileThreshold=1m",
		"-c", "core.fsmonitor=false",
		"-c", "gc.auto=0",
		"-c", "maintenance.auto=false",
		"-c", "diff.external=",
		"-c", "credential.helper=",
		"-c", "protocol.allow=never",
	}
	protos := c.Protocols
	if len(protos) == 0 {
		protos = []string{"https"}
	}
	for _, p := range protos {
		argv = append(argv, "-c", "protocol."+p+".allow=always")
	}
	for _, kv := range cfg {
		argv = append(argv, "-c", kv)
	}
	argv = append(argv, args...)

	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_ASKPASS=",
		"SSH_ASKPASS=",
		"GIT_PAGER=cat",
		"LC_ALL=C",
		"GIT_DIR=" + m.dir,
	}
	if tok != "" {
		env = append(env,
			"GIT_CONFIG_COUNT=1",
			"GIT_CONFIG_KEY_0=http.extraHeader",
			"GIT_CONFIG_VALUE_0="+authHeader(tok))
	}
	env = append(env, extraEnv...)

	//nolint:gosec // G204: the binary is the factory's configured git; every argument is built here from validated refs, SHAs and paths, never from bundle content
	cmd := exec.CommandContext(ctx, c.gitBin(), argv...)
	cmd.Env = env
	cmd.Dir = os.TempDir()
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	cmd.WaitDelay = 5 * time.Second
	stdout := &capWriter{max: maxStdout, cancel: cancel}
	stderr := &tailWriter{max: maxStderr}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err := cmd.Run()
	op := opName(args)
	if stdout.over {
		return nil, fmt.Errorf("gitbundle: git %s: %w", op, errOutputCap)
	}
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, fmt.Errorf("gitbundle: git %s: %w", op, ctxErr)
		}
		all := redact(stderr.String(), tok)
		code := -1
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		}
		return stdout.buf.Bytes(), &gitError{op: op, line: lastLine(all), all: all, code: code}
	}
	return stdout.buf.Bytes(), nil
}

// authHeader is the http.extraHeader value carrying tok.
func authHeader(tok string) string {
	return "Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:"+tok))
}

// redact removes tok, and the header that carries it, from s.
func redact(s, tok string) string {
	if tok == "" {
		return s
	}
	s = strings.ReplaceAll(s, authHeader(tok), "[redacted]")
	s = strings.ReplaceAll(s, base64.StdEncoding.EncodeToString([]byte("x-access-token:"+tok)), "[redacted]")
	return strings.ReplaceAll(s, tok, "[redacted]")
}

// lastLine is s's last non-empty line, cut at maxDetail bytes.
func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return cut(strings.TrimSpace(lines[len(lines)-1]))
}

// cut bounds s to maxDetail bytes and quotes its control characters.
func cut(s string) string {
	if len(s) > maxDetail {
		s = s[:maxDetail]
	}
	q := strconv.QuoteToASCII(s)
	return q[1 : len(q)-1]
}

// opName is the git subcommand args run, for error messages.
func opName(args []string) string {
	for _, a := range args {
		if !strings.HasPrefix(a, "-") {
			return a
		}
	}
	return "git"
}

// capWriter keeps up to max bytes; past it, it marks over and cancels the call.
type capWriter struct {
	buf    bytes.Buffer
	max    int
	over   bool
	cancel context.CancelFunc
}

func (w *capWriter) Write(p []byte) (int, error) {
	if w.over {
		return len(p), nil
	}
	if w.buf.Len()+len(p) > w.max {
		w.over = true
		w.cancel()
		return len(p), nil
	}
	w.buf.Write(p)
	return len(p), nil
}

// tailWriter keeps the last max bytes written.
type tailWriter struct {
	buf []byte
	max int
}

func (w *tailWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	if len(w.buf) > w.max {
		w.buf = append([]byte(nil), w.buf[len(w.buf)-w.max:]...)
	}
	return len(p), nil
}

func (w *tailWriter) String() string { return string(w.buf) }

// ensure makes the mirror exist and answer as a git directory, re-creating it when it does not.
func (m *Mirror) ensure(ctx context.Context) error {
	if _, err := os.Stat(m.dir); err == nil {
		if _, err := m.run(ctx, "", nil, "rev-parse", "--git-dir"); err == nil {
			if _, err := m.run(ctx, "", nil, "config", "--get", "extensions.partialclone"); err == nil {
				return nil
			}
		}
		if err := os.RemoveAll(m.dir); err != nil {
			return fmt.Errorf("gitbundle: remove broken mirror: %w", err)
		}
	}
	return m.create(ctx)
}

// create initialises an empty bare, blobless mirror of the repository: a partial clone of no refs,
// so that every later fetch names exactly the refs it needs.
func (m *Mirror) create(ctx context.Context) error {
	if err := os.MkdirAll(filepath.Dir(m.dir), 0o700); err != nil {
		return fmt.Errorf("gitbundle: mirror parent: %w", err)
	}
	if _, err := m.run(ctx, "", nil, "init", "--bare", "--quiet", m.dir); err != nil {
		_ = os.RemoveAll(m.dir)
		return err
	}
	for _, kv := range [][2]string{
		{"core.repositoryformatversion", "1"},
		{"remote.origin.url", m.cache.remote(m.repo)},
		{"remote.origin.promisor", "true"},
		{"remote.origin.partialclonefilter", "blob:none"},
		{"extensions.partialclone", "origin"},
	} {
		if _, err := m.run(ctx, "", nil, "config", kv[0], kv[1]); err != nil {
			_ = os.RemoveAll(m.dir)
			return err
		}
	}
	return nil
}

// DefaultMaxBytes is a mirror's size limit on disk when Cache.MaxBytes is zero.
const DefaultMaxBytes = 1 << 30

// MaxBlob is the largest blob a push may change, compressed or not: larger is push_size, read by
// its header alone, never inflated (core.bigFileThreshold matches it).
const MaxBlob = 1 << 20

// overSize reports whether the mirror's directory holds more than the cache's MaxBytes.
func (m *Mirror) overSize() bool {
	limit := m.cache.MaxBytes
	if limit <= 0 {
		limit = DefaultMaxBytes
	}
	var total int64
	_ = filepath.WalkDir(m.dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil //nolint:nilerr // an unreadable entry counts nothing; ensure probes the mirror
		}
		if info, err := d.Info(); err == nil {
			total += info.Size()
		}
		if total > limit {
			return fs.SkipAll
		}
		return nil
	})
	return total > limit
}

// withMirror runs op on a ready mirror; a repository-level failure removes the mirror, re-creates it
// and runs op once more.
func (m *Mirror) withMirror(ctx context.Context, op func() error) error {
	if m.overSize() {
		if err := os.RemoveAll(m.dir); err != nil {
			return fmt.Errorf("gitbundle: remove a mirror over its size limit: %w", err)
		}
	}
	if err := m.ensure(ctx); err != nil {
		return err
	}
	err := op()
	if err == nil || !repoBroken(err) {
		return err
	}
	if rmErr := os.RemoveAll(m.dir); rmErr != nil {
		return fmt.Errorf("gitbundle: remove broken mirror: %w", rmErr)
	}
	if err := m.create(ctx); err != nil {
		return err
	}
	return op()
}

// lsRemote reads ref's head on the remote, "" when absent.
func (m *Mirror) lsRemote(ctx context.Context, tok, ref string) (string, error) {
	out, err := m.run(ctx, tok, nil, "ls-remote", "--refs", "--", "origin", ref)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(out), "\n") {
		sha, name, ok := strings.Cut(line, "\t")
		if ok && name == ref {
			if !isSHA(sha) {
				return "", fmt.Errorf("gitbundle: ls-remote %s: not a sha", ref)
			}
			return sha, nil
		}
	}
	return "", nil
}

// sync reads base's and branch's heads on the remote and fetches only those into the mirror's
// refs of the same name, blobless. It answers the mirror's heads after the fetch (the lease and the
// base); a missing base is an error, a missing branch is "".
func (m *Mirror) sync(ctx context.Context, tok, branch, base string) (oldHead, baseHead string, err error) {
	baseRef, branchRef := "refs/heads/"+base, "refs/heads/"+branch
	remoteBase, err := m.lsRemote(ctx, tok, baseRef)
	if err != nil {
		return "", "", err
	}
	if remoteBase == "" {
		return "", "", fmt.Errorf("gitbundle: base %s is absent on the remote", cut(base))
	}
	remoteBranch, err := m.lsRemote(ctx, tok, branchRef)
	if err != nil {
		return "", "", err
	}
	specs := []string{"+" + baseRef + ":" + baseRef}
	if remoteBranch != "" {
		specs = append(specs, "+"+branchRef+":"+branchRef)
	} else if _, err := m.run(ctx, "", nil, "update-ref", "-d", branchRef); err != nil {
		return "", "", err
	}
	args := append([]string{"fetch", "--quiet", "--no-tags", "--no-write-fetch-head", "--filter=blob:none", "--no-recurse-submodules", "origin"}, specs...)
	if _, err := m.run(ctx, tok, nil, args...); err != nil {
		return "", "", err
	}
	if baseHead, err = m.revParse(ctx, baseRef); err != nil {
		return "", "", err
	}
	if remoteBranch != "" {
		if oldHead, err = m.revParse(ctx, branchRef); err != nil {
			return "", "", err
		}
	}
	return oldHead, baseHead, nil
}

// revParse is ref's SHA in the mirror.
func (m *Mirror) revParse(ctx context.Context, ref string) (string, error) {
	out, err := m.run(ctx, "", nil, "rev-parse", "--verify", "--quiet", "--end-of-options", ref+"^{commit}")
	if err != nil {
		return "", err
	}
	sha := strings.TrimSpace(string(out))
	if !isSHA(sha) {
		return "", fmt.Errorf("gitbundle: rev-parse %s: not a sha", cut(ref))
	}
	return sha, nil
}

// isSHA reports whether s is a full SHA-1 or SHA-256 in lower-case hex.
func isSHA(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, ch := range s {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
			return false
		}
	}
	return true
}

// validBranch reports whether name is a branch name this package passes to git: a conservative
// subset of git check-ref-format.
func validBranch(name string) bool {
	if name == "" || len(name) > 200 || strings.HasPrefix(name, "-") || strings.HasPrefix(name, "/") ||
		strings.HasSuffix(name, "/") || strings.Contains(name, "..") || strings.Contains(name, "//") ||
		strings.Contains(name, "@{") || name == "@" {
		return false
	}
	for _, ch := range name {
		ok := ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' ||
			ch == '/' || ch == '.' || ch == '_' || ch == '-'
		if !ok {
			return false
		}
	}
	for _, seg := range strings.Split(name, "/") {
		if strings.HasPrefix(seg, ".") || strings.HasSuffix(seg, ".lock") || strings.HasSuffix(seg, ".") {
			return false
		}
	}
	return true
}
