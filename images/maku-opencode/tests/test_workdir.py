"""The OpenCode server starts inside the run's clone even though agent_run clones after start()."""
import os
import sys
import tempfile
import unittest
from unittest import mock

HERE = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..")
sys.path.insert(0, HERE)
import opencode_driver  # noqa: E402


class WorkdirTest(unittest.TestCase):
    def _start(self, env):
        d = opencode_driver.OpenCodeDriver(env)
        with mock.patch.object(opencode_driver.subprocess, "Popen") as popen, \
                mock.patch.object(d, "_write_initial_config"):
            d.start()
        return popen.call_args.kwargs["cwd"]

    def test_starts_in_the_clone_directory_before_it_exists(self):
        tmp = tempfile.mkdtemp(prefix="oc-wd-")
        repo = os.path.join(tmp, "repo")
        env = {"OPENCODE_WORKSPACE_ROOT": os.path.join(tmp, ".opencode"),
               "OPENCODE_PASSWORD_FILE": os.path.join(tmp, ".password")}
        with mock.patch.object(opencode_driver, "REPO_DIR", repo):
            cwd = self._start(env)
        self.assertEqual(cwd, repo, "OpenCode must start in the clone, not its config root")
        self.assertTrue(os.path.isdir(repo))
        self.assertEqual(os.listdir(repo), [], "the directory stays empty so git can clone into it")

    def test_an_explicit_workdir_wins(self):
        tmp = tempfile.mkdtemp(prefix="oc-wd-")
        env = {"OPENCODE_WORKSPACE_ROOT": os.path.join(tmp, ".opencode"),
               "OPENCODE_PASSWORD_FILE": os.path.join(tmp, ".password"),
               "OPENCODE_WORKDIR": os.path.join(tmp, "elsewhere")}
        self.assertEqual(self._start(env), os.path.join(tmp, "elsewhere"))


if __name__ == "__main__":
    unittest.main()
