"""Unit tests for Maku-owned stuck-loop detection (OpenCode plan Task 7)."""
import os
import sys
import unittest

HERE = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..")
sys.path.insert(0, HERE)
import loop_guard  # noqa: E402


class LoopGuardTest(unittest.TestCase):
    def test_three_actions_are_not_a_loop_but_four_are(self):
        pair = {"tool": "shell", "input": {"command": "true"}, "output": "ok"}
        self.assertIsNone(loop_guard.detect([pair] * 3))
        self.assertEqual(loop_guard.detect([pair] * 4), "repeated_action")

    def test_different_file_reads_are_not_identical_actions(self):
        rows = [{"tool": "read", "input": {"path": str(n)}, "output": "ok"} for n in range(10)]
        self.assertIsNone(loop_guard.detect(rows))

    def test_three_repeated_errors_trigger_repeated_error(self):
        err = {"tool": "shell", "input": {"command": "go test"}, "error": "FAIL"}
        self.assertIsNone(loop_guard.detect([err] * 2))
        self.assertEqual(loop_guard.detect([err] * 3), "repeated_error")

    def test_three_agent_messages_without_action_trigger_monologue(self):
        msg = {"kind": "MessageEvent", "source": "agent", "llm_message": {"content": [{"type": "text", "text": "thinking"}]}}
        self.assertIsNone(loop_guard.detect([msg] * 2))
        self.assertEqual(loop_guard.detect([msg] * 3), "monologue")

    def test_six_alternating_cycles_trigger_alternating(self):
        a = {"tool": "read", "input": {"path": "a.go"}, "output": "a"}
        b = {"tool": "read", "input": {"path": "b.go"}, "output": "b"}
        self.assertIsNone(loop_guard.detect([a, b, a, b, a]))
        self.assertEqual(loop_guard.detect([a, b, a, b, a, b]), "alternating")

    def test_repeated_context_window_errors_trigger_context_window(self):
        e1 = {"kind": "ConversationErrorEvent", "code": "LLMError", "detail": "maximum context length exceeded"}
        self.assertIsNone(loop_guard.detect([e1]))
        self.assertEqual(loop_guard.detect([e1, e1]), "context_window")

    def test_only_events_after_last_user_message_count(self):
        pair = {"tool": "shell", "input": {"command": "true"}, "output": "ok"}
        user = {"kind": "MessageEvent", "source": "user", "llm_message": {"content": [{"type": "text", "text": "try another file"}]}}
        self.assertIsNone(loop_guard.detect([pair] * 3 + [user] + [pair] * 2))
        self.assertEqual(loop_guard.detect([pair] * 3 + [user] + [pair] * 4), "repeated_action")

    def test_normalized_action_and_observation_events_ignore_ids(self):
        events = []
        for i in range(4):
            events.append({"id": f"a{i}", "kind": "ActionEvent", "tool_call_id": f"c{i}", "tool_name": "shell", "action": {"command": "ls"}})
            events.append({"id": f"o{i}", "kind": "ObservationEvent", "tool_call_id": f"c{i}", "tool_name": "shell", "observation": {"content": [{"type": "text", "text": "same"}], "is_error": False}})
        self.assertEqual(loop_guard.detect(events), "repeated_action")


if __name__ == "__main__":
    unittest.main()
