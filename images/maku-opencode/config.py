"""OpenCode V2 workspace layout and config file writing helpers."""
import json
import os
import secrets
import tempfile

OPENCODE_ROOT = "/workspace/.opencode"
SESSION_ID_FILE = "/workspace/.agent-opencode-session-id"
PASSWORD_FILE = "/workspace/.agent-opencode-password"
TERMINAL_FILE = "/workspace/.agent-opencode-terminal.json"

# OpenCode OTLP keys must never reach the OpenCode server process (design §7.6).
OPENCODE_BLOCKED_OTEL_KEYS = (
    "OTEL_EXPORTER_OTLP_ENDPOINT",
    "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT",
    "OTEL_EXPORTER_OTLP_LOGS_ENDPOINT",
    "OTEL_EXPORTER_OTLP_METRICS_ENDPOINT",
    "OTEL_ENDPOINT",
    "OPENCODE_ENABLE_EXA",
)


def atomic_write(path: str, text: str, mode: int = 0o600) -> None:
    """Atomically write text to path with restrictive permissions."""
    parent = os.path.dirname(path) or "."
    os.makedirs(parent, exist_ok=True)
    fd, tmp = tempfile.mkstemp(prefix=".tmp-", dir=parent)
    try:
        os.chmod(tmp, mode)
        with os.fdopen(fd, "w", encoding="utf-8") as f:
            f.write(text)
            f.flush()
            os.fsync(f.fileno())
        os.replace(tmp, path)
    except Exception:
        try:
            os.unlink(tmp)
        except OSError:
            pass
        raise


def workspace_paths(root: str = OPENCODE_ROOT) -> dict[str, str]:
    """Return isolated OpenCode directories kept under /workspace/.opencode/."""
    paths = {
        "root": root,
        "home": os.path.join(root, "home"),
        "config": os.path.join(root, "config"),
        "data": os.path.join(root, "data"),
        "state": os.path.join(root, "state"),
        "cache": os.path.join(root, "cache"),
    }
    for p in paths.values():
        os.makedirs(p, exist_ok=True)
    return paths


def ensure_server_password(path: str = PASSWORD_FILE) -> str:
    """Read or create the per-run loopback server password (mode 0600)."""
    try:
        with open(path, encoding="utf-8") as f:
            pw = f.read().strip()
            if pw:
                return pw
    except FileNotFoundError:
        pass
    pw = secrets.token_urlsafe(32)
    atomic_write(path, pw + "\n", mode=0o600)
    return pw


def write_config(config_dir: str, cfg: dict) -> str:
    """Write opencode.jsonc atomically inside config_dir."""
    path = os.path.join(config_dir, "opencode.jsonc")
    atomic_write(path, json.dumps(cfg, indent=2, sort_keys=True) + "\n", mode=0o600)
    return path
