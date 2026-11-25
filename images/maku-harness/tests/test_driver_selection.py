"""Harness driver selection and OpenHands request compatibility tests (OpenCode plan Task 2)."""
import os
import sys
import unittest

HERE = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..")
sys.path.insert(0, HERE)
import agent_run  # noqa: E402
import harness_driver  # noqa: E402
import openhands_driver  # noqa: E402

PUBLIC_ENV = {
    "MODEL": "agent-default",
    "LLM_BASE_URL": "http://127.0.0.1:4000/v1",
    "MCP_URL": "http://127.0.0.1:4000/mcp",
    "CONVERSATION_ID": "0b3c6f0e-5d1a-4b8e-9f41-2a7c3e9d8b10",
    "DATA_CLASS": "public",
}

INTERNAL_ENV = {
    **PUBLIC_ENV,
    "DATA_CLASS": "internal",
    "LIGHT_BASE_URL": "http://127.0.0.1:4000/v1",
    "LIGHT_MODEL": "tier-light",
}


class DriverSelectionTest(unittest.TestCase):
    def test_default_is_openhands(self):
        driver = agent_run.driver_for({})
        self.assertIsInstance(driver, harness_driver.HarnessDriver)
        self.assertIsInstance(driver, openhands_driver.OpenHandsDriver)

    def test_explicit_openhands_is_selected(self):
        self.assertIsInstance(agent_run.driver_for({"AGENT_HARNESS": "openhands"}), openhands_driver.OpenHandsDriver)

    def test_unknown_is_refused(self):
        with self.assertRaisesRegex(ValueError, 'AGENT_HARNESS "other"'):
            agent_run.driver_for({"AGENT_HARNESS": "other"})

    def test_public_and_internal_build_request_goldens(self):
        pub = agent_run.build_request(PUBLIC_ENV, "fix the link", "rules text")
        self.assertEqual(pub["conversation_id"], PUBLIC_ENV["CONVERSATION_ID"])
        self.assertEqual(pub["workspace"]["working_dir"], "/workspace/repo")
        self.assertEqual(pub["agent"]["llm"]["model"], "openai/agent-default")
        self.assertEqual(pub["agent"]["llm"]["litellm_extra_body"], {"reasoning_effort": "high"})
        self.assertEqual(pub["agent"]["llm"].get("capability_overrides") or {}, {})
        self.assertEqual(pub["tags"], {"effort": "high"})
        self.assertFalse(pub["autotitle"])

        internal = agent_run.build_request(INTERNAL_ENV, "triage incident", "internal rules")
        self.assertEqual(internal["agent"]["llm"]["capability_overrides"], {"supports_prompt_cache": True})
        self.assertEqual(internal["agent"]["condenser"]["max_size"], openhands_driver.CONDENSER_MAX_SIZE)
        self.assertEqual(internal["agent"]["condenser"]["keep_first"], openhands_driver.CONDENSER_KEEP_FIRST)
        self.assertEqual(internal["agent"]["condenser"]["llm"]["model"], "openai/tier-light")
        self.assertEqual(internal["agent"]["condenser"]["llm"]["litellm_extra_body"], {"reasoning_effort": "low"})


if __name__ == "__main__":
    unittest.main()
