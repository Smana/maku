# SPDX-License-Identifier: Apache-2.0
"""The probe's caller: one chat completion through the gateway, printed as `<status> <body>`.

usage: client.py URL TOKEN_FILE|- MODEL [MESSAGE]
"""
import json
import sys
import urllib.error
import urllib.request

url, token_file, model = sys.argv[1], sys.argv[2], sys.argv[3]
message = sys.argv[4] if len(sys.argv) > 4 else "hello"
body = {"model": model, "messages": [{"role": "user", "content": message}], "temperature": 0, "top_p": 0.5,
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
