"""Real OpenCode 2.3.0 metadata-only tracing and W3C traceparent correlation tests (OpenCode plan Task 10)."""
import http.server
import json
import os
import sys
import tempfile
import threading
import time
import unittest

TESTS_DIR = os.path.dirname(os.path.abspath(__file__))
HERE = os.path.join(TESTS_DIR, "..")
sys.path.insert(0, HERE)
sys.path.insert(0, TESTS_DIR)
import event_tracing  # noqa: E402
from harness_types import ConversationRequest  # noqa: E402
import mcp_proxy  # noqa: E402
import opencode_driver  # noqa: E402
from test_provider_contract import ScriptedLLMServer, _free_port  # noqa: E402

FORBIDDEN_ATTRS = (
    "gen_ai.prompt",
    "gen_ai.completion",
    "process.command",
    "shell.command",
    "tool.input",
    "tool.output",
)


class RealServerTracingTest(unittest.TestCase):
    def test_metadata_only_spans_and_trace_propagation_with_real_opencode(self):
        secret = "SUPER_SECRET_PLANTED_VALUE_9911"
        trace_id = "4bf92f3577b34da6a3ce929d0e0e4736"
        traceparent = f"00-{trace_id}-00f067aa0ba902b7-01"

        tmp = tempfile.mkdtemp(prefix="oc-trace-")
        llm = ScriptedLLMServer([
            {
                "tool_call": {
                    "id": "call_echo_secret",
                    "name": "shell",
                    "arguments": {"command": f"echo {secret}", "description": "echo secret"},
                }
            },
            {
                "text": f"saw {secret}",
                "usage": {"prompt_tokens": 30, "completion_tokens": 7, "total_tokens": 37},
            },
        ])
        llm.start()
        self.addCleanup(llm.stop)

        # Fake upstream MCP server to verify traceparent propagation through MCPCredentialProxy.
        mcp_seen_headers: list[dict] = []

        class MCPHandler(http.server.BaseHTTPRequestHandler):
            def do_POST(self):  # noqa: N802
                length = int(self.headers.get("Content-Length") or 0)
                _ = self.rfile.read(length)
                mcp_seen_headers.append({k.lower(): v for k, v in self.headers.items()})
                raw = json.dumps({"jsonrpc": "2.0", "id": 1, "result": {}}).encode()
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(raw)))
                self.end_headers()
                self.wfile.write(raw)

            def log_message(self, *args):
                pass

        mcp_srv = http.server.ThreadingHTTPServer(("127.0.0.1", 0), MCPHandler)
        threading.Thread(target=mcp_srv.serve_forever, daemon=True).start()
        self.addCleanup(lambda: (mcp_srv.shutdown(), mcp_srv.server_close()))

        token_file = os.path.join(tmp, "mcp-token")
        with open(token_file, "w", encoding="utf-8") as f:
            f.write("mcp-tok-v1\n")

        proxy = mcp_proxy.MCPCredentialProxy(
            upstream_url=f"http://127.0.0.1:{mcp_srv.server_port}/mcp",
            token_file=token_file,
            traceparent=traceparent,
        )
        proxy.start()
        self.addCleanup(proxy.stop)

        # Send a request through the MCP proxy and confirm exact traceparent + token forwarding.
        import urllib.request

        req = urllib.request.Request(
            proxy.url,
            data=json.dumps({"jsonrpc": "2.0", "id": 1, "method": "ping"}).encode(),
            method="POST",
            headers={"Content-Type": "application/json"},
        )
        with urllib.request.urlopen(req, timeout=5) as resp:
            self.assertEqual(resp.status, 200)
        self.assertEqual(len(mcp_seen_headers), 1)
        self.assertEqual(mcp_seen_headers[0].get("traceparent"), traceparent)
        self.assertEqual(mcp_seen_headers[0].get("authorization"), "Bearer mcp-tok-v1")

        # Now run real OpenCode with EventTracer attached and verify metadata-only spans.
        port = _free_port()
        env = {
            "AGENT_HARNESS": "opencode",
            "ROLE": "implementer",
            "MODEL": "agent-default",
            "DATA_CLASS": "public",
            "LLM_BASE_URL": llm.base_url,
            "LLM_REASONING_EFFORT": "low",
            "TRACEPARENT": traceparent,
            "OTEL_EXPORTER_OTLP_ENDPOINT": "http://127.0.0.1:4318",
            "OTEL_LOGS_EXPORTER": "otlp",
            "OPENCODE_PORT": str(port),
            "OPENCODE_WORKSPACE_ROOT": os.path.join(tmp, ".opencode"),
            "OPENCODE_SESSION_ID_FILE": os.path.join(tmp, ".session-id"),
            "OPENCODE_PASSWORD_FILE": os.path.join(tmp, ".password"),
            "OPENCODE_TERMINAL_FILE": os.path.join(tmp, ".terminal.json"),
            "OPENCODE_WORKDIR": tmp,
        }
        d = opencode_driver.OpenCodeDriver(env)
        d.start()
        try:
            # Verify server env never exposes OTLP endpoints or log exporters.
            senv = d._server_env()
            self.assertNotIn("OTEL_EXPORTER_OTLP_ENDPOINT", senv)
            self.assertNotIn("OTEL_LOGS_EXPORTER", senv)

            d.wait_ready(deadline_s=30)
            cid = d.create_conversation(ConversationRequest(env=env, task=f"echo {secret}", rules="brief", span=object()))
            end = time.monotonic() + 20.0
            while time.monotonic() < end:
                if d.status(cid) in ("finished", "idle", "error"):
                    break
                time.sleep(0.2)

            page = d.events(cid)
            for ev in page.items:
                d.on_event(ev)
            d.close(cid)

            tracer = d._event_tracer
            self.assertIsNotNone(tracer)
            self.assertGreaterEqual(len(tracer.finished), 2)
            # Every span must be metadata-only and never leak the planted secret.
            for sp in tracer.finished:
                self.assertNotIn(secret, sp.name)
                for forbidden in FORBIDDEN_ATTRS:
                    self.assertNotIn(forbidden, sp.attributes)
                self.assertNotIn(secret, json.dumps(sp.attributes))

            step_spans = [sp for sp in tracer.finished if sp.name == "gen_ai.step"]
            tool_spans = [sp for sp in tracer.finished if sp.name == "gen_ai.tool"]
            self.assertGreaterEqual(len(step_spans), 1)
            self.assertGreaterEqual(len(tool_spans), 1)
            self.assertEqual(step_spans[-1].attributes.get("gen_ai.usage.input_tokens"), 30)
            self.assertEqual(tool_spans[0].attributes.get("gen_ai.tool.name"), "shell")

            # Model server received a W3C traceparent header.
            llm_tp = llm.headers[0].get("traceparent") or llm.headers[0].get("Traceparent") or ""
            self.assertRegex(llm_tp, r"^00-[0-9a-f]{32}-[0-9a-f]{16}-[0-9a-f]{2}$")
        finally:
            d.stop(timeout=5)


if __name__ == "__main__":
    unittest.main()
