# SPDX-License-Identifier: Apache-2.0
"""A fake Gemini for probe P7: Vertex AI, the Gemini API and the GKE metadata server.

It enforces thought signatures as Google documents them for Gemini 3: every function call the model
made earlier in the turn must come back carrying the signature it was sent with, else 400. It plays
the model in a two-tool loop: the first two calls are answered with a function call (signatures
sig-1, sig-2), the third with text, whose content is a JSON report of what the gateway forwarded.

  POST .../publishers/google/models/<model>:generateContent   Vertex native (signature on the part)
  POST .../endpoints/openapi/chat/completions                  Vertex OpenAI-compatible (extra_content)
  POST /v1beta/openai/chat/completions                         the Gemini API (extra_content)
  GET  /computeMetadata/v1/...                                 the metadata server (HTTP port)
  GET  /report                                                 the metadata server's hits

usage: fake-gemini.py HTTPS_PORT HTTP_PORT CERT KEY
"""
import json
import re
import ssl
import sys
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

TOKEN = "fake-wi-token"
USAGE_NATIVE = {"promptTokenCount": 100, "candidatesTokenCount": 10, "thoughtsTokenCount": 20, "totalTokenCount": 130}
USAGE_OPENAI = {"prompt_tokens": 100, "completion_tokens": 30, "total_tokens": 130,
                "completion_tokens_details": {"reasoning_tokens": 20}}
MDS_HITS = []


def bad(msg):
    return 400, {"error": {"code": 400, "status": "INVALID_ARGUMENT", "message": msg}}


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, fmt, *args):
        pass

    def _send(self, code, body, ctype="application/json", headers=None):
        data = body.encode() if isinstance(body, str) else json.dumps(body).encode()
        self.send_response(code)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(data)))
        for k, v in (headers or {}).items():
            self.send_header(k, v)
        self.end_headers()
        self.wfile.write(data)

    def do_GET(self):
        if self.path.startswith("/healthz"):
            return self._send(200, {"ok": True})
        if self.path == "/report":
            return self._send(200, {"mds": MDS_HITS})
        if self.path.startswith("/computeMetadata/"):
            MDS_HITS.append({"path": self.path, "flavor": self.headers.get("Metadata-Flavor", "<absent>")})
            if self.headers.get("Metadata-Flavor") != "Google":
                return self._send(403, "Missing Metadata-Flavor:Google header", "text/plain")
            h = {"Metadata-Flavor": "Google"}
            p = self.path.split("?")[0]
            if p.endswith("/token"):
                return self._send(200, {"access_token": TOKEN, "expires_in": 3600, "token_type": "Bearer"}, headers=h)
            if p.endswith("/universe/universe-domain"):
                return self._send(200, "googleapis.com", "text/plain", h)
            if p.endswith("/project/project-id"):
                return self._send(200, "probe-project", "text/plain", h)
            if p.endswith("/email"):
                return self._send(200, "model-gateway@probe-project.iam.gserviceaccount.com", "text/plain", h)
            if p.endswith("/service-accounts/default/") or p.endswith("/service-accounts/default"):
                return self._send(200, {"email": "model-gateway@probe-project.iam.gserviceaccount.com", "scopes": []}, headers=h)
            return self._send(404, "not found", "text/plain", h)
        return self._send(404, {"error": "not found"})

    def do_POST(self):
        length = int(self.headers.get("Content-Length") or 0)
        raw = self.rfile.read(length) if length else b"{}"
        try:
            req = json.loads(raw)
        except ValueError:
            return self._send(400, {"error": "bad json"})
        auth = self.headers.get("Authorization", "")
        report = {
            "path": self.path,
            "host": self.headers.get("Host", "<absent>"),
            "auth": "fake-wi-token" if auth == "Bearer " + TOKEN else (f"bearer len={len(auth) - 7}" if auth.startswith("Bearer ") else "<absent>"),
        }
        if re.search(r"/publishers/google/models/[^/:]+:generateContent$", self.path):
            code, body = self.native(req, report)
        elif self.path.endswith("/chat/completions"):
            code, body = self.openai(req, report)
        else:
            code, body = 404, {"error": f"no such path {self.path}"}
        print(json.dumps({"code": code, **report}), flush=True)
        return self._send(code, body)

    @staticmethod
    def native(req, report):
        report["model"] = re.search(r"/models/([^/:]+):", report["path"]).group(1)
        gc = req.get("generationConfig", {})
        report["thinkingConfig"] = gc.get("thinkingConfig", "<absent>")
        report["temperature"] = gc.get("temperature", "<absent>")
        report["maxOutputTokens"] = gc.get("maxOutputTokens", "<absent>")
        calls = [p for c in req.get("contents", []) if c.get("role") == "model" for p in c.get("parts", []) if "functionCall" in p]
        responses = [p for c in req.get("contents", []) for p in c.get("parts", []) if "functionResponse" in p]
        for i, part in enumerate(calls):
            want = f"sig-{i + 1}"
            if part.get("thoughtSignature") != want:
                return bad(f"Function call is missing a thought_signature in functionCall parts (call {i + 1}: got {part.get('thoughtSignature')!r}, want {want!r})")
        report["signatures"] = [p["thoughtSignature"] for p in calls]
        n = len(responses)
        if n < 2:
            part = {"functionCall": {"name": "clock", "args": {"step": n + 1}}, "thoughtSignature": f"sig-{n + 1}"}
        else:
            part = {"text": json.dumps(report)}
        return 200, {"candidates": [{"content": {"role": "model", "parts": [part]}, "finishReason": "STOP", "index": 0}],
                     "usageMetadata": USAGE_NATIVE, "modelVersion": report["model"]}

    @staticmethod
    def openai(req, report):
        report["model"] = req.get("model", "<absent>")
        report["reasoning_effort"] = req.get("reasoning_effort", "<absent>")
        calls = [tc for m in req.get("messages", []) if m.get("role") == "assistant" for tc in (m.get("tool_calls") or [])]
        results = [m for m in req.get("messages", []) if m.get("role") == "tool"]
        for i, tc in enumerate(calls):
            want = f"sig-{i + 1}"
            got = ((tc.get("extra_content") or {}).get("google") or {}).get("thought_signature")
            if got != want:
                return bad(f"Function call is missing a thought_signature in functionCall parts (call {i + 1}: got {got!r}, want {want!r})")
        report["signatures"] = [tc["extra_content"]["google"]["thought_signature"] for tc in calls]
        n = len(results)
        if n < 2:
            msg = {"role": "assistant", "content": None, "tool_calls": [{
                "id": f"call-{n + 1}", "type": "function", "function": {"name": "clock", "arguments": json.dumps({"step": n + 1})},
                "extra_content": {"google": {"thought_signature": f"sig-{n + 1}"}}}]}
            finish = "tool_calls"
        else:
            msg, finish = {"role": "assistant", "content": json.dumps(report)}, "stop"
        return 200, {"id": "fake", "object": "chat.completion", "created": 0, "model": report["model"],
                     "choices": [{"index": 0, "finish_reason": finish, "message": msg}], "usage": USAGE_OPENAI}


if __name__ == "__main__":
    https_port, http_port, cert, key = int(sys.argv[1]), int(sys.argv[2]), sys.argv[3], sys.argv[4]
    tls = ThreadingHTTPServer(("0.0.0.0", https_port), Handler)
    ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    ctx.load_cert_chain(cert, key)
    tls.socket = ctx.wrap_socket(tls.socket, server_side=True)
    threading.Thread(target=tls.serve_forever, daemon=True).start()
    ThreadingHTTPServer(("0.0.0.0", http_port), Handler).serve_forever()
