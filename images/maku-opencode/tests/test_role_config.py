"""Role configuration and OpenCode 2.3.0 debug config validation tests (OpenCode plan Task 6)."""
import json
import os
import subprocess
import sys
import tempfile
import unittest

HERE = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..")
sys.path.insert(0, HERE)
import config as oc_config  # noqa: E402
import role_config  # noqa: E402

PUBLIC = {
    "ROLE": "implementer",
    "MODEL": "agent-default",
    "LLM_BASE_URL": "http://127.0.0.1:4000/v1",
    "DATA_CLASS": "public",
}

INTERNAL = {
    **PUBLIC,
    "DATA_CLASS": "internal",
    "LIGHT_BASE_URL": "http://127.0.0.1:4000/v1",
    "LIGHT_MODEL": "tier-light",
}


class RoleConfigTest(unittest.TestCase):
    def test_reviewer_cannot_edit_or_launch_subagents(self):
        c = role_config.role_config({"ROLE": "reviewer", "MODEL": "agent-default"})
        perms = c["agents"]["reviewer"]["permissions"]
        self.assertIn({"action": "edit", "resource": "*", "effect": "deny"}, perms)
        self.assertIn({"action": "subagent", "resource": "*", "effect": "deny"}, perms)
        self.assertIn({"action": "webfetch", "resource": "*", "effect": "deny"}, perms)
        self.assertIn({"action": "external_directory", "resource": "*", "effect": "deny"}, perms)

    def test_tester_edits_are_limited_to_declared_test_paths(self):
        c = role_config.role_config({"ROLE": "tester", "MODEL": "agent-default"}, test_paths=["tests/**"])
        perms = c["agents"]["tester"]["permissions"]
        deny_idx = perms.index({"action": "edit", "resource": "*", "effect": "deny"})
        allow_idx = perms.index({"action": "edit", "resource": "tests/**", "effect": "allow"})
        self.assertLess(deny_idx, allow_idx)

    def test_effort_is_a_model_variant_not_a_secret(self):
        c = role_config.role_config({"MODEL": "agent-default", "LLM_REASONING_EFFORT": "medium"})
        self.assertEqual(c["model"], "maku/agent-default#medium")
        serialized = json.dumps(c)
        self.assertNotIn("injected-by-identity-proxy", serialized)
        variants = c["providers"]["maku"]["models"]["agent-default"]["variants"]
        self.assertEqual([v["id"] for v in variants], ["low", "medium", "high"])
        self.assertEqual(variants[1]["body"], {"reasoning_effort": "medium"})

    def test_internal_cache_marker_and_public_omission(self):
        self.assertEqual(
            role_config.role_config(INTERNAL)["providers"]["maku"]["compatibility"]["cacheFormat"],
            "anthropic",
        )
        self.assertNotIn(
            "cacheFormat",
            role_config.role_config(PUBLIC)["providers"]["maku"].get("compatibility", {}),
        )

    def test_mcp_proxy_config_disables_oauth_and_codemode(self):
        c = role_config.role_config(PUBLIC, mcp_proxy_url="http://127.0.0.1:9999/mcp")
        srv = c["mcp"]["servers"]["platform"]
        self.assertEqual(srv["type"], "remote")
        self.assertFalse(srv["oauth"])
        self.assertFalse(srv["codemode"])

    def test_real_opencode_accepts_generated_v2_config(self):
        tmp = tempfile.mkdtemp(prefix="oc-role-cfg-")
        paths = oc_config.workspace_paths(tmp)
        cfg = role_config.role_config(INTERNAL, rules="stay focused", mcp_proxy_url="http://127.0.0.1:9999/mcp")
        oc_config.write_config(paths["config"], cfg)
        env = {
            "PATH": os.environ.get("PATH", "/usr/local/bin:/usr/bin:/bin"),
            "HOME": paths["home"],
            "XDG_CONFIG_HOME": paths["config"],
            "XDG_DATA_HOME": paths["data"],
            "XDG_STATE_HOME": paths["state"],
            "XDG_CACHE_HOME": paths["cache"],
            "OPENCODE_CONFIG_DIR": paths["config"],
            "OPENCODE_DISABLE_CLAUDE_CODE": "1",
            "MAKU_LLM_API_KEY": "placeholder",
        }
        proc = subprocess.run(["/usr/local/bin/opencode", "debug", "config"], env=env, capture_output=True, text=True, timeout=20, check=True)
        docs = json.loads(proc.stdout)
        doc = next(d["info"] for d in docs if d.get("type") == "document")
        self.assertEqual(doc["update"], "disable")
        self.assertEqual(doc["providers"]["maku"]["compatibility"]["cacheFormat"], "anthropic")
        self.assertEqual(doc["mcp"]["servers"]["platform"]["codemode"], False)


if __name__ == "__main__":
    unittest.main()
