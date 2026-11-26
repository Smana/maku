"""The install phase (GitHub egress tightening, 2026-11-26): agent-run installs the toolchain before the
agent starts, closes the install hosts at the egress proxy, checks that a release download is refused,
and only then starts the agent. Against the harness's test proxy (tests/checks/proxy.py), which stands
in for the egress proxy's install set and its close."""
import contextlib
import io
import os
import socket
import subprocess
import sys
import tempfile
import time
import unittest
from unittest import mock

HERE = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..")
sys.path.insert(0, HERE)
import agent_run  # noqa: E402

PROXY = os.path.join(os.path.dirname(os.path.abspath(__file__)), "checks", "proxy.py")
RELEASE = "release-assets.githubusercontent.com"


def free_port() -> int:
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


class ProxyRig(unittest.TestCase):
    def proxy(self, **env):
        """The test proxy on a free port, allowing github.com and the install set; its env from env."""
        port = free_port()
        self.log = os.path.join(self.tmp, "proxy.log")
        # -I -S and a bare environment: the image's sitecustomize (tracing, MCP) stays out of the proxy.
        p = subprocess.Popen([sys.executable, "-I", "-S", PROXY, str(port)],
                             env={"PATH": os.environ.get("PATH", "/usr/bin:/bin"), "ALLOW": "github.com", "INSTALL": RELEASE,
                                  "LOG": self.log, "DIAL": "0", **env},
                             stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)

        def stop():
            p.terminate()
            try:
                p.wait(timeout=5)
            except subprocess.TimeoutExpired:
                p.kill()
                p.wait()
        self.addCleanup(stop)
        deadline = time.monotonic() + 10
        while True:
            try:
                socket.create_connection(("127.0.0.1", port), timeout=1).close()
                break
            except OSError:
                if time.monotonic() > deadline:
                    raise
                time.sleep(0.05)
        url = "http://127.0.0.1:%d" % port
        return {"HTTPS_PROXY": url, "EGRESS_INSTALL_CLOSE_URL": url + "/v1/close-install", "POD_UID": "uid-1", "AGENT_CHECKS": "1"}

    def setUp(self):
        self.tmp = tempfile.mkdtemp()
        for name, value in {"CHECKS_DIR": os.path.join(self.tmp, "checks"), "SELF_CHECK_S": 1}.items():
            patcher = mock.patch.object(agent_run, name, value)
            patcher.start()
            self.addCleanup(patcher.stop)

    def proxy_log(self):
        with open(self.log) as f:
            return f.read()

    def marker(self):
        return os.path.exists(os.path.join(agent_run.CHECKS_DIR, "install-closed"))


class CloseInstallTest(ProxyRig):
    def test_the_close_narrows_the_proxy_and_is_checked(self):
        env = self.proxy()
        self.assertEqual(agent_run.connect_status(env, RELEASE + ":443"), 200, "the install set is open before the close")
        out = io.StringIO()
        with contextlib.redirect_stdout(out):
            agent_run.close_install(env)
        self.assertIn('close {"podUID": "uid-1"}', self.proxy_log())
        self.assertIn("install-check closed", self.proxy_log())
        self.assertEqual(agent_run.connect_status(env, RELEASE + ":443"), 403, "a release download is refused")
        self.assertTrue(self.marker(), "agent-checks learns the phase is closed")
        self.assertIn("install hosts closed", out.getvalue())
        self.assertEqual(agent_run.connect_status(env, "github.com:443"), 200, "the run set stays open")

    def test_a_close_the_proxy_refuses_fails_closed(self):
        env = self.proxy(CLOSE="fail")
        with self.assertRaises(agent_run.EgressInstallOpen) as cm:
            agent_run.close_install(env)
        self.assertIn("500", str(cm.exception))
        self.assertFalse(self.marker())

    def test_a_set_still_open_after_the_close_fails_closed(self):
        env = self.proxy(CLOSE="ignore")
        with self.assertRaises(agent_run.EgressInstallOpen) as cm:
            agent_run.close_install(env)
        self.assertIn("409", str(cm.exception))
        self.assertFalse(self.marker())

    def test_an_unreachable_proxy_fails_closed(self):
        url = "http://127.0.0.1:%d" % free_port()
        env = {"HTTPS_PROXY": url, "EGRESS_INSTALL_CLOSE_URL": url + "/v1/close-install", "POD_UID": "uid-1"}
        with self.assertRaises(agent_run.EgressInstallOpen):
            agent_run.close_install(env)

    def test_without_a_close_endpoint_the_check_run_says_the_hosts_stay_open(self):
        err = io.StringIO()
        with contextlib.redirect_stderr(err):
            agent_run.close_install({"AGENT_CHECKS": "1"})
        self.assertIn("stay open", err.getvalue())
        self.assertFalse(self.marker())
        err = io.StringIO()
        with contextlib.redirect_stderr(err):
            agent_run.close_install({})
        self.assertEqual(err.getvalue(), "", "a run without checks has no install set to speak of")


class InstallPhaseOrderTest(unittest.TestCase):
    """main: the install, then the close, then the conversation; a close that fails starts no agent."""

    def _main(self, close_side_effect=None, decl={"tools": [], "steps": []}):
        order = []
        with tempfile.TemporaryDirectory() as d:
            task = os.path.join(d, "task.md")
            with open(task, "w") as f:
                f.write("rules")
            env = {"TASK_FILE": task, "RULES_FILE": task, "ROLE": "implementer", "AGENT_CHECKS": "1"}
            with mock.patch.dict(os.environ, env, clear=True), \
                    mock.patch("agent_run.subprocess.Popen"), \
                    mock.patch("agent_run.wait_ready"), \
                    mock.patch("agent_run.persisted", return_value=False), \
                    mock.patch("agent_run.clone", return_value=False), \
                    mock.patch("agent_run.declared", return_value=decl), \
                    mock.patch("agent_run.install_toolchain", side_effect=lambda *a, **k: order.append("install")), \
                    mock.patch("agent_run.close_install", side_effect=close_side_effect or (lambda *a, **k: order.append("close"))), \
                    mock.patch("agent_run.final_checks"), \
                    mock.patch("agent_run.build_request", side_effect=lambda *a, **k: order.append("agent") or {"x": 1}), \
                    mock.patch("agent_run.http", return_value={"id": "c1"}), \
                    mock.patch("agent_run.poll", return_value=0), \
                    mock.patch("agent_run.StepLog"), \
                    mock.patch("agent_run.stop", return_value="stopped"), \
                    mock.patch("agent_run.timed"), \
                    contextlib.redirect_stderr(io.StringIO()) as err:
                code = agent_run.main()
        return code, order, err.getvalue()

    def test_the_agent_starts_after_the_install_and_the_close(self):
        code, order, _ = self._main()
        self.assertEqual(code, 0)
        self.assertEqual(order, ["install", "close", "agent"])

    def test_a_run_whose_repository_declares_nothing_still_closes(self):
        _, order, _ = self._main(decl=None)
        self.assertEqual(order, ["close", "agent"])

    def test_an_install_set_left_open_starts_no_agent(self):
        code, order, err = self._main(close_side_effect=agent_run.EgressInstallOpen("the close answered 500"))
        self.assertEqual(code, 1)
        self.assertEqual(order, ["install"])
        self.assertIn("egress_install_open: the close answered 500; the agent does not start", err)


if __name__ == "__main__":
    unittest.main()
