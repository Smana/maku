"""Real OpenCode 2.3.0 provider contract, steering, loop_guard, and recovery tests (Tasks 5, 6, 7, 9, 10)."""
import http.server
import json
import os
import socket
import sys
import tempfile
import threading
import time
import unittest

HERE = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..")
sys.path.insert(0, HERE)
from harness_types import ConversationRequest  # noqa: E402
import opencode_driver  # noqa: E402


def _free_port() -> int:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


class ScriptedLLMServer:
    """Fake OpenAI-compatible provider supporting SSE and JSON chat completions."""

    def __init__(self, responses: list[dict]):
        self.responses = list(responses)
        self.requests: list[dict] = []
        self.headers: list[dict] = []
        self._lock = threading.Lock()
        self._srv = None

    @property
    def base_url(self) -> str:
        return f"http://127.0.0.1:{self._srv.server_port}/v1"

    def start(self) -> None:
        parent = self

        class Handler(http.server.BaseHTTPRequestHandler):
            def do_POST(self):  # noqa: N802
                length = int(self.headers.get("Content-Length") or 0)
                body = json.loads(self.rfile.read(length).decode("utf-8"))
                with parent._lock:
                    parent.requests.append(body)
                    parent.headers.append(dict(self.headers))
                    spec = parent.responses.pop(0) if parent.responses else {"text": "done"}
                status = spec.get("status", 200)
                if status != 200:
                    err = json.dumps({"error": {"message": spec.get("error", "rate limit"), "type": "rate_limit_error"}}).encode()
                    self.send_response(status)
                    self.send_header("Content-Type", "application/json")
                    self.send_header("Content-Length", str(len(err)))
                    self.end_headers()
                    self.wfile.write(err)
                    return

                model = body.get("model", "agent-default")
                usage = spec.get("usage", {"prompt_tokens": 12, "completion_tokens": 5, "total_tokens": 17})
                if body.get("stream"):
                    chunks = []
                    if "tool_call" in spec:
                        tc = spec["tool_call"]
                        chunks.append({
                            "id": "chatcmpl-1", "object": "chat.completion.chunk", "created": 1, "model": model,
                            "choices": [{"index": 0, "delta": {"role": "assistant", "tool_calls": [{
                                "index": 0, "id": tc["id"], "type": "function",
                                "function": {"name": tc["name"], "arguments": json.dumps(tc["arguments"])},
                            }]}, "finish_reason": None}],
                        })
                        chunks.append({
                            "id": "chatcmpl-1", "object": "chat.completion.chunk", "created": 1, "model": model,
                            "choices": [{"index": 0, "delta": {}, "finish_reason": "tool_calls"}],
                            "usage": usage,
                        })
                    else:
                        chunks.append({
                            "id": "chatcmpl-1", "object": "chat.completion.chunk", "created": 1, "model": model,
                            "choices": [{"index": 0, "delta": {"role": "assistant", "content": spec.get("text", "ok")}, "finish_reason": None}],
                        })
                        chunks.append({
                            "id": "chatcmpl-1", "object": "chat.completion.chunk", "created": 1, "model": model,
                            "choices": [{"index": 0, "delta": {}, "finish_reason": "stop"}],
                            "usage": usage,
                        })
                    payload = "".join(f"data: {json.dumps(c)}\n\n" for c in chunks) + "data: [DONE]\n\n"
                    raw = payload.encode("utf-8")
                    self.send_response(200)
                    self.send_header("Content-Type", "text/event-stream")
                    self.send_header("Content-Length", str(len(raw)))
                    self.end_headers()
                    self.wfile.write(raw)
                    return

                raw = json.dumps({
                    "id": "chatcmpl-1", "object": "chat.completion", "created": 1, "model": model,
                    "choices": [{"index": 0, "message": {"role": "assistant", "content": spec.get("text", "ok")}, "finish_reason": "stop"}],
                    "usage": usage,
                }).encode("utf-8")
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(raw)))
                self.end_headers()
                self.wfile.write(raw)

            def log_message(self, *args):
                pass

        self._srv = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        threading.Thread(target=self._srv.serve_forever, daemon=True).start()

    def stop(self) -> None:
        if self._srv is not None:
            self._srv.shutdown()
            self._srv.server_close()


class RealServerContractTest(unittest.TestCase):
    def _make_driver(self, tmp: str, llm_url: str, **extra_env) -> tuple[opencode_driver.OpenCodeDriver, dict]:
        port = _free_port()
        env = {
            "AGENT_HARNESS": "opencode",
            "ROLE": "implementer",
            "MODEL": "agent-default",
            "DATA_CLASS": "public",
            "LLM_BASE_URL": llm_url,
            "LLM_REASONING_EFFORT": "medium",
            "OPENCODE_PORT": str(port),
            "OPENCODE_WORKSPACE_ROOT": os.path.join(tmp, ".opencode"),
            "OPENCODE_SESSION_ID_FILE": os.path.join(tmp, ".session-id"),
            "OPENCODE_PASSWORD_FILE": os.path.join(tmp, ".password"),
            "OPENCODE_TERMINAL_FILE": os.path.join(tmp, ".terminal.json"),
            "OPENCODE_WORKDIR": tmp,
            **extra_env,
        }
        return opencode_driver.OpenCodeDriver(env), env

    def _wait_terminal(self, driver: opencode_driver.OpenCodeDriver, cid: str, want: set[str], timeout_s: float = 15.0) -> str:
        end = time.monotonic() + timeout_s
        last = ""
        while time.monotonic() < end:
            last = driver.status(cid)
            if last in want:
                return last
            time.sleep(0.2)
        return last

    def test_provider_effort_auth_usage_and_resume_persistence(self):
        """Real OpenCode 2.3.0 sends model, reasoning_effort, bearer auth, records messages, and persists session on PVC."""
        tmp = tempfile.mkdtemp(prefix="oc-prov-")
        llm = ScriptedLLMServer([
            {"text": "first turn complete", "usage": {"prompt_tokens": 25, "completion_tokens": 9, "total_tokens": 34}},
        ])
        llm.start()
        self.addCleanup(llm.stop)

        d1, env1 = self._make_driver(tmp, llm.base_url, AGENT_SUSPENDABLE="true", OTEL_EXPORTER_OTLP_ENDPOINT="http://127.0.0.1:4318")
        # OpenCode server must never receive native OTLP endpoint (design §7.6).
        d1.paths = opencode_driver.oc_config.workspace_paths(d1.root)
        d1.password = "pw-check"
        self.assertNotIn("OTEL_EXPORTER_OTLP_ENDPOINT", d1._server_env())

        d1.start()
        try:
            d1.wait_ready(deadline_s=30)
            cid = d1.create_conversation(ConversationRequest(env=env1, task="say hello", rules="be brief"))
            st = self._wait_terminal(d1, cid, {"finished", "idle", "error"})
            self.assertIn(st, ("finished", "idle"))
            self.assertEqual(len(llm.requests), 1)
            self.assertEqual(llm.requests[0]["model"], "agent-default")
            self.assertEqual(llm.requests[0].get("reasoning_effort"), "medium")
            self.assertEqual(llm.headers[0].get("Authorization"), "Bearer " + opencode_driver.PLACEHOLDER_KEY)
            page = d1.events(cid)
            kinds = [e["kind"] for e in page.items]
            self.assertIn("MessageEvent", kinds)
        finally:
            d1.stop(timeout=5)

        # Restart a fresh server on the same /workspace/.opencode PVC directory and verify session restoration.
        d2, env2 = self._make_driver(tmp, llm.base_url, AGENT_SUSPENDABLE="true")
        self.assertTrue(d2.persisted())
        self.assertEqual(d2.persisted_session_id(), cid)
        d2.start()
        try:
            d2.wait_ready(deadline_s=30)
            restored = d2.create_conversation(ConversationRequest(env=env2, task="ignored", rules="be brief", resumed=True))
            self.assertEqual(restored, cid)
            page2 = d2.events(restored)
            self.assertTrue(any(e["kind"] == "MessageEvent" and e["source"] == "agent" for e in page2.items))
        finally:
            d2.stop(timeout=5)

    def test_loop_guard_marks_stuck_and_interrupts_real_session(self):
        tmp = tempfile.mkdtemp(prefix="oc-stuck-")
        llm = ScriptedLLMServer([{"text": "initial"}])
        llm.start()
        self.addCleanup(llm.stop)
        d, env = self._make_driver(tmp, llm.base_url)
        d.start()
        try:
            d.wait_ready(deadline_s=30)
            cid = d.create_conversation(ConversationRequest(env=env, task="loop", rules="rules"))
            self._wait_terminal(d, cid, {"finished", "idle"})
            for i in range(4):
                d.on_event({"id": f"act-{i}", "kind": "ActionEvent", "tool_call_id": f"c-{i}", "tool_name": "shell", "action": {"command": "true"}})
                d.on_event({"id": f"obs-{i}", "kind": "ObservationEvent", "tool_call_id": f"c-{i}", "tool_name": "shell", "observation": {"content": [{"type": "text", "text": "ok"}], "is_error": False}})
            self.assertEqual(d.status(cid), "stuck")
            with open(env["OPENCODE_TERMINAL_FILE"], encoding="utf-8") as f:
                marker = json.load(f)
            self.assertEqual(marker, {"reason": "stuck", "pattern": "repeated_action"})
        finally:
            d.stop(timeout=5)

    def test_provider_families_glm_anthropic_kimi_mistral_config_and_requests(self):
        """Verify all four provider families configure cleanly and send expected model & effort shapes."""
        cases = [
            ("glm", "public", "agent-default", "high", False),
            ("anthropic", "internal", "tier-standard", "high", True),
            ("kimi", "public", "kimi-frontier", "medium", False),
            ("mistral", "public", "mistral-standard", "low", False),
        ]
        for family, data_class, model, effort, want_cache in cases:
            with self.subTest(family=family):
                tmp = tempfile.mkdtemp(prefix=f"oc-{family}-")
                llm = ScriptedLLMServer([{"text": f"{family} ok"}])
                llm.start()
                self.addCleanup(llm.stop)
                d, env = self._make_driver(tmp, llm.base_url, DATA_CLASS=data_class, MODEL=model, LLM_REASONING_EFFORT=effort)
                d.start()
                try:
                    d.wait_ready(deadline_s=30)
                    cid = d.create_conversation(ConversationRequest(env=env, task=f"check {family}", rules="be concise"))
                    st = self._wait_terminal(d, cid, {"finished", "idle", "error"})
                    self.assertIn(st, ("finished", "idle"))
                    self.assertEqual(len(llm.requests), 1)
                    self.assertEqual(llm.requests[0]["model"], model)
                    self.assertEqual(llm.requests[0].get("reasoning_effort"), effort)
                    with open(os.path.join(d.paths["config"], "opencode.jsonc"), encoding="utf-8") as f:
                        cfg = json.load(f)
                    has_cache = "cacheFormat" in cfg["providers"]["maku"].get("compatibility", {})
                    self.assertEqual(has_cache, want_cache)
                finally:
                    d.stop(timeout=5)


if __name__ == "__main__":
    unittest.main()
