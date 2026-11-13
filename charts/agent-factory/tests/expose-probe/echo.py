# SPDX-License-Identifier: Apache-2.0
"""The exposure probe's backend (plan v4 step 6, Task 9): it answers every HTTP request with what it
received (method, path, headers) as JSON, and holds a WebSocket open, pinging every PING_EVERY
seconds as the broker's /v1/ws does and echoing each text frame. Standard library only."""
import base64
import hashlib
import json
import os
import select
import struct
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

PING_EVERY = float(os.environ.get("PING_EVERY", "30"))
GUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"


def frame(opcode, payload=b""):
    head = bytes([0x80 | opcode])
    n = len(payload)
    if n < 126:
        head += bytes([n])
    elif n < 65536:
        head += bytes([126]) + struct.pack("!H", n)
    else:
        head += bytes([127]) + struct.pack("!Q", n)
    return head + payload


def read_exact(sock, n):
    buf = b""
    while len(buf) < n:
        chunk = sock.recv(n - len(buf))
        if not chunk:
            raise EOFError
        buf += chunk
    return buf


def read_frame(sock):
    b0, b1 = read_exact(sock, 2)
    opcode, n = b0 & 0x0F, b1 & 0x7F
    if n == 126:
        n = struct.unpack("!H", read_exact(sock, 2))[0]
    elif n == 127:
        n = struct.unpack("!Q", read_exact(sock, 8))[0]
    mask = read_exact(sock, 4) if b1 & 0x80 else None
    data = read_exact(sock, n)
    if mask:
        data = bytes(c ^ mask[i % 4] for i, c in enumerate(data))
    return opcode, data


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, *args):
        pass

    def answer(self):
        if self.headers.get("Upgrade", "").lower() == "websocket":
            return self.websocket()
        n = int(self.headers.get("Content-Length") or 0)
        if n:
            self.rfile.read(n)
        body = json.dumps({"method": self.command, "path": self.path,
                           "headers": {k.lower(): v for k, v in self.headers.items()}}).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    do_GET = do_POST = do_PUT = do_DELETE = answer

    def websocket(self):
        key = self.headers["Sec-WebSocket-Key"]
        accept = base64.b64encode(hashlib.sha1((key + GUID).encode()).digest()).decode()
        self.send_response(101)
        self.send_header("Upgrade", "websocket")
        self.send_header("Connection", "Upgrade")
        self.send_header("Sec-WebSocket-Accept", accept)
        self.end_headers()
        self.wfile.flush()
        sock = self.connection
        try:
            while True:
                ready, _, _ = select.select([sock], [], [], PING_EVERY)
                if not ready:
                    sock.sendall(frame(0x9, b"beat"))
                    continue
                opcode, data = read_frame(sock)
                if opcode == 0x1:
                    sock.sendall(frame(0x1, data))
                elif opcode == 0x9:
                    sock.sendall(frame(0xA, data))
                elif opcode == 0x8:
                    sock.sendall(frame(0x8, data[:2]))
                    return
        except (EOFError, OSError):
            return
        finally:
            self.close_connection = True


ThreadingHTTPServer(("0.0.0.0", 8080), Handler).serve_forever()
