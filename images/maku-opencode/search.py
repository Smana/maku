"""Bounded synchronous search child session for OpenCode (design §7.2)."""
from dataclasses import dataclass
import json
import os
import time

DEFAULT_LEDGER = "/workspace/.agent-search.jsonl"
MAX_CALLS = 6
MAX_STEPS = 40
MAX_TOKENS = 300_000
MAX_DEADLINE_S = 300
MAX_RESULT_CHARS = 8_000
STARTED_OUTCOMES = ("complete", "budget", "error", "interrupted")


class BudgetExceeded(Exception):
    """Raised when a search exceeds call, step, token, or wall-time limits."""


@dataclass
class SearchResult:
    """Result of one bounded synchronous search."""

    answer: str
    model: str
    effort: str
    tool_calls: int
    tokens: int
    outcome: str


def _read_ledger_calls(path: str) -> int:
    try:
        with open(path, encoding="utf-8") as f:
            count = 0
            for line in f:
                try:
                    row = json.loads(line)
                except ValueError:
                    continue
                if isinstance(row, dict) and row.get("outcome") in STARTED_OUTCOMES:
                    count += 1
            return count
    except OSError:
        return 0


def _append_ledger(path: str, row: dict) -> None:
    parent = os.path.dirname(path) or "."
    os.makedirs(parent, exist_ok=True)
    with open(path, "a", encoding="utf-8") as f:
        f.write(json.dumps(row, sort_keys=True) + "\n")


def _truncate(text: str, limit: int = MAX_RESULT_CHARS) -> str:
    if len(text) <= limit:
        return text
    return text[: limit - 1] + "…"


def search(
    query: str,
    env: dict,
    driver,
    *,
    max_calls: int = MAX_CALLS,
    max_steps: int = MAX_STEPS,
    max_tokens: int = MAX_TOKENS,
    deadline_s: float = MAX_DEADLINE_S,
    max_chars: int = MAX_RESULT_CHARS,
    clock=time.monotonic,
    sleep=time.sleep,
) -> SearchResult:
    """Run one bounded synchronous read-only OpenCode search child session.

    Creates an independent session (no parentID) on agent `search` using the
    light alias when configured and `low` effort. Interrupts on any limit and
    records a row in SEARCH_LEDGER using Maku's existing ledger schema.
    """
    ledger = env.get("SEARCH_LEDGER") or DEFAULT_LEDGER
    model = (env.get("LIGHT_MODEL") or env.get("MODEL") or "agent-default").strip()
    effort = "low"

    prior_calls = _read_ledger_calls(ledger)
    if prior_calls >= max_calls:
        _append_ledger(ledger, {
            "outcome": "refused",
            "reason": "call_limit",
            "model": model,
            "effort": effort,
            "toolCalls": 0,
            "tokens": {"input": 0, "output": 0},
        })
        raise BudgetExceeded("six search calls limit reached")

    started = clock()
    sid = driver.create_search_session(query=query, model=model, effort=effort, max_steps=max_steps)
    tool_calls = 0
    input_tokens = 0
    output_tokens = 0
    answer = ""
    cursor = ""
    seen: set[str] = set()

    while True:
        if clock() - started > deadline_s:
            driver.interrupt(sid)
            _append_ledger(ledger, {
                "outcome": "budget",
                "reason": "deadline",
                "model": model,
                "effort": effort,
                "toolCalls": tool_calls,
                "tokens": {"input": input_tokens, "output": output_tokens},
            })
            raise BudgetExceeded(f"search exceeded {deadline_s}s deadline")

        page = driver.events(sid, cursor)
        if page.next_cursor:
            cursor = page.next_cursor
        for ev in page.items:
            eid = ev.get("id") or ""
            if eid and eid in seen:
                continue
            if eid:
                seen.add(eid)
            if ev.get("kind") == "ActionEvent":
                tool_calls += 1
            elif ev.get("kind") == "MessageEvent" and ev.get("source") == "agent":
                parts = (ev.get("llm_message") or {}).get("content") or []
                txt = " ".join(p.get("text", "") for p in parts if isinstance(p, dict) and p.get("type") == "text").strip()
                if txt:
                    answer = txt
                usage = ev.get("usage") or {}
                if isinstance(usage, dict):
                    input_tokens += int(usage.get("input") or 0)
                    output_tokens += int(usage.get("output") or 0)

        total_tokens = input_tokens + output_tokens
        if total_tokens > max_tokens:
            driver.interrupt(sid)
            _append_ledger(ledger, {
                "outcome": "budget",
                "reason": "tokens",
                "model": model,
                "effort": effort,
                "toolCalls": tool_calls,
                "tokens": {"input": input_tokens, "output": output_tokens},
            })
            raise BudgetExceeded(f"search exceeded {max_tokens} tokens")
        if tool_calls > max_steps:
            driver.interrupt(sid)
            _append_ledger(ledger, {
                "outcome": "budget",
                "reason": "steps",
                "model": model,
                "effort": effort,
                "toolCalls": tool_calls,
                "tokens": {"input": input_tokens, "output": output_tokens},
            })
            raise BudgetExceeded(f"search exceeded {max_steps} steps")

        st = driver.status(sid)
        if st in ("finished", "idle"):
            out = _truncate(answer, max_chars)
            _append_ledger(ledger, {
                "outcome": "complete",
                "model": model,
                "effort": effort,
                "toolCalls": tool_calls,
                "tokens": {"input": input_tokens, "output": output_tokens},
            })
            return SearchResult(
                answer=out,
                model=model,
                effort=effort,
                tool_calls=tool_calls,
                tokens=total_tokens,
                outcome="complete",
            )
        if st in ("error", "stuck"):
            _append_ledger(ledger, {
                "outcome": "error",
                "model": model,
                "effort": effort,
                "toolCalls": tool_calls,
                "tokens": {"input": input_tokens, "output": output_tokens},
            })
            return SearchResult(
                answer=_truncate(answer, max_chars),
                model=model,
                effort=effort,
                tool_calls=tool_calls,
                tokens=total_tokens,
                outcome="error",
            )
        if st == "paused":
            _append_ledger(ledger, {
                "outcome": "interrupted",
                "model": model,
                "effort": effort,
                "toolCalls": tool_calls,
                "tokens": {"input": input_tokens, "output": output_tokens},
            })
            return SearchResult(
                answer=_truncate(answer, max_chars),
                model=model,
                effort=effort,
                tool_calls=tool_calls,
                tokens=total_tokens,
                outcome="interrupted",
            )
        sleep(0.1)
