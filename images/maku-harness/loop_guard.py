"""Maku-owned stuck-loop detector over normalized harness events (OpenCode design §7.3)."""
import json
import re

REPEATED_ACTION_LIMIT = 4
REPEATED_ERROR_LIMIT = 3
MONOLOGUE_LIMIT = 3
ALTERNATING_LIMIT = 6
CONTEXT_WINDOW_LIMIT = 2

CONTEXT_WINDOW_RE = re.compile(
    r"context[_ -]?(?:window|length)|maximum context|prompt is too long|too many tokens|token limit exceeded",
    re.IGNORECASE,
)


def _canonical(value) -> str:
    return json.dumps(value, sort_keys=True, separators=(",", ":"), default=str)


def _event_text(ev: dict) -> str:
    llm_msg = ev.get("llm_message")
    if isinstance(llm_msg, dict):
        parts = llm_msg.get("content") or []
        return " ".join(p.get("text", "") for p in parts if isinstance(p, dict) and p.get("type") == "text").strip()
    obs = ev.get("observation")
    if isinstance(obs, dict):
        parts = obs.get("content") or []
        return " ".join(p.get("text", "") for p in parts if isinstance(p, dict) and p.get("type") == "text").strip()
    return str(ev.get("output") or ev.get("error") or ev.get("detail") or ev.get("text") or "").strip()


def _after_last_user(events: list[dict]) -> list[dict]:
    last_user = -1
    for i, ev in enumerate(events):
        if not isinstance(ev, dict):
            continue
        if (ev.get("kind") == "MessageEvent" and ev.get("source") == "user") or ev.get("role") == "user":
            last_user = i
    return events[last_user + 1 :] if last_user >= 0 else list(events)


def _extract_cycles(events: list[dict]) -> tuple[list[tuple[str, str, str, bool]], int, int]:
    """Return (action_cycles, trailing_monologues, context_window_errors).

    Each cycle is (tool, canonical_input, output_or_error, is_error).
    Supports both simplified pair dicts ({tool, input, output/error}) and
    normalized ActionEvent/ObservationEvent/AgentErrorEvent/MessageEvent streams.
    """
    cycles: list[tuple[str, str, str, bool]] = []
    pending: dict[str, tuple[str, str]] = {}
    monologues = 0
    ctx_errors = 0

    for ev in events:
        if not isinstance(ev, dict):
            continue
        # Simplified pair format used in direct detector tests.
        if "tool" in ev and ("output" in ev or "error" in ev):
            monologues = 0
            is_err = bool(ev.get("is_error") or ("error" in ev and ev.get("error")))
            out = str(ev.get("error") if is_err else ev.get("output") or "")
            cycles.append((str(ev["tool"]), _canonical(ev.get("input") or {}), out, is_err))
            if is_err and CONTEXT_WINDOW_RE.search(out):
                ctx_errors += 1
            continue

        kind = ev.get("kind") or ""
        if kind == "ActionEvent":
            monologues = 0
            cid = str(ev.get("tool_call_id") or "")
            tool = str(ev.get("tool_name") or "")
            inp = _canonical(ev.get("action") or {})
            if cid:
                pending[cid] = (tool, inp)
            else:
                cycles.append((tool, inp, "", False))
        elif kind == "ObservationEvent":
            monologues = 0
            cid = str(ev.get("tool_call_id") or "")
            tool, inp = pending.pop(cid, (str(ev.get("tool_name") or ""), "{}"))
            obs = ev.get("observation") if isinstance(ev.get("observation"), dict) else {}
            is_err = bool(obs.get("is_error"))
            out = _event_text(ev)
            cycles.append((tool, inp, out, is_err))
            if is_err and CONTEXT_WINDOW_RE.search(out):
                ctx_errors += 1
        elif kind == "AgentErrorEvent":
            monologues = 0
            cid = str(ev.get("tool_call_id") or "")
            tool, inp = pending.pop(cid, (str(ev.get("tool_name") or ""), "{}"))
            err = str(ev.get("error") or "")
            cycles.append((tool, inp, err, True))
            if CONTEXT_WINDOW_RE.search(err):
                ctx_errors += 1
        elif kind == "ConversationErrorEvent":
            text = f"{ev.get('code') or ''} {ev.get('detail') or ''}"
            if CONTEXT_WINDOW_RE.search(text):
                ctx_errors += 1
        elif kind == "MessageEvent" and ev.get("source") == "agent":
            monologues += 1
        elif ev.get("role") == "assistant" and not ev.get("tool"):
            monologues += 1

    return cycles, monologues, ctx_errors


def detect(events: list[dict]) -> str | None:
    """Detect a stuck pattern in events since the last user message.

    Returns one of: 'repeated_action', 'repeated_error', 'monologue',
    'alternating', 'context_window', or None.
    """
    window = _after_last_user(events)
    cycles, monologues, ctx_errors = _extract_cycles(window)

    if ctx_errors >= CONTEXT_WINDOW_LIMIT:
        return "context_window"
    if monologues >= MONOLOGUE_LIMIT:
        return "monologue"
    if len(cycles) >= REPEATED_ERROR_LIMIT:
        tail_err = cycles[-REPEATED_ERROR_LIMIT:]
        if all(c[3] for c in tail_err) and all(c == tail_err[0] for c in tail_err):
            return "repeated_error"
    if len(cycles) >= REPEATED_ACTION_LIMIT:
        tail_act = cycles[-REPEATED_ACTION_LIMIT:]
        if all(c == tail_act[0] for c in tail_act):
            return "repeated_action"
    if len(cycles) >= ALTERNATING_LIMIT:
        tail_alt = cycles[-ALTERNATING_LIMIT:]
        a, b = tail_alt[0], tail_alt[1]
        if a != b and all(tail_alt[i] == (a if i % 2 == 0 else b) for i in range(ALTERNATING_LIMIT)):
            return "alternating"
    return None
