"""Metadata-only OTel span tests for normalized harness events (OpenCode plan Task 10)."""
import json
import os
import sys
import unittest

HERE = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..")
sys.path.insert(0, HERE)
import event_tracing  # noqa: E402

FORBIDDEN = ("gen_ai.prompt", "gen_ai.completion", "process.command", "shell.command", "tool.input", "tool.output")


class EventTracingTest(unittest.TestCase):
    def test_span_has_usage_but_no_content_or_command(self):
        tracer = event_tracing.EventTracer()
        tracer.consume({
            "type": "step_finish",
            "tokens": {"input": 10, "output": 4, "cache": {"read": 8, "write": 2}},
            "finish": "stop",
            "text": "SUPER_SECRET_TOKEN_12345",
        })
        self.assertEqual(len(tracer.finished), 1)
        span = tracer.finished[-1]
        self.assertEqual(span.attributes["gen_ai.usage.input_tokens"], 10)
        self.assertEqual(span.attributes["gen_ai.usage.output_tokens"], 4)
        self.assertEqual(span.attributes["gen_ai.usage.cache_read_input_tokens"], 8)
        self.assertEqual(span.attributes["gen_ai.usage.cache_creation_input_tokens"], 2)
        for key in FORBIDDEN:
            self.assertNotIn(key, span.attributes)
        self.assertNotIn("SUPER_SECRET_TOKEN_12345", json.dumps(span.attributes))

    def test_tool_span_omits_command_and_output_secrets(self):
        tracer = event_tracing.EventTracer()
        tracer.consume({
            "kind": "ActionEvent",
            "tool_call_id": "call-1",
            "tool_name": "shell",
            "action": {"command": "echo SUPER_SECRET_CMD"},
        })
        tracer.consume({
            "kind": "ObservationEvent",
            "tool_call_id": "call-1",
            "tool_name": "shell",
            "observation": {"content": [{"type": "text", "text": "SUPER_SECRET_OUT"}], "is_error": False},
        })
        self.assertEqual(len(tracer.finished), 1)
        span = tracer.finished[-1]
        self.assertEqual(span.attributes["gen_ai.tool.name"], "shell")
        for key in FORBIDDEN:
            self.assertNotIn(key, span.attributes)
        serialized = json.dumps(span.attributes)
        self.assertNotIn("SUPER_SECRET_CMD", serialized)
        self.assertNotIn("SUPER_SECRET_OUT", serialized)


if __name__ == "__main__":
    unittest.main()
