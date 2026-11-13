#!/agent-server/.venv/bin/python
"""git-credential-agent: git's credential helper inside an AgentRun sandbox.

`get` exchanges through identity-proxy :4001 for a <= 1 h installation token
scoped to $REPOSITORY and $ROLE (octo-sts, C6), and caches it in memory
(/run/agent/git is a Memory emptyDir, T3). `token` prints it for gh.
`revoke` deletes it at GitHub; agent-run calls it on its way out, after the
checkpoint push (disruption design §2). With `GIT_TOKEN_FILE` set (run-controller,
adoption 3a) it prints that file's token and never exchanges; it revokes that token
only with `GIT_TOKEN_REVOCABLE=1`, the run-controller's perRun mode, where the
factory minted it for this run alone (D10). A user's PAT is never revoked.
"""
import fcntl
import json
import logging
import os
import sys
import time
import urllib.error
import urllib.request

CACHE = os.environ.get("GIT_TOKEN_CACHE", "/run/agent/git/token.json")
GITHUB_API = os.environ.get("GITHUB_API", "https://api.github.com")
# Installation tokens live 1 h. Refresh with 10 minutes to spare.
LIFETIME_S = 3600
REFRESH_MARGIN_S = 600

log = logging.getLogger("git-credential-agent")


def _token_file() -> str | None:
    # Read at call time: the run-controller's runtime mounts the user's PAT (adoption 3a).
    return os.environ.get("GIT_TOKEN_FILE") or None


def _cached() -> str | None:
    try:
        with open(CACHE) as f:
            entry = json.load(f)
    except (OSError, ValueError):
        return None
    if entry.get("expires_at", 0) - time.time() > REFRESH_MARGIN_S:
        return entry.get("token")
    return None


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


def _exchange() -> str:
    old = None
    try:
        with open(CACHE) as f:
            old = json.load(f).get("token")
    except (OSError, ValueError):
        pass
    url = "{}?scope={}&identity=agent-{}".format(os.environ["STS_URL"], os.environ["REPOSITORY"], os.environ["ROLE"])
    with urllib.request.urlopen(url, timeout=15) as resp:
        new_token = json.load(resp)["token"]
    if old and old != new_token:
        # Revoke the token being replaced before the cache stops pointing at
        # it, so a leaked old token has the shortest possible life.
        _revoke_value(old)
    fd = os.open(CACHE, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(fd, "w") as f:
        json.dump({"token": new_token, "expires_at": time.time() + LIFETIME_S}, f)
    return new_token


def _read(path: str) -> str:
    with open(path) as f:
        return f.read().strip()


def token() -> str:
    path = _token_file()
    if path and os.environ.get("GIT_TOKEN_REVOCABLE") != "1":
        return _read(path)  # a PAT: never revoked, nothing to serialize with
    if path:
        # The run's minted token (perRun): read under revoke()'s lock, so a read never
        # interleaves with the revoke on the way out.
        lock_fd = os.open(CACHE + ".lock", os.O_CREAT | os.O_RDWR, 0o600)
        try:
            fcntl.flock(lock_fd, fcntl.LOCK_EX)
            return _read(path)
        finally:
            fcntl.flock(lock_fd, fcntl.LOCK_UN)
            os.close(lock_fd)
    # Serialize concurrent `get`s on the cache file itself: two credential
    # helper invocations racing here must not both exchange and stomp on
    # each other's cache write.
    lock_fd = os.open(CACHE + ".lock", os.O_CREAT | os.O_RDWR, 0o600)
    try:
        fcntl.flock(lock_fd, fcntl.LOCK_EX)
        return _cached() or _exchange()
    finally:
        fcntl.flock(lock_fd, fcntl.LOCK_UN)
        os.close(lock_fd)


def revoke() -> None:
    path = _token_file()
    if path and os.environ.get("GIT_TOKEN_REVOCABLE") != "1":
        return  # the user's PAT is theirs to revoke, not the run's
    # Serialize with token() on the same lock to ensure revoke atomically
    # reads, revokes, and deletes the cache. Without this, a concurrent
    # token() exchange can write a new token after we read the old one but
    # before we delete the cache, leaving it unrevoked.
    lock_fd = os.open(CACHE + ".lock", os.O_CREAT | os.O_RDWR, 0o600)
    try:
        fcntl.flock(lock_fd, fcntl.LOCK_EX)
        if path:
            # The run's own minted token (perRun): the factory deletes its Secret at the run's end.
            try:
                with open(path) as f:
                    value = f.read().strip()
            except OSError:
                return
            if value:
                _revoke_value(value)
            return
        try:
            with open(CACHE) as f:
                value = json.load(f)["token"]
        except (OSError, ValueError, KeyError):
            return
        _revoke_value(value)
        try:
            os.remove(CACHE)
        except OSError:
            pass
    finally:
        fcntl.flock(lock_fd, fcntl.LOCK_UN)
        os.close(lock_fd)


def main(argv: list[str]) -> int:
    action = argv[1] if len(argv) > 1 else ""
    if action == "get":
        attrs = dict(line.split("=", 1) for line in sys.stdin.read().splitlines() if "=" in line)
        if attrs.get("host") != "github.com":
            return 0
        sys.stdout.write("username=x-access-token\npassword={}\n".format(token()))
    elif action == "token":
        sys.stdout.write(token() + "\n")
    elif action == "revoke":
        revoke()
    # `store` and `erase` are no-ops: the cache is ours, not git's.
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
