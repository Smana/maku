"""Rotating-token loopback MCP credential proxy tests (OpenCode plan Task 6)."""
import http.server
import json
import os
import sys
import tempfile
import threading
import unittest
import urllib.error
import urllib.request

HERE = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..")
sys.path.insert(0, HERE)
import mcp_proxy  # noqa: E402


class MCPProxyTest(unittest.TestCase):
    def test_rotates_bearer_token_and_forwards_traceparent(self):
        seen = []

        class Upstream(http.server.BaseHTTPRequestHandler):
            def do_POST(self):  # noqa: N802
                body = self.rfile.read(int(self.headers.get("Content-Length") or 0))
                seen.append({
                    "auth": self.headers.get("Authorization"),
                    "traceparent": self.headers.get("traceparent"),
                    "body": body.decode("utf-8"),
                })
                out = json.dumps({"jsonrpc": "2.0", "id": 1, "result": {"ok": True}}).encode("utf-8")
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(out)))
                self.end_headers()
                self.wfile.write(out)

            def log_message(self, *args):
                pass

        srv = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Upstream)
        threading.Thread(target=srv.serve_forever, daemon=True).start()
        self.addCleanup(srv.shutdown)
        self.addCleanup(srv.server_close)

        tok_file = os.path.join(tempfile.mkdtemp(), "token")
        with open(tok_file, "w", encoding="utf-8") as f:
            f.write("tok-v1\n")

        tp = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
        proxy = mcp_proxy.MCPCredentialProxy(
            upstream_url=f"http://127.0.0.1:{srv.server_port}/mcp",
            token_file=tok_file,
            traceparent=tp,
        )
        url = proxy.start()
        self.addCleanup(proxy.stop)

        def call():
            req = urllib.request.Request(url, data=b'{"jsonrpc":"2.0","id":1}', method="POST", headers={"Content-Type": "application/json"})
            with urllib.request.urlopen(req, timeout=5) as resp:
                return json.loads(resp.read().decode("utf-8"))

        self.assertEqual(call()["result"]["ok"], True)
        with open(tok_file, "w", encoding="utf-8") as f:
            f.write("tok-v2\n")
        self.assertEqual(call()["result"]["ok"], True)

        self.assertEqual([s["auth"] for s in seen], ["Bearer tok-v1", "Bearer tok-v2"])
        self.assertEqual([s["traceparent"] for s in seen], [tp, tp])

    def test_refuses_redirects_without_leaking_credentials(self):
        leaked = []

        class Redirector(http.server.BaseHTTPRequestHandler):
            def do_POST(self):  # noqa: N802
                self.send_response(302)
                self.send_header("Location", "http://127.0.0.1:1/steal")
                self.end_headers()

            def log_message(self, *args):
                pass

        srv = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Redirector)
        threading.Thread(target=srv.serve_forever, daemon=True).start()
        self.addCleanup(srv.shutdown)
        self.addCleanup(srv.server_close)

        tok_file = os.path.join(tempfile.mkdtemp(), "token")
        with open(tok_file, "w", encoding="utf-8") as f:
            f.write("secret-tok\n")

        proxy = mcp_proxy.MCPCredentialProxy(upstream_url=f"http://127.0.0.1:{srv.server_port}/mcp", token_file=tok_file)
        url = proxy.start()
        self.addCleanup(proxy.stop)

        req = urllib.request.Request(url, data=b"{}", method="POST", headers={"Content-Type": "application/json"})
        with self.assertRaises(urllib.error.HTTPError) as ctx:
            urllib.request.urlopen(req, timeout=5)
        self.assertEqual(ctx.exception.code, 302)
        self.assertEqual(leaked, [])


if __name__ == "__main__":
    unittest.main()
