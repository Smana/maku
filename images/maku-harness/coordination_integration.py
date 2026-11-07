"""The coordination integration runtime (v4 MR10): what a triager run carrying a manifest does
instead of a conversation.

The run controller hands the factory's canonical manifest and its SHA-256 over as
COORDINATION_MANIFEST and COORDINATION_MANIFEST_SHA256. This module verifies the hash, writes the
bytes to a read-only file outside the workspace, checks every subject out in its own temporary
detached worktree at its exact SHA from the subject's public repository (anonymously: no token,
no credential helper), runs only the manifest's confirmed acceptance checks, and posts exactly one
typed integration_result. No model is called, nothing is pushed and no pull request is opened.

An infrastructure failure (a malformed manifest, a fetch, a worktree, a SHA that does not verify)
posts nothing typed and exits non-zero: the factory escalates integration_pending. The refusal
rules mirror maku's `coordination checkout` (internal/maku/coordination_checkout.go, git.go).

Not the v4 item 4 hardened safe-output/threat checks: until they land the factory's execute gate
refuses every integration admission, so this runtime is never reached in production.
"""
import hashlib
import json
import os
import re
import signal
import subprocess
import tempfile

MANIFEST_ENV, HASH_ENV = "COORDINATION_MANIFEST", "COORDINATION_MANIFEST_SHA256"
# What a check reads: the read-only manifest and the root holding one worktree per child id.
EXPORT_MANIFEST, EXPORT_ROOT = "MAKU_COORDINATION_MANIFEST", "MAKU_COORDINATION_ROOT"
# Outside /workspace, which the checks' commands run in.
MANIFEST_DIR = "/tmp/maku-coordination"
WORK_ROOT = "/workspace/coordination"
REMOTE_BASE, PROTOCOL = "https://github.com/", "https"

MAX_MANIFEST_BYTES = 8192  # runs.maxCoordinationJSON
MAX_SUBJECTS, MAX_CHECKS = 8, 32
FETCH_TIMEOUT_S, GIT_TIMEOUT_S = 600, 60
CHECK_TIMEOUT_S = 1800
OUTPUT_TAIL = 2000

PASSED, FAILED = "passed", "failed"
REASON_PASSED, REASON_FAILED, REASON_TIMEOUT = "acceptance_passed", "acceptance_failed", "acceptance_timeout"

# coordination_checkout.go's coordIDRE, issuefile.go's repoRE, the factory's 40-hex SHA and
# 64-hex plan hash, and defaultBranch's branchRE.
CHILD_ID = re.compile(r"^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$")
REPOSITORY = re.compile(r"^[A-Za-z0-9-]+/[A-Za-z0-9._-]+$")
SHA40 = re.compile(r"^[0-9a-f]{40}$")
HEX64 = re.compile(r"^[0-9a-f]{64}$")
BRANCH = re.compile(r"^[A-Za-z0-9._/-]{1,200}$")
PURPOSES = ("beforeDependentMerge", "final")


class Infrastructure(Exception):
    """The run cannot vouch for a result: it posts nothing typed and exits non-zero."""


def requested(env: dict) -> bool:
    return bool(env.get(MANIFEST_ENV) or env.get(HASH_ENV))


def verified(env: dict) -> tuple[bytes, dict]:
    """The manifest's exact bytes and its fields, refused unless a triager's under its SHA-256."""
    raw, want = env.get(MANIFEST_ENV, "").encode(), env.get(HASH_ENV, "")
    if env.get("ROLE") != "triager":
        raise Infrastructure("a coordination manifest rides a triager run only")
    if not raw or len(raw) > MAX_MANIFEST_BYTES or not HEX64.match(want) or hashlib.sha256(raw).hexdigest() != want:
        raise Infrastructure("the manifest is not the bytes its SHA-256 names")
    try:
        m = json.loads(raw)
    except ValueError:
        raise Infrastructure("the manifest is not JSON") from None
    validate(m)
    return raw, m


def validate(m) -> None:
    """Refuses, before any git call, a manifest whose ids or commits could name a path or a ref
    this runtime does not build (validCheckoutDocument), or that holds no exact input to check."""
    def bad(why):
        raise Infrastructure("the manifest is malformed: " + why)
    if not isinstance(m, dict) or not HEX64.match(str(m.get("planHash", ""))) or m.get("purpose") not in PURPOSES:
        bad("plan hash or purpose")
    subjects, checks = m.get("subjects"), m.get("acceptance")
    if not isinstance(subjects, list) or not 1 <= len(subjects) <= MAX_SUBJECTS:
        bad("subjects")
    if not isinstance(checks, list) or not 1 <= len(checks) <= MAX_CHECKS:
        bad("acceptance")
    ids, repos = set(), set()
    for s in subjects:
        if not isinstance(s, dict):
            bad("subject")
        cid, repo = str(s.get("childID", "")), str(s.get("repository", ""))
        head, merge = str(s.get("headSHA", "")), str(s.get("mergeCommitSHA", ""))
        if not CHILD_ID.match(cid) or cid in ids:
            bad("child id")
        if not REPOSITORY.match(repo) or repo.split("/")[1] in (".", "..") or repo in repos:
            bad("repository of " + cid)
        if bool(head) == bool(merge) or not SHA40.match(head + merge):
            bad("commit of " + cid)
        ids.add(cid)
        repos.add(repo)
    for c in checks:
        if not isinstance(c, dict) or not isinstance(c.get("check"), str) or not 1 <= len(c["check"]) <= 300:
            bad("acceptance check")


def git_env() -> dict:
    """RunGit's environment, and no configuration but the command line's: the image's
    /etc/gitconfig names the run's credential helper and its hooks, neither of which runs here."""
    return {"PATH": os.environ.get("PATH", "/usr/local/bin:/usr/bin:/bin"), "GIT_TERMINAL_PROMPT": "0",
            "GIT_LFS_SKIP_SMUDGE": "1", "GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": os.devnull}


def git_argv(cwd: str, *args: str) -> list[str]:
    """RunGit's argv: no hook, no LFS smudge, no submodule recursion, no credential, one protocol."""
    return ["git", "-c", "core.hooksPath=" + os.devnull, "-c", "filter.lfs.smudge=", "-c", "filter.lfs.process=",
            "-c", "filter.lfs.required=false", "-c", "submodule.recurse=false", "-c", "fetch.recurseSubmodules=false",
            "-c", "credential.helper=", "-c", "protocol.allow=never", "-c", "protocol.%s.allow=always" % PROTOCOL,
            "-C", cwd, *args]


def git(cwd: str, *args: str) -> str:
    slow = args[0] in ("fetch", "ls-remote") or args[:2] == ("worktree", "add")
    try:
        p = subprocess.run(git_argv(cwd, *args), env=git_env(), capture_output=True, text=True,
                           timeout=FETCH_TIMEOUT_S if slow else GIT_TIMEOUT_S)
    except subprocess.TimeoutExpired:
        raise Infrastructure("git %s timed out" % args[0]) from None
    if p.returncode != 0:
        first = (p.stderr.strip().splitlines() or [""])[0][:200]
        raise Infrastructure("git %s: %s" % (args[0], first))
    return p.stdout


def rev_parse(cwd: str, ref: str) -> str:
    try:
        return git(cwd, "rev-parse", "--verify", "--quiet", ref + "^{commit}").strip()
    except Infrastructure:
        return ""


def default_branch(repo_dir: str) -> str:
    """origin's HEAD branch, as origin says it now (defaultBranch)."""
    for line in git(repo_dir, "ls-remote", "--symref", "origin", "HEAD").splitlines():
        if line.startswith("ref: refs/heads/"):
            name = line[len("ref: refs/heads/"):].split("\t")[0]
            if BRANCH.match(name) and ".." not in name:
                return name
    raise Infrastructure("origin names no default branch")


def checkout(subject: dict, root: str) -> str:
    """One subject's detached worktree at its exact SHA, verified (verify, then worktree add):
    a landed merge must be on origin's default branch; a head is fetched by its id."""
    cid, repo = subject["childID"], subject["repository"]
    sha = subject.get("headSHA") or subject["mergeCommitSHA"]
    repo_dir, path = os.path.join(root, ".repos", cid), os.path.join(root, cid)
    os.makedirs(repo_dir)
    git(repo_dir, "init", "-q", "--bare")
    git(repo_dir, "remote", "add", "origin", REMOTE_BASE + repo + ".git")
    if subject.get("headSHA"):
        git(repo_dir, "fetch", "--no-tags", "origin", sha)
        if rev_parse(repo_dir, sha) != sha:
            raise Infrastructure("%s's head is not on origin" % cid)
    else:
        tracked = "refs/remotes/origin/" + default_branch(repo_dir)
        git(repo_dir, "fetch", "--no-tags", "origin", "+refs/heads/%s:%s" % (tracked[len("refs/remotes/origin/"):], tracked))
        if rev_parse(repo_dir, sha) != sha:
            raise Infrastructure("%s's merge commit is not on origin" % cid)
        git(repo_dir, "merge-base", "--is-ancestor", sha, tracked)  # no longer on the default branch: refused
    git(repo_dir, "worktree", "add", "--detach", path, sha)
    if rev_parse(path, "HEAD") != sha:
        raise Infrastructure("%s is not at %s" % (cid, sha[:12]))
    return path


def write_manifest(raw: bytes, directory: str) -> str:
    """The exact verified bytes, 0444 in a 0555 directory of their own: never written twice."""
    os.makedirs(directory, mode=0o755)
    path = os.path.join(directory, "manifest.json")
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o444)
    with os.fdopen(fd, "wb") as f:
        f.write(raw)
    os.chmod(path, 0o444)
    os.chmod(directory, 0o555)
    return path


def check_env(env: dict, manifest: str, root: str) -> dict:
    """A check's environment: the manifest and the worktrees, and none of the run's credentials,
    broker addresses or model settings."""
    return {"PATH": env.get("PATH", "/usr/local/bin:/usr/bin:/bin"), "HOME": env.get("HOME", "/tmp"),
            "LANG": "C.UTF-8", "TMPDIR": "/tmp", EXPORT_MANIFEST: manifest, EXPORT_ROOT: root}


def run_check(command: str, cwd: str, env: dict, redact, timeout: float = None) -> str:
    """One confirmed check, verbatim (there is no template language), in its own process group,
    which is killed when its shell exits so nothing it started outlives it. Its output goes to a
    file, never a pipe a leftover child could hold open; the redacted tail is the log's."""
    with tempfile.TemporaryFile() as out:
        proc = subprocess.Popen(["bash", "-c", command], cwd=cwd, env=env, start_new_session=True,
                                stdin=subprocess.DEVNULL, stdout=out, stderr=subprocess.STDOUT)
        try:
            result = PASSED if proc.wait(timeout=CHECK_TIMEOUT_S if timeout is None else timeout) == 0 else FAILED
        except subprocess.TimeoutExpired:
            result = REASON_TIMEOUT
        finally:
            try:
                os.killpg(proc.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
            proc.wait()
        size = out.seek(0, os.SEEK_END)
        out.seek(max(0, size - OUTPUT_TAIL))
        tail = redact(out.read().decode(errors="replace")).strip()
    if tail:
        print(tail, flush=True)
    return result


def run(env: dict, post, redact=lambda s: s, manifest_dir: str = MANIFEST_DIR, root: str = WORK_ROOT,
        timeout: float = None) -> int:
    """The integration run: 0 once its one typed result is posted, 1 when it cannot vouch for one."""
    try:
        raw, m = verified(env)
        manifest_hash = env[HASH_ENV]
        path = write_manifest(raw, manifest_dir)
        os.makedirs(root)
        for s in m["subjects"]:
            checkout(s, root)
        print("agent-run integration: %d subject(s) at their exact SHAs, %d check(s)" % (len(m["subjects"]), len(m["acceptance"])), flush=True)
    except (Infrastructure, OSError) as exc:
        print("agent-run integration: not run, nothing reported: %s" % exc, flush=True)
        return 1
    state, reason = PASSED, REASON_PASSED
    cenv = check_env(env, path, root)
    for i, c in enumerate(m["acceptance"], 1):
        got = run_check(c["check"], root, cenv, redact, timeout)
        print("agent-run integration: check %d of %d: %s" % (i, len(m["acceptance"]), got), flush=True)
        if got != PASSED:
            state, reason = FAILED, REASON_TIMEOUT if got == REASON_TIMEOUT else REASON_FAILED
            break
    try:
        post(state, reason, manifest_hash)
    except Exception as exc:  # noqa: BLE001 -- an unposted result is no result: the factory escalates
        print("agent-run integration: the %s result was not posted: %s" % (state, str(exc)[:200]), flush=True)
        return 1
    print("agent-run integration: %s (%s), manifest %s" % (state, reason, manifest_hash), flush=True)
    return 0
