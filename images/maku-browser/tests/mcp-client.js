// SPDX-License-Identifier: Apache-2.0
// The contract's MCP client, run inside the maku-browser container (the image has node, not curl):
//   docker exec -i <c> node - list            < mcp-client.js   tools/list's result
//   docker exec -i <c> node - call TOOL [ARGS] < mcp-client.js   tools/call's result (ARGS is JSON)
// It speaks Streamable HTTP to the gate on 127.0.0.1:9223, keeping one session in
// /tmp/mcp-client/session across invocations so a navigate and the snapshot after it share a page.
// It prints the JSON-RPC result, or {"rpcError": ...}, on one line.
"use strict";
const fs = require("fs");

const URL_ = "http://127.0.0.1:9223/mcp";
const SESSION = process.env.MCP_SESSION || "/tmp/mcp-client/session";
const PROTOCOL = "2025-06-18";
let nextId = Date.now();

async function post(msg, sid) {
  const headers = { "Content-Type": "application/json", Accept: "application/json, text/event-stream", "Mcp-Protocol-Version": PROTOCOL };
  if (sid) headers["Mcp-Session-Id"] = sid;
  const res = await fetch(URL_, { method: "POST", headers, body: JSON.stringify(msg), signal: AbortSignal.timeout(90_000) });
  const text = await res.text();
  if (res.status >= 400) throw new Error(`HTTP ${res.status}: ${text.slice(0, 300)}`);
  let reply = null;
  if ((res.headers.get("content-type") || "").startsWith("text/event-stream")) {
    for (const line of text.split("\n")) {
      if (!line.startsWith("data:")) continue;
      const m = JSON.parse(line.slice(5));
      if (m.id === msg.id) reply = m;
    }
  } else if (text.trim()) {
    reply = JSON.parse(text);
  }
  return { reply, sid: res.headers.get("mcp-session-id") };
}

async function session() {
  try {
    return fs.readFileSync(SESSION, "utf8").trim();
  } catch {}
  const init = { jsonrpc: "2.0", id: nextId++, method: "initialize",
    params: { protocolVersion: PROTOCOL, capabilities: {}, clientInfo: { name: "maku-browser-contract", version: "1" } } };
  const { reply, sid } = await post(init);
  if (!sid || !reply || reply.error) throw new Error(`initialize: ${JSON.stringify(reply)}`);
  await post({ jsonrpc: "2.0", method: "notifications/initialized" }, sid);
  fs.mkdirSync(require("path").dirname(SESSION), { recursive: true });
  fs.writeFileSync(SESSION, sid);
  return sid;
}

async function main() {
  const [cmd, tool, args] = process.argv.slice(2);
  const sid = await session();
  const msg = cmd === "list"
    ? { jsonrpc: "2.0", id: nextId++, method: "tools/list", params: {} }
    : { jsonrpc: "2.0", id: nextId++, method: "tools/call", params: { name: tool, arguments: args ? JSON.parse(args) : {} } };
  const { reply } = await post(msg, sid);
  process.stdout.write(JSON.stringify(reply && reply.error ? { rpcError: reply.error } : reply ? reply.result : null) + "\n");
}

main().catch((e) => {
  process.stderr.write(`mcp-client: ${e.message}\n`);
  process.exit(1);
});
