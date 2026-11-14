"""git-credential-agent over the run-controller's token file and a stub GitHub. Stdlib only."""
import fcntl
import http.server
import io
import os
import sys
import tempfile
import threading
import time
import unittest
from unittest import mock

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))
import git_credential_agent as helper  # noqa: E402


class Stub(http.server.BaseHTTPRequestHandler):
    calls = []
    revoke_status = 204

    def do_GET(self):  # nothing exchanges any more: a GET is a failure
        Stub.calls.append(("GET", self.path, self.headers.get("Authorization")))
        self.send_response(500)
        self.end_headers()

    def do_DELETE(self):  # GitHub revoke
        Stub.calls.append(("DELETE", self.path, self.headers.get("Authorization")))
        self.send_response(Stub.revoke_status)
        self.end_headers()

    def log_message(self, *args):
        pass


class HelperTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.server = http.server.HTTPServer(("127.0.0.1", 0), Stub)
        threading.Thread(target=cls.server.serve_forever, daemon=True).start()
        cls.base = "http://127.0.0.1:%d" % cls.server.server_port

    @classmethod
    def tearDownClass(cls):
        cls.server.shutdown()

    def setUp(self):
        Stub.calls.clear()
        self.tmp = tempfile.mkdtemp()
        helper.LOCK = os.path.join(self.tmp, "token.lock")
        helper.GITHUB_API = self.base
        self.path = os.path.join(self.tmp, "github-token")
        with open(self.path, "w") as f:
            f.write("ghs_minted\n")
        env = {k: v for k, v in os.environ.items() if k not in ("STS_URL", "GIT_TOKEN_FILE", "GIT_TOKEN_REVOCABLE", "FORGE_READ")}
        self.env = mock.patch.dict(os.environ, {**env, "GIT_TOKEN_FILE": self.path, "GIT_TOKEN_REVOCABLE": "1"}, clear=True)
        self.env.start()

    def tearDown(self):
        self.env.stop()

    def get(self, host):
        out = io.StringIO()
        with mock.patch("sys.stdin", io.StringIO("protocol=https\nhost=%s\n" % host)), mock.patch("sys.stdout", out):
            helper.main(["git-credential-agent", "get"])
        return out.getvalue()

    def test_get_prints_the_mounted_token(self):
        self.assertEqual(self.get("github.com"), "username=x-access-token\npassword=ghs_minted\n")
        self.assertEqual(Stub.calls, [], "the token is the run-controller's file: nothing is exchanged")

    def test_ignores_other_hosts(self):
        self.assertEqual(self.get("gitlab.com"), "")
        self.assertEqual(Stub.calls, [])

    def test_token_action_prints_the_token(self):
        out = io.StringIO()
        with mock.patch("sys.stdout", out):
            helper.main(["git-credential-agent", "token"])
        self.assertEqual(out.getvalue(), "ghs_minted\n")

    def test_an_octo_sts_setting_is_refused_by_name(self):
        """v4 step 8 (R3): octo-sts is gone; a run that still names it fails, naming it."""
        err = io.StringIO()
        with mock.patch.dict(os.environ, {"STS_URL": self.base + "/sts/exchange"}), mock.patch("sys.stderr", err), \
                mock.patch("sys.stdout", io.StringIO()):
            self.assertEqual(helper.main(["git-credential-agent", "token"]), 1)
        self.assertIn("STS_URL (octo-sts) was removed in v4 step 8", err.getvalue())
        self.assertEqual(Stub.calls, [])

    def test_no_token_file_is_refused(self):
        err = io.StringIO()
        with mock.patch.dict(os.environ, {"GIT_TOKEN_FILE": ""}), mock.patch("sys.stderr", err), \
                mock.patch("sys.stdout", io.StringIO()):
            self.assertEqual(helper.main(["git-credential-agent", "token"]), 1)
        self.assertIn("GIT_TOKEN_FILE is unset", err.getvalue())

    def test_revoke_revokes_a_minted_file_token(self):
        """perRun (D10): the factory minted the file's token for this run alone."""
        helper.main(["git-credential-agent", "revoke"])
        self.assertEqual(Stub.calls, [("DELETE", "/installation/token", "Bearer ghs_minted")])

    def test_revoke_is_skipped_under_forge_read_and_logged_once(self):
        helper._revoke_skipped_logged = False
        with mock.patch.dict(os.environ, {"FORGE_READ": "1"}), self.assertLogs(helper.log, "INFO") as logs:
            helper.main(["git-credential-agent", "revoke"])
            helper._revoke_value("ghs_other")
        self.assertEqual(Stub.calls, [])
        self.assertEqual(sum("revoke skipped" in m for m in logs.output), 1)

    def test_revoke_without_a_token_file_is_a_no_op(self):
        with mock.patch.dict(os.environ, {"GIT_TOKEN_FILE": ""}):
            self.assertEqual(helper.main(["git-credential-agent", "revoke"]), 0)
        self.assertEqual(Stub.calls, [])

    def test_revoke_of_a_minted_file_token_logs_a_401(self):
        Stub.revoke_status = 401
        self.addCleanup(setattr, Stub, "revoke_status", 204)
        with self.assertLogs("git-credential-agent", level="WARNING") as logs:
            helper.main(["git-credential-agent", "revoke"])  # must not traceback
        self.assertEqual(len(Stub.calls), 1)
        self.assertNotIn("ghs_minted", "".join(logs.output))

    def test_a_minted_file_token_is_read_under_the_lock(self):
        """perRun: token() takes the lock revoke() holds, so no read interleaves with the revoke."""
        lock_fd = os.open(helper.LOCK, os.O_CREAT | os.O_RDWR, 0o600)
        fcntl.flock(lock_fd, fcntl.LOCK_EX)
        got = []
        th = threading.Thread(target=lambda: got.append(helper.token()))
        th.start()
        time.sleep(0.1)
        self.assertEqual(got, [], "token() must wait for the lock")
        fcntl.flock(lock_fd, fcntl.LOCK_UN)
        os.close(lock_fd)
        th.join(timeout=5)
        self.assertEqual(got, ["ghs_minted"])

    def test_revoke_waits_for_the_lock(self):
        """revoke() holds the lock token() takes, so a read never interleaves with the revoke."""
        lock_fd = os.open(helper.LOCK, os.O_CREAT | os.O_RDWR, 0o600)
        fcntl.flock(lock_fd, fcntl.LOCK_EX)
        done = threading.Event()
        th = threading.Thread(target=lambda: (helper.revoke(), done.set()))
        th.start()
        time.sleep(0.1)
        self.assertFalse(done.is_set(), "revoke() should block waiting for the lock")
        fcntl.flock(lock_fd, fcntl.LOCK_UN)
        os.close(lock_fd)
        th.join(timeout=5)
        self.assertTrue(done.is_set())
        self.assertEqual(Stub.calls, [("DELETE", "/installation/token", "Bearer ghs_minted")])

    def test_revoke_of_a_file_token_without_the_flag_makes_no_call(self):
        with mock.patch.dict(os.environ, {"GIT_TOKEN_REVOCABLE": ""}):
            helper.main(["git-credential-agent", "revoke"])
        self.assertEqual(Stub.calls, [])


class TokenFileMode(unittest.TestCase):
    def setUp(self):
        self.dir = tempfile.mkdtemp()
        self.path = os.path.join(self.dir, "token")
        with open(self.path, "w") as f:
            f.write("github_pat_x\n")

    def test_token_reads_a_pat_without_the_lock(self):
        with mock.patch.dict(os.environ, {"GIT_TOKEN_FILE": self.path, "GIT_TOKEN_REVOCABLE": ""}), \
                mock.patch.object(helper, "_locked", side_effect=AssertionError("a PAT takes no lock")):
            self.assertEqual(helper.token(), "github_pat_x")

    def test_revoke_never_touches_the_users_pat(self):
        with mock.patch.dict(os.environ, {"GIT_TOKEN_FILE": self.path, "GIT_TOKEN_REVOCABLE": ""}), \
                mock.patch.object(helper, "_revoke_value", side_effect=AssertionError("never revoke a PAT")):
            helper.revoke()
        self.assertTrue(os.path.exists(self.path))


if __name__ == "__main__":
    unittest.main()
