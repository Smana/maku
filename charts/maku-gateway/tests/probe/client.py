# SPDX-License-Identifier: Apache-2.0
"""The probe's caller: one chat completion through the gateway, printed as `<status> <body>`.

usage: client.py URL TOKEN_FILE|- MODEL [MESSAGE]

MESSAGE IMAGE sends a tool call, its result and a user message carrying one image part.
"""
import json
import sys
import urllib.error
import urllib.request

url, token_file, model = sys.argv[1], sys.argv[2], sys.argv[3]
message = sys.argv[4] if len(sys.argv) > 4 else "hello"
messages = [{"role": "user", "content": message}]
if message == "IMAGE":
    # What both harnesses send after an MCP tool answers with an image (screenshots ledger L1, L2): the
    # tool message keeps its text, and the image rides a user message right after it.
    messages = [
        {"role": "user", "content": "take a screenshot"},
        {"role": "assistant", "content": "", "tool_calls": [
            {"id": "call_1", "type": "function", "function": {"name": "browser_take_screenshot", "arguments": "{}"}}]},
        {"role": "tool", "tool_call_id": "call_1", "content": "ok"},
        {"role": "user", "content": [
            {"type": "text", "text": "look"},
            {"type": "image_url", "image_url": {"url": "data:image/png;base64,iVBORw0KGgo="}}]},
    ]
body = {"model": model, "messages": messages, "temperature": 0, "top_p": 0.5,
        "reasoning_effort": "low", "max_tokens": 64}
req = urllib.request.Request(url, data=json.dumps(body).encode(), method="POST",
                             headers={"Content-Type": "application/json", "x-ar-agent": "forged", "x-ar-human": "forged"})
if token_file != "-":
    with open(token_file, encoding="utf-8") as f:
        req.add_header("Authorization", "Bearer " + f.read().strip())
try:
    with urllib.request.urlopen(req, timeout=30) as resp:
        print(resp.status, resp.read().decode())
except urllib.error.HTTPError as e:
    print(e.code, e.read().decode())
except (urllib.error.URLError, OSError) as e:
    print(0, str(e))
