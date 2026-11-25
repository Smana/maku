"""Real OpenCode 2.3.0 server boot and shutdown tests (OpenCode plan Task 3)."""
import base64
import json
import os
import socket
import sys
import tempfile
import unittest
import urllib.request

HERE = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..")
sys.path.insert(0, HERE)
import agent_run  # noqa: E402
import opencode_driver  # noqa: E402


def _free_port() -> int:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


def _request_json(url: str, password: str) -> dict:
    tok = base64.b64encode(f"opencode:{password}".encode("utf-8")).decode("ascii")
    req = urllib.request.Request(url, headers={"Authorization": "Basic " + tok})
    with urllib.request.urlopen(req, timeout=2) as resp:
        return json.loads(resp.read().decode("utf-8"))


class BootTest(unittest.TestCase):
    def test_driver_for_selects_opencode(self):
        d = agent_run.driver_for({"AGENT_HARNESS": "opencode"})
        self.assertIsInstance(d, opencode_driver.OpenCodeDriver)

    def test_real_server_becomes_ready_and_stops(self):
        tmp = tempfile.mkdtemp(prefix="oc-boot-")
        port = _free_port()
        env = {
            "AGENT_HARNESS": "opencode",
            "OPENCODE_PORT": str(port),
            "OPENCODE_WORKSPACE_ROOT": os.path.join(tmp, ".opencode"),
            "OPENCODE_SESSION_ID_FILE": os.path.join(tmp, ".session-id"),
            "OPENCODE_PASSWORD_FILE": os.path.join(tmp, ".password"),
            "OPENCODE_TERMINAL_FILE": os.path.join(tmp, ".terminal.json"),
            "OPENCODE_WORKDIR": tmp,
        }
        d = opencode_driver.OpenCodeDriver(env)
        d.start()
        try:
            d.wait_ready(deadline_s=30)
            info = _request_json(f"http://127.0.0.1:{port}/api/info", d.password)
            self.assertEqual(info["version"], "2.4.1")
            self.assertIn(f"http://127.0.0.1:{port}", info["urls"])
            # Session creation works on the real server and persists session ID atomically.
            created = d._request("POST", "/api/session", {})
            sid = created["data"]["id"]
            self.assertRegex(sid, opencode_driver.SESSION_ID_RE)
        finally:
            result = d.stop(timeout=5)
        self.assertIn(result, ("stopped", "killed"))
        with self.assertRaises(OSError):
            _request_json(f"http://127.0.0.1:{port}/api/info", d.password)

    def test_non_root_uid_and_no_auto_update_config(self):
        self.assertEqual(os.getuid(), 10001)
        tmp = tempfile.mkdtemp(prefix="oc-cfg-")
        env = {
            "OPENCODE_WORKSPACE_ROOT": os.path.join(tmp, ".opencode"),
            "OPENCODE_PASSWORD_FILE": os.path.join(tmp, ".password"),
        }
        d = opencode_driver.OpenCodeDriver(env)
        d.paths = opencode_driver.oc_config.workspace_paths(d.root)
        d._write_initial_config()
        with open(os.path.join(d.paths["config"], "opencode.jsonc"), encoding="utf-8") as f:
            cfg = json.load(f)
        self.assertEqual(cfg["update"], "disable")
        self.assertEqual(cfg["share"], "disabled")


if __name__ == "__main__":
    unittest.main()
