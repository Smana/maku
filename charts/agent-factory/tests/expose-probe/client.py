# SPDX-License-Identifier: Apache-2.0
"""The exposure probe's client (plan v4 step 6, Task 9), standard library only. Every command prints
one JSON object.
  http METHOD URL [-H 'Name: value']...      the answer: status, location, set-cookie names, body
  ws URL SECONDS [-H 'Name: value']...       holds a WebSocket SECONDS, answering pings, then echoes
                                             (with --hello, sends it first and needs a ping instead)
  login URL                                  the code flow from URL to a session: the cookie header
--via BASE sends every request for the command's own host (the URL's) to BASE over plain http, with
that host as Host (no DNS for test hosts; the gateway terminates TLS for them); any other host is
dialled directly, https verified against --cacert FILE (the stub issuer)."""
import argparse
import base64
import http.client
import json
import os
import socket
import ssl
import struct
import sys
import time
import urllib.parse


def parse_args():
    p = argparse.ArgumentParser()
    p.add_argument("cmd")
    p.add_argument("rest", nargs="*")
    p.add_argument("-H", dest="headers", action="append", default=[])
    p.add_argument("--via", default="")
    p.add_argument("--cacert", default="")
    p.add_argument("--hello", default="", help="ws: a first text frame (the broker's hello); the hold then ends without an echo")
    return p.parse_args()


ARGS = parse_args()
VIA_HOST = urllib.parse.urlsplit(ARGS.rest[1] if ARGS.cmd == "http" else ARGS.rest[0]).hostname


def connect(url):
    u = urllib.parse.urlsplit(url)
    if ARGS.via and u.hostname == VIA_HOST:
        v = urllib.parse.urlsplit(ARGS.via)
        return http.client.HTTPConnection(v.hostname, v.port or 80, timeout=30), u
    if u.scheme == "https":
        ctx = ssl.create_default_context(cafile=ARGS.cacert or None)
        return http.client.HTTPSConnection(u.hostname, u.port or 443, context=ctx, timeout=30), u
    return http.client.HTTPConnection(u.hostname, u.port or 80, timeout=30), u


def headers_of(extra):
    out = {}
    for h in extra:
        k, _, v = h.partition(":")
        out[k.strip()] = v.strip()
    return out


def request(method, url, headers):
    conn, u = connect(url)
    path = u.path or "/"
    if u.query:
        path += "?" + u.query
    h = {"Host": u.netloc}
    h.update(headers)
    conn.request(method, path, body=b"{}" if method == "POST" else None, headers=h)
    r = conn.getresponse()
    body = r.read().decode(errors="replace")
    cookies = r.headers.get_all("Set-Cookie") or []
    conn.close()
    return r.status, r.getheader("Location", ""), cookies, body


def cmd_http():
    method, url = ARGS.rest[0], ARGS.rest[1]
    status, location, cookies, body = request(method, url, headers_of(ARGS.headers))
    try:
        body = json.loads(body)
    except ValueError:
        body = body[:400]
    print(json.dumps({"status": status, "location": location,
                      "cookies": [c.split("=", 1)[0] for c in cookies], "cookieAttrs": cookies and cookies[-1].split(";", 1)[-1] or "",
                      "body": body}))


def cmd_login():
    url, jar, hops = ARGS.rest[0], {}, []
    for _ in range(8):
        h = headers_of(ARGS.headers)
        host = urllib.parse.urlsplit(url).hostname
        mine = {k: v for (k_host, k), v in jar.items() if k_host == host}
        if mine:
            h["Cookie"] = "; ".join(f"{k}={v}" for k, v in mine.items())
        status, location, cookies, body = request("GET", url, h)
        hops.append(f"{status} {urllib.parse.urlsplit(url).netloc}{urllib.parse.urlsplit(url).path}")
        for c in cookies:
            k, _, rest = c.partition("=")
            jar[(host, k)] = rest.split(";", 1)[0]
        if status not in (301, 302, 303, 307):
            break
        url = urllib.parse.urljoin(url, location)
    host = urllib.parse.urlsplit(ARGS.rest[0]).hostname
    print(json.dumps({"hops": hops, "cookie": "; ".join(f"{k}={v}" for (k_host, k), v in jar.items() if k_host == host and v)}))


def ws_send(sock, opcode, payload=b""):
    mask = os.urandom(4)
    head = bytes([0x80 | opcode])
    n = len(payload)
    head += bytes([0x80 | n]) if n < 126 else bytes([0x80 | 126]) + struct.pack("!H", n)
    sock.sendall(head + mask + bytes(c ^ mask[i % 4] for i, c in enumerate(payload)))


def ws_recv(sock):
    def exact(n):
        buf = b""
        while len(buf) < n:
            chunk = sock.recv(n - len(buf))
            if not chunk:
                raise EOFError("closed by the peer")
            buf += chunk
        return buf
    b0, b1 = exact(2)
    n = b1 & 0x7F
    if n == 126:
        n = struct.unpack("!H", exact(2))[0]
    elif n == 127:
        n = struct.unpack("!Q", exact(8))[0]
    return b0 & 0x0F, exact(n)


def cmd_ws():
    url, seconds = ARGS.rest[0], float(ARGS.rest[1])
    u = urllib.parse.urlsplit(url)
    target = urllib.parse.urlsplit(ARGS.via) if ARGS.via else u
    start, pings = time.time(), 0
    try:
        sock = socket.create_connection((target.hostname, target.port or 80), timeout=90)
        key = base64.b64encode(os.urandom(16)).decode()
        lines = [f"GET {u.path or '/'} HTTP/1.1", f"Host: {u.netloc}", "Upgrade: websocket", "Connection: Upgrade",
                 f"Sec-WebSocket-Key: {key}", "Sec-WebSocket-Version: 13"]
        lines += [f"{k}: {v}" for k, v in headers_of(ARGS.headers).items()]
        sock.sendall(("\r\n".join(lines) + "\r\n\r\n").encode())
        resp = b""
        while b"\r\n\r\n" not in resp:
            chunk = sock.recv(1)
            if not chunk:
                raise EOFError("closed during the handshake")
            resp += chunk
        status = resp.split(b" ", 2)[1].decode()
        if status != "101":
            raise RuntimeError(f"handshake answered {status}")
        if ARGS.hello:
            ws_send(sock, 0x1, ARGS.hello.encode())
        while time.time() - start < seconds:
            sock.settimeout(max(1.0, min(90.0, seconds - (time.time() - start))))
            try:
                op, data = ws_recv(sock)
            except socket.timeout:
                continue
            if op == 0x9:
                pings += 1
                ws_send(sock, 0xA, data)
            elif op == 0x8:
                raise EOFError("closed by the peer (close frame)")
        if ARGS.hello:
            if not pings:
                raise RuntimeError("no ping in the hold")
            ws_send(sock, 0x8, struct.pack("!H", 1000))
            print(json.dumps({"ok": True, "seconds": round(time.time() - start), "pings": pings}))
            return
        sock.settimeout(15)
        ws_send(sock, 0x1, b"still-here")
        while True:
            op, data = ws_recv(sock)
            if op == 0x9:
                ws_send(sock, 0xA, data)
                continue
            if op == 0x1 and data == b"still-here":
                break
            raise RuntimeError(f"unexpected frame {op}")
        ws_send(sock, 0x8, struct.pack("!H", 1000))
        print(json.dumps({"ok": True, "seconds": round(time.time() - start), "pings": pings}))
    except Exception as e:  # noqa: BLE001 - the probe reports every failure as data
        print(json.dumps({"ok": False, "seconds": round(time.time() - start), "pings": pings, "error": str(e)}))


{"http": cmd_http, "ws": cmd_ws, "login": cmd_login}[ARGS.cmd]()
sys.stdout.flush()
