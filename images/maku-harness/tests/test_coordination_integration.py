"""The coordination integration runtime (v4 MR10): exact-SHA worktrees, confirmed checks only,
one typed result, and nothing typed when the sandbox cannot vouch for one."""
import hashlib
import json
import os
import shutil
import stat
import subprocess
import sys
import tempfile
import time
import unittest
from unittest import mock

HERE = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..")
sys.path.insert(0, HERE)
import coordination_integration as ci  # noqa: E402

GIT_ENV = {"GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": os.devnull, "GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@t",
           "GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@t", "PATH": os.environ["PATH"]}


def sh(cwd, *args):
    return subprocess.run(["git", "-C", cwd, *args], env=GIT_ENV, check=True, capture_output=True, text=True).stdout.strip()


class Upstream:
    """Two public repositories as GitHub serves them: Smana/contract with a landed merge on main,
    Smana/consumer with an open head on its agent branch, never on main."""

    def __init__(self, base):
        self.base = base
        self.merge = self.repo("Smana/contract", "contract.txt", "v1")
        self.off_main = self.commit("Smana/contract", "side", "stray.txt", "x")
        self.head = self.commit("Smana/consumer", "agent/room", "consumer.txt", "uses v1", start=self.repo("Smana/consumer", "README", "c"))

    def work(self, name):
        return os.path.join(self.base, "src", name)

    def repo(self, name, path, text):
        w = self.work(name)
        os.makedirs(w)
        sh(w, "init", "-q", "-b", "main")
        return self._commit(w, path, text)

    def commit(self, name, branch, path, text, start=None):
        w = self.work(name)
        sh(w, "checkout", "-q", "-b", branch, start or "main")
        sha = self._commit(w, path, text)
        sh(w, "checkout", "-q", "main")
        return sha

    def _commit(self, w, path, text):
        with open(os.path.join(w, path), "w") as f:
            f.write(text)
        sh(w, "add", path)
        sh(w, "commit", "-q", "-m", path)
        return sh(w, "rev-parse", "HEAD")

    def serve(self):
        for name in ("Smana/contract", "Smana/consumer"):
            bare = os.path.join(self.base, "remote", name + ".git")
            subprocess.run(["git", "clone", "-q", "--bare", self.work(name), bare], env=GIT_ENV, check=True)
            # A pushed agent branch, as GitHub holds it: fetchable by its id.
            sh(bare, "fetch", "-q", self.work(name), "+refs/heads/*:refs/heads/*")
        return "file://" + os.path.join(self.base, "remote") + "/"


def manifest(up, checks, **over):
    m = {"parentTask": "p4zt6wce", "planVersion": 3, "planHash": "b" * 64, "purpose": "beforeDependentMerge",
         "subjects": [{"childID": "consumer", "repository": "Smana/consumer", "headSHA": up.head},
                      {"childID": "contract", "repository": "Smana/contract", "mergeCommitSHA": up.merge}],
         "acceptance": [{"check": c, "proves": "it works"} for c in checks]}
    m.update(over)
    return json.dumps(m, sort_keys=True, separators=(",", ":"))


def env_for(raw, **over):
    e = {"ROLE": "triager", ci.MANIFEST_ENV: raw, ci.HASH_ENV: hashlib.sha256(raw.encode()).hexdigest(),
         "PATH": os.environ["PATH"], "HOME": "/tmp", "MCP_TOKEN_FILE": "/run/secret", "GIT_TOKEN_FILE": "/run/git"}
    e.update(over)
    return e


class IntegrationTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.mkdtemp()
        self.addCleanup(self._cleanup)
        self.up = Upstream(self.tmp)
        remote = self.up.serve()
        for k, v in (("REMOTE_BASE", remote), ("PROTOCOL", "file")):
            p = mock.patch.object(ci, k, v)
            p.start()
            self.addCleanup(p.stop)
        self.posted = []

    def _cleanup(self):
        for d, _, _ in os.walk(self.tmp):
            os.chmod(d, 0o755)
        shutil.rmtree(self.tmp)

    def run_it(self, env, post=None, timeout=None):
        with mock.patch("sys.stdout"):
            return ci.run(env, post or (lambda *a: self.posted.append(a)), manifest_dir=os.path.join(self.tmp, "manifest"),
                          root=os.path.join(self.tmp, "work"), timeout=timeout)

    def test_requested_only_with_a_manifest(self):
        self.assertFalse(ci.requested({"ROLE": "triager"}))
        self.assertTrue(ci.requested({ci.MANIFEST_ENV: "{}"}))
        self.assertTrue(ci.requested({ci.HASH_ENV: "a" * 64}))

    def test_a_pass_runs_every_check_at_the_exact_shas_and_posts_once(self):
        checks = ['test "$(git -C consumer rev-parse HEAD)" = %s' % self.up.head,
                  'test "$(git -C contract rev-parse HEAD)" = %s' % self.up.merge,
                  "! git -C consumer symbolic-ref -q HEAD && ! git -C contract symbolic-ref -q HEAD",  # detached
                  'test -r "$MAKU_COORDINATION_MANIFEST" && ! test -w "$MAKU_COORDINATION_MANIFEST"',
                  'test "$PWD" = "$MAKU_COORDINATION_ROOT"']
        raw = manifest(self.up, checks)
        env = env_for(raw)
        self.assertEqual(self.run_it(env), 0)
        self.assertEqual(self.posted, [("passed", "acceptance_passed", env[ci.HASH_ENV])])
        path = os.path.join(self.tmp, "manifest", "manifest.json")
        with open(path, "rb") as f:
            self.assertEqual(f.read(), raw.encode(), "the exact verified bytes")
        self.assertEqual(stat.S_IMODE(os.stat(path).st_mode), 0o444)
        self.assertEqual(stat.S_IMODE(os.stat(os.path.dirname(path)).st_mode), 0o555)
        # No implementation branch: every worktree is detached, and no branch was created anywhere.
        for cid in ("consumer", "contract"):
            self.assertEqual(sh(os.path.join(self.tmp, "work", ".repos", cid), "for-each-ref", "refs/heads"), "")

    def test_a_check_sees_none_of_the_runs_credentials(self):
        raw = manifest(self.up, ['test -z "$MCP_TOKEN_FILE$GIT_TOKEN_FILE$ROLE$COORDINATION_MANIFEST"'])
        self.assertEqual(self.run_it(env_for(raw)), 0)
        self.assertEqual(self.posted[0][0], "passed")

    def test_the_first_failing_check_fails_the_run_and_the_rest_never_run(self):
        marker = os.path.join(self.tmp, "ran")
        raw = manifest(self.up, ["true", "exit 3", "touch " + marker])
        env = env_for(raw)
        self.assertEqual(self.run_it(env), 0, "a failed acceptance is a result, not an infrastructure failure")
        self.assertEqual(self.posted, [("failed", "acceptance_failed", env[ci.HASH_ENV])])
        self.assertFalse(os.path.exists(marker))

    def test_a_check_past_its_time_fails(self):
        raw = manifest(self.up, ["sleep 30"])
        self.assertEqual(self.run_it(env_for(raw), timeout=0.5), 0)
        self.assertEqual(self.posted[0][:2], ("failed", "acceptance_timeout"))

    def test_nothing_a_check_started_outlives_it(self):
        pidfile = os.path.join(self.tmp, "pid")
        raw = manifest(self.up, ["(sleep 300 & echo $! > %s)" % pidfile])
        self.assertEqual(self.run_it(env_for(raw)), 0)
        with open(pidfile) as f:
            pid = int(f.read())
        def alive():
            try:
                with open("/proc/%d/stat" % pid) as f:
                    return f.read().split()[2] != "Z"  # a zombie awaits its reaper, it runs nothing
            except FileNotFoundError:
                return False
        deadline = time.monotonic() + 5  # SIGKILL is delivered asynchronously
        while alive() and time.monotonic() < deadline:
            time.sleep(0.05)
        self.assertFalse(alive(), "the check's background process survived it")

    def assert_infrastructure(self, env):
        self.assertEqual(self.run_it(env), 1)
        self.assertEqual(self.posted, [], "an infrastructure failure posts nothing typed")

    def test_a_manifest_not_under_its_hash_is_refused(self):
        raw = manifest(self.up, ["true"])
        self.assert_infrastructure(env_for(raw, **{ci.HASH_ENV: "0" * 64}))
        self.assert_infrastructure(env_for(raw, **{ci.MANIFEST_ENV: raw.replace("p4zt6wce", "p4zt6wcf")}))
        self.assert_infrastructure(env_for(raw, **{ci.HASH_ENV: ""}))

    def test_only_a_triager_runs_a_manifest(self):
        self.assert_infrastructure(env_for(manifest(self.up, ["true"]), ROLE="implementer"))

    def test_a_malformed_manifest_is_refused_before_any_git_call(self):
        up = self.up
        both = [{"childID": "contract", "repository": "Smana/contract", "headSHA": up.merge, "mergeCommitSHA": up.merge}]
        neither = [{"childID": "contract", "repository": "Smana/contract"}]
        short = [{"childID": "contract", "repository": "Smana/contract", "mergeCommitSHA": up.merge[:12]}]
        path = [{"childID": "../x", "repository": "Smana/contract", "mergeCommitSHA": up.merge}]
        repo = [{"childID": "contract", "repository": "Smana/..", "mergeCommitSHA": up.merge}]
        twice = [{"childID": "contract", "repository": "Smana/contract", "mergeCommitSHA": up.merge}] * 2
        for over in ({"subjects": both}, {"subjects": neither}, {"subjects": short}, {"subjects": path},
                     {"subjects": repo}, {"subjects": twice}, {"subjects": []}, {"acceptance": []},
                     {"purpose": "whenever"}, {"planHash": "x"}):
            with self.subTest(over=over), mock.patch.object(ci, "git", side_effect=AssertionError("git ran")):
                self.assert_infrastructure(env_for(manifest(up, ["true"], **over)))

    def test_a_head_that_is_not_on_origin_is_infrastructure(self):
        raw = manifest(self.up, ["true"], subjects=[{"childID": "consumer", "repository": "Smana/consumer", "headSHA": "1" * 40}])
        self.assert_infrastructure(env_for(raw))

    def test_a_merge_off_the_default_branch_is_refused(self):
        # coordination checkout's verify: a landed child's merge must be on origin's default branch.
        raw = manifest(self.up, ["true"], purpose="final",
                       subjects=[{"childID": "contract", "repository": "Smana/contract", "mergeCommitSHA": self.up.off_main}])
        self.assert_infrastructure(env_for(raw))

    def test_an_unreachable_repository_is_infrastructure(self):
        raw = manifest(self.up, ["true"], subjects=[{"childID": "gone", "repository": "Smana/gone", "mergeCommitSHA": self.up.merge}])
        self.assert_infrastructure(env_for(raw))

    def test_an_unposted_result_exits_non_zero(self):
        def refuse(*_):
            raise RuntimeError("room_integration is not exposed to this run")
        self.assertEqual(self.run_it(env_for(manifest(self.up, ["true"])), post=refuse), 1)


class GitTest(unittest.TestCase):
    """RunGit's hardening, plus anonymity: no credential helper, no system or global config, one protocol."""

    def test_argv_runs_no_hook_and_no_credential(self):
        argv = ci.git_argv("/w", "fetch", "origin")
        pairs = [argv[i + 1] for i, a in enumerate(argv) if a == "-c"]
        for want in ("core.hooksPath=" + os.devnull, "credential.helper=", "protocol.allow=never", "protocol.https.allow=always",
                     "submodule.recurse=false", "filter.lfs.smudge="):
            self.assertIn(want, pairs)
        self.assertEqual(argv[-4:], ["-C", "/w", "fetch", "origin"])

    def test_env_reads_no_system_or_global_config(self):
        env = ci.git_env()
        self.assertEqual((env["GIT_CONFIG_NOSYSTEM"], env["GIT_CONFIG_GLOBAL"], env["GIT_TERMINAL_PROMPT"]), ("1", os.devnull, "0"))
        self.assertNotIn("GIT_TOKEN_FILE", env)


if __name__ == "__main__":
    unittest.main()
