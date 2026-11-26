"""OpenCode 2.3.0 loopback server driver for agent-run (OpenCode design §6.1)."""
import base64
import json
import os
import re
import shutil
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

import config as oc_config
from harness_driver import HarnessDriver
from harness_types import ConversationRequest, EventPage

OPENCODE_BIN = "/usr/local/bin/opencode"
DEFAULT_HOST = "127.0.0.1"
DEFAULT_PORT = 8000
SESSION_ID_RE = re.compile(r"^ses_[A-Za-z0-9_-]+$")
PLACEHOLDER_KEY = "injected-by-identity-proxy"


# The run's clone (agent_run.REPO_DIR): OpenCode must start inside it.
REPO_DIR = "/workspace/repo"

def _read_api_key(env: dict, var: str = "LLM_API_KEY_FILE") -> str:
    path = env.get(var)
    if not path:
        return PLACEHOLDER_KEY
    with open(path, encoding="utf-8") as f:
        return f.read().strip()


def _valid_session_id(value: str) -> bool:
    return bool(value and len(value) <= 128 and SESSION_ID_RE.match(value))


class OpenCodeDriver(HarnessDriver):
    """Drives a version-pinned `opencode serve` process over loopback."""

    def __init__(
        self,
        env: dict,
        *,
        trace_env: dict | None = None,
        runner=None,
        host: str = DEFAULT_HOST,
        port: int | None = None,
        root: str | None = None,
        session_file: str | None = None,
        password_file: str | None = None,
        terminal_file: str | None = None,
        mcp_proxy=None,
    ):
        self.env = dict(env)
        self.trace_env = dict(trace_env or {})
        self.runner = runner
        self.host = host
        self.port = port if port is not None else int(env.get("OPENCODE_PORT", DEFAULT_PORT))
        self.base = f"http://{self.host}:{self.port}"
        self.root = root or env.get("OPENCODE_WORKSPACE_ROOT") or oc_config.OPENCODE_ROOT
        self.session_file = session_file or env.get("OPENCODE_SESSION_ID_FILE") or oc_config.SESSION_ID_FILE
        self.password_file = password_file or env.get("OPENCODE_PASSWORD_FILE") or oc_config.PASSWORD_FILE
        self.terminal_file = terminal_file or env.get("OPENCODE_TERMINAL_FILE") or oc_config.TERMINAL_FILE
        self.server: subprocess.Popen | None = None
        self.password: str = ""
        self.paths: dict[str, str] = {}
        self.mcp_proxy = mcp_proxy
        self._loop_events: list[dict] = []
        self._seen_event_ids: set[str] = set()
        self._stuck_pattern: str | None = None
        self._event_tracer = None
        self._active_cid: str = ""
        self._role_name: str = env.get("ROLE") or "implementer"

    def _auth_header(self) -> str:
        token = base64.b64encode(f"opencode:{self.password}".encode("utf-8")).decode("ascii")
        return "Basic " + token

    def _request(self, method: str, path: str, body: dict | None = None, timeout: float = 30) -> dict:
        data = json.dumps(body).encode("utf-8") if body is not None else None
        headers = {"Authorization": self._auth_header()}
        tp = self.env.get("TRACEPARENT") or self.trace_env.get("TRACEPARENT")
        if tp:
            headers["traceparent"] = tp
        ts = self.env.get("TRACESTATE") or self.trace_env.get("TRACESTATE")
        if ts:
            headers["tracestate"] = ts
        if data is not None:
            headers["Content-Type"] = "application/json"
        req = urllib.request.Request(self.base + path, data=data, method=method, headers=headers)
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            raw = resp.read()
            return json.loads(raw.decode("utf-8")) if raw else {}

    def _server_env(self) -> dict[str, str]:
        out = dict(self.env)
        if not out.get("PATH"):
            out["PATH"] = os.environ.get("PATH", "/usr/local/bin:/usr/bin:/bin")
        # Never export OpenCode's native OTLP logs, metrics, or traces (design §7.6).
        for k in list(out):
            if k in oc_config.OPENCODE_BLOCKED_OTEL_KEYS or k.startswith("OTEL_"):
                out.pop(k, None)
        out["HOME"] = self.paths["home"]
        out["XDG_CONFIG_HOME"] = self.paths["config"]
        out["XDG_DATA_HOME"] = self.paths["data"]
        out["XDG_STATE_HOME"] = self.paths["state"]
        out["XDG_CACHE_HOME"] = self.paths["cache"]
        out["OPENCODE_CONFIG_DIR"] = self.paths["config"]
        out["OPENCODE_DISABLE_CLAUDE_CODE"] = "1"
        out["OPENCODE_SERVER_PASSWORD"] = self.password
        out["MAKU_LLM_API_KEY"] = _read_api_key(self.env, "LLM_API_KEY_FILE")
        if self.env.get("LIGHT_BASE_URL"):
            out["MAKU_LIGHT_API_KEY"] = _read_api_key(self.env, "LIGHT_API_KEY_FILE")
        return out

    def _write_initial_config(self, cfg: dict | None = None) -> None:
        if cfg is None:
            try:
                import role_config
                proxy_url = self.mcp_proxy.url if self.mcp_proxy is not None else None
                cfg = role_config.role_config(self.env, mcp_proxy_url=proxy_url)
            except ImportError:
                cfg = {"update": "disable", "share": "disabled"}
        oc_config.write_config(self.paths["config"], cfg)

    def start(self) -> None:
        self.paths = oc_config.workspace_paths(self.root)
        self.password = oc_config.ensure_server_password(self.password_file)
        if self.mcp_proxy is None and self.env.get("MCP_URL"):
            try:
                import mcp_proxy as mp
                self.mcp_proxy = mp.MCPCredentialProxy(
                    upstream_url=self.env["MCP_URL"],
                    token_file=self.env.get("MCP_TOKEN_FILE"),
                    traceparent=self.env.get("TRACEPARENT"),
                )
                self.mcp_proxy.start()
            except ImportError:
                pass
        self._write_initial_config()
        binary = shutil.which("opencode") or OPENCODE_BIN
        cmd = [binary, "serve", "--hostname", self.host, "--port", str(self.port)]
        # OpenCode bounds its tools to the directory it starts in: anything outside is
        # external_directory, denied for every role. agent_run starts the driver before it clones,
        # so the clone's directory is created here (git clones into an empty directory) and the
        # server always starts in it. Falling back to the config root made every read of the
        # repository "external" and the run ended stuck (gcp-0 live trial, 2026-11-25).
        cwd = self.env.get("OPENCODE_WORKDIR") or REPO_DIR
        os.makedirs(cwd, exist_ok=True)
        # No secret on argv; stdout/stderr inherited for pod logging.
        self.server = subprocess.Popen(cmd, cwd=cwd, env=self._server_env())

    def wait_ready(self, deadline_s: int = 120) -> None:
        end = time.monotonic() + deadline_s
        while time.monotonic() < end:
            if self.server is not None and self.server.poll() is not None:
                raise RuntimeError("opencode serve exited at start-up with code %s" % self.server.returncode)
            try:
                info = self._request("GET", "/api/info", timeout=2)
                if isinstance(info, dict) and info.get("version"):
                    return
            except (urllib.error.URLError, OSError, ValueError):
                time.sleep(0.25)
        raise TimeoutError("opencode serve never became ready")

    def persisted_session_id(self) -> str:
        try:
            with open(self.session_file, encoding="utf-8") as f:
                sid = f.read(256).strip()
            return sid if _valid_session_id(sid) else ""
        except OSError:
            return ""

    def persisted(self) -> bool:
        if self.env.get("AGENT_SUSPENDABLE") != "true":
            return False
        return bool(self.persisted_session_id())

    def search_available(self) -> bool:
        if self.env.get("AGENT_SEARCH") != "1":
            return False
        if self.env.get("ROLE") != "implementer":
            return False
        try:
            import search  # noqa: F401
            return True
        except ImportError:
            return False

    def create_conversation(self, request: ConversationRequest) -> str:
        # Regenerate role config with the final rules/system prompt if role_config is available.
        try:
            import role_config
            proxy_url = self.mcp_proxy.url if self.mcp_proxy is not None else None
            cfg = role_config.role_config(request.env, rules=request.rules, mcp_proxy_url=proxy_url)
            self._write_initial_config(cfg)
            try:
                self._request("POST", "/api/location/reload", timeout=5)
            except (urllib.error.URLError, OSError, ValueError):
                pass
        except ImportError:
            pass

        existing = self.persisted_session_id() if request.resumed else ""
        if existing:
            try:
                self._request("GET", "/api/session/" + urllib.parse.quote(existing, safe=""), timeout=10)
                self._active_cid = existing
                self._attach_tracer(request.span)
                return existing
            except (urllib.error.URLError, OSError):
                pass

        role = request.env.get("ROLE") or self._role_name
        run_id = request.env.get("RUN_ID") or "run"
        model = (request.env.get("MODEL") or self.env.get("MODEL") or "agent-default").strip()
        effort = (request.env.get("LLM_REASONING_EFFORT") or self.env.get("LLM_REASONING_EFFORT") or "high").strip().lower()
        if effort not in ("low", "medium", "high"):
            effort = "high"
        # Supplying title suppresses OpenCode's extra auto-title LLM call; explicit model ref activates the effort variant.
        created = self._request(
            "POST",
            "/api/session",
            {
                "agent": role,
                "title": f"maku-{role}-{run_id}",
                "model": {"providerID": "maku", "id": model, "variant": effort},
            },
            timeout=15,
        )
        sid = (created.get("data") or {}).get("id") or ""
        if not _valid_session_id(sid):
            raise RuntimeError("opencode returned invalid session id: %r" % (sid,))
        oc_config.atomic_write(self.session_file, sid + "\n", mode=0o600)
        self._active_cid = sid
        self._attach_tracer(request.span)
        # A new session always gets the task. request.resumed also means "the branch was already
        # pushed" (every retry and every reviewer): only a reattached session, returned above, has it.
        self.send(sid, request.task, run=True)
        return sid

    def _attach_tracer(self, span) -> None:
        if span is None:
            return
        try:
            import event_tracing
            self._event_tracer = event_tracing.EventTracer(root_span=span)
        except ImportError:
            pass

    def on_event(self, event: dict) -> None:
        """Feed normalized events to Maku's loop_guard and metadata-only EventTracer."""
        eid = event.get("id") or ""
        if eid and eid in self._seen_event_ids:
            return
        if eid:
            self._seen_event_ids.add(eid)
        if self._event_tracer is not None:
            self._event_tracer.consume(event)
        self._loop_events.append(event)
        if not self._stuck_pattern and self._active_cid:
            try:
                import loop_guard
                pattern = loop_guard.detect(self._loop_events)
                if pattern:
                    self.mark_stuck(self._active_cid, pattern)
            except ImportError:
                pass

    def create_search_session(self, *, query: str, model: str, effort: str = "low", max_steps: int = 40) -> str:
        """Create an independent read-only search session (no parentID, design §7.2)."""
        provider_id = "maku_light" if (self.env.get("LIGHT_BASE_URL") and self.env.get("LIGHT_MODEL")) else "maku"
        variant = effort if effort in ("low", "medium", "high") else "low"
        created = self._request(
            "POST",
            "/api/session",
            {
                "agent": "search",
                "title": "maku-search",
                "model": {"providerID": provider_id, "id": model, "variant": variant},
            },
            timeout=15,
        )
        sid = (created.get("data") or {}).get("id") or ""
        if not _valid_session_id(sid):
            raise RuntimeError("opencode returned invalid search session id: %r" % (sid,))
        self.send(sid, query, run=True)
        return sid

    def _read_terminal_marker(self) -> dict | None:
        try:
            with open(self.terminal_file, encoding="utf-8") as f:
                data = json.loads(f.read(4096))
            if isinstance(data, dict) and data.get("reason") == "stuck" and isinstance(data.get("pattern"), str):
                return data
        except (OSError, ValueError):
            pass
        return None

    def mark_stuck(self, conversation_id: str, pattern: str) -> None:
        self._stuck_pattern = pattern
        oc_config.atomic_write(
            self.terminal_file,
            json.dumps({"reason": "stuck", "pattern": pattern}) + "\n",
            mode=0o600,
        )
        try:
            self.interrupt(conversation_id, timeout=5)
        except Exception as exc:  # noqa: BLE001
            print("agent-run: opencode stuck interrupt failed: %s" % exc, file=sys.stderr, flush=True)

    def status(self, conversation_id: str) -> str:
        if self._stuck_pattern or self._read_terminal_marker():
            return "stuck"
        q_sid = urllib.parse.quote(conversation_id, safe="")
        perms = (self._request("GET", f"/api/session/{q_sid}/permission", timeout=10).get("data") or [])
        if perms:
            return "waiting_for_confirmation"
        active = (self._request("GET", "/api/session/active", timeout=10).get("data") or {})
        if conversation_id in active:
            return "running"
        info = (self._request("GET", f"/api/session/{q_sid}", timeout=10).get("data") or {})
        outcome = info.get("outcome") or ""
        if outcome == "succeeded":
            return "finished"
        if outcome == "failed":
            return "error"
        if outcome == "interrupted":
            return "paused"
        return "idle"

    def _normalize_message(self, msg: dict) -> list[dict]:
        """Translate one OpenCode 2.3.0 Session.Message.Info into Maku's normalized event dicts."""
        mid = str(msg.get("id") or "")
        mtype = msg.get("type") or ""
        out: list[dict] = []
        if mtype == "user":
            out.append({
                "id": f"{mid}:user",
                "kind": "MessageEvent",
                "source": "user",
                "llm_message": {"content": [{"type": "text", "text": str(msg.get("text") or "")}]},
            })
            return out
        if mtype == "assistant":
            texts: list[dict] = []
            for idx, part in enumerate(msg.get("content") or []):
                if not isinstance(part, dict):
                    continue
                ptype = part.get("type")
                if ptype == "text" and part.get("text"):
                    texts.append({"type": "text", "text": str(part["text"])})
                elif ptype == "tool":
                    call_id = str(part.get("id") or f"{mid}-tool-{idx}")
                    tool_name = str(part.get("name") or "")
                    state = part.get("state") if isinstance(part.get("state"), dict) else {}
                    tstatus = state.get("status") or ""
                    tool_in = state.get("input") if isinstance(state.get("input"), dict) else {}
                    if tstatus in ("running", "completed", "error"):
                        out.append({
                            "id": f"{mid}:tool:{call_id}:call",
                            "kind": "ActionEvent",
                            "source": "agent",
                            "tool_name": tool_name,
                            "tool_call_id": call_id,
                            "action": tool_in,
                        })
                    if tstatus == "completed":
                        content_parts = []
                        for c in state.get("content") or []:
                            if isinstance(c, dict) and c.get("type") == "text":
                                content_parts.append({"type": "text", "text": str(c.get("text") or "")})
                        out.append({
                            "id": f"{mid}:tool:{call_id}:obs",
                            "kind": "ObservationEvent",
                            "source": "environment",
                            "tool_name": tool_name,
                            "tool_call_id": call_id,
                            "observation": {"content": content_parts, "is_error": False},
                        })
                    elif tstatus == "error":
                        err_obj = state.get("error") if isinstance(state.get("error"), dict) else {}
                        err_msg = str(err_obj.get("message") or err_obj.get("type") or "tool error")
                        out.append({
                            "id": f"{mid}:tool:{call_id}:err",
                            "kind": "AgentErrorEvent",
                            "source": "environment",
                            "tool_name": tool_name,
                            "tool_call_id": call_id,
                            "error": err_msg,
                        })
            if texts:
                out.append({
                    "id": f"{mid}:assistant",
                    "kind": "MessageEvent",
                    "source": "agent",
                    "llm_message": {"content": texts},
                    "usage": msg.get("tokens") or {},
                    "finish": msg.get("finish") or "",
                })
            return out
        if mtype == "idle":
            outcome = msg.get("outcome") or ""
            err_obj = msg.get("error") if isinstance(msg.get("error"), dict) else None
            if outcome == "failed" and err_obj:
                out.append({
                    "id": f"{mid}:idle:err",
                    "kind": "ConversationErrorEvent",
                    "source": "environment",
                    "code": str(err_obj.get("type") or "error"),
                    "detail": str(err_obj.get("message") or ""),
                })
            elif outcome == "interrupted":
                out.append({"id": f"{mid}:idle:int", "kind": "InterruptEvent", "source": "environment"})
        return out

    def events(self, conversation_id: str, cursor: str = "") -> EventPage:
        q_sid = urllib.parse.quote(conversation_id, safe="")
        # OpenCode 2.3.0 rejects combining `cursor` with `order` (InvalidCursorError);
        # the cursor token already encodes the sort order.
        if cursor:
            path = f"/api/session/{q_sid}/message?limit=100&cursor=" + urllib.parse.quote(cursor, safe="")
        else:
            path = f"/api/session/{q_sid}/message?limit=100&order=asc"
        resp = self._request("GET", path, timeout=15)
        items: list[dict] = []
        for msg in resp.get("data") or []:
            if isinstance(msg, dict):
                items.extend(self._normalize_message(msg))
        nxt = ((resp.get("cursor") or {}).get("next")) or ""
        return EventPage(items=items, next_cursor=nxt)

    def send(self, conversation_id: str, text: str, run: bool = True) -> None:
        q_sid = urllib.parse.quote(conversation_id, safe="")
        body: dict = {"text": text}
        if not run:
            body["delivery"] = "queue"
        self._request("POST", f"/api/session/{q_sid}/prompt", body, timeout=15)

    def stop_owned_jobs(self, conversation_id: str, timeout: float = 1.0) -> None:
        """Stop every shell/subagent job owned by conversation_id via the pinned job API."""
        q_sid = urllib.parse.quote(conversation_id, safe="")
        deadline = time.monotonic() + max(timeout, 0.2)
        try:
            jobs = self._request(
                "GET",
                f"/api/experimental/job?sessionID={q_sid}&descendants=true",
                timeout=max(0.2, deadline - time.monotonic()),
            ).get("data") or []
        except (urllib.error.URLError, OSError, ValueError):
            return
        for job in jobs:
            if not isinstance(job, dict) or job.get("status") not in ("running", "pending", "queued"):
                continue
            left = max(0.2, deadline - time.monotonic())
            kind = job.get("kind")
            try:
                if kind == "shell" and job.get("shellID"):
                    self._request(
                        "DELETE",
                        "/api/shell/" + urllib.parse.quote(str(job["shellID"]), safe=""),
                        timeout=left,
                    )
                elif kind == "subagent" and job.get("childSessionID"):
                    child = urllib.parse.quote(str(job["childSessionID"]), safe="")
                    self._request("POST", f"/api/session/{child}/interrupt", timeout=left)
            except (urllib.error.URLError, OSError, ValueError):
                pass

    def interrupt(self, conversation_id: str, timeout: float = 10) -> None:
        q_sid = urllib.parse.quote(conversation_id, safe="")
        try:
            self._request("POST", f"/api/session/{q_sid}/interrupt", timeout=min(timeout, 5))
        finally:
            self.stop_owned_jobs(conversation_id, timeout=min(timeout, 1.0))

    def close(self, conversation_id: str) -> None:
        # Session stays persisted on /workspace/.opencode for suspend/resume; flush tracer if attached.
        if self._event_tracer is not None:
            self._event_tracer.flush()

    def stop(self, timeout: float = 10) -> str:
        if self.mcp_proxy is not None:
            try:
                self.mcp_proxy.stop()
            except Exception:  # noqa: BLE001
                pass
        if self.server is None:
            return "not started"
        self.server.terminate()
        try:
            self.server.wait(max(0.5, timeout - 1))
            return "stopped"
        except subprocess.TimeoutExpired:
            self.server.kill()
        try:
            self.server.wait(1)
            return "killed"
        except subprocess.TimeoutExpired:
            return "killed, not yet reaped"
