// SPDX-License-Identifier: Apache-2.0
// The contract's stand-in for the harness's dev server, run inside the maku-browser container
// (which has no python3): node -e "$(cat serve.js)" DIR PORT serves DIR's files on 127.0.0.1:PORT.
"use strict";
const fs = require("fs");
const http = require("http");
const path = require("path");

const [dir, port] = process.argv.slice(1);
const types = { ".html": "text/html; charset=utf-8", ".png": "image/png" };
http.createServer((req, res) => {
  const name = path.basename(new URL(req.url, "http://x").pathname) || "index.html";
  fs.readFile(path.join(dir, name), (err, body) => {
    if (err) { res.writeHead(404).end("not found\n"); return; }
    res.writeHead(200, { "Content-Type": types[path.extname(name)] || "application/octet-stream" }).end(body);
  });
}).listen(Number(port), "127.0.0.1");
