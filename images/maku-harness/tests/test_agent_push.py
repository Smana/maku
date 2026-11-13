"""The git shim and agent-push under hardening H3 (HR23): a run whose pushes the factory applies requests
its branch as a git bundle through output_push, against a local bare remote and a fake room MCP server that
applies the bundle as the factory would."""
import base64
import http.server
import json
import os
import shutil
import subprocess
import sys
import tempfile
import threading
import unittest
from unittest import mock

HERE = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..")
sys.path.insert(0, HERE)

import agent_push  # noqa: E402
import agent_run  # noqa: E402

# The source tree has the shim beside agent_run.py; the image installs it as /usr/local/bin/git.
SHIM = next(p for p in (os.path.join(HERE, "git"), "/usr/local/bin/git") if os.path.isfile(p))
REAL = "/usr/bin/git"  # never shutil.which: in the image the shim is first on PATH
BRANCH = "agent/t-1"
OUTPUT_ID = "0123456789abcdef"


class FakeFactory:
    """The room MCP server's output_push and output_status, with the applier behind them: an applied
    bundle is fetched into the bare remote. answers is the status sequence output_status walks."""

    def __init__(self, test, remote, answers=("applied",), branch=BRANCH):
        self.remote, self.answers, self.branch, self.calls, self.bundles = remote, list(answers), branch, [], []
        self.tools = ["room_progress", "output_push", "output_status"]
        factory = self

        class Handler(http.server.BaseHTTPRequestHandler):
            def do_POST(self):
                body = json.loads(self.rfile.read(int(self.headers["content-length"])))
                if "id" not in body:
                    self.send_response(202)
                    self.send_header("Content-Length", "0")
                    self.end_headers()
                    return
                result = {}
                if body["method"] == "initialize":
                    result = {"protocolVersion": "2025-06-18", "capabilities": {"tools": {}},
                              "serverInfo": {"name": "room-broker", "version": "0"}}
                elif body["method"] == "tools/list":
                    result = {"tools": [{"name": n, "inputSchema": {"type": "object"}} for n in factory.tools]}
                elif body["method"] == "tools/call":
                    name, args = body["params"]["name"], body["params"]["arguments"]
                    factory.calls.append((name, self.headers.get("X-Agent-Harness"), self.headers.get("Authorization")))
                    result = {"content": [{"type": "text", "text": json.dumps(factory.tool(name, args))}]}
                out = json.dumps({"jsonrpc": "2.0", "id": body["id"], "result": result}).encode()
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(out)))
                self.end_headers()
                self.wfile.write(out)

            def log_message(self, *args):
                pass

        self.server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.server.daemon_threads = True
        threading.Thread(target=self.server.serve_forever, daemon=True).start()
        test.addCleanup(self.server.server_close)
        test.addCleanup(self.server.shutdown)
        self.url = "http://127.0.0.1:%d/mcp" % self.server.server_port
        self.dir = tempfile.mkdtemp()
        test.addCleanup(shutil.rmtree, self.dir)

    def names(self):
        return [c[0] for c in self.calls]

    def tool(self, name, args):
        if name == "output_push":
            path = os.path.join(self.dir, "%d.bundle" % len(self.bundles))
            with open(path, "wb") as f:
                f.write(base64.b64decode(args["bundle"]))
            self.bundles.append(path)
            return {"outputId": OUTPUT_ID, "status": "requested", "seq": 3}
        kind = self.answers.pop(0) if len(self.answers) > 1 else self.answers[0]
        if kind == "applied":
            head = self.heads(self.bundles[-1])[0].split()[0]
            subprocess.run([REAL, "--git-dir", self.remote, "fetch", "-q", self.bundles[-1],
                            "+refs/heads/%s:refs/heads/%s" % (self.branch, self.branch)], check=True, capture_output=True)
            return {"outputId": args["outputId"], "status": "applied", "object": "https://github.com/o/r/commit/" + head}
        if kind == "refused":
            return {"outputId": args["outputId"], "status": "refused", "reason": "push_history"}
        if kind == "waiting":
            return {"outputId": args["outputId"], "status": "waiting", "approvalId": "ap-7"}
        return {"outputId": args["outputId"], "status": kind}

    def heads(self, path):
        return subprocess.run([REAL, "bundle", "list-heads", path], capture_output=True, text=True,
                              check=True).stdout.splitlines()

    def requires(self, path):
        p = subprocess.run([REAL, "--git-dir", self.remote, "bundle", "verify", path], capture_output=True, text=True)
        return p.returncode, p.stdout + p.stderr


class Clone:
    """A bare remote with main, and a clone on agent/t-1 holding one commit the remote lacks."""

    def setUp(self):
        self.d = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.d)
        gitconfig = os.path.join(self.d, "gitconfig")
        with open(gitconfig, "w") as f:
            f.write("[user]\n\tname = t\n\temail = t@t\n[init]\n\tdefaultBranch = main\n")
        self.token = os.path.join(self.d, "token")
        with open(self.token, "w") as f:
            f.write("room-token\n")
        self.genv = {"GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": gitconfig}
        patched = mock.patch.dict(os.environ, self.genv)
        patched.start()
        self.addCleanup(patched.stop)
        self.remote, self.repo, seed = (os.path.join(self.d, n) for n in ("remote.git", "repo", "seed"))
        self.git(self.d, "init", "-q", "--bare", self.remote)
        self.git(self.d, "clone", "-q", self.remote, seed)
        self.commit(seed, "README.md", "seed\n")
        self.git(seed, "push", "-q", "origin", "HEAD:main")
        self.git(self.d, "clone", "-q", self.remote, self.repo)
        self.git(self.repo, "checkout", "-q", "-B", BRANCH, "origin/main")
        self.main = self.git(self.repo, "rev-parse", "origin/main")
        self.head = self.commit(self.repo, "change.md", "one\n")
        stub = os.path.join(self.d, "agent-push")
        with open(stub, "w") as f:
            f.write('#!/bin/sh\nexec "%s" "%s" "$@"\n' % (sys.executable, os.path.join(HERE, "agent_push.py")))
        os.chmod(stub, 0o755)
        self.stub = stub

    def git(self, cwd, *args):
        return subprocess.run([REAL, *args], cwd=cwd, check=True, capture_output=True, text=True).stdout.strip()

    def commit(self, cwd, name, text, data=None):
        with open(os.path.join(cwd, name), "wb") as f:
            f.write(data if data is not None else text.encode())
        self.git(cwd, "add", name)
        self.git(cwd, "commit", "-q", "-m", "docs: " + name)
        return self.git(cwd, "rev-parse", "HEAD")

    def env(self, factory=None, safe="pr,push"):
        env = {k: v for k, v in os.environ.items() if k not in ("SAFE_OUTPUTS", "GIT_DIR", "GIT_WORK_TREE")}
        env.update(self.genv, REAL_GIT=REAL, AGENT_PUSH=self.stub, BRANCH=BRANCH, BASE_REF="main",
                   MCP_TOKEN_FILE=self.token)
        if factory:
            env["MCP_URL"] = factory.url
        if safe is not None:
            env["SAFE_OUTPUTS"] = safe
        return env

    def push(self, *argv, factory=None, cwd=None, safe="pr,push"):
        return subprocess.run(["sh", SHIM, *argv], cwd=cwd or self.repo, capture_output=True, text=True,
                              env=self.env(factory, safe), timeout=120)


class AgentPushTest(Clone, unittest.TestCase):
    def test_each_push_form_requests_a_bundle_of_the_branch(self):
        for argv in (["push"], ["push", "origin", "HEAD"], ["push", "origin", BRANCH],
                     ["push", "-u", "origin", "HEAD:refs/heads/" + BRANCH], ["push", "--force-with-lease", "origin"]):
            with self.subTest(argv=argv):
                factory = FakeFactory(self, self.remote, ["requested", "refused"])
                p = self.push(*argv, factory=factory)
                self.assertEqual(p.returncode, 1, p.stderr)
                self.assertEqual(factory.names()[0], "output_push")
                self.assertEqual(factory.calls[0][1:], ("1", "Bearer room-token"), "the harness marker and the bearer")
                [bundle] = factory.bundles
                self.assertEqual(factory.heads(bundle), ["%s refs/heads/%s" % (self.head, BRANCH)], "one ref, no tag")
                code, out = factory.requires(bundle)
                self.assertEqual(code, 0, out)
                self.assertIn(self.main, out, "the remote-tracking main is a prerequisite, not in the bundle")

    def test_the_answers(self):
        for answers, code, line in ((["applied"], 0, "applied " + "%s"), (["refused"], 1, "refused push_history"),
                                    (["requested", "waiting"], 0, "waiting for a maintainer's approval (ap-7)")):
            with self.subTest(answer=answers[-1]):
                factory = FakeFactory(self, self.remote, answers)
                p = self.push("push", "origin", "HEAD", factory=factory)
                self.assertEqual(p.returncode, code, p.stderr)
                self.assertEqual(p.stdout.strip().splitlines()[-1], line.replace("%s", self.head))
                self.assertEqual(factory.names().count("output_push"), 1)
                self.git(self.repo, "update-ref", "-d", "refs/remotes/origin/" + BRANCH)

    def test_applied_moves_the_remote_tracking_branch(self):
        factory = FakeFactory(self, self.remote)
        self.assertEqual(self.push("push", factory=factory).returncode, 0)
        self.assertEqual(self.git(self.remote, "rev-parse", BRANCH), self.head, "the factory applied the bundle")
        with mock.patch.object(agent_run, "REPO_DIR", self.repo):
            self.assertEqual(agent_run.unpushed({"BRANCH": BRANCH}), 0)
        # The next push excludes the branch's head as well: its bundle holds the new commit only.
        second = self.commit(self.repo, "two.md", "two\n")
        self.assertEqual(self.push("push", factory=factory).returncode, 0)
        code, out = factory.requires(factory.bundles[-1])
        self.assertEqual(code, 0, out)
        self.assertIn(self.head, out)
        self.assertEqual(self.git(self.remote, "rev-parse", BRANCH), second)

    def test_anything_but_the_branch_exits_3_with_no_call(self):
        self.git(self.repo, "tag", "v1")
        for argv in (["push", "--delete", "origin", BRANCH], ["push", "-d", "origin", BRANCH],
                     ["push", "origin", ":" + BRANCH], ["push", "origin", "v1"], ["push", "origin", "refs/tags/v1"],
                     ["push", "--tags"], ["push", "origin", "main"], ["push", "origin", "HEAD:main"],
                     ["push", "--all"], ["push", "--mirror"]):
            with self.subTest(argv=argv):
                factory = FakeFactory(self, self.remote)
                p = self.push(*argv, factory=factory)
                self.assertEqual(p.returncode, 3, p.stderr)
                self.assertIn("push only " + BRANCH, p.stderr)
                self.assertEqual(factory.calls, [])

    def test_another_checked_out_branch_exits_3(self):
        self.git(self.repo, "checkout", "-q", "-b", "other")
        factory = FakeFactory(self, self.remote)
        p = self.push("push", factory=factory)
        self.assertEqual(p.returncode, 3)
        self.assertIn("push only " + BRANCH, p.stderr)
        self.assertEqual(factory.calls, [])

    def test_a_bundle_over_8_mib_is_push_size_before_any_call(self):
        self.commit(self.repo, "big.bin", "", os.urandom(agent_push.MAX_BUNDLE + (1 << 20)))
        factory = FakeFactory(self, self.remote)
        p = self.push("push", factory=factory)
        self.assertEqual(p.returncode, 1)
        self.assertIn("push_size", p.stderr)
        self.assertEqual(factory.calls, [])

    def test_nothing_new_is_up_to_date_with_no_call(self):
        self.git(self.repo, "reset", "-q", "--hard", "origin/main")
        factory = FakeFactory(self, self.remote)
        p = self.push("push", factory=factory)
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertIn("Everything up-to-date", p.stdout)
        self.assertEqual(factory.calls, [])

    def test_global_options_before_push(self):
        factory = FakeFactory(self, self.remote)
        p = self.push("-C", self.repo, "-c", "push.default=nothing", "push", "origin", "HEAD", factory=factory,
                      cwd=self.d)
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertEqual(p.stdout.strip(), "applied " + self.head)

    def test_an_unanswered_request_is_reported_after_the_wait(self):
        factory = FakeFactory(self, self.remote, ["requested"])
        env = self.env(factory)
        with mock.patch.object(agent_push, "WAIT_S", 0), mock.patch.dict(os.environ, env), \
                mock.patch("sys.stdout") as out:
            cwd = os.getcwd()
            os.chdir(self.repo)
            try:
                code = agent_push.main(["origin", "HEAD"], env)
            finally:
                os.chdir(cwd)
        self.assertEqual(code, 0)
        printed = "".join(c.args[0] for c in out.write.call_args_list)
        self.assertIn("requested %s: no answer" % OUTPUT_ID, printed)

    def test_request_returns_the_output_id_without_waiting(self):
        factory = FakeFactory(self, self.remote)
        self.assertEqual(agent_push.request(self.env(factory), "HEAD", self.repo), OUTPUT_ID)
        self.assertEqual(factory.names(), ["output_push"])


class ShimTest(unittest.TestCase):
    """Every other subcommand, and push without push in SAFE_OUTPUTS, runs the real git unchanged."""

    def setUp(self):
        self.d = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.d)
        self.real, self.push = os.path.join(self.d, "real"), os.path.join(self.d, "push")
        for path, word in ((self.real, "real"), (self.push, "push")):
            with open(path, "w") as f:
                f.write('#!/bin/sh\necho %s "$PWD" "$@"\n' % word)
            os.chmod(path, 0o755)

    def run_shim(self, argv, safe=None, cwd=None):
        env = {k: v for k, v in os.environ.items() if k != "SAFE_OUTPUTS"}
        env.update(REAL_GIT=self.real, AGENT_PUSH=self.push)
        if safe is not None:
            env["SAFE_OUTPUTS"] = safe
        p = subprocess.run(["sh", SHIM, *argv], capture_output=True, text=True, env=env, cwd=cwd or self.d)
        self.assertEqual(p.returncode, 0, p.stderr)
        return p.stdout.strip()

    def test_without_push_in_safe_outputs_even_push_passes_through(self):
        for safe in (None, "", "pr,comment", "pushes"):
            with self.subTest(safe=safe):
                self.assertEqual(self.run_shim(["push", "origin", "HEAD"], safe), "real %s push origin HEAD" % self.d)

    def test_every_other_subcommand_passes_through_unchanged(self):
        for argv in (["status"], ["commit", "-m", "push"], ["-C", "x", "log", "push"], ["-c", "a=b", "fetch"],
                     ["--git-dir=x", "rev-parse", "HEAD"], ["--no-pager", "diff"], ["-c", "push"], []):
            with self.subTest(argv=argv):
                self.assertEqual(self.run_shim(argv, "pr,push"), ("real %s %s" % (self.d, " ".join(argv))).strip())

    def test_push_is_handed_to_agent_push(self):
        sub = os.path.join(self.d, "sub")
        os.mkdir(sub)
        self.assertEqual(self.run_shim(["push", "-u", "origin", "HEAD"], "push"), "push %s -u origin HEAD" % self.d)
        self.assertEqual(self.run_shim(["-C", "sub", "-c", "k=v", "--no-pager", "push", "origin"], "comment,push"),
                         "push %s origin" % sub)


if __name__ == "__main__":
    unittest.main()
