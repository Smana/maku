"""report_head: a finished implementer run whose agent made no handoff reports the head it pushed (gmmrmund)."""
import os
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

HERE = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..")
sys.path.insert(0, HERE)
import agent_run  # noqa: E402

GIT_ENV = {**os.environ, "GIT_CONFIG_GLOBAL": os.devnull, "GIT_CONFIG_NOSYSTEM": "1", "GIT_AUTHOR_NAME": "t",
           "GIT_AUTHOR_EMAIL": "t@x", "GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@x"}
SHA = "a" * 40


def git(repo, *args):
    return subprocess.run(["git", "-C", repo, *args], check=True, capture_output=True, text=True, env=GIT_ENV).stdout.strip()


class PushedHeadTest(unittest.TestCase):
    def setUp(self):
        d = tempfile.mkdtemp()
        self.remote, self.repo = os.path.join(d, "remote.git"), os.path.join(d, "repo")
        subprocess.run(["git", "init", "-q", "--bare", self.remote], check=True, env=GIT_ENV)
        subprocess.run(["git", "init", "-q", "-b", "main", self.repo], check=True, env=GIT_ENV)
        git(self.repo, "commit", "-q", "--allow-empty", "-m", "base")
        self.start = git(self.repo, "rev-parse", "HEAD")
        git(self.repo, "checkout", "-q", "-b", "agent/t1")
        self.env = {"REPOSITORY": self.remote, "BRANCH": "agent/t1"}
        for p in (mock.patch.object(agent_run, "REPO_DIR", self.repo), mock.patch.object(agent_run, "REMOTE_URL", "%s"),
                  mock.patch.dict(os.environ, {"GIT_CONFIG_GLOBAL": os.devnull, "GIT_CONFIG_NOSYSTEM": "1"})):
            p.start()
            self.addCleanup(p.stop)

    def commit(self):
        git(self.repo, "commit", "-q", "--allow-empty", "-m", "work")
        return git(self.repo, "rev-parse", "HEAD")

    def test_the_head_github_holds_is_the_pushed_head(self):
        sha = self.commit()
        git(self.repo, "push", "-q", self.remote, "HEAD:refs/heads/agent/t1")
        self.assertEqual(agent_run.pushed_head(self.env, self.start), sha)

    def test_an_unpushed_head_is_not_reported(self):
        self.commit()
        git(self.repo, "push", "-q", self.remote, "HEAD:refs/heads/agent/t1")
        self.commit()  # GitHub's branch is one behind the clone
        self.assertIsNone(agent_run.pushed_head(self.env, self.start))

    def test_a_branch_github_lacks_is_not_reported(self):
        self.commit()
        self.assertIsNone(agent_run.pushed_head(self.env, self.start))

    def test_a_head_the_run_did_not_move_is_not_its_to_report(self):
        git(self.repo, "push", "-q", self.remote, "HEAD:refs/heads/agent/t1")  # an earlier run's, or a human's
        self.assertIsNone(agent_run.pushed_head(self.env, self.start))


class FakeSteps:
    def __init__(self, handed_off=False):
        self.handed_off = handed_off


class FakeNotes:
    def __init__(self, errors=()):
        self.calls, self.errors = [], list(errors)

    def harness_call(self, tool, arguments):
        self.calls.append((tool, arguments))
        if self.errors:
            raise RuntimeError(self.errors.pop(0))


class ReportHeadTest(unittest.TestCase):
    env = {"ROLE": "implementer", "BRANCH": "agent/t1"}

    def report(self, notes, steps=None, env=None, remote=lambda env, start: SHA):
        return agent_run.report_head(env or self.env, steps or FakeSteps(), notes, "s", remote=remote, sleep=lambda s: None)

    def test_a_finish_without_a_handoff_hands_off_the_pushed_head(self):
        notes = FakeNotes()
        self.assertEqual(self.report(notes), SHA)
        self.assertEqual(len(notes.calls), 1)
        tool, args = notes.calls[0]
        self.assertEqual(tool, "room_handoff")
        self.assertEqual((args["toRole"], args["commit"]), ("reviewer", SHA))
        self.assertIn("finished without a room_handoff", args["summary"])

    def test_the_agents_own_handoff_stands(self):
        notes = FakeNotes()
        self.assertIsNone(self.report(notes, steps=FakeSteps(handed_off=True)))
        self.assertEqual(notes.calls, [])

    def test_only_an_implementer_reports(self):
        for role in ("reviewer", "tester", "triager"):
            notes = FakeNotes()
            self.assertIsNone(self.report(notes, env={"ROLE": role}))
            self.assertEqual(notes.calls, [])

    def test_no_pushed_head_reports_nothing(self):
        notes = FakeNotes()
        self.assertIsNone(self.report(notes, remote=lambda env, start: None))
        self.assertEqual(notes.calls, [])

    def test_the_brokers_second_is_waited_out_once(self):
        notes = FakeNotes(errors=["rate_limited: one room tool call per second"])
        self.assertEqual(self.report(notes), SHA)
        self.assertEqual(len(notes.calls), 2)

    def test_a_refused_report_never_fails_the_run(self):
        notes = FakeNotes(errors=["policy: denied", "policy: denied"])
        self.assertIsNone(self.report(notes))
        self.assertEqual(len(notes.calls), 1)


if __name__ == "__main__":
    unittest.main()
