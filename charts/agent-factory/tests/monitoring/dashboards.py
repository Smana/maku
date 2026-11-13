# SPDX-License-Identifier: Apache-2.0
"""The dashboards' queries, for promtool (v4 step 6, Task 8). Run by dashboards.sh.

usage: dashboards.py RENDERED_DASHBOARDS_DIR OUT_DIR

Writes OUT_DIR/exprs.yaml (every panel query, Grafana's variables substituted, as recording rules
for `promtool check rules`) and OUT_DIR/queries.test.yaml (the behaviour cloud-native-ref's
dashboard tests proved, ported to Maku's names, for `promtool test rules`). Exits 1 naming every
dashboard, panel and variable it refuses.
"""
import json
import os
import re
import sys

import yaml

src, out = sys.argv[1], sys.argv[2]
errors = []
boards = {f[:-5]: json.load(open(os.path.join(src, f))) for f in sorted(os.listdir(src)) if f.endswith(".json")}
WANT = {"agents-overview", "agent-factory", "agent-room", "agent-run", "agent-identity", "agent-fleet", "agent-gateway"}
if set(boards) != WANT:
    sys.exit(f"FAIL: rendered dashboards {sorted(boards)}, want {sorted(WANT)}")


def flat(ps):
    for p in ps:
        yield p
        yield from flat(p.get("panels", []))


# 1. Every query parses as PromQL once Grafana's variables are replaced.
INTERVALS = re.compile(r"\$__(rate_interval|interval|range)\b")
rules = []
for uid, b in boards.items():
    names = [v["name"] for v in b.get("templating", {}).get("list", [])]
    for p in flat(b.get("panels", [])):
        for t in p.get("targets", []):
            e = t.get("expr")
            if e is None:
                continue
            where = f"{uid}: {p.get('title')} {t.get('refId')}"
            if "${datasource}" in e or "$datasource" in e:
                errors.append(f"{where}: ${{datasource}} belongs in a datasource field, never in an expr")
            e = INTERVALS.sub("5m", e)
            for n in sorted(names, key=len, reverse=True):
                e = e.replace("${%s}" % n, ".*").replace("${%s:regex}" % n, ".*")
                e = re.sub(r"\$%s\b" % re.escape(n), ".*", e)
            left = re.findall(r"\$(?![0-9])[{\w]+", e)
            if left:
                errors.append(f"{where}: a variable is left after substitution: {sorted(set(left))}")
                continue
            rules.append({"record": "dashboard:%s:%d" % (re.sub(r"[^a-z0-9]", "_", uid), len(rules)), "expr": e})
yaml.safe_dump({"groups": [{"name": "dashboards", "rules": rules}]}, open(os.path.join(out, "exprs.yaml"), "w"),
               sort_keys=False, width=100000)


# 2. Behaviour: the queries cloud-native-ref's dashboard tests evaluated, on Maku's names.
def expr(uid, title, ref="A"):
    p = next(p for p in flat(boards[uid]["panels"]) if p.get("title") == title and p.get("targets"))
    return next(t["expr"] for t in p["targets"] if t["refId"] == ref)


def prep(e, rng="1h", **variables):
    e = e.replace("$__range", rng).replace("$__rate_interval", "5m")
    for k, v in variables.items():
        e = e.replace("${%s}" % k, v)
    if re.search(r"\$(?![0-9])", e):
        sys.exit(f"FAIL: a variable this test does not set: {e}")
    return e


SUBJECT = "system:serviceaccount:agents:run-"
GW = 'job="model-gateway", namespace="maku"'


def phase(run, ph, pod, values):
    return {"series": f'agentrun_status_phase{{namespace="agents", run_id="{run}", phase="{ph}", pod="{pod}"}}', "values": values}


def info(run, task, role, branch=""):
    b = f', branch="{branch}"' if branch else ""
    return {"series": f'agentrun_info{{namespace="agents", run_id="{run}", task="{task}", role="{role}"{b}}}', "values": "1x30"}


# A run's gateway series is born at its first call, already holding it: no leading 0 (the #529 rule).
def tok(run, model, kind, values):
    return {"series": f'agentgateway_gen_ai_client_token_usage_sum{{{GW}, ar_agent="{SUBJECT}{run}", gen_ai_request_model="{model}", gen_ai_token_type="{kind}"}}', "values": values}


def cost(run, model, values):
    return {"series": f'agentgateway_gen_ai_client_cost_usd_total{{{GW}, ar_agent="{SUBJECT}{run}", gen_ai_request_model="{model}"}}', "values": values}


def charge(run, principal, room=""):
    return {"series": f'maku_factory_run_charge_info{{run="{run}", principal="{principal}", room="{room}", task="{room}"}}', "values": "1x30"}


OLD, NEW1, NEW2 = "ksm-old", "ksm-new1", "ksm-new2"
series = [
    # aaaaaaaa: stale Pending on the old pod, Succeeded on both new pods (an HA pair): one Succeeded
    phase("aaaaaaaa", "Pending", OLD, "1x10"), phase("aaaaaaaa", "Succeeded", NEW1, "_x12 1x17"), phase("aaaaaaaa", "Succeeded", NEW2, "_x12 1x17"),
    # cccccccc: Running on the new pod while the old pod's Pending is stale: one Running
    phase("cccccccc", "Pending", OLD, "1x10"), phase("cccccccc", "Running", NEW1, "_x12 1x17"), phase("cccccccc", "Succeeded", NEW1, "_x12 0x17"),
    phase("dddddddd", "Running", NEW1, "1x30"),
    info("aaaaaaaa", "room1", "impl"), info("cccccccc", "room1", "review"), info("dddddddd", "room2", "impl"),
    info("eeeeeeee", "", "impl", branch="agent/room3"),
    # aaaaaaaa: 1M input and 100k output, priced 3 USD by the gateway; dddddddd 0.5M input, 1 USD
    tok("aaaaaaaa", "m1", "input", "1000000x30"), tok("aaaaaaaa", "m1", "output", "100000x30"), cost("aaaaaaaa", "m1", "3x30"),
    tok("dddddddd", "m1", "input", "500000x30"), cost("dddddddd", "m1", "1x30"),
    # ffffffff (room4, alice's) called m1 and m2: 2 USD and 4 USD
    info("ffffffff", "room4", "impl"),
    charge("aaaaaaaa", "github:alice", "room1"), charge("dddddddd", "github:bob", "room2"), charge("ffffffff", "github:alice", "room4"),
    tok("ffffffff", "m1", "input", "1000000x30"), tok("ffffffff", "m2", "input", "500000x30"), tok("ffffffff", "m2", "output", "100000x30"),
    cost("ffffffff", "m1", "2x30"), cost("ffffffff", "m2", "4x30"),
    {"series": 'maku_factory_pr_outcomes_total{outcome="auto_merged", class="deps"}', "values": "0 4x30"},
    {"series": 'maku_factory_pr_outcomes_total{outcome="human_merged", class="review"}', "values": "0 2x30"},
    {"series": 'maku_factory_pr_outcomes_total{outcome="approved_merged", class="review"}', "values": "0 1x30"},
    {"series": 'maku_factory_prs_opened_total{principal="github:alice"}', "values": "0 7x30"},
    # the breaker: two paused named classes of two repositories, one at 0, and an unnamed class it ignores
    {"series": 'maku_factory_class_paused{repository="o/a", class="docs-links"}', "values": "1x30"},
    {"series": 'maku_factory_class_paused{repository="o/a", class="revert"}', "values": "0x30"},
    {"series": 'maku_factory_class_paused{repository="o/b", class="docs-links"}', "values": "1x30"},
    {"series": 'maku_factory_class_paused{repository="o/a", class=""}', "values": "1x30"},
]


def rows(e, want, t="29m"):
    return {"expr": e, "eval_time": t, "exp_samples": want}


def ph(*pairs):
    return [{"labels": '{phase="%s"}' % p, "value": v} for p, v in pairs]


def by_model(*pairs):
    return [{"labels": '{gen_ai_request_model="%s"}' % m, "value": v} for m, v in pairs]


def run_model(*rs):
    return [{"labels": '{gen_ai_request_model="%s", run_id="%s"}' % (m, r), "value": v} for r, m, v in rs]


room1 = dict(room="room1")
alice, bob = dict(principal="github:alice"), dict(principal="github:bob")
tests = [{"interval": "1m", "input_series": series, "promql_expr_test": [
    rows(prep(expr("agent-fleet", "Runs by phase")), ph(("Succeeded", 1), ("Running", 2))),
    rows(prep(expr("agents-overview", "Runs in flight")), [{"value": 2}]),
    rows(prep(expr("agent-room", "Runs", "A"), **room1), [{"value": 2}]),
    rows(prep(expr("agent-room", "Runs of this room", "B"), **room1),
         [{"labels": '{phase="Succeeded", run_id="aaaaaaaa"}', "value": 1}, {"labels": '{phase="Running", run_id="cccccccc"}', "value": 1}]),
    rows(prep(expr("agent-room", "Runs by phase", "A"), **room1), ph(("Succeeded", 1), ("Running", 1))),
    rows(prep(expr("agent-room", "Runs", "A"), room="room3"), [{"value": 1}]),
    # spend reads each run's priced counter from its first sample: 3 USD, and room2's 1 USD
    rows(prep(expr("agent-room", "Spend", "A"), **room1), [{"value": 3}]),
    rows(prep(expr("agent-room", "Spend", "A"), room="room2"), [{"value": 1}]),
    rows(prep(expr("agent-room", "Tokens", "A"), **room1), [{"value": 1100000}]),
    rows(prep(expr("agent-room", "Runs of this room", "E"), **room1), [{"labels": '{run_id="aaaaaaaa"}', "value": 3}]),
    rows(prep(expr("agent-identity", "Spend by model"), **alice), by_model(("m1", 5), ("m2", 4))),
    rows(prep(expr("agent-identity", "Spend by model"), **bob), by_model(("m1", 1))),
    rows(prep(expr("agent-identity", "Tokens by model"), **alice), by_model(("m1", 2100000), ("m2", 600000))),
    rows(prep(expr("agent-identity", "Tokens by model"), **bob), by_model(("m1", 500000))),
    rows(prep(expr("agent-identity", "Runs", "G"), **alice), run_model(("aaaaaaaa", "m1", 1100000), ("ffffffff", "m1", 1000000), ("ffffffff", "m2", 600000))),
    rows(prep(expr("agent-run", "Models"), run="ffffffff"), by_model(("m1", 1000000), ("m2", 600000))),
    rows(prep(expr("agent-run", "Cost by model"), run="ffffffff"), by_model(("m1", 2), ("m2", 4))),
    rows(prep(expr("agent-run", "Cost (USD)"), run="ffffffff"), [{"value": 6}]),
    rows(prep(expr("agent-room", "Runs of this room", "H"), room="room4"), run_model(("ffffffff", "m1", 1000000), ("ffffffff", "m2", 600000))),
    rows(prep(expr("agent-fleet", "Spend by model and principal")),
         [{"labels": '{gen_ai_request_model="m1", principal="github:alice"}', "value": 5}, {"labels": '{gen_ai_request_model="m2", principal="github:alice"}', "value": 4},
          {"labels": '{gen_ai_request_model="m1", principal="github:bob"}', "value": 1}]),
    rows(prep(expr("agents-overview", "PRs merged")), [{"value": 7}]),
    # a run's tokens per interval count the call its series was born with: 1.1M in the first window
    rows(prep(expr("agent-run", "Tokens in / out"), run="aaaaaaaa"),
         [{"labels": '{gen_ai_token_type="input"}', "value": 1000000}, {"labels": '{gen_ai_token_type="output"}', "value": 100000}], "2m"),
    rows(prep(expr("agents-overview", "PRs reverted")), []),
    rows(prep(expr("agents-overview", "PRs opened")), [{"value": 7}]),
    rows(prep(expr("agent-factory", "Reverted after auto-merge (SC-11: ≤ 5 %)")), [{"value": 0}]),
    # two named classes paused, in two repositories; the unnamed class is not counted
    rows(prep(expr("agent-factory", "Classes paused")), [{"value": 2}]),
]}, {"interval": "1m", "input_series": [{"series": 'maku_factory_pr_outcomes_total{outcome="human_merged"}', "values": "0 1x30"}], "promql_expr_test": [
    rows(prep(expr("agent-factory", "Reverted after auto-merge (SC-11: ≤ 5 %)")), [{"value": 0}]),
]}]

# The identity page (cloud-native-ref's test-agent-identity-queries.sh), two principals at once.
A, B, F = "github:alice", "github:bob", "system:factory"
X = 'namespace="maku", pod="agent-factory-0"'


def q(title, ref, principal):
    return prep(expr("agent-identity", title, ref), principal=principal)


def icharge(principal, run, task, room):
    labels = f'{X}, run="{run}", principal="{principal}"' + (f', task="{task}"' if task else "") + (f', room="{room}"' if room else "")
    return "maku_factory_run_charge_info{" + labels + "}"


def itok(run, kind):
    return f'agentgateway_gen_ai_client_token_usage_sum{{{GW}, ar_agent="{SUBJECT}{run}", gen_ai_request_model="glm-5.3", gen_ai_token_type="{kind}"}}'


tests += [
    {"interval": "1m", "input_series": [
        {"series": f'maku_factory_charged_usd_total{{{X}, principal="{A}", kind="task"}}', "values": "0 2x50"},
        {"series": f'maku_factory_charged_usd_total{{{X}, principal="{A}", kind="room"}}', "values": "0 0.5x50"},
        {"series": f'maku_factory_charged_usd_total{{{X}, principal="{B}", kind="room"}}', "values": "0 1x50"},
        {"series": f'maku_factory_budget_spent_usd{{{X}, principal="{A}"}}', "values": "0 2.5x50"},
        {"series": f'maku_factory_budget_spent_usd{{{X}, principal="{B}"}}', "values": "0 1x50"},
        {"series": f'maku_factory_budget_spent_usd{{{X}, principal="{F}"}}', "values": "0 3.5x50"},
    ], "promql_expr_test": [
        rows(q("Spend in the range", "A", A), [{"value": 2.5}], "50m"),
        rows(f'{q("Spend in the range", "A", A)} + {q("Spend in the range", "A", B)}', [{"value": 3.5}], "50m"),
        rows(q("The fleet's day", "A", A), [{"value": 3.5}], "50m"),
        rows(q("The fleet's day", "C", A), [{"value": 2.5 / 3.5}], "50m"),
    ]},
    # Spend per day: a 1d step at UTC midnight reads the day that begins there (offset -1d); promtool
    # cannot read ahead, so the test evaluates the unshifted query a day later.
    {"interval": "1h", "input_series": [
        {"series": f'maku_factory_charged_usd_total{{{X}, principal="{A}", kind="task"}}', "values": "_ 0+0.5x30"},
    ], "promql_expr_test": [
        rows(q("Spend per day", "A", A).replace(" offset -1d", ""), [{"labels": '{kind="task"}', "value": 11.5}], "24h"),
        rows(q("Spend per day", "A", B).replace(" offset -1d", ""), [], "24h"),
        rows(q("Spend per day", "A", A).replace(" offset -1d", ""), [], "0m"),
    ]},
    # Tokens: alice's run reads 1050 input with 900 cache reads; a fleet run's tokens are not hers.
    {"interval": "1m", "input_series": [
        {"series": itok("aaaaaaaa", "input"), "values": "1050x50"},
        {"series": itok("aaaaaaaa", "input_cache_read"), "values": "900x50"},
        {"series": itok("aaaaaaaa", "output"), "values": "10x50"},
        {"series": itok("cccccccc", "input"), "values": "5000x50"},
        {"series": itok("cccccccc", "input_cache_read"), "values": "100x50"},
        {"series": icharge(A, "aaaaaaaa", "3buqdlot", "3buqdlot"), "values": "1x50"},
        {"series": icharge(F, "cccccccc", "4buqdlot", "4buqdlot"), "values": "1x50"},
    ], "promql_expr_test": [
        rows(q("Cache-read share", "A", A), [{"value": 900 / 1050}], "50m"),
        rows(q("Cache-read share", "A", B), [], "50m"),
        rows(q("Tokens by type", "A", A), [{"labels": '{gen_ai_token_type="input"}', "value": 1050},
                                           {"labels": '{gen_ai_token_type="input_cache_read"}', "value": 900},
                                           {"labels": '{gen_ai_token_type="output"}', "value": 10}], "50m"),
    ]},
    {"interval": "1m", "input_series": [
        {"series": icharge(A, "aaaaaaaa", "3buqdlot", "3buqdlot"), "values": "1x20"},
        {"series": icharge(A, "bbbbbbbb", "3buqdlot", "3buqdlot"), "values": "_x20 1x20"},
        {"series": icharge(A, "cccccccc", "3buqdlot", "4buqdlot"), "values": "_x40 1x10"},
        {"series": icharge(B, "dddddddd", "", "5buqdlot"), "values": "1x50"},
    ], "promql_expr_test": [
        rows(q("Runs · rooms · tasks", "A", A), [{"value": 3}], "50m"),
        rows(q("Runs · rooms · tasks", "B", A), [{"value": 2}], "50m"),
        rows(q("Runs · rooms · tasks", "C", A), [{"value": 1}], "50m"),
        rows(q("Runs · rooms · tasks", "A", B), [{"value": 1}], "50m"),
        rows(q("Runs · rooms · tasks", "C", B), [], "50m"),
    ]},
    {"interval": "1m", "input_series": [
        {"series": f'maku_factory_charged_usd_total{{{X}, principal="{A}", kind="task"}}', "values": "0 3x50"},
        {"series": f'maku_factory_charged_usd_total{{{X}, principal="{A}", kind="room"}}', "values": "0 7x50"},
        {"series": f'maku_factory_charged_usd_total{{{X}, principal="{B}", kind="task"}}', "values": "0 4x50"},
    ] + [
        {"series": f'maku_factory_pr_outcomes_total{{{X}, principal="{who}", class="review", outcome="{o}", repository="o/r"}}', "values": "0 1x50"}
        for who, o in ((A, "auto_merged"), (A, "human_merged"), (A, "reverted"), (B, "human_merged"))
    ] + [
        {"series": f'maku_factory_pr_outcomes_total{{{X}, principal="{A}", class="docs", outcome="auto_merged", repository="o/r"}}', "values": "0 1x50"},
    ], "promql_expr_test": [
        rows(q("Cost per accepted PR", "A", A), [], "50m"),
        rows(q("Cost per accepted PR", "B", A), [{"value": 2}], "50m"),
        rows(q("Cost per accepted PR", "A", B), [], "50m"),
        rows(q("Cost per accepted PR", "B", B), [{"value": 1}], "50m"),
    ]},
    {"interval": "1m", "input_series": [
        {"series": f'maku_factory_charged_usd_total{{{X}, principal="{A}", kind="task"}}', "values": "0 3x50"},
    ] + [
        {"series": f'maku_factory_pr_outcomes_total{{{X}, principal="{A}", class="review", outcome="{o}", repository="o/r"}}', "values": "0 " + str(n) + "x50"}
        for o, n in (("auto_merged", 2), ("human_merged", 1), ("approved_merged", 1), ("reverted", 1))
    ], "promql_expr_test": [
        rows(q("Cost per accepted PR", "A", A), [{"value": 1}], "50m"),
        rows(q("Cost per accepted PR", "B", A), [{"value": 3}], "50m"),
    ]},
]
open(os.path.join(out, "rules.yaml"), "w").write("groups: []\n")
yaml.safe_dump({"rule_files": ["rules.yaml"], "evaluation_interval": "1m", "tests": tests},
               open(os.path.join(out, "queries.test.yaml"), "w"), sort_keys=False, width=100000)

# 3. The identity page (test-agent-identity-dashboard.py): one principal at a time, never ranked;
# counters read with increase(), a run's gateway series with max_over_time (born at its first call).
b = boards["agent-identity"]
variables = {v["name"]: v for v in b.get("templating", {}).get("list", [])}
var = variables.get("principal", {})
if not (b.get("uid") == "agent-identity" and b.get("timezone") == "utc"):
    errors.append("agent-identity: uid agent-identity, in UTC (the ledger's day)")
if var.get("query") != "label_values(maku_factory_charged_usd_total, principal)" or var.get("multi") is not False or var.get("includeAll") is not False:
    errors.append("agent-identity: principal comes from the charged counter, single-valued, without All")
if set(variables) != {"datasource", "principal"}:
    errors.append(f"agent-identity: no other variable: {sorted(variables)}")
RANKS = re.compile(r"\b(by|without)\s*\([^)]*\bprincipal\b|\b(topk|bottomk|sort|sort_desc|limitk)\s*\(")
COUNTERS = ("maku_factory_charged_usd_total", "maku_factory_prs_opened_total", "maku_factory_pr_outcomes_total",
            "maku_factory_admission_refusals_total", "maku_factory_run_revocations_total",
            "maku_factory_human_wait_seconds_total", "maku_factory_max_cost_usd_total")
PER_RUN = ("agentgateway_gen_ai_client_token_usage_sum", "agentgateway_gen_ai_client_cost_usd_total")
for p in flat(b["panels"]):
    if p.get("type") in ("row", "text"):
        continue
    title = p["title"]
    if (p.get("datasource") or {}).get("type") != "prometheus":
        errors.append(f"agent-identity {title}: Prometheus only")
    for t in p.get("targets", []):
        e = t.get("expr", "")
        where = f"agent-identity {title} {t['refId']}"
        if RANKS.search(e):
            errors.append(f"{where}: groups, sorts or ranks by principal")
        if 'principal="${principal}"' not in e and not (title == "The fleet's day" and 'principal="system:factory"' in e):
            errors.append(f"{where}: filters on the page's principal")
        if re.search(r"\b(rate|irate)\(", e):
            errors.append(f"{where}: a range total, never a rate")
        for s in COUNTERS:
            for m in re.finditer(re.escape(s), e):
                if not e[:m.start()].rstrip().endswith("increase("):
                    errors.append(f"{where}: {s} is read through increase()")
        for s in PER_RUN:
            for m in re.finditer(re.escape(s), e):
                if not e[:m.start()].rstrip().endswith("max_over_time("):
                    errors.append(f"{where}: {s} is read through max_over_time(), from its first sample")
links = json.dumps(next((p for p in flat(b["panels"]) if p.get("title") == "Runs"), {}).get("fieldConfig", {}))
if "/d/agent-run/agent-run?var-run=${__value.text}" not in links:
    errors.append("agent-identity Runs: a run_id opens its Agent run page")
fleet_runs = json.dumps(next((p for p in flat(boards["agent-fleet"]["panels"]) if p.get("title") == "Runs"), {}).get("fieldConfig", {}))
if "/d/agent-identity/agent-identity?var-principal=${__value.text}" not in fleet_runs:
    errors.append("agent-fleet Runs: a payer opens its Spend per identity page")

# No dashboard reads a run's gateway series with increase(): it would drop the first call.
for uid, bd in boards.items():
    for p in flat(bd.get("panels", [])):
        for t in p.get("targets", []):
            for s in PER_RUN:
                e = t.get("expr", "")
                if re.search(r"increase\(\s*" + s + r"\{[^}]*ar_agent", e) and not re.search(r"unless\s+" + s + r"\{(?:[^{}]|\$\{[^}]*\})*\}\s+offset", e):
                    errors.append(f"{uid} {p.get('title')} {t['refId']}: {s} per run read with increase(), which drops its first sample")

if errors:
    print("\n".join("FAIL: " + e for e in errors), file=sys.stderr)
    sys.exit(1)
print(f"{len(rules)} dashboard queries written for promtool")
