"""The gh wrapper under hardening H2: a run with SAFE_OUTPUTS requests its writes (HR14)."""
import os
import shutil
import stat
import subprocess
import tempfile
import unittest

HERE = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..")


class SafeOutputsGhTest(unittest.TestCase):
    """Which gh writes a run with safe outputs is turned away from, and to which tool."""

    def run_gh(self, argv, safe_outputs=None):
        d = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, d)
        stubs = {}
        for name, out in (("cred", "echo tok"), ("footer", 'echo footer "$@"'), ("real", 'echo real "$@"')):
            stubs[name] = os.path.join(d, name)
            with open(stubs[name], "w") as f:
                f.write("#!/bin/sh\n%s\n" % out)
            os.chmod(stubs[name], stat.S_IRWXU)
        wrapper = os.path.join(HERE, "gh") if os.path.exists(os.path.join(HERE, "gh")) else "/usr/local/bin/gh"
        with open(wrapper) as f:
            script = (f.read().replace("/usr/local/bin/git-credential-agent", stubs["cred"])
                      .replace("/opt/agent/pr_footer.py", stubs["footer"]).replace("/usr/local/lib/gh-real", stubs["real"]))
        path = os.path.join(d, "gh")
        with open(path, "w") as f:
            f.write(script)
        env = {k: v for k, v in os.environ.items() if k != "SAFE_OUTPUTS"}
        if safe_outputs is not None:
            env["SAFE_OUTPUTS"] = safe_outputs
        return subprocess.run(["sh", path] + list(argv), capture_output=True, text=True, env=env)

    ALL = "comment,issue,labels,pr"

    def test_enforced_writes_are_refused_with_their_tool(self):
        for argv, tool in (
            (["pr", "create", "--fill"], "output_pr"), (["pr", "new"], "output_pr"),
            (["pr", "-R", "o/r", "create"], "output_pr"), (["pr", "--repo=o/r", "edit", "1"], "output_pr"),
            (["pr", "edit", "1", "--title", "t"], "output_pr"), (["pr", "ready", "1"], "output_pr"),
            (["pr", "comment", "1", "-b", "x"], "output_comment"), (["issue", "comment", "7", "-b", "x"], "output_comment"),
            (["issue", "create", "-t", "x"], "output_issue"), (["issue", "edit", "7", "-t", "x"], "output_issue"),
            (["pr", "edit", "1", "--add-label", "documentation"], "output_labels"),
            (["pr", "edit", "1", "--remove-label=bug"], "output_labels"),
            (["issue", "edit", "7", "--add-label", "x"], "output_labels"),
            (["label", "create", "x"], "output_labels"), (["label", "delete", "x"], "output_labels"),
            # #491's review: the writes the tracer does not observe are turned away too.
            (["pr", "review", "1", "--approve"], "output_comment"), (["pr", "close", "1"], "output_pr"),
            (["pr", "reopen", "1"], "output_pr"), (["issue", "close", "7"], "output_issue"),
            (["issue", "reopen", "7"], "output_issue"),
        ):
            p = self.run_gh(argv, self.ALL)
            self.assertEqual(p.returncode, 3, argv)
            self.assertIn("use " + tool, p.stderr, argv)
            self.assertEqual(p.stdout, "", argv)

    def test_a_type_not_enforced_passes_through(self):
        p = self.run_gh(["pr", "comment", "1", "-b", "x"], "pr")
        self.assertEqual((p.returncode, p.stdout.split()[0]), (0, "real"))
        p = self.run_gh(["pr", "create"], "comment")
        self.assertEqual((p.returncode, p.stdout.split()[0]), (0, "footer"))

    def test_reads_and_the_api_pass_through(self):
        for argv in (["pr", "view", "1"], ["pr", "checks", "1"], ["issue", "view", "7"], ["label", "list"],
                     ["api", "-X", "POST", "repos/o/r/pulls"]):
            p = self.run_gh(argv, self.ALL)
            self.assertEqual((p.returncode, p.stdout.split()[0]), (0, "real"), argv)

    def test_without_the_variable_nothing_changes(self):
        for safe in (None, ""):
            self.assertEqual(self.run_gh(["pr", "create"], safe).stdout.split()[0], "footer")
            for argv in (["pr", "edit", "1"], ["issue", "create"], ["label", "create", "x"]):
                p = self.run_gh(argv, safe)
                self.assertEqual((p.returncode, p.stdout.split()[0]), (0, "real"), argv)


if __name__ == "__main__":
    unittest.main()
