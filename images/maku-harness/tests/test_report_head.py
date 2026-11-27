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

    # main reads no head before the conversation (a loop-off run is today's run): without a loop
    # state, the start is the clone's first head, from its reflog, which a resumed pod keeps.
    def test_without_a_start_the_clones_first_head_is_the_start(self):
        sha = self.commit()
        git(self.repo, "push", "-q", self.remote, "HEAD:refs/heads/agent/t1")
        self.assertEqual(agent_run.first_head(), self.start)
        self.assertEqual(agent_run.pushed_head(self.env, None), sha)

    def test_without_a_start_an_unmoved_head_is_not_reported(self):
        git(self.repo, "push", "-q", self.remote, "HEAD:refs/heads/agent/t1")
        self.assertIsNone(agent_run.pushed_head(self.env, None))


class FakeSteps:
    def __init__(self, handed_off=False, handoff=None):
        self.handed_off, self.handoff = handed_off, handoff


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
        own = {"toRole": "reviewer", "summary": "Fixed the link.", "commit": SHA}
        self.assertIsNone(self.report(notes, steps=FakeSteps(handed_off=True, handoff=own)))
        self.assertEqual(notes.calls, [])

    def test_a_handoff_whose_arguments_were_not_read_stands(self):
        notes = FakeNotes()
        self.assertIsNone(self.report(notes, steps=FakeSteps(handed_off=True)))
        self.assertEqual(notes.calls, [])

    # mhln4xkd: the agent typed the pushed head's first seven digits and made up the rest.
    def test_a_made_up_commit_is_handed_off_again_with_the_pushed_head(self):
        pushed = "56b8ffb7f93dda8664332df7c9678296b9daeec7"
        for named in ("56b8ffb3bf4a1c44d5f74cd9a51b456390b8c7d4", "56b8ffb"):
            notes = FakeNotes()
            own = {"toRole": "tester", "summary": "Fixed the hero button in dark mode.", "commit": named}
            got = self.report(notes, steps=FakeSteps(handed_off=True, handoff=own), remote=lambda env, start: pushed)
            self.assertEqual(got, pushed, named)
            tool, args = notes.calls[0]
            self.assertEqual((tool, args["toRole"], args["commit"]), ("room_handoff", "tester", pushed))
            self.assertTrue(args["summary"].startswith("Fixed the hero button in dark mode.\n\n"), args["summary"])
            self.assertIn("named commit %s, which is not the head this run pushed" % named, args["summary"])

    def test_a_corrected_summary_stays_within_the_tools_bound(self):
        notes = FakeNotes()
        own = {"toRole": "reviewer", "summary": "é" * 8000, "commit": "b" * 40}
        self.report(notes, steps=FakeSteps(handed_off=True, handoff=own))
        self.assertLessEqual(len(notes.calls[0][1]["summary"].encode()), agent_run.HANDOFF_SUMMARY_MAX)

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


class HandoffArgumentsTest(unittest.TestCase):
    """Both harnesses' ActionEvents carry the handoff's arguments, in two shapes."""

    def test_opencode_tool_input(self):
        args = {"toRole": "reviewer", "summary": "s", "commit": SHA}
        self.assertEqual(agent_run.handoff_arguments(args), args)

    def test_openhands_mcp_tool_action(self):
        args = {"toRole": "reviewer", "summary": "s", "commit": SHA}
        self.assertEqual(agent_run.handoff_arguments({"kind": "MCPToolAction", "data": args}), args)

    def test_not_a_dict(self):
        self.assertEqual(agent_run.handoff_arguments(None), {})

    def test_the_step_log_keeps_the_handoff_the_broker_took(self):
        log = agent_run.StepLog("cid")
        good = {"toRole": "reviewer", "summary": "s", "commit": "c" * 40}
        for i, (args, error) in enumerate([({"commit": "bad"}, True), (good, False)]):
            log.describe({"kind": "ActionEvent", "tool_name": "platform_room_handoff", "tool_call_id": "t%d" % i,
                          "action": {"kind": "MCPToolAction", "data": args}})
            log.describe({"kind": "ObservationEvent", "tool_call_id": "t%d" % i, "observation": {"is_error": error}})
        self.assertTrue(log.handed_off)
        self.assertEqual(log.handoff, good)


if __name__ == "__main__":
    unittest.main()
