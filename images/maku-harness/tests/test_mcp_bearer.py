"""Task 6: a run with MCP_TOKEN_FILE (the portable runtime's room tools token, audience room-tools)
sends it as a bearer on every MCP request: the broker's :8090 verifies it itself, with no gateway.

The projected token is rotated by the kubelet (600 s, refreshed at 80 %), so it is read per request,
never once a pod: the harness's own notes per RPC, and agent-server's client per POST through
sitecustomize, over the header build_request put on the platform entry at conversation start.
"""
import http.server
import importlib.util
import json
import os
import subprocess
import sys
import tempfile
import threading
import unittest
from unittest import mock

HERE = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..")
sys.path.insert(0, HERE)

import agent_run  # noqa: E402

# Loaded under another name, so its install hook (which needs agent-server's packages) does not run.
_spec = importlib.util.spec_from_file_location("harness_sitecustomize", os.path.join(HERE, "site", "sitecustomize.py"))
sitecustomize = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(sitecustomize)


def token_file(test, value):
    d = tempfile.mkdtemp()
    path = os.path.join(d, "token")
    with open(path, "w") as f:
        f.write(value + "\n")
    test.addCleanup(lambda: os.path.exists(path) and os.unlink(path))
    return path


class BearerTest(unittest.TestCase):
    def test_the_file_is_read_on_each_call(self):
        path = token_file(self, "first")
        self.assertEqual(agent_run.mcp_bearer({"MCP_TOKEN_FILE": path}), "Bearer first")
        with open(path, "w") as f:
            f.write("rotated")
        self.assertEqual(agent_run.mcp_bearer({"MCP_TOKEN_FILE": path}), "Bearer rotated")

    def test_no_file_no_header(self):
        self.assertIsNone(agent_run.mcp_bearer({}))
        self.assertIsNone(agent_run.mcp_bearer({"MCP_TOKEN_FILE": "/nonexistent/token"}))
        self.assertIsNone(agent_run.mcp_bearer({"MCP_TOKEN_FILE": token_file(self, "  ")}))


class Room(http.server.BaseHTTPRequestHandler):
    auth = []

    def do_POST(self):
        body = json.loads(self.rfile.read(int(self.headers["content-length"])))
        type(self).auth.append(self.headers.get("Authorization"))
        if "id" not in body:
            self.send_response(202)
            self.send_header("Content-Length", "0")
            self.end_headers()
            return
        result = {}
        if body["method"] == "initialize":
            result = {"protocolVersion": "2025-06-18", "capabilities": {"tools": {}},
                      "serverInfo": {"name": "room-broker", "version": "0"}}
        if body["method"] == "tools/list":
            result = {"tools": [{"name": "room_progress", "inputSchema": {"type": "object"}}]}
        if body["method"] == "tools/call":
            result = {"content": [{"type": "text", "text": '{"seq":7}'}]}
        out = json.dumps({"jsonrpc": "2.0", "id": body["id"], "result": result}).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(out)))
        self.end_headers()
        self.wfile.write(out)

    def log_message(self, *args):
        pass


class ProgressNotesBearerTest(unittest.TestCase):
    def setUp(self):
        Room.auth = []
        self.server = http.server.HTTPServer(("127.0.0.1", 0), Room)
        threading.Thread(target=self.server.serve_forever, daemon=True).start()
        self.addCleanup(self.server.server_close)
        self.addCleanup(self.server.shutdown)
        self.url = "http://127.0.0.1:%d/mcp" % self.server.server_port

    def test_every_rpc_carries_the_current_token(self):
        path = token_file(self, "t1")
        notes = agent_run.ProgressNotes(self.url, "reviewer", token_file=path)
        self.assertTrue(notes.send("one"))
        with open(path, "w") as f:
            f.write("t2")
        notes.names = None  # a fresh handshake, as after a rotation-era 401
        self.assertTrue(notes.send("two"))
        self.assertEqual(Room.auth[:3], ["Bearer t1"] * 3)
        self.assertEqual(Room.auth[-1], "Bearer t2")

    def test_without_a_token_file_no_authorization(self):
        self.assertTrue(agent_run.ProgressNotes(self.url, "reviewer").send("one"))
        self.assertEqual(set(Room.auth), {None})


class SiteBearerTest(unittest.TestCase):
    """agent-server's client: the platform server's requests get the file's token per POST."""

    URL = "http://room-broker.maku.svc.cluster.local:8090/mcp"

    def transport(self, url):
        class Transport:
            def __init__(self):
                self.url = url

            def _prepare_headers(self):
                return {"traceparent": "x"}
        self.assertTrue(sitecustomize.patch_bearer(Transport))
        return Transport()

    def test_the_platform_server_gets_the_current_token(self):
        path = token_file(self, "fresh")
        t = self.transport(self.URL)
        with mock.patch.dict(os.environ, {"MCP_TOKEN_FILE": path, "MCP_URL": self.URL}):
            self.assertEqual(t._prepare_headers(), {"Authorization": "Bearer fresh", "traceparent": "x"})
            with open(path, "w") as f:
                f.write("rotated")
            self.assertEqual(t._prepare_headers()["Authorization"], "Bearer rotated")

    def test_another_server_gets_none(self):
        path = token_file(self, "fresh")
        t = self.transport("https://elsewhere.example/mcp")
        with mock.patch.dict(os.environ, {"MCP_TOKEN_FILE": path, "MCP_URL": self.URL}):
            self.assertEqual(t._prepare_headers(), {"traceparent": "x"})

    def test_no_file_or_an_unreadable_one_adds_nothing(self):
        t = self.transport(self.URL)
        with mock.patch.dict(os.environ, {"MCP_URL": self.URL}):
            os.environ.pop("MCP_TOKEN_FILE", None)
            self.assertEqual(t._prepare_headers(), {"traceparent": "x"})
        with mock.patch.dict(os.environ, {"MCP_TOKEN_FILE": "/nonexistent/token", "MCP_URL": self.URL}):
            self.assertEqual(t._prepare_headers(), {"traceparent": "x"})


# The real MCP client, as agent-server drives it: the platform entry's header, set at conversation
# start, then two calls. Run under server_env, so sitecustomize is loaded as in agent-server.
CLIENT = r"""
import asyncio, os, sys
from fastmcp import Client
from fastmcp.client.transports import StreamableHttpTransport

async def main(url):
    async with Client(StreamableHttpTransport(url, headers={"Authorization": "Bearer stale"})) as client:
        for _ in range(2):
            await asyncio.wait_for(client.call_tool_mcp("room_progress", {"text": "x"}), 5)
    os._exit(0)

asyncio.run(main(sys.argv[1]))
"""


@unittest.skipUnless(importlib.util.find_spec("fastmcp"), "needs agent-server's packages: docker build --target test")
class AgentServerBearerTest(unittest.TestCase):
    def test_every_post_carries_the_files_token_not_the_configured_one(self):
        Room.auth = []
        server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Room)
        server.daemon_threads = True
        threading.Thread(target=server.serve_forever, daemon=True).start()
        self.addCleanup(server.server_close)
        self.addCleanup(server.shutdown)
        url = "http://127.0.0.1:%d/mcp" % server.server_port
        env = {k: v for k, v in os.environ.items() if k != "PYTHONPATH"}
        env.update(MCP_URL=url, MCP_TOKEN_FILE=token_file(self, "fresh"))
        done = subprocess.run([sys.executable, "-c", CLIENT, url], env=agent_run.server_env(env), cwd="/",
                              capture_output=True, text=True, timeout=60)
        self.assertEqual(done.returncode, 0, done.stderr)
        self.assertGreaterEqual(len(Room.auth), 3, Room.auth)
        self.assertEqual(set(Room.auth), {"Bearer fresh"})


if __name__ == "__main__":
    unittest.main()
