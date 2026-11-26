# SPDX-License-Identifier: Apache-2.0
"""Probe P7's caller: a two-tool loop through the gateway, as a harness runs one.

Each assistant turn is sent back as the caller received it, with the tool's result after it. In mode
drop the caller keeps only the fields of OpenAI's schema (id, type, function), as a client that
drops unknown fields does; in mode echo it sends each tool call back whole (extra_content too).
Prints one JSON line: {"statuses": [...], "ids": [...], "error": ..., "report": <the fake's report>}.

usage: tool-loop.py URL TOKEN_FILE MODEL drop|echo
"""
import json
import sys
import urllib.error
import urllib.request

url, token_file, model, mode = sys.argv[1:5]
with open(token_file, encoding="utf-8") as f:
    token = f.read().strip()
tools = [{"type": "function", "function": {"name": "clock", "description": "The time.",
                                           "parameters": {"type": "object", "properties": {"step": {"type": "integer"}}}}}]
messages = [{"role": "user", "content": "What time is it? Ask the clock twice."}]
out = {"statuses": [], "ids": [], "error": None, "report": None}
for _ in range(4):
    body = {"model": model, "messages": messages, "tools": tools, "temperature": 0, "reasoning_effort": "low"}
    req = urllib.request.Request(url, data=json.dumps(body).encode(), method="POST", headers={
        "Content-Type": "application/json", "Authorization": "Bearer " + token})
    try:
        with urllib.request.urlopen(req, timeout=30) as resp:
            status, data = resp.status, json.loads(resp.read())
    except urllib.error.HTTPError as e:
        out["statuses"].append(e.code)
        out["error"] = e.read().decode()[:400]
        break
    out["statuses"].append(status)
    msg = data["choices"][0]["message"]
    calls = msg.get("tool_calls") or []
    if not calls:
        try:
            out["report"] = json.loads(msg.get("content") or "{}")
        except ValueError:
            out["report"] = {"content": msg.get("content")}
        out["usage"] = data.get("usage")
        break
    if mode == "drop":
        calls = [{"id": c["id"], "type": c.get("type", "function"), "function": c["function"]} for c in calls]
    out["ids"] += [c["id"] for c in calls]
    messages.append({"role": "assistant", "content": None, "tool_calls": calls})
    messages += [{"role": "tool", "tool_call_id": c["id"], "content": "12:00"} for c in calls]
print(json.dumps(out))
