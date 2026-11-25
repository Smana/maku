"""Bounded synchronous OpenCode search tests (OpenCode plan Task 8)."""
import json
import os
import pathlib
import sys
import tempfile
import unittest

HERE = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..")
sys.path.insert(0, HERE)
from harness_types import EventPage  # noqa: E402
import search  # noqa: E402


class FakeSearchDriver:
    def __init__(self, *, tokens: int = 400, steps: int = 3, answer: str = "found in internal/bridge/harness.go", status: str = "finished"):
        self.tokens = tokens
        self.steps = steps
        self.answer = answer
        self.final_status = status
        self.sessions_created = 0
        self.interrupts = 0
        self.last_request = None

    def create_search_session(self, *, query: str, model: str, effort: str = "low", max_steps: int = 40) -> str:
        self.sessions_created += 1
        self.last_request = {"query": query, "model": model, "effort": effort, "max_steps": max_steps}
        return f"ses_search_{self.sessions_created}"

    def events(self, conversation_id: str, cursor: str = "") -> EventPage:
        items = [
            {"id": f"{conversation_id}:a{i}", "kind": "ActionEvent", "tool_name": "read", "tool_call_id": f"c{i}"}
            for i in range(self.steps)
        ]
        items.append({
            "id": f"{conversation_id}:msg",
            "kind": "MessageEvent",
            "source": "agent",
            "llm_message": {"content": [{"type": "text", "text": self.answer}]},
            "usage": {"input": self.tokens, "output": 0},
        })
        return EventPage(items=items)

    def status(self, conversation_id: str) -> str:
        return self.final_status

    def interrupt(self, conversation_id: str, timeout: float = 10) -> None:
        self.interrupts += 1


class SearchTest(unittest.TestCase):
    def _env(self):
        return {
            "MODEL": "agent-default",
            "LIGHT_MODEL": "tier-light",
            "LIGHT_BASE_URL": "http://127.0.0.1:4000/v1",
            "SEARCH_LEDGER": os.path.join(tempfile.mkdtemp(), ".agent-search.jsonl"),
        }

    def test_search_is_sync_light_and_bounded(self):
        env = self._env()
        driver = FakeSearchDriver(tokens=400, steps=3)
        result = search.search("where is validation?", env, driver)
        self.assertEqual(result.model, "tier-light")
        self.assertEqual(result.effort, "low")
        self.assertEqual(result.tool_calls, 3)
        self.assertEqual(result.outcome, "complete")
        row = json.loads(pathlib.Path(env["SEARCH_LEDGER"]).read_text().splitlines()[-1])
        self.assertEqual(row["outcome"], "complete")

    def test_sixth_call_is_the_last(self):
        env = self._env()
        driver = FakeSearchDriver(tokens=100, steps=1)
        for _ in range(6):
            self.assertEqual(search.search("where is validation?", env, driver).outcome, "complete")
        with self.assertRaisesRegex(search.BudgetExceeded, "six search calls"):
            search.search("one lookup too many", env, driver)
        self.assertEqual(driver.sessions_created, 6)

    def test_token_ceiling_interrupts_the_child(self):
        env = self._env()
        driver = FakeSearchDriver(tokens=300_001, steps=2)
        with self.assertRaisesRegex(search.BudgetExceeded, "300000 tokens"):
            search.search("where is validation?", env, driver)
        self.assertEqual(driver.interrupts, 1)
        row = json.loads(pathlib.Path(env["SEARCH_LEDGER"]).read_text().splitlines()[-1])
        self.assertEqual(row["outcome"], "budget")

    def test_result_is_truncated_to_8000_chars(self):
        env = self._env()
        driver = FakeSearchDriver(answer="A" * 12_000, steps=1)
        result = search.search("big output", env, driver)
        self.assertEqual(len(result.answer), 8000)


if __name__ == "__main__":
    unittest.main()
