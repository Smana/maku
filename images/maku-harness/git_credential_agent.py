#!/agent-server/.venv/bin/python
"""git-credential-agent: git's credential helper inside an AgentRun sandbox.

`get` prints the token in `GIT_TOKEN_FILE`, which the run-controller mounts: the run's own token
the factory minted for its repository and role (perRun, D10), or a user's PAT. `token` prints it
for gh. `revoke` deletes the minted token at GitHub, only with `GIT_TOKEN_REVOCABLE=1` (perRun),
where the factory minted it for this run alone; agent-run calls it on its way out, after the
checkpoint push (disruption design §2). A user's PAT is never revoked. octo-sts's exchange
(`STS_URL`), the XR's token source, was removed in v4 step 8 (R3): a run that names it is refused.
"""
import fcntl
import logging
import os
import sys
import urllib.error
import urllib.request

# token() and revoke() serialize on this lock, in the run's Memory emptyDir (T3).
LOCK = os.environ.get("GIT_TOKEN_LOCK", "/run/agent/git/token.lock")
GITHUB_API = os.environ.get("GITHUB_API", "https://api.github.com")

log = logging.getLogger("git-credential-agent")


class Removed(RuntimeError):
    """A token source this harness no longer has."""


def _token_file() -> str:
    # Read at call time: the run-controller mounts the run's token (adoption 3a, D10).
    if os.environ.get("STS_URL"):
        raise Removed("STS_URL (octo-sts) was removed in v4 step 8 (R3): the run's token is the run-controller's GIT_TOKEN_FILE")
    path = os.environ.get("GIT_TOKEN_FILE")
    if not path:
        raise Removed("GIT_TOKEN_FILE is unset: the run-controller mounts the run's token there")
    return path


_revoke_skipped_logged = False


def _revoke_value(value: str) -> None:
    """Best-effort revoke: the exit path must not traceback over a token GitHub
    already considers gone (expired, or revoked by an earlier attempt). Under
    FORGE_READ=1 (hardening H4, HR36) the run has no api.github.com egress, so the
    revoke is skipped and logged once: the token is read-only and expires within the hour."""
    global _revoke_skipped_logged
    if os.environ.get("FORGE_READ") == "1":
        if not _revoke_skipped_logged:
            log.info("revoke skipped: this run has no api.github.com egress (FORGE_READ); the token expires within the hour")
            _revoke_skipped_logged = True
        return
    req = urllib.request.Request(GITHUB_API + "/installation/token", method="DELETE", headers={"Authorization": "Bearer " + value})
    try:
        urllib.request.urlopen(req, timeout=10)
    except urllib.error.HTTPError as exc:
        log.warning("revoke: GitHub returned %s", exc.code)
    except urllib.error.URLError as exc:
        log.warning("revoke: %s", exc.reason)


def _read(path: str) -> str:
    with open(path) as f:
        return f.read().strip()


def _locked(fn):
    lock_fd = os.open(LOCK, os.O_CREAT | os.O_RDWR, 0o600)
    try:
        fcntl.flock(lock_fd, fcntl.LOCK_EX)
        return fn()
    finally:
        fcntl.flock(lock_fd, fcntl.LOCK_UN)
        os.close(lock_fd)


def token() -> str:
    path = _token_file()
    if os.environ.get("GIT_TOKEN_REVOCABLE") != "1":
        return _read(path)  # a PAT: never revoked, nothing to serialize with
    # The run's minted token (perRun): read under revoke()'s lock, so a read never interleaves
    # with the revoke on the way out.
    return _locked(lambda: _read(path))


def revoke() -> None:
    path = os.environ.get("GIT_TOKEN_FILE")
    if not path or os.environ.get("GIT_TOKEN_REVOCABLE") != "1":
        return  # no token of the run's own: the user's PAT is theirs to revoke

    def run():
        # The run's own minted token (perRun): the factory deletes its Secret at the run's end.
        try:
            value = _read(path)
        except OSError:
            return
        if value:
            _revoke_value(value)

    _locked(run)


def main(argv: list[str]) -> int:
    action = argv[1] if len(argv) > 1 else ""
    try:
        if action == "get":
            attrs = dict(line.split("=", 1) for line in sys.stdin.read().splitlines() if "=" in line)
            if attrs.get("host") != "github.com":
                return 0
            sys.stdout.write("username=x-access-token\npassword={}\n".format(token()))
        elif action == "token":
            sys.stdout.write(token() + "\n")
        elif action == "revoke":
            revoke()
    except Removed as exc:
        sys.stderr.write("git-credential-agent: {}\n".format(exc))
        return 1
    # `store` and `erase` are no-ops: the token is the run-controller's, not git's.
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
