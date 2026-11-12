# SPDX-License-Identifier: Apache-2.0
"""A fake model provider for the gateway probe and the k3d proof.

It answers every POST with a Chat Completion whose content is a JSON report of what the gateway
forwarded: the path, the body's model and the sampling fields a preset rewrites, and whether an
Authorization or an x-ar-* header arrived (never a credential's value). A message containing
THROTTLE is answered 429, so the gateway's request counter shows a throttled call. The usage
carries cached tokens, so the token-usage histogram shows every token type it knows.
"""
import json
import sys
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

ABSENT = "<absent>"


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, fmt, *args):
        pass

    def _send(self, code, body):
        data = json.dumps(body).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def do_GET(self):
        if self.path.startswith("/healthz"):
            return self._send(200, {"ok": True})
        return self._send(404, {"error": "not found"})

    def do_POST(self):
        length = int(self.headers.get("Content-Length") or 0)
        raw = self.rfile.read(length) if length else b"{}"
        try:
            req = json.loads(raw)
        except ValueError:
            return self._send(400, {"error": "bad json"})
        text = json.dumps(req.get("messages", []))
        if "THROTTLE" in text:
            return self._send(429, {"error": {"type": "rate_limit_error", "message": "echo-llm throttles on request"}})
        report = {
            "path": self.path,
            "model": req.get("model", ABSENT),
            "temperature": req.get("temperature", ABSENT),
            "top_p": req.get("top_p", ABSENT),
            "reasoning_effort": req.get("reasoning_effort", ABSENT),
            "authorization": "present" if self.headers.get("Authorization") else ABSENT,
            "x-api-key": "present" if self.headers.get("x-api-key") else ABSENT,
            "x-ar-agent": self.headers.get("x-ar-agent", ABSENT),
            "x-ar-human": self.headers.get("x-ar-human", ABSENT),
        }
        print(json.dumps(report), flush=True)
        return self._send(200, {
            "id": "echo", "object": "chat.completion", "created": 0, "model": req.get("model", "echo"),
            "choices": [{"index": 0, "finish_reason": "stop",
                         "message": {"role": "assistant", "content": json.dumps(report)}}],
            "usage": {"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15,
                      "prompt_tokens_details": {"cached_tokens": 4}},
        })


if __name__ == "__main__":
    port = int(sys.argv[1]) if len(sys.argv) > 1 else 8080
    ThreadingHTTPServer(("0.0.0.0", port), Handler).serve_forever()
