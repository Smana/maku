#!/usr/bin/env python3
"""A CONNECT proxy that allows exactly the hosts in ALLOW (comma-separated) on 443 and logs each
CONNECT as `allow <host>` or `refuse <host>` to the file named by LOG. Stands in for the run's CNP,
or for the egress proxy: the hosts in INSTALL are allowed too until a `POST /v1/close-install`
closes them (logged `close <body>`; then `refuse install-closed <host>`), and
install-check.maku.invalid:443 answers 409 while they are open, 403 once closed. CLOSE=fail answers the close
500 and CLOSE=ignore answers 200 and keeps them open: the harness's fail-closed paths. DIAL=0 answers
an allowed CONNECT 200 without dialling it, for tests with no way out."""
import asyncio
import os
import sys

ALLOW = set(filter(None, os.environ["ALLOW"].split(",")))
INSTALL = set(filter(None, os.environ.get("INSTALL", "").split(",")))
STATE = {"closed": False}
LOG = open(os.environ["LOG"], "a", buffering=1)
os.chmod(os.environ["LOG"], 0o666)  # the suite on the host truncates it between phases


async def pipe(r, w):
    try:
        while data := await r.read(65536):
            w.write(data)
            await w.drain()
    except Exception:
        pass
    finally:
        w.close()


async def handle(r, w):
    line = (await r.readline()).decode(errors="replace")
    length = 0
    while (h := await r.readline()) not in (b"\r\n", b""):
        k, _, v = h.decode(errors="replace").partition(":")
        if k.strip().lower() == "content-length":
            length = int(v.strip() or 0)
    parts = line.split()
    if parts[:2] == ["POST", "/v1/close-install"]:
        body = (await r.readexactly(length)).decode(errors="replace") if length else ""
        LOG.write("close %s\n" % body)
        mode = os.environ.get("CLOSE", "ok")
        if mode == "fail":
            w.write(b"HTTP/1.1 500 Internal Server Error\r\nContent-Length: 0\r\n\r\n")
        else:
            STATE["closed"] = STATE["closed"] or mode == "ok"
            w.write(b"HTTP/1.1 200 OK\r\nContent-Length: 7\r\n\r\nclosed\n")
        await w.drain()
        w.close()
        return
    if len(parts) < 2 or parts[0] != "CONNECT":
        LOG.write("refuse non-connect %s\n" % line.strip())
        w.write(b"HTTP/1.1 405 Method Not Allowed\r\n\r\n")
        w.close()
        return
    host, _, port = parts[1].rpartition(":")
    if parts[1] == "install-check.maku.invalid:443":  # the egress proxy's self-check host, never dialled
        LOG.write("install-check %s\n" % ("closed" if STATE["closed"] else "open"))
        w.write(b"HTTP/1.1 403 Forbidden\r\n\r\n" if STATE["closed"] else b"HTTP/1.1 409 Conflict\r\n\r\n")
        w.close()
        return
    if host in INSTALL and port == "443" and STATE["closed"]:
        LOG.write("refuse install-closed %s\n" % host)
        w.write(b"HTTP/1.1 403 Forbidden\r\n\r\n")
        w.close()
        return
    if (host not in ALLOW and host not in INSTALL) or port != "443":
        LOG.write("refuse %s:%s\n" % (host, port))
        w.write(b"HTTP/1.1 403 Forbidden\r\n\r\n")
        w.close()
        return
    LOG.write("allow %s\n" % host)
    if os.environ.get("DIAL") == "0":
        w.write(b"HTTP/1.1 200 Connection established\r\n\r\n")
        await w.drain()
        w.close()
        return
    try:
        ur, uw = await asyncio.open_connection(host, 443)
    except OSError as exc:
        LOG.write("error %s %s\n" % (host, exc))
        w.write(b"HTTP/1.1 502 Bad Gateway\r\n\r\n")
        w.close()
        return
    w.write(b"HTTP/1.1 200 Connection established\r\n\r\n")
    await w.drain()
    await asyncio.gather(pipe(r, uw), pipe(ur, w))


async def main():
    server = await asyncio.start_server(handle, "0.0.0.0", int(sys.argv[1]))
    async with server:
        await server.serve_forever()


asyncio.run(main())
