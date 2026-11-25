"""Real OpenCode 2.3.0 approval, rejection, and steering tests (OpenCode plan Task 5)."""
import json
import os
import sys
import tempfile
import time
import unittest
import urllib.parse

TESTS_DIR = os.path.dirname(os.path.abspath(__file__))
HERE = os.path.join(TESTS_DIR, "..")
sys.path.insert(0, HERE)
sys.path.insert(0, TESTS_DIR)
from harness_types import ConversationRequest  # noqa: E402
import opencode_driver  # noqa: E402
from test_provider_contract import ScriptedLLMServer, _free_port  # noqa: E402


class RealServerControlTest(unittest.TestCase):
    def _make_driver(self, tmp: str, llm_url: str, **extra_env) -> tuple[opencode_driver.OpenCodeDriver, dict]:
        port = _free_port()
        env = {
            "AGENT_HARNESS": "opencode",
            "ROLE": "implementer",
            "MODEL": "agent-default",
            "DATA_CLASS": "public",
            "LLM_BASE_URL": llm_url,
            "LLM_REASONING_EFFORT": "low",
            "OPENCODE_PORT": str(port),
            "OPENCODE_WORKSPACE_ROOT": os.path.join(tmp, ".opencode"),
            "OPENCODE_SESSION_ID_FILE": os.path.join(tmp, ".session-id"),
            "OPENCODE_PASSWORD_FILE": os.path.join(tmp, ".password"),
            "OPENCODE_TERMINAL_FILE": os.path.join(tmp, ".terminal.json"),
            "OPENCODE_WORKDIR": tmp,
            **extra_env,
        }
        return opencode_driver.OpenCodeDriver(env), env

    def _wait_status(self, driver: opencode_driver.OpenCodeDriver, cid: str, want: set[str], timeout_s: float = 20.0) -> str:
        end = time.monotonic() + timeout_s
        last = ""
        while time.monotonic() < end:
            last = driver.status(cid)
            if last in want:
                return last
            time.sleep(0.2)
        return last

    def test_permission_ask_waits_and_reject_blocks_shell_execution(self):
        """When OPENCODE_ALWAYS_ASK=1, a shell tool call pauses in waiting_for_confirmation and reject prevents side effects."""
        tmp = tempfile.mkdtemp(prefix="oc-perm-reject-")
        sentinel = os.path.join(tmp, "should-not-exist.txt")
        llm = ScriptedLLMServer([
            {
                "tool_call": {
                    "id": "call_shell_1",
                    "name": "shell",
                    "arguments": {"command": f"echo pwned > {sentinel}", "description": "create sentinel"},
                }
            },
            {"text": "understood, skipped creating file"},
        ])
        llm.start()
        self.addCleanup(llm.stop)

        d, env = self._make_driver(tmp, llm.base_url, OPENCODE_ALWAYS_ASK="1")
        d.start()
        try:
            d.wait_ready(deadline_s=30)
            cid = d.create_conversation(ConversationRequest(env=env, task="run shell", rules="brief"))
            st = self._wait_status(d, cid, {"waiting_for_confirmation", "finished", "error"})
            self.assertEqual(st, "waiting_for_confirmation")
            q_sid = urllib.parse.quote(cid, safe="")
            perms = (d._request("GET", f"/api/session/{q_sid}/permission", timeout=10).get("data") or [])
            self.assertGreaterEqual(len(perms), 1)
            req_id = perms[0]["id"]
            # Reject via the real OpenCode 2.3.0 permission reply endpoint.
            d._request(
                "POST",
                f"/api/session/{q_sid}/permission/{urllib.parse.quote(req_id, safe='')}/reply",
                {"decision": "reject", "message": "denied by operator"},
                timeout=10,
            )
            st2 = self._wait_status(d, cid, {"finished", "idle", "error"})
            self.assertIn(st2, ("finished", "idle"))
            self.assertFalse(os.path.exists(sentinel), "rejected shell command must not execute")
        finally:
            d.stop(timeout=5)

    def test_permission_approve_once_executes_shell_and_completes(self):
        tmp = tempfile.mkdtemp(prefix="oc-perm-allow-")
        sentinel = os.path.join(tmp, "approved.txt")
        llm = ScriptedLLMServer([
            {
                "tool_call": {
                    "id": "call_shell_2",
                    "name": "shell",
                    "arguments": {"command": f"echo approved > {sentinel}", "description": "create approved marker"},
                }
            },
            {"text": "marker created"},
        ])
        llm.start()
        self.addCleanup(llm.stop)

        d, env = self._make_driver(tmp, llm.base_url, OPENCODE_ALWAYS_ASK="1")
        d.start()
        try:
            d.wait_ready(deadline_s=30)
            cid = d.create_conversation(ConversationRequest(env=env, task="run shell", rules="brief"))
            st = self._wait_status(d, cid, {"waiting_for_confirmation", "finished", "error"})
            self.assertEqual(st, "waiting_for_confirmation")
            q_sid = urllib.parse.quote(cid, safe="")
            perms = (d._request("GET", f"/api/session/{q_sid}/permission", timeout=10).get("data") or [])
            self.assertGreaterEqual(len(perms), 1)
            req_id = perms[0]["id"]
            d._request(
                "POST",
                f"/api/session/{q_sid}/permission/{urllib.parse.quote(req_id, safe='')}/reply",
                {"decision": "once"},
                timeout=10,
            )
            st2 = self._wait_status(d, cid, {"finished", "idle", "error"})
            self.assertIn(st2, ("finished", "idle"))
            self.assertTrue(os.path.exists(sentinel), "approved shell command must execute")
            page = d.events(cid)
            self.assertTrue(any(e["kind"] == "ActionEvent" for e in page.items))
            self.assertTrue(any(e["kind"] == "ObservationEvent" for e in page.items))
        finally:
            d.stop(timeout=5)

    def test_steering_delivers_follow_up_message_to_session(self):
        tmp = tempfile.mkdtemp(prefix="oc-steer-")
        llm = ScriptedLLMServer([
            {"text": "first response"},
            {"text": "steered response"},
        ])
        llm.start()
        self.addCleanup(llm.stop)

        d, env = self._make_driver(tmp, llm.base_url)
        d.start()
        try:
            d.wait_ready(deadline_s=30)
            cid = d.create_conversation(ConversationRequest(env=env, task="initial task", rules="brief"))
            self.assertIn(self._wait_status(d, cid, {"finished", "idle"}), ("finished", "idle"))
            # Operator steers with a follow-up prompt.
            d.send(cid, "please change direction", run=True)
            # Wait until second LLM call completes and session returns to idle/finished.
            end = time.monotonic() + 15.0
            while time.monotonic() < end:
                if len(llm.requests) >= 2 and d.status(cid) in ("finished", "idle"):
                    break
                time.sleep(0.2)
            self.assertEqual(len(llm.requests), 2)
            page = d.events(cid)
            texts = [
                part.get("text", "")
                for e in page.items
                if e.get("kind") == "MessageEvent"
                for part in (e.get("llm_message") or {}).get("content", [])
            ]
            self.assertTrue(any("steered response" in t for t in texts), texts)
        finally:
            d.stop(timeout=5)


if __name__ == "__main__":
    unittest.main()
