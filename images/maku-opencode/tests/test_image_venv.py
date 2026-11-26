"""agent_run's own interpreter, the one its shebang names, is the venv with the locked packages.

A symlink to the venv's python outside the venv ran with the system prefix: agent_checks could not
import yaml and tracing could not import opentelemetry, on every OpenCode run (gcp-0, 2026-11-26).
"""
import os
import subprocess
import unittest

AGENT_RUN = "/opt/agent/agent_run.py"


@unittest.skipUnless(os.path.exists(AGENT_RUN), "in the image only")
class ImageVenvTest(unittest.TestCase):
    def test_the_shebang_interpreter_imports_the_locked_packages(self):
        with open(AGENT_RUN, encoding="utf-8") as f:
            interpreter = f.readline()[2:].strip()
        out = subprocess.run(
            [interpreter, "-c", "import sys, yaml, opentelemetry.sdk.trace; print(sys.prefix)"],
            capture_output=True, text=True, timeout=30,
        )
        self.assertEqual(out.returncode, 0, out.stderr)
        self.assertNotEqual(out.stdout.strip(), "/usr", "the interpreter must run inside the venv")


if __name__ == "__main__":
    unittest.main()
