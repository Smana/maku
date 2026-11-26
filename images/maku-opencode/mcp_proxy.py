"""Loopback MCP credential proxy for OpenCode (design §7.1).

OpenCode talks only to this loopback proxy. On every outbound request, the proxy
re-reads MCP_TOKEN_FILE for the current projected bearer token, attaches the
run's W3C traceparent, forwards strictly to MCP_URL, bounds request/response
sizes, and refuses redirects so credentials never leave the configured upstream.
"""
import http.server
import json
import threading
import urllib.error
import urllib.request

# Tools whose answer is an image (v5 Q16 B3): OpenCode forwards a tool's image to any model
# (screenshots ledger L4), so a run whose model cannot see images never calls them.
IMAGE_TOOLS = ("room_image",)

MAX_REQUEST_BYTES = 4 << 20
MAX_RESPONSE_BYTES = 4 << 20
UPSTREAM_TIMEOUT_S = 30


class _NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *args, **kwargs):
        return None


_OPENER = urllib.request.build_opener(_NoRedirect)


def _read_token(path: str | None) -> str | None:
    if not path:
        return None
    try:
        with open(path, encoding="utf-8") as f:
            tok = f.read(8192).strip()
            return tok or None
    except OSError:
        return None


def _image_call(body: bytes | None):
    """The JSON-RPC id of a tools/call naming an image tool, or None for anything else."""
    if not body:
        return None
    try:
        msg = json.loads(body)
    except ValueError:
        return None
    if not isinstance(msg, dict) or msg.get("method") != "tools/call":
        return None
    name = str((msg.get("params") or {}).get("name") or "")
    return msg.get("id") if any(name == t or name.endswith("_" + t) for t in IMAGE_TOOLS) else None


class MCPCredentialProxy:
    """Loopback HTTP proxy that injects rotating MCP bearer tokens per request."""

    def __init__(
        self,
        upstream_url: str,
        token_file: str | None = None,
        traceparent: str | None = None,
        timeout: float = UPSTREAM_TIMEOUT_S,
        vision: bool = False,
    ):
        self.vision = vision
        self.upstream_url = upstream_url
        self.token_file = token_file
        self.traceparent = traceparent
        self.timeout = timeout
        self._server: http.server.ThreadingHTTPServer | None = None
        self._thread: threading.Thread | None = None

    @property
    def url(self) -> str:
        if self._server is None:
            raise RuntimeError("MCPCredentialProxy has not started")
        host, port = self._server.server_address
        return f"http://{host}:{port}/mcp"

    def start(self) -> str:
        proxy = self

        class Handler(http.server.BaseHTTPRequestHandler):
            def _forward(self, method: str) -> None:
                length = int(self.headers.get("Content-Length") or 0)
                if length > MAX_REQUEST_BYTES:
                    self.send_error(413, "request body too large")
                    return
                body = self.rfile.read(length) if length > 0 else None
                refused = None if proxy.vision else _image_call(body)
                if refused is not None:
                    raw = json.dumps({"jsonrpc": "2.0", "id": refused, "result": {"isError": True, "content": [
                        {"type": "text", "text": "not_available: this run's model cannot see images; read the request's text instead"}]}}).encode()
                    self.send_response(200)
                    self.send_header("Content-Type", "application/json")
                    self.send_header("Content-Length", str(len(raw)))
                    self.end_headers()
                    self.wfile.write(raw)
                    return
                headers = {}
                for k in ("Content-Type", "Accept", "Mcp-Session-Id", "Last-Event-ID"):
                    if v := self.headers.get(k):
                        headers[k] = v
                tok = _read_token(proxy.token_file)
                if tok:
                    headers["Authorization"] = "Bearer " + tok
                if proxy.traceparent:
                    headers["traceparent"] = proxy.traceparent
                req = urllib.request.Request(proxy.upstream_url, data=body, method=method, headers=headers)
                try:
                    with _OPENER.open(req, timeout=proxy.timeout) as resp:
                        raw = resp.read(MAX_RESPONSE_BYTES + 1)
                        if len(raw) > MAX_RESPONSE_BYTES:
                            self.send_error(502, "upstream response too large")
                            return
                        self.send_response(resp.status)
                        for hk in ("Content-Type", "Mcp-Session-Id"):
                            if hv := resp.headers.get(hk):
                                self.send_header(hk, hv)
                        self.send_header("Content-Length", str(len(raw)))
                        self.end_headers()
                        self.wfile.write(raw)
                except urllib.error.HTTPError as exc:
                    raw = exc.read(MAX_RESPONSE_BYTES)
                    self.send_response(exc.code)
                    if ct := exc.headers.get("Content-Type"):
                        self.send_header("Content-Type", ct)
                    self.send_header("Content-Length", str(len(raw)))
                    self.end_headers()
                    self.wfile.write(raw)
                except OSError:
                    self.send_error(502, "upstream unavailable")

            def do_POST(self) -> None:  # noqa: N802
                self._forward("POST")

            def do_GET(self) -> None:  # noqa: N802
                self._forward("GET")

            def do_DELETE(self) -> None:  # noqa: N802
                self._forward("DELETE")

            def log_message(self, *args) -> None:
                pass

        self._server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self._thread = threading.Thread(target=self._server.serve_forever, daemon=True)
        self._thread.start()
        return self.url

    def stop(self) -> None:
        if self._server is not None:
            self._server.shutdown()
            self._server.server_close()
            self._server = None
        if self._thread is not None:
            self._thread.join(timeout=2)
            self._thread = None
