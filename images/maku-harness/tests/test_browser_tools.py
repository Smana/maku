"""The opt-in browser sidecar's harness side (v5 Q16): the MCP server, image input and the rules."""
import os
import sys
import unittest

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, HERE)
import browser_tools as bt  # noqa: E402

ON = {"BROWSER_MCP_URL": "http://127.0.0.1:9223/mcp", "SCREENSHOTS_VISION": "1"}


class BrowserTools(unittest.TestCase):
    def test_off_without_url(self):
        self.assertIsNone(bt.browser_url({}))
        self.assertFalse(bt.vision_on({"SCREENSHOTS_VISION": "1"}))
        self.assertEqual(bt.browser_rules({}), "")
        self.assertIsNone(bt.mcp_server({}))

    def test_only_loopback_http_urls_are_used(self):
        for bad in ("http://10.0.0.1:9223/mcp", "https://127.0.0.1:9223/mcp", "http://127.0.0.1.nip.io:9223/mcp",
                    "http://user@127.0.0.1:9223/mcp", "file:///tmp/mcp"):
            self.assertIsNone(bt.browser_url({"BROWSER_MCP_URL": bad}), bad)
        self.assertEqual(bt.browser_url(ON), "http://127.0.0.1:9223/mcp")

    def test_vision_needs_the_browser_and_the_flag(self):
        self.assertTrue(bt.vision_on(ON))
        self.assertFalse(bt.vision_on({**ON, "SCREENSHOTS_VISION": "0"}))

    def test_rules_name_loopback_and_snapshot_first(self):
        r = bt.browser_rules(ON)
        for s in ("127.0.0.1", "browser_snapshot", "browser_take_screenshot", "--bind 127.0.0.1", "room"):
            self.assertIn(s, r)

    def test_rules_without_vision_say_the_model_cannot_see(self):
        r = bt.browser_rules({**ON, "SCREENSHOTS_VISION": "0"})
        self.assertIn("cannot see", r)
        self.assertIn("browser_take_screenshot", r, "screenshots still reach the room (C15)")

    def test_mcp_server(self):
        self.assertEqual(bt.mcp_server(ON), {"url": "http://127.0.0.1:9223/mcp", "transport": "http"})


if __name__ == "__main__":
    unittest.main()
