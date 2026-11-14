"""Anthropic prompt caching on internal runs.

The SDK marks its breakpoints with Anthropic's `cache_control`, but agentgateway 1.6 converts a Chat
Completions request to Messages from the OpenAI field only: a text part's
`prompt_cache_breakpoint: {"mode": "explicit"}` becomes `cache_control: {"type": "ephemeral"}`
(golden test prompt-cache-breakpoint.anthropic.snap), and `cache_control` is dropped. Drives the
SDK's real LLM, as agent-run configures it, against a stand-in gateway that records each body.
"""
import asyncio
import http.server
import importlib.util
import io
import json
import os
import sys
import tempfile
import threading
import unittest
from unittest import mock

HERE = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..")
sys.path.insert(0, HERE)

import agent_run  # noqa: E402

SITE = os.path.join(HERE, "site", "sitecustomize.py")
BREAKPOINT = {"mode": "explicit"}
USAGE = {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}
OK = json.dumps({"id": "x", "object": "chat.completion", "created": 0, "model": "m", "usage": USAGE,
                 "choices": [{"index": 0, "finish_reason": "stop", "message": {"role": "assistant", "content": "ok"}}]}).encode()


class Gateway(http.server.BaseHTTPRequestHandler):
    bodies = []

    def do_POST(self):
        Gateway.bodies.append(json.loads(self.rfile.read(int(self.headers["content-length"]))))
        self.send_response(200)
        self.send_header("content-type", "application/json")
        self.send_header("content-length", str(len(OK)))
        self.end_headers()
        self.wfile.write(OK)

    def log_message(self, *args):
        pass


def load_site():
    spec = importlib.util.spec_from_file_location("harness_sitecustomize", SITE)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def env(**extra):
    return {"MODEL": "agent-default", "LLM_BASE_URL": "http://127.0.0.1:4000/v1",
            "MCP_URL": "http://127.0.0.1:1/mcp", **extra}


class RequestTest(unittest.TestCase):
    def overrides(self, **extra):
        return agent_run.build_request(env(**extra), "t", "r")["agent"]["llm"].get("capability_overrides", {})

    def test_an_internal_run_turns_the_sdk_cache_on(self):
        self.assertIs(self.overrides(DATA_CLASS="internal")["supports_prompt_cache"], True)

    def test_a_public_run_does_not(self):
        # Z.ai is a passthrough: an unknown part field would reach its API.
        self.assertNotIn("supports_prompt_cache", self.overrides(DATA_CLASS="public"))
        self.assertNotIn("supports_prompt_cache", self.overrides())


class BreakpointsTest(unittest.TestCase):
    def setUp(self):
        self.site = load_site()

    def test_a_part_marker_becomes_the_openai_breakpoint(self):
        messages = [{"role": "system", "content": [{"type": "text", "text": "s", "cache_control": {"type": "ephemeral"}}]}]
        self.site.cache_breakpoints(messages)
        self.assertEqual(messages, [{"role": "system", "content": [
            {"type": "text", "text": "s", "prompt_cache_breakpoint": BREAKPOINT}]}])

    def test_a_tool_message_marker_moves_onto_its_last_part(self):
        messages = [{"role": "tool", "tool_call_id": "c", "cache_control": {"type": "ephemeral"},
                     "content": [{"type": "text", "text": "a"}, {"type": "text", "text": "b"}]}]
        self.site.cache_breakpoints(messages)
        self.assertEqual(messages[0], {"role": "tool", "tool_call_id": "c", "content": [
            {"type": "text", "text": "a"}, {"type": "text", "text": "b", "prompt_cache_breakpoint": BREAKPOINT}]})

    def test_a_string_content_marker_becomes_a_text_part(self):
        messages = [{"role": "tool", "tool_call_id": "c", "cache_control": {"type": "ephemeral"}, "content": "out"}]
        self.site.cache_breakpoints(messages)
        self.assertEqual(messages[0]["content"], [{"type": "text", "text": "out", "prompt_cache_breakpoint": BREAKPOINT}])

    def test_unmarked_messages_are_untouched(self):
        messages = [{"role": "user", "content": [{"type": "text", "text": "hi"}]}, {"role": "user", "content": "x"}]
        before = json.loads(json.dumps(messages))
        self.site.cache_breakpoints(messages)
        self.assertEqual(messages, before)


class WireTest(unittest.TestCase):
    """What leaves the harness, through litellm, for the gateway to convert."""

    def setUp(self):
        from openhands.sdk import LLM

        site = load_site()
        for name in ("_transport_call", "_atransport_call"):
            self.addCleanup(setattr, LLM, name, getattr(LLM, name))
        self.assertTrue(site.patch_cache_breakpoints(LLM))
        server = http.server.HTTPServer(("127.0.0.1", 0), Gateway)
        threading.Thread(target=server.serve_forever, daemon=True).start()
        self.addCleanup(server.server_close)
        self.addCleanup(server.shutdown)
        self.base_url = "http://127.0.0.1:%d/v1" % server.server_port
        Gateway.bodies = []

    def send(self, data_class, run=lambda llm, m: llm.completion(messages=m)):
        from openhands.sdk import LLM, Message, TextContent
        from openhands.sdk.llm import MessageToolCall

        llm = LLM.model_validate(agent_run.build_request(
            env(LLM_BASE_URL=self.base_url, DATA_CLASS=data_class), "t", "r")["agent"]["llm"])
        call = MessageToolCall(id="c1", name="terminal", arguments="{}", origin="completion")
        run(llm, [
            Message(role="system", content=[TextContent(text="system prompt")]),
            Message(role="user", content=[TextContent(text="task")]),
            Message(role="assistant", content=[], tool_calls=[call]),
            Message(role="tool", tool_call_id="c1", name="terminal", content=[TextContent(text="output")]),
        ])
        self.assertEqual(len(Gateway.bodies), 1)
        return Gateway.bodies[0]["messages"]

    def assert_breakpoints(self, messages):
        self.assertNotIn("cache_control", json.dumps(messages), "1.6 drops it: no cache would be written")
        marked = [(m["role"], p["text"]) for m in messages if isinstance(m.get("content"), list)
                  for p in m["content"] if p.get("prompt_cache_breakpoint") == BREAKPOINT]
        # The SDK's two: the system prompt, and the conversation's newest turn.
        self.assertEqual(marked, [("system", "system prompt"), ("tool", "output")])

    def test_an_internal_call_carries_the_breakpoints(self):
        self.assert_breakpoints(self.send("internal"))

    def test_agent_servers_async_path_carries_them(self):
        self.assert_breakpoints(self.send("internal", run=lambda llm, m: asyncio.run(llm.agenerate(messages=m))))

    def test_a_public_call_carries_none(self):
        sent = json.dumps(self.send("public"))
        self.assertNotIn("prompt_cache_breakpoint", sent)
        self.assertNotIn("cache_control", sent)


class NotAppliedTest(unittest.TestCase):
    """The line AgentCacheMarkerMissing greps (observability/base/agent-platform/vmrule-logs.yaml in
    ai-native-ref): `prompt cache NOT applied`. A reworded line would silence the alert."""

    ALERT_GREP = "prompt cache NOT applied"

    def test_an_sdk_without_the_method_says_so_and_stays_unpatched(self):
        for missing in ("_transport_call", "_atransport_call"):
            with self.subTest(missing=missing):
                present = [n for n in ("_transport_call", "_atransport_call") if n != missing]
                llm = type("LLM", (), {n: (lambda self, **kw: None) for n in present})
                before = {n: getattr(llm, n) for n in present}
                with mock.patch("sys.stderr", new=io.StringIO()) as err:
                    self.assertFalse(load_site().patch_cache_breakpoints(llm))
                self.assertIn(self.ALERT_GREP, err.getvalue())
                self.assertEqual({n: getattr(llm, n) for n in present}, before)

    def test_an_sdk_that_cannot_be_imported_says_so_too(self):
        site = load_site()
        with mock.patch.dict(os.environ, {"PYTHONPATH": ""}), \
                mock.patch.dict(sys.modules, {"openhands.sdk.llm.llm": None, "mcp.client.streamable_http": None,
                                          "mcp.shared.session": None}), \
                mock.patch("sys.stderr", new=io.StringIO()) as err:
            site._install()
        self.assertIn(self.ALERT_GREP, err.getvalue())


if __name__ == "__main__":
    unittest.main()


class CacheLineTest(unittest.TestCase):
    def test_the_cache_line(self):
        usage = {"prompt_tokens": 182340, "cache_read_tokens": 176902, "cache_write_tokens": 5438}
        self.assertEqual(agent_run.cache_line(41, usage, "none"),
                         "agent-run cache: step=41 input=182340 cache_read=176902 cache_write=5438 miss=none")

    def test_a_provider_without_cache_fields_prints_zeros(self):
        self.assertEqual(agent_run.cache_line(3, {"prompt_tokens": 900}, "first"),
                         "agent-run cache: step=3 input=900 cache_read=0 cache_write=0 miss=first")


class MissKindTest(unittest.TestCase):
    def test_miss_kinds(self):
        k = agent_run.miss_kind
        self.assertEqual(k(1, 0, False, False), "first")
        self.assertEqual(k(1, 0, False, True), "resume")
        self.assertEqual(k(81, 0, True, False), "condensation")
        self.assertEqual(k(12, 0, False, False), "unexpected")
        self.assertEqual(k(12, 5000, False, False), "none")


class CondensationLineTest(unittest.TestCase):
    def test_the_condensation_line(self):
        self.assertEqual(agent_run.condensation_line(80, 76, "tier-light"),
                         "agent-run condensation: step=80 forgotten=76 summary_model=tier-light")


class StepLogCacheLinesTest(unittest.TestCase):
    """The StepLog wiring: one cache line per model response, one line per condensation."""

    def log(self, **kwargs):
        return agent_run.StepLog("cid", **kwargs)

    @staticmethod
    def stats(usages, usage_id="agent"):
        return {"usage_to_metrics": {usage_id: {"token_usages": usages}}}

    def test_a_stats_update_prints_one_cache_line_per_response(self):
        log = self.log()
        line = log.describe({"kind": "ConversationStateUpdateEvent", "key": "stats",
                             "value": self.stats([{"prompt_tokens": 900, "cache_read_tokens": 0,
                                                   "cache_write_tokens": 0}])})
        self.assertEqual(line, "agent-run cache: step=1 input=900 cache_read=0 cache_write=0 miss=first")

    def test_a_hit_prints_miss_none(self):
        log = self.log()
        line = log.describe({"kind": "ConversationStateUpdateEvent", "key": "stats",
                             "value": self.stats([{"prompt_tokens": 182340, "cache_read_tokens": 176902,
                                                   "cache_write_tokens": 5438}])})
        self.assertIn("miss=none", line)

    def test_a_response_after_a_condensation_is_a_condensation_miss(self):
        log = self.log(summary_model="agent-default")
        line = log.describe({"kind": "ConversationStateUpdateEvent", "key": "stats",
                             "value": self.stats([{"prompt_tokens": 100, "cache_read_tokens": 90,
                                                   "cache_write_tokens": 1}])})
        self.assertIn("miss=none", line)
        cline = log.describe({"kind": "Condensation", "forgotten_event_ids": ["e1", "e2"]})
        self.assertEqual(cline, "agent-run condensation: step=2 forgotten=2 summary_model=agent-default")
        line = log.describe({"kind": "ConversationStateUpdateEvent", "key": "stats",
                             "value": self.stats([{"prompt_tokens": 100, "cache_read_tokens": 90,
                                                   "cache_write_tokens": 1},
                                                  {"prompt_tokens": 40, "cache_read_tokens": 0,
                                                   "cache_write_tokens": 2}])})
        self.assertEqual(line, "agent-run cache: step=2 input=40 cache_read=0 cache_write=2 miss=condensation")

    def test_a_later_miss_is_unexpected(self):
        log = self.log()
        for _ in range(2):
            log.describe({"kind": "ConversationStateUpdateEvent", "key": "stats",
                          "value": self.stats([{"prompt_tokens": 10, "cache_read_tokens": 5,
                                                "cache_write_tokens": 1}])})
        line = log.describe({"kind": "ConversationStateUpdateEvent", "key": "stats",
                             "value": self.stats([{"prompt_tokens": 10, "cache_read_tokens": 5,
                                                   "cache_write_tokens": 1},
                                                  {"prompt_tokens": 10, "cache_read_tokens": 0,
                                                   "cache_write_tokens": 1}])})
        self.assertIn("miss=unexpected", line)

    def test_a_resumed_runs_first_response_is_a_resume_miss(self):
        log = self.log(resumed=True)
        line = log.describe({"kind": "ConversationStateUpdateEvent", "key": "stats",
                             "value": self.stats([{"prompt_tokens": 900, "cache_read_tokens": 0,
                                                   "cache_write_tokens": 0}])})
        self.assertIn("miss=resume", line)

    def test_only_the_agents_own_usage_is_read(self):
        log = self.log()
        self.assertIsNone(log.describe({"kind": "ConversationStateUpdateEvent", "key": "stats",
                                        "value": self.stats([{"prompt_tokens": 1}], "condenser")}))

    def test_an_unreadable_stats_value_never_fails_the_run(self):
        log = self.log()
        for value in ("garbage", {}, {"usage_to_metrics": "no"}, {"usage_to_metrics": {"agent": {}}},
                      {"usage_to_metrics": {"agent": {"token_usages": []}}}):
            with self.subTest(value=value):
                self.assertIsNone(log.describe({"kind": "ConversationStateUpdateEvent", "key": "stats",
                                                "value": value}))

    def test_a_stats_update_for_another_key_is_ignored(self):
        log = self.log()
        self.assertIsNone(log.describe({"kind": "ConversationStateUpdateEvent", "key": "other", "value": {}}))


GOLDEN_DIR = os.path.join(os.path.dirname(os.path.abspath(__file__)), "golden")
PUBLIC_ENV = {"MODEL": "agent-default", "LLM_BASE_URL": "http://127.0.0.1:4000/v1",
              "MCP_URL": "http://127.0.0.1:4000/mcp", "CONVERSATION_ID": "0b3c6f0e-5d1a-4b8e-9f41-2a7c3e9d8b10"}
INTERNAL_ENV = {**PUBLIC_ENV, "DATA_CLASS": "internal"}
LIGHT_ENV = {"LIGHT_BASE_URL": "http://127.0.0.1:4000/m/tier-light/v1", "LIGHT_MODEL": "tier-light"}


def build(env):
    return agent_run.build_request(env, "fix the link", "rules text")


def rendered(env):
    return json.dumps(build(env), indent=1, sort_keys=True) + "\n"


def golden(name):
    with open(os.path.join(GOLDEN_DIR, name)) as f:
        return f.read()


class InternalCondenserTest(unittest.TestCase):
    def test_a_public_request_is_todays(self):
        self.assertEqual(rendered(PUBLIC_ENV), golden("start-request.json"))

    def test_an_internal_run_condenses_later_and_on_the_light_alias(self):
        c = build(INTERNAL_ENV | LIGHT_ENV)["agent"]["condenser"]
        self.assertEqual((c["max_size"], c["keep_first"]), (240, 8))
        self.assertEqual(c["llm"]["model"], "openai/tier-light")
        self.assertEqual(c["llm"]["base_url"], LIGHT_ENV["LIGHT_BASE_URL"])
        self.assertEqual(c["llm"]["litellm_extra_body"], {"reasoning_effort": "low"})

    def test_an_internal_run_without_a_light_alias_keeps_the_summary_on_its_own_model(self):
        c = build(INTERNAL_ENV)["agent"]["condenser"]
        self.assertEqual((c["max_size"], c["keep_first"]), (240, 8))
        self.assertEqual(c["llm"]["model"], "openai/" + INTERNAL_ENV["MODEL"])

    def test_the_internal_request_is_its_golden(self):
        self.assertEqual(rendered(INTERNAL_ENV | LIGHT_ENV), golden("start-request-internal.json"))


class LightKeyRedactionTest(unittest.TestCase):
    def test_the_light_key_is_redacted(self):
        with tempfile.NamedTemporaryFile("w", delete=False) as f:
            f.write("sk-light\n")
        self.addCleanup(os.unlink, f.name)
        with mock.patch.dict(os.environ, {"LIGHT_API_KEY_FILE": f.name}):
            self.assertNotIn("sk-light", agent_run.redact("x sk-light"))
