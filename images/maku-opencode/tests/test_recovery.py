"""Real OpenCode 2.3.0 recovery, owned job cleanup, and graceful shutdown tests (OpenCode plan Task 9)."""
import os
import sys
import tempfile
import time
import unittest

TESTS_DIR = os.path.dirname(os.path.abspath(__file__))
HERE = os.path.join(TESTS_DIR, "..")
sys.path.insert(0, HERE)
sys.path.insert(0, TESTS_DIR)
from harness_types import ConversationRequest  # noqa: E402
import opencode_driver  # noqa: E402
from test_provider_contract import ScriptedLLMServer, _free_port  # noqa: E402


class RealServerRecoveryTest(unittest.TestCase):
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

    def test_interrupt_stops_owned_long_running_shell_and_shutdown_finishes_within_15s(self):
        """Interrupting a session with a live shell tool stops the child process and server shuts down cleanly within 15s."""
        tmp = tempfile.mkdtemp(prefix="oc-shutdown-")
        pid_file = os.path.join(tmp, "sleeper.pid")
        llm = ScriptedLLMServer([
            {
                "tool_call": {
                    "id": "call_sleep_1",
                    "name": "shell",
                    "arguments": {
                        "command": f"echo $$ > {pid_file}; sleep 60",
                        "description": "long running background command",
                    },
                }
            },
            {"text": "interrupted"},
        ])
        llm.start()
        self.addCleanup(llm.stop)

        d, env = self._make_driver(tmp, llm.base_url)
        d.start()
        try:
            d.wait_ready(deadline_s=30)
            cid = d.create_conversation(ConversationRequest(env=env, task="start long command", rules="brief"))
            # Wait until the shell command actually started and wrote its PID.
            deadline = time.monotonic() + 15.0
            while time.monotonic() < deadline and not os.path.exists(pid_file):
                time.sleep(0.1)
            self.assertTrue(os.path.exists(pid_file), "shell command should have started")
            with open(pid_file, encoding="utf-8") as f:
                child_pid = int(f.read().strip())

            t0 = time.monotonic()
            d.interrupt(cid, timeout=5)
            d.close(cid)
            stop_result = d.stop(timeout=10)
            elapsed = time.monotonic() - t0

            self.assertEqual(stop_result, "stopped")
            self.assertLess(elapsed, 15.0, f"shutdown took too long: {elapsed:.2f}s")

            # Verify child process is no longer alive (reaped or terminated shortly after stop).
            reap_end = time.monotonic() + 3.0
            alive = True
            while time.monotonic() < reap_end:
                try:
                    os.kill(child_pid, 0)
                    time.sleep(0.1)
                except ProcessLookupError:
                    alive = False
                    break
                except PermissionError:
                    alive = False
                    break
            self.assertFalse(alive, f"owned shell pid {child_pid} leaked after interrupt+stop")
        finally:
            if d.server is not None and d.server.poll() is None:
                d.stop(timeout=5)

    def test_pvc_restart_preserves_session_and_resumes_with_new_turn(self):
        """A recreated OpenCode server pointed at the same workspace PVC restores the session and continues it."""
        tmp = tempfile.mkdtemp(prefix="oc-pvc-resume-")
        llm = ScriptedLLMServer([
            {"text": "before suspend"},
            {"text": "after resume"},
        ])
        llm.start()
        self.addCleanup(llm.stop)

        d1, env1 = self._make_driver(tmp, llm.base_url, AGENT_SUSPENDABLE="true")
        d1.start()
        try:
            d1.wait_ready(deadline_s=30)
            cid = d1.create_conversation(ConversationRequest(env=env1, task="turn 1", rules="brief"))
            self.assertIn(self._wait_status(d1, cid, {"finished", "idle"}), ("finished", "idle"))
        finally:
            d1.stop(timeout=5)

        d2, env2 = self._make_driver(tmp, llm.base_url, AGENT_SUSPENDABLE="true")
        self.assertTrue(d2.persisted())
        self.assertEqual(d2.persisted_session_id(), cid)
        d2.start()
        try:
            d2.wait_ready(deadline_s=30)
            restored = d2.create_conversation(ConversationRequest(env=env2, task="ignored", rules="brief", resumed=True))
            self.assertEqual(restored, cid)
            d2.send(restored, "turn 2", run=True)
            end = time.monotonic() + 15.0
            while time.monotonic() < end:
                if len(llm.requests) >= 2 and d2.status(restored) in ("finished", "idle"):
                    break
                time.sleep(0.2)
            self.assertEqual(len(llm.requests), 2)
            page = d2.events(restored)
            texts = [
                part.get("text", "")
                for e in page.items
                if e.get("kind") == "MessageEvent"
                for part in (e.get("llm_message") or {}).get("content", [])
            ]
            self.assertTrue(any("before suspend" in t for t in texts), texts)
            self.assertTrue(any("after resume" in t for t in texts), texts)
        finally:
            d2.stop(timeout=5)


if __name__ == "__main__":
    unittest.main()
