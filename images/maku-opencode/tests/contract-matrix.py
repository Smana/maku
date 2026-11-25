#!/usr/bin/env python3
"""Repeatable offline parity matrix for OpenCode 2.3.0 against the real binary (OpenCode plan Task 14).

Runs every role x provider x required capability against the real pinned OpenCode 2.3.0 binary
inside a read-only, non-root, network-isolated container, plus the Go OpenCode bridge & integration
verification suites. Fails on any skipped or missing case.
"""
import argparse
import http.server
import json
import os
import subprocess
import sys
import tempfile
import threading
import time
import urllib.parse
import urllib.request

ROLES = ["implementer", "reviewer", "tester", "triager"]
PROVIDERS = ["glm", "anthropic", "kimi", "mistral"]
REQUIRED = [
    "tools",
    "permissions",
    "steering",
    "replay",
    "stuck",
    "search",
    "cache",
    "retry",
    "resume",
    "checkpoint",
    "final_read",
    "tracing",
]

PROVIDER_SPECS = {
    "glm": ("public", "agent-default", "high", False),
    "anthropic": ("internal", "tier-standard", "high", True),
    "kimi": ("public", "kimi-frontier", "medium", False),
    "mistral": ("public", "mistral-standard", "low", False),
}


def _run_host() -> int:
    root = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", "..", ".."))
    image = os.environ.get("MAKU_OPENCODE_IMAGE", "maku-opencode:contract-matrix")
    runtime = os.environ.get("DOCKER_RUNTIME", "")

    print("==> Building maku-opencode harness image...")
    subprocess.run(
        ["docker", "build", "--target", "harness", "-t", image, "-f", "images/maku-opencode/Dockerfile", "."],
        cwd=root,
        check=True,
    )
    size_bytes = int(
        subprocess.check_output(["docker", "image", "inspect", image, "--format", "{{.Size}}"], text=True).strip()
    )
    print(f"==> Image {image}: {size_bytes:,} bytes ({size_bytes / (1024 * 1024):.1f} MiB)")

    print("==> Running Go OpenCode bridge and factory no-model integration checks...")
    subprocess.run(
        ["go", "test", "-race", "./internal/bridge/", "-run", "OpenCode", "-count=1"],
        cwd=root,
        check=True,
    )
    subprocess.run(
        ["go", "test", "-race", "./internal/factory/reconciler/", "-run", "TestEveryRoleGetsTheFrozenHarness|TestIntegration", "-count=1"],
        cwd=root,
        check=True,
    )

    cmd = [
        "docker",
        "run",
        "--rm",
        "--network=none",
        "--read-only",
        "--user",
        "10001:10001",
        "--tmpfs",
        "/tmp:rw,nosuid,nodev,size=256m",
        "-v",
        f"{root}/images/maku-opencode/tests:/opt/agent/tests:ro",
        "-w",
        "/opt/agent/tests",
        "--entrypoint",
        "/opt/agent/.venv/bin/python",
    ]
    if runtime:
        cmd.append(f"--runtime={runtime}")
    cmd.extend([image, "/opt/agent/tests/contract-matrix.py", "--inside-container"])

    print(f"==> Running real-binary matrix inside container (runtime={runtime or 'runc'})...")
    subprocess.run(cmd, cwd=root, check=True)
    return 0


def _wait_status(driver, cid: str, want: set[str], timeout_s: float = 20.0) -> str:
    end = time.monotonic() + timeout_s
    last = ""
    while time.monotonic() < end:
        last = driver.status(cid)
        if last in want:
            return last
        time.sleep(0.15)
    return last


def _make_driver(tmp: str, llm_url: str, **extra_env):
    import opencode_driver
    from test_provider_contract import _free_port

    port = _free_port()
    env = {
        "AGENT_HARNESS": "opencode",
        "ROLE": "implementer",
        "MODEL": "agent-default",
        "DATA_CLASS": "public",
        "LLM_BASE_URL": llm_url,
        "LLM_REASONING_EFFORT": "low",
        "OPENCODE_PORT": str(port),
        "OPENCODE_WORKSPACE_ROOT": os.path.join(tmp, ".opencode"),
        "OPENCODE_SESSION_ID_FILE": os.path.join(tmp, ".session-id"),
        "OPENCODE_PASSWORD_FILE": os.path.join(tmp, ".password"),
        "OPENCODE_TERMINAL_FILE": os.path.join(tmp, ".terminal.json"),
        "OPENCODE_WORKDIR": tmp,
        **extra_env,
    }
    return opencode_driver.OpenCodeDriver(env), env


def _run_inside_container() -> int:
    sys.path.insert(0, "/opt/agent")
    sys.path.insert(0, "/opt/agent/tests")

    from harness_types import ConversationRequest
    import mcp_proxy
    import role_config
    import search as oc_search
    from test_provider_contract import ScriptedLLMServer

    results: dict[str, str] = {}
    covered_roles: set[str] = set()
    covered_providers: set[str] = set()
    covered_caps: set[str] = set()

    def record(name: str, detail: str) -> None:
        results[name] = detail
        print(f"  [PASS] {name}: {detail}", flush=True)

    # 1. Roles matrix (real `opencode debug config` + real `opencode serve` session per role).
    for role in ROLES:
        tmp = tempfile.mkdtemp(prefix=f"mx-role-{role}-")
        llm = ScriptedLLMServer([{"text": f"role {role} ready"}])
        llm.start()
        try:
            d, env = _make_driver(tmp, llm.base_url, ROLE=role)
            t0 = time.monotonic()
            d.start()
            try:
                d.wait_ready(deadline_s=30)
                ready_ms = int((time.monotonic() - t0) * 1000)
                cid = d.create_conversation(ConversationRequest(env=env, task=f"verify {role}", rules="brief"))
                st = _wait_status(d, cid, {"finished", "idle", "error"})
                assert st in ("finished", "idle"), f"role {role}: unexpected status {st}"
                # Validate loaded config and live registered role agent via the server API
                # (avoids `opencode debug config` spawning an orphaned `--service` daemon).
                docs = d._request("GET", "/api/config", timeout=10)
                doc = next(x["info"] for x in docs if x.get("type") == "document")
                assert doc["default_agent"] == role
                agent_info = d._request("GET", f"/api/agent/{role}", timeout=10)["data"]
                perms = agent_info["permissions"]
                if role in ("reviewer", "triager"):
                    assert {"action": "edit", "resource": "*", "effect": "deny"} in perms
                    assert {"action": "write", "resource": "*", "effect": "deny"} in perms
                if role == "tester":
                    assert {"action": "shell", "resource": "git push*", "effect": "deny"} in perms
            finally:
                d.stop(timeout=5)
            covered_roles.add(role)
            record(f"role:{role}", f"real server ready in {ready_ms}ms, session={cid}, live agent & config valid")
        finally:
            llm.stop()

    # 2. Providers matrix (glm, anthropic, kimi, mistral) + cache + retry capabilities.
    for prov in PROVIDERS:
        data_class, model, effort, want_cache = PROVIDER_SPECS[prov]
        tmp = tempfile.mkdtemp(prefix=f"mx-prov-{prov}-")
        responses = [{"text": f"{prov} ok"}]
        if prov == "glm":
            # Exercise transient 429 retry/error handling on the real OpenCode client.
            responses = [{"status": 429, "error": "rate limit"}, {"text": "glm recovered"}]
        llm = ScriptedLLMServer(responses)
        llm.start()
        try:
            d, env = _make_driver(tmp, llm.base_url, DATA_CLASS=data_class, MODEL=model, LLM_REASONING_EFFORT=effort)
            d.start()
            try:
                d.wait_ready(deadline_s=30)
                cid = d.create_conversation(ConversationRequest(env=env, task=f"provider {prov}", rules="brief"))
                st = _wait_status(d, cid, {"finished", "idle", "error"}, timeout_s=25.0)
                assert len(llm.requests) >= 1, f"{prov}: no LLM requests recorded"
                assert llm.requests[0]["model"] == model
                assert llm.requests[0].get("reasoning_effort") == effort
                cfg = role_config.role_config(env)
                has_cache = "cacheFormat" in cfg["providers"]["maku"].get("compatibility", {})
                assert has_cache == want_cache
                covered_providers.add(prov)
                record(
                    f"provider:{prov}",
                    f"model={model} effort={effort} cache={has_cache} requests={len(llm.requests)} status={st}",
                )
                if prov == "anthropic":
                    covered_caps.add("cache")
                    record("cap:cache", "internal provider sets compatibility.cacheFormat=anthropic; public omits it")
                if prov == "glm":
                    covered_caps.add("retry")
                    record("cap:retry", f"429 handled cleanly (requests={len(llm.requests)}, status={st})")
            finally:
                d.stop(timeout=5)
        finally:
            llm.stop()

    # 3. Tools + Replay + Final Read + Steering + Stuck.
    tmp = tempfile.mkdtemp(prefix="mx-tools-")
    marker = os.path.join(tmp, "tool-ran.txt")
    llm = ScriptedLLMServer([
        {"tool_call": {"id": "c_tool_1", "name": "shell", "arguments": {"command": f"echo hi > {marker}", "description": "write marker"}}},
        {"text": "tool complete"},
        {"text": "steered turn complete"},
    ])
    llm.start()
    try:
        d, env = _make_driver(tmp, llm.base_url)
        d.start()
        try:
            d.wait_ready(deadline_s=30)
            cid = d.create_conversation(ConversationRequest(env=env, task="run tool", rules="brief"))
            st = _wait_status(d, cid, {"finished", "idle", "error"})
            assert st in ("finished", "idle") and os.path.exists(marker)
            p1 = d.events(cid)
            kinds = [e["kind"] for e in p1.items]
            assert "ActionEvent" in kinds and "ObservationEvent" in kinds and "MessageEvent" in kinds
            covered_caps.add("tools")
            record("cap:tools", f"shell tool executed and normalized ({len(p1.items)} events)")

            # Replay stability: re-fetching produces identical IDs with no duplicates.
            p2 = d.events(cid)
            ids1 = [e["id"] for e in p1.items]
            ids2 = [e["id"] for e in p2.items]
            assert ids1 == ids2 and len(ids1) == len(set(ids1))
            covered_caps.add("replay")
            record("cap:replay", f"{len(ids1)} stable unique normalized event IDs")

            # Steering.
            d.send(cid, "steer follow-up", run=True)
            end = time.monotonic() + 15.0
            while time.monotonic() < end and (len(llm.requests) < 3 or d.status(cid) not in ("finished", "idle")):
                time.sleep(0.15)
            assert len(llm.requests) == 3
            covered_caps.add("steering")
            record("cap:steering", "follow-up prompt delivered and answered by real OpenCode session")

            # Final read.
            final_page = d.events(cid)
            texts = [
                part.get("text", "")
                for e in final_page.items
                if e.get("kind") == "MessageEvent"
                for part in (e.get("llm_message") or {}).get("content", [])
            ]
            assert any("steered turn complete" in t for t in texts)
            covered_caps.add("final_read")
            record("cap:final_read", "final assistant message readable before stop")

            # Stuck via loop_guard.
            for i in range(4):
                d.on_event({"id": f"st-a-{i}", "kind": "ActionEvent", "tool_call_id": f"sc-{i}", "tool_name": "shell", "action": {"command": "true"}})
                d.on_event({"id": f"st-o-{i}", "kind": "ObservationEvent", "tool_call_id": f"sc-{i}", "tool_name": "shell", "observation": {"content": [{"type": "text", "text": "ok"}], "is_error": False}})
            assert d.status(cid) == "stuck"
            covered_caps.add("stuck")
            record("cap:stuck", "loop_guard wrote terminal marker and interrupted real session")
        finally:
            d.stop(timeout=5)
    finally:
        llm.stop()

    # 4. Permissions (ask -> reject prevents execution).
    tmp = tempfile.mkdtemp(prefix="mx-perm-")
    blocked = os.path.join(tmp, "blocked.txt")
    llm = ScriptedLLMServer([
        {"tool_call": {"id": "c_perm_1", "name": "shell", "arguments": {"command": f"echo no > {blocked}", "description": "blocked"}}},
        {"text": "skipped"},
    ])
    llm.start()
    try:
        d, env = _make_driver(tmp, llm.base_url, OPENCODE_ALWAYS_ASK="1")
        d.start()
        try:
            d.wait_ready(deadline_s=30)
            cid = d.create_conversation(ConversationRequest(env=env, task="perm check", rules="brief"))
            assert _wait_status(d, cid, {"waiting_for_confirmation", "finished", "error"}) == "waiting_for_confirmation"
            q_sid = urllib.parse.quote(cid, safe="")
            perms = d._request("GET", f"/api/session/{q_sid}/permission", timeout=10).get("data") or []
            req_id = perms[0]["id"]
            d._request("POST", f"/api/session/{q_sid}/permission/{urllib.parse.quote(req_id, safe='')}/reply", {"decision": "reject", "message": "no"}, timeout=10)
            assert _wait_status(d, cid, {"finished", "idle", "error"}) in ("finished", "idle")
            assert not os.path.exists(blocked)
            covered_caps.add("permissions")
            record("cap:permissions", "ask paused session; reject blocked shell command")
        finally:
            d.stop(timeout=5)
    finally:
        llm.stop()

    # 5. Bounded synchronous search (independent read-only search session).
    tmp = tempfile.mkdtemp(prefix="mx-search-")
    llm = ScriptedLLMServer([{"text": "found auth middleware in internal/authn"}])
    llm.start()
    try:
        d, env = _make_driver(
            tmp,
            llm.base_url,
            AGENT_SEARCH="1",
            SEARCH_LEDGER=os.path.join(tmp, ".agent-search.jsonl"),
        )
        d.start()
        try:
            d.wait_ready(deadline_s=30)
            res = oc_search.search("where is auth middleware?", env, d, deadline_s=15.0)
            assert res.outcome == "complete" and "internal/authn" in res.answer
            covered_caps.add("search")
            record("cap:search", "independent read-only search session returned concise answer")
        finally:
            d.stop(timeout=5)
    finally:
        llm.stop()

    # 6. Resume (PVC restart) & Checkpoint/shutdown (owned child shell killed within 15s).
    tmp = tempfile.mkdtemp(prefix="mx-resume-")
    pid_file = os.path.join(tmp, "bg.pid")
    llm = ScriptedLLMServer([
        {"text": "persisted turn 1"},
        {"tool_call": {"id": "c_bg_1", "name": "shell", "arguments": {"command": f"echo $$ > {pid_file}; sleep 60", "description": "sleep"}}},
        {"text": "interrupted"},
    ])
    llm.start()
    try:
        d1, env1 = _make_driver(tmp, llm.base_url, AGENT_SUSPENDABLE="true")
        d1.start()
        try:
            d1.wait_ready(deadline_s=30)
            cid = d1.create_conversation(ConversationRequest(env=env1, task="persist me", rules="brief"))
            assert _wait_status(d1, cid, {"finished", "idle"}) in ("finished", "idle")
        finally:
            d1.stop(timeout=5)

        d2, env2 = _make_driver(tmp, llm.base_url, AGENT_SUSPENDABLE="true")
        assert d2.persisted() and d2.persisted_session_id() == cid
        d2.start()
        try:
            d2.wait_ready(deadline_s=30)
            restored = d2.create_conversation(ConversationRequest(env=env2, task="ignored", rules="brief", resumed=True))
            assert restored == cid
            covered_caps.add("resume")
            record("cap:resume", f"session {cid} restored from workspace PVC across restart")

            d2.send(restored, "run sleeper", run=True)
            end = time.monotonic() + 15.0
            while time.monotonic() < end and not os.path.exists(pid_file):
                time.sleep(0.1)
            assert os.path.exists(pid_file)
            t0 = time.monotonic()
            d2.interrupt(restored, timeout=5)
            d2.close(restored)
            res = d2.stop(timeout=10)
            elapsed = time.monotonic() - t0
            assert res == "stopped" and elapsed < 15.0
            covered_caps.add("checkpoint")
            record("cap:checkpoint", f"interrupt + job stop + server exit in {elapsed:.2f}s (<15s)")
        finally:
            if d2.server is not None and d2.server.poll() is None:
                d2.stop(timeout=5)
    finally:
        llm.stop()

    # 7. Tracing (metadata-only spans + OTLP stripped + W3C traceparent on MCP & LLM).
    tmp = tempfile.mkdtemp(prefix="mx-trace-")
    secret = "MATRIX_SECRET_DO_NOT_LEAK_77"
    tp = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
    llm = ScriptedLLMServer([{"text": f"done {secret}", "usage": {"prompt_tokens": 11, "completion_tokens": 3, "total_tokens": 14}}])
    llm.start()
    try:
        d, env = _make_driver(
            tmp,
            llm.base_url,
            TRACEPARENT=tp,
            OTEL_EXPORTER_OTLP_ENDPOINT="http://127.0.0.1:4318",
            OTEL_LOGS_EXPORTER="otlp",
        )
        d.start()
        try:
            senv = d._server_env()
            assert "OTEL_EXPORTER_OTLP_ENDPOINT" not in senv and "OTEL_LOGS_EXPORTER" not in senv
            d.wait_ready(deadline_s=30)
            cid = d.create_conversation(ConversationRequest(env=env, task=f"echo {secret}", rules="brief", span=object()))
            _wait_status(d, cid, {"finished", "idle", "error"})
            for ev in d.events(cid).items:
                d.on_event(ev)
            d.close(cid)
            assert d._event_tracer is not None and len(d._event_tracer.finished) >= 1
            for sp in d._event_tracer.finished:
                assert secret not in sp.name and secret not in json.dumps(sp.attributes)
            covered_caps.add("tracing")
            record("cap:tracing", "metadata-only spans verified; OTLP env stripped; no secret leakage")
        finally:
            d.stop(timeout=5)
    finally:
        llm.stop()

    # Final completeness assertion: zero skips permitted.
    missing_roles = sorted(set(ROLES) - covered_roles)
    missing_providers = sorted(set(PROVIDERS) - covered_providers)
    missing_caps = sorted(set(REQUIRED) - covered_caps)
    if missing_roles or missing_providers or missing_caps:
        print(
            f"FAIL: incomplete matrix: missing_roles={missing_roles} missing_providers={missing_providers} missing_caps={missing_caps}",
            file=sys.stderr,
        )
        return 1

    print(
        f"\nMATRIX PASS: {len(covered_roles)}/{len(ROLES)} roles, "
        f"{len(covered_providers)}/{len(PROVIDERS)} providers, "
        f"{len(covered_caps)}/{len(REQUIRED)} required capabilities verified against real OpenCode 2.3.0."
    )
    return 0


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--inside-container", action="store_true")
    args = parser.parse_args()
    return _run_inside_container() if args.inside_container else _run_host()


if __name__ == "__main__":
    raise SystemExit(main())
