#!/agent-server/.venv/bin/python
"""agent-push: `git push` as a push output (hardening H3, HR23).

When the factory applies a run's pushes (SAFE_OUTPUTS names `push`), the git shim hands `git push` here.
The run's branch, and only it, is packed into a git bundle that excludes what the remote already holds
(its remote-tracking base and branch heads), and the bundle is requested through the harness-only
`output_push` tool. The factory checks it and pushes it as the publisher. This then reads
`output_status` for up to WAIT_S seconds and prints the answer:

    applied <sha>                                  exit 0
    refused <reason>                               exit 1
    waiting for a maintainer's approval (<id>)     exit 0

`--delete`, a tag or another branch exits 3. A bundle over MAX_BUNDLE exits 1 with push_size before
any call. This is advisory, as the gh wrapper is: the run's read-only token is the boundary, and the
factory checks every bundle it is handed.

agent-run calls request() for the disruption checkpoint (no wait, HR33) and the tester's contract.
"""
import base64
import json
import os
import re
import subprocess
import sys
import tempfile
import time

MAX_BUNDLE = 8 << 20  # the broker's cap on a decoded bundle (HR22)
WAIT_S = 90
POLL_S = 1
CALL_TIMEOUT_S = 60  # an 8 MiB bundle is about 11 MiB of base64 in one request
GIT_TIMEOUT_S = 60
ONLY = 3
SHA = re.compile(r"^[0-9a-f]{40}$")

# git push's options that take a separate value; every other option is a flag.
VALUED = {"-o", "--push-option", "--repo", "--receive-pack", "--exec"}
# Options that push something other than the run's branch.
WIDE = {"--delete", "--tags", "--follow-tags", "--mirror", "--all", "--branches", "--prune"}


class PushSize(Exception):
    """The bundle is over MAX_BUNDLE: refused push_size, before any call."""


class Only(Exception):
    """The push names something other than the run's branch."""


def enforced(env) -> bool:
    return "push" in [s.strip() for s in env.get("SAFE_OUTPUTS", "").split(",")]


def real_git(env=None) -> str:
    return (env or {}).get("REAL_GIT") or os.environ.get("REAL_GIT") or "/usr/bin/git"


def git(repo, *args, env=None, timeout=GIT_TIMEOUT_S, check=True) -> str:
    """The real git's stdout, run in repo (the current directory when None); raises when it fails. env is laid
    over the process's environment, and a None value unsets a variable."""
    full = {k: v for k, v in {**os.environ, **(env or {})}.items() if v is not None}
    p = subprocess.run([real_git(env), *args], cwd=repo, capture_output=True, text=True, errors="replace",
                       timeout=timeout, env=full)
    if check and p.returncode:
        raise RuntimeError("git %s: %s" % (args[0], p.stderr.strip()[:200]))
    return p.stdout.strip() if p.returncode == 0 else ""


def _left(deadline) -> float:
    if deadline is None:
        return GIT_TIMEOUT_S
    left = deadline - time.monotonic()
    if left <= 0:
        raise TimeoutError("no time left for the push request")
    return left


def held(repo, env, branch: str) -> list[str]:
    """The commits the remote holds, as the clone last read them: its base and the run's branch."""
    refs = ["refs/remotes/origin/HEAD", "refs/remotes/origin/" + (env.get("BASE_REF") or "main"),
            "refs/remotes/origin/main", "refs/remotes/origin/" + branch]
    out = []
    for ref in refs:
        sha = git(repo, "rev-parse", "--verify", "--quiet", ref + "^{commit}", env=env, check=False)
        if sha and sha not in out:
            out.append(sha)
    return out


def bundle(repo, env, branch: str, sha: str, deadline=None) -> bytes | None:
    """A bundle of refs/heads/<branch> at sha, less what the remote holds; None when it holds sha already.
    Built in a scratch repository that borrows the clone's objects, so the clone's config and hooks, which
    are the agent's, play no part, and sha needs no local branch (the tester's contract has none)."""
    objects = os.path.abspath(os.path.join(repo or os.getcwd(),
                                           git(repo, "rev-parse", "--git-path", "objects", env=env)))
    have = held(repo, env, branch)
    with tempfile.TemporaryDirectory() as scratch:
        genv = {**(env or {}), "GIT_ALTERNATE_OBJECT_DIRECTORIES": objects, "GIT_CONFIG_GLOBAL": os.devnull,
                "GIT_DIR": None, "GIT_WORK_TREE": None}
        bare = os.path.join(scratch, "b.git")
        git(None, "init", "-q", "--bare", bare, env=genv, timeout=_left(deadline))
        if git(None, "--git-dir", bare, "rev-list", "--count", sha, *["^" + h for h in have], env=genv,
               timeout=_left(deadline)) == "0":
            return None
        git(None, "--git-dir", bare, "update-ref", "refs/heads/" + branch, sha, env=genv, timeout=_left(deadline))
        path = os.path.join(scratch, "push.bundle")
        git(None, "--git-dir", bare, "bundle", "create", "-q", path, "refs/heads/" + branch,
            *["^" + h for h in have], env=genv, timeout=_left(deadline))
        with open(path, "rb") as f:
            data = f.read(MAX_BUNDLE + 1)
            size = os.fstat(f.fileno()).st_size
    if size > MAX_BUNDLE:
        raise PushSize("push_size: the bundle is %d bytes, over the %d-byte cap" % (size, MAX_BUNDLE))
    return data


def client(env, timeout: float = CALL_TIMEOUT_S):
    """agent-run's MCP client: the MCP_TOKEN_FILE bearer and the harness marker output_push needs."""
    sys.path.insert(0, os.path.dirname(os.path.realpath(__file__)))
    import agent_run

    if not env.get("MCP_URL"):
        raise RuntimeError("the run has no MCP_URL: no push can be requested")
    c = agent_run.ProgressNotes(env["MCP_URL"], env.get("ROLE") or "agent", token_file=env.get("MCP_TOKEN_FILE"))
    c.timeout = timeout
    return c


def call(c, tool: str, arguments: dict) -> dict:
    result = c.harness_call(tool, arguments) or {}
    if isinstance(result.get("structuredContent"), dict):
        return result["structuredContent"]
    text = "".join(p.get("text", "") for p in result.get("content") or [] if isinstance(p, dict))
    return json.loads(text) if text.strip().startswith("{") else {}


def request(env, rev: str = "HEAD", repo=None, deadline=None, mcp=None) -> str | None:
    """Requests a push of rev to the run's branch and returns the output id, without waiting for the
    answer; None when the remote holds rev already. Raises PushSize for a bundle over MAX_BUNDLE."""
    branch = env["BRANCH"]
    sha = git(repo, "rev-parse", "--verify", rev + "^{commit}", env=env, timeout=_left(deadline))
    data = bundle(repo, env, branch, sha, deadline)
    if data is None:
        return None
    c = mcp or client(env, min(CALL_TIMEOUT_S, _left(deadline)))
    out = call(c, "output_push", {"bundle": base64.b64encode(data).decode()})
    if not out.get("outputId"):
        raise RuntimeError("output_push answered no outputId")
    return out["outputId"]


def wait(env, output_id: str, mcp=None, wait_s: float | None = None, sleep=time.sleep, clock=time.monotonic) -> dict:
    """output_status until it is answered (applied, refused) or held (waiting), at most wait_s (WAIT_S) seconds."""
    c = mcp or client(env)
    end = clock() + (WAIT_S if wait_s is None else wait_s)
    while True:
        status = call(c, "output_status", {"outputId": output_id})
        if status.get("status") in ("applied", "refused", "waiting") or clock() + POLL_S > end:
            return status
        sleep(POLL_S)


def settle(repo, env, branch: str, sha: str, output_id: str, status: dict) -> tuple[int, str]:
    """The exit code and the line for an answer. An applied push moves the remote-tracking branch, as git
    push would, so the harness's count of unpushed commits reads it."""
    kind = status.get("status")
    if kind == "applied":
        obj = str(status.get("object") or "").rstrip("/").rsplit("/", 1)[-1]
        git(repo, "update-ref", "refs/remotes/origin/" + branch, sha, env=env, check=False)
        return 0, "applied %s" % (obj if SHA.match(obj) else sha)
    if kind == "refused":
        return 1, "refused %s" % (status.get("reason") or "without a reason")
    if kind == "waiting":
        return 0, "waiting for a maintainer's approval (%s)" % status.get("approvalId", "")
    if kind == "unknown":
        return 1, "refused: the factory does not know output %s" % output_id
    return 0, "requested %s: no answer within %d s; the factory answers it later" % (output_id, WAIT_S)


def target(argv: list[str], repo, env, branch: str) -> tuple[str, bool]:
    """The commit `git push argv` would put on the run's branch, and whether it is a dry run. Raises Only
    for anything else: a deletion, a tag, another branch, or every branch."""
    current = git(repo, "symbolic-ref", "-q", "--short", "HEAD", env=env, check=False)
    dry, positional, args = False, [], iter(argv)
    for a in args:
        if a == "--":
            positional += list(args)
            break
        if a in VALUED:
            next(args, None)
        elif a.split("=", 1)[0] in WIDE or a == "--dry-run":
            if a != "--dry-run":
                raise Only(a)
            dry = True
        elif a.startswith("-") and not a.startswith("--") and len(a) > 1:
            flags = a[1:]
            if "d" in flags:
                raise Only(a)
            dry = dry or "n" in flags
            if flags.endswith("o"):
                next(args, None)
        elif not a.startswith("-"):
            positional.append(a)
    specs = positional[1:] or ["HEAD"]
    shas = set()
    for spec in specs:
        spec = spec[1:] if spec.startswith("+") else spec
        src, colon, dst = spec.partition(":")
        if not src:
            raise Only(spec)  # :branch deletes
        if not colon:
            dst = current if src == "HEAD" else src
        elif dst == "HEAD":
            dst = current
        dst = (dst or "").removeprefix("refs/heads/")
        if dst != branch:
            raise Only(spec)
        src = "refs/heads/" + src if not colon and src != "HEAD" and not src.startswith("refs/") else src
        sha = git(repo, "rev-parse", "--verify", "--quiet", src + "^{commit}", env=env, check=False)
        if not sha:
            raise Only(spec)
        shas.add(sha)
    if len(shas) != 1:
        raise Only(" ".join(specs))
    return shas.pop(), dry


USAGE = """usage: git push [<options>] [<repository> [<refspec>...]]

This run's pushes are applied by the factory: the run's branch, %s, is sent as a git bundle
through output_push, checked, then pushed. --delete, tags and other branches are refused."""


def main(argv: list[str], env=None) -> int:
    env = dict(os.environ if env is None else env)
    if not enforced(env):
        os.execv(real_git(env), [real_git(env), "push", *argv])
    branch = env.get("BRANCH", "")
    if any(a in ("-h", "--help") for a in argv):
        print(USAGE % (branch or "unset"))
        return 0
    if not branch:
        print("agent-push: the run has no BRANCH", file=sys.stderr)
        return 1
    try:
        sha, dry = target(argv, None, env, branch)
    except Only:
        print("agent-push: push only %s" % branch, file=sys.stderr)
        return ONLY
    try:
        if dry:
            data = bundle(None, env, branch, sha)
            print("would request a push of %s to %s (%d bytes)" % (sha, branch, len(data or b"")))
            return 0
        c = client(env)
        output_id = request(env, sha, mcp=c)
        if output_id is None:
            print("Everything up-to-date")
            return 0
        print("agent-push: requested %s as output %s" % (sha, output_id), file=sys.stderr, flush=True)
        code, line = settle(None, env, branch, sha, output_id, wait(env, output_id, c))
    except PushSize as exc:
        print("agent-push: %s" % exc, file=sys.stderr)
        return 1
    except Exception as exc:  # noqa: BLE001 -- the agent reads one line, never a traceback
        print("agent-push: the push was not requested: %s" % str(exc)[:300], file=sys.stderr)
        return 1
    print(line, flush=True)
    return code


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
