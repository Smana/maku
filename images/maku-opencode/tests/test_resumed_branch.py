"""A run on a branch an earlier run pushed still sends its task to the session it creates.

agent_run passes resumed=True whenever the clone checked out a pushed branch: every retry and every
reviewer. Only a reattached session already has the task; a new one with no prompt never starts,
and the run ends stuck with 0 steps (gcp-0 trial, 2026-11-26, runs mn445p3v and y33syb37).
"""
import os
import sys
import tempfile
import unittest
import urllib.error

HERE = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..")
sys.path.insert(0, HERE)
from harness_types import ConversationRequest  # noqa: E402
import opencode_driver  # noqa: E402

SID = "ses_new123"
OLD = "ses_old456"


class FakeDriver(opencode_driver.OpenCodeDriver):
    def __init__(self, env, known=()):
        super().__init__(env)
        self.calls = []
        self.known = set(known)

    def _write_initial_config(self, cfg=None):
        pass

    def _request(self, method, path, body=None, timeout=30):
        self.calls.append((method, path, body))
        if method == "POST" and path == "/api/session":
            return {"data": {"id": SID}}
        if method == "GET" and path.startswith("/api/session/"):
            if path.rsplit("/", 1)[-1] in self.known:
                return {"data": {}}
            raise urllib.error.URLError("404")
        return {}

    def prompts(self):
        return [(p, b) for m, p, b in self.calls if m == "POST" and p.endswith("/prompt")]


class ResumedBranchTest(unittest.TestCase):
    def _driver(self, known=(), persisted=""):
        tmp = tempfile.mkdtemp(prefix="oc-resumed-")
        session_file = os.path.join(tmp, ".session-id")
        if persisted:
            with open(session_file, "w", encoding="utf-8") as f:
                f.write(persisted + "\n")
        env = {"ROLE": "implementer", "RUN_ID": "r1", "OPENCODE_SESSION_ID_FILE": session_file,
               "OPENCODE_TERMINAL_FILE": os.path.join(tmp, ".terminal.json")}
        return FakeDriver(env, known), env

    def test_a_new_session_on_a_pushed_branch_gets_the_task(self):
        d, env = self._driver()
        sid = d.create_conversation(ConversationRequest(env=env, task="do the task", rules="r", resumed=True))
        self.assertEqual(sid, SID)
        self.assertEqual(d.prompts(), [(f"/api/session/{SID}/prompt", {"text": "do the task"})])

    def test_a_persisted_session_that_is_gone_gets_the_task_in_a_new_one(self):
        d, env = self._driver(persisted=OLD)
        sid = d.create_conversation(ConversationRequest(env=env, task="do the task", rules="r", resumed=True))
        self.assertEqual(sid, SID)
        self.assertEqual(len(d.prompts()), 1)

    def test_a_reattached_session_is_sent_nothing(self):
        d, env = self._driver(known={OLD}, persisted=OLD)
        sid = d.create_conversation(ConversationRequest(env=env, task="ignored", rules="r", resumed=True))
        self.assertEqual(sid, OLD)
        self.assertEqual(d.prompts(), [], "a session waiting on an approval must not get a new message")

    def test_a_fresh_branch_gets_the_task(self):
        d, env = self._driver()
        d.create_conversation(ConversationRequest(env=env, task="do the task", rules="r"))
        self.assertEqual(len(d.prompts()), 1)


if __name__ == "__main__":
    unittest.main()
