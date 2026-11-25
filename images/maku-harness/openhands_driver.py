"""OpenHands agent-server 1.56.0 driver for agent-run (OpenCode design §6.1)."""
import json
import os
import secrets
import subprocess
import sys
import time
import urllib.error
import urllib.request
import uuid

from harness_driver import HarnessDriver
from harness_types import ConversationRequest, EventPage

AGENT_SERVER = "http://127.0.0.1:8000"
# Loopback only (plan P13): the API is unauthenticated, and agent-server
# itself switches to 0.0.0.0 by default once a session key is set.
SERVER_CMD = ["/agent-server/.venv/bin/python", "-m", "openhands.agent_server", "--host", "127.0.0.1", "--port", "8000"]
REPO_DIR = "/workspace/repo"
# A suspendable run's state on its PVC (warm-sandboxes design §6). agent-server keeps a conversation
# in workspace/conversations/<uuid.hex>, relative to its cwd "/" (config.py conversations_path).
KEY_FILE = "/workspace/.oh-key"
CONVERSATIONS_DIR = "/workspace/conversations"
# agent-server's start-up hook (F29, see site/sitecustomize.py), beside this file in /opt/agent.
SITE_DIR = os.path.join(os.path.dirname(os.path.realpath(__file__)), "site")
PLACEHOLDER_KEY = "injected-by-identity-proxy"
DEFAULT_INPUT_USD_PER_MTOK = "1.40"
DEFAULT_OUTPUT_USD_PER_MTOK = "4.40"
FLUSH_WAIT_S = 2
SEARCH_TOOL, SEARCH_MODULE = "search", "agent_search"

# CP6: an internal run condenses later (240 kept where 80, the first 8 where 4), and its summary
# runs at low effort on the light alias when the run holds one.
CONDENSER_MAX_SIZE = 240
CONDENSER_KEEP_FIRST = 8
CONDENSER_EFFORT = "low"

# What turns agent-server's own tracing on (OpenHands' observability, lmnr) and parents it on the
# run's span. Its spans carry prompts, model answers and tool input and output, so whatever the agent
# read, a token included: agent-server gets none of these unless AGENT_TRACE_CONTENT is "1", for a
# collector that filters span attributes. Off, the run exports its own root span only.
AGENT_SERVER_TRACE_KEYS = (
    "OTEL_EXPORTER_OTLP_ENDPOINT",
    "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT",
    "OTEL_ENDPOINT",
    "LMNR_PROJECT_API_KEY",
    "LMNR_SPAN_CONTEXT",
    "OPENHANDS_OBSERVABILITY_PARENT_SPAN_CONTEXT",
    "OTEL_BSP_SCHEDULE_DELAY",
)


def _api_key(env: dict, var: str = "LLM_API_KEY_FILE") -> str:
    path = env.get(var)
    if not path:
        return PLACEHOLDER_KEY
    with open(path) as f:
        return f.read().strip()


def _mcp_bearer(env: dict) -> str | None:
    path = env.get("MCP_TOKEN_FILE")
    if not path:
        return None
    try:
        with open(path) as f:
            token = f.read().strip()
    except (OSError, ValueError):
        return None
    return "Bearer " + token if token else None


def _span_traceparent(span) -> str | None:
    sc = span.get_span_context() if span else None
    if not sc or not sc.is_valid:
        return None
    return "00-%032x-%016x-01" % (sc.trace_id, sc.span_id)


def tool_names(agent: dict) -> str:
    return "agent-run tools: " + ", ".join(t.get("name", "?") for t in agent.get("tools", []))


def http(method: str, path: str, body: dict | None = None, timeout: int = 30, base: str = AGENT_SERVER) -> dict:
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(base + path, data=data, method=method, headers={"Content-Type": "application/json"})
    with urllib.request.urlopen(req, timeout=timeout) as resp:
        return json.loads(resp.read() or b"{}")


def wait_ready(deadline_s: int = 120, server=None, base: str = AGENT_SERVER) -> None:
    """Waits for agent-server's /ready. With its process, fails at once when it has exited."""
    end = time.monotonic() + deadline_s
    while time.monotonic() < end:
        if server is not None and server.poll() is not None:
            raise RuntimeError("agent-server exited at start-up with code %s (see its log above)" % server.returncode)
        try:
            urllib.request.urlopen(base + "/ready", timeout=2)
            return
        except (urllib.error.URLError, OSError):
            time.sleep(1)
    raise TimeoutError("agent-server never became ready")


def light_llm(env: dict, usage_id: str, effort: str):
    """The LLM for the light alias, or None when the run has none (CP12)."""
    if not env.get("LIGHT_BASE_URL"):
        return None
    from openhands.sdk import LLM

    return LLM(
        model="openai/" + env["LIGHT_MODEL"],
        base_url=env["LIGHT_BASE_URL"],
        api_key=_api_key(env, "LIGHT_API_KEY_FILE"),
        usage_id=usage_id,
        num_retries=0,
        litellm_extra_body={"reasoning_effort": effort},
    )


def internal_condenser(condenser, env: dict):
    """The default agent's condenser tuned for an internal run (CP6)."""
    if not condenser:
        return condenser
    llm = light_llm(env, "condenser", CONDENSER_EFFORT) or condenser.llm
    return condenser.model_copy(update={"max_size": CONDENSER_MAX_SIZE, "keep_first": CONDENSER_KEEP_FIRST, "llm": llm})


def build_request(
    env: dict,
    task: str,
    rules: str,
    span=None,
    search: bool = False,
    *,
    repo_dir: str = REPO_DIR,
    placeholder_key: str = PLACEHOLDER_KEY,
    default_input_usd: str = DEFAULT_INPUT_USD_PER_MTOK,
    default_output_usd: str = DEFAULT_OUTPUT_USD_PER_MTOK,
) -> dict:
    """The StartConversationRequest body, as plain JSON."""
    from openhands.sdk import LLM
    from openhands.sdk.conversation.request import StartConversationRequest
    from openhands.tools.preset.default import get_default_agent

    effort = env.get("LLM_REASONING_EFFORT", "high")
    api_key = _api_key(env) if env.get("LLM_API_KEY_FILE") else placeholder_key
    llm = LLM(
        model="openai/" + env["MODEL"],
        base_url=env["LLM_BASE_URL"],
        api_key=api_key,
        usage_id="agent",
        num_retries=0,
        input_cost_per_token=float(env.get("LLM_INPUT_USD_PER_MTOK", default_input_usd)) / 1e6,
        output_cost_per_token=float(env.get("LLM_OUTPUT_USD_PER_MTOK", default_output_usd)) / 1e6,
        litellm_extra_body={"reasoning_effort": effort},
        capability_overrides={"supports_prompt_cache": True} if env.get("DATA_CLASS") == "internal" else {},
    )
    plain = {"expose_secrets": True}
    agent = get_default_agent(llm=llm, cli_mode=True)
    if search:
        from openhands.sdk.tool import Tool

        agent = agent.model_copy(update={"tools": [*agent.tools, Tool(name=SEARCH_TOOL)]})
    if env.get("DATA_CLASS") == "internal":
        agent = agent.model_copy(update={"condenser": internal_condenser(agent.condenser, env)})
    agent = agent.model_dump(mode="json", context=plain)
    print(tool_names(agent), flush=True)
    mcp_url = env.get("MCP_URL")
    if mcp_url:
        platform = {"url": mcp_url, "transport": "http"}
        headers = {}
        tp = _span_traceparent(span)
        if tp:
            headers["traceparent"] = tp
        bearer = _mcp_bearer(env)
        if bearer:
            headers["Authorization"] = bearer
        if headers:
            platform["headers"] = headers
        agent["mcp_config"] = {"platform": platform}
    extra = {"tool_module_qualnames": {SEARCH_TOOL: SEARCH_MODULE}} if search else {}
    request = StartConversationRequest.model_validate({
        **extra,
        "conversation_id": env.get("CONVERSATION_ID") or str(uuid.uuid4()),
        "workspace": {"working_dir": repo_dir},
        "agent": agent,
        "initial_message": {"role": "user", "content": [{"type": "text", "text": task}], "run": True},
        "agent_launch_additions": {"system_message_suffix_append": rules},
        "max_iterations": 500,
        "autotitle": False,
        "tags": {"effort": effort},
    })
    return json.loads(request.model_dump_json(exclude_none=True, context=plain))


def suspendable(env: dict) -> bool:
    return env.get("AGENT_SUSPENDABLE") == "true"


def run_key(path: str = KEY_FILE) -> str:
    """A suspendable run's OH_SECRET_KEY, made once and kept on its PVC."""
    try:
        with open(path) as f:
            return f.read().strip()
    except FileNotFoundError:
        key = secrets.token_urlsafe(32)
        fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        with os.fdopen(fd, "w") as f:
            f.write(key)
        return key


def persisted(env: dict, root: str = CONVERSATIONS_DIR) -> bool:
    """True when a suspendable run's conversation is already on its PVC: the run was resumed."""
    if not suspendable(env):
        return False
    try:
        cid = uuid.UUID(env.get("CONVERSATION_ID", ""))
    except ValueError:
        return False
    return os.path.isfile(os.path.join(root, cid.hex, "base_state.json"))


def server_env(env: dict, *, site_dir: str = SITE_DIR, retry_log: str = "/tmp/agent-run-model-retries",
               key_fn=run_key) -> dict:
    """agent-server's environment, with an OH_SECRET_KEY made for this pod when unset."""
    out = dict(env)
    if env.get("AGENT_TRACE_CONTENT") != "1":
        for k in AGENT_SERVER_TRACE_KEYS:
            out.pop(k, None)
    if "OH_SECRET_KEY" not in out:
        out["OH_SECRET_KEY"] = key_fn() if suspendable(env) else secrets.token_urlsafe(32)
    out["PYTHONPATH"] = os.pathsep.join(p for p in (site_dir, env.get("PYTHONPATH")) if p)
    out["AGENT_RUN_RETRY_LOG"] = retry_log
    if env.get("AGENT_RUN_SECONDS", "").isdigit():
        out["AGENT_RUN_DEADLINE"] = str(time.time() + int(env["AGENT_RUN_SECONDS"]))
    return out


def stop_process(server, timeout: float) -> str:
    """Stops a server process within timeout, killed for its last second."""
    server.terminate()
    try:
        server.wait(timeout - 1)
        return "stopped"
    except subprocess.TimeoutExpired:
        server.kill()
    try:
        server.wait(1)
        return "killed"
    except subprocess.TimeoutExpired:
        return "killed, not yet reaped"


class OpenHandsDriver(HarnessDriver):
    """Drives OpenHands agent-server 1.56.0 over loopback."""

    def __init__(self, env: dict, *, trace_env: dict | None = None, runner=None):
        self.env = dict(env)
        self.trace_env = dict(trace_env or {})
        self.runner = runner
        self.server = None

    def _http(self, method: str, path: str, body: dict | None = None, timeout: int | None = None) -> dict:
        fn = self.runner.http if self.runner is not None else http
        if body is None and timeout is None:
            return fn(method, path)
        if body is None:
            return fn(method, path, timeout=timeout)
        if timeout is None:
            return fn(method, path, body)
        return fn(method, path, body, timeout=timeout)

    def start(self) -> None:
        merged = {**self.env, **self.trace_env}
        if self.runner is not None:
            self.server = self.runner.subprocess.Popen(
                self.runner.SERVER_CMD, cwd="/", env=self.runner.server_env(merged)
            )
            return
        self.server = subprocess.Popen(SERVER_CMD, cwd="/", env=server_env(merged))

    def wait_ready(self, deadline_s: int = 120) -> None:
        if self.runner is not None:
            self.runner.wait_ready(deadline_s=deadline_s, server=self.server)
            return
        wait_ready(deadline_s=deadline_s, server=self.server)

    def persisted(self) -> bool:
        if self.runner is not None:
            return self.runner.persisted(self.env)
        return persisted(self.env)

    def search_available(self) -> bool:
        if self.runner is not None:
            return self.runner.search_on(self.env)
        site_dir = SITE_DIR
        if site_dir not in sys.path:
            sys.path.append(site_dir)
        try:
            import agent_search  # noqa: F401
        except Exception:  # noqa: BLE001
            return False
        return self.env.get("AGENT_SEARCH") == "1" and self.env.get("ROLE") == "implementer"

    def create_conversation(self, request: ConversationRequest) -> str:
        if self.runner is not None:
            body = self.runner.build_request(request.env, request.task, request.rules, request.span, request.search)
        else:
            body = build_request(request.env, request.task, request.rules, request.span, request.search)
        return self._http("POST", "/api/conversations", body)["id"]

    def status(self, conversation_id: str) -> str:
        return self._http("GET", "/api/conversations/" + conversation_id).get("execution_status", "")

    def events(self, conversation_id: str, cursor: str = "") -> EventPage:
        path = "/api/conversations/%s/events/search?limit=100" % conversation_id
        if cursor:
            path += "&page_id=" + cursor
        body = self._http("GET", path)
        return EventPage(items=list(body.get("items") or []), next_cursor=body.get("next_page_id") or "")

    def send(self, conversation_id: str, text: str, run: bool = True) -> None:
        self._http(
            "POST",
            "/api/conversations/%s/events" % conversation_id,
            {"role": "user", "content": [{"type": "text", "text": text}], "run": run},
        )

    def interrupt(self, conversation_id: str, timeout: float = 10) -> None:
        self._http("POST", "/api/conversations/%s/interrupt" % conversation_id, timeout=timeout)

    def close(self, conversation_id: str) -> None:
        if self.runner is not None:
            self.runner.close_conversation(conversation_id)
            return
        try:
            self._http("DELETE", "/api/conversations/" + conversation_id, timeout=5)
        except Exception as exc:  # noqa: BLE001
            print("agent-run: conversation not closed: %s" % exc, file=sys.stderr, flush=True)
        time.sleep(FLUSH_WAIT_S)

    def stop(self, timeout: float = 10) -> str:
        if self.server is None:
            return "not started"
        if self.runner is not None:
            return self.runner.stop(self.server, timeout)
        return stop_process(self.server, timeout)
