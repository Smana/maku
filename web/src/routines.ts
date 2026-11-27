// SPDX-License-Identifier: Apache-2.0

import { api, SignInRequired } from "./api";
import { mountRoutineDetail } from "./routine-detail";
import { settingsFrame } from "./settings";
import { explain } from "./settings-api";
import type { ThemeController } from "./theme";

export interface RoutineSparklineDay {
  date: string;
  merged: number;
  closed: number;
  noOp: number;
  escalated: number;
}

export interface RoutineOverview {
  repository: string;
  routine: string;
  description?: string;
  mode: string;
  workflow?: string;
  probeKind?: string;
  cron?: string;
  paused: boolean;
  declarativePaused?: boolean;
  pausedReason?: string;
  pausedBy?: string;
  pausedAt?: string;
  maxOpenPRs: number;
  maxCostUSD: number;
  openCount: number;
  nextRunAt: string | null;
  sparkline: RoutineSparklineDay[];
  rollingMerged: number;
  rollingClosed: number;
  rollingNoOp: number;
  mergeRate: number | null;
  usdPerMerged: number | null;
  promptEditURL: string;
  promptSHA?: string;
  skill?: string;
  breakerState: string;
  canWrite: boolean;
}

export interface RoutineTaskRun {
  name: string;
  createdAt: string;
  endedAt?: string;
  phase: string;
  reason?: string;
  roomRef?: string;
  prURL?: string;
  prNumber?: number;
  promptSHA?: string;
  promptSource?: string;
  requestedBy?: string;
  costUSD: number;
  outcome?: string;
}

export interface SkipRecord {
  slotKey?: string;
  at: string;
  reason: string;
}

export interface RoutineDetail extends RoutineOverview {
  defaultPrompt?: string;
  runs: RoutineTaskRun[];
  skips: SkipRecord[];
}

export interface RoutineRunCreated {
  task: string;
  room: string;
  repository: string;
  routine: string;
  mode: string;
  requestedBy: string;
}

export type SparklineOutcome = "escalated" | "closed" | "merged" | "no-op" | "empty";

export function isRoutines(path: string): boolean {
  return /^\/routines(?:\/[^/]+\/[^/]+\/[^/]+)?$/.test(path);
}

export function el<K extends keyof HTMLElementTagNameMap>(tag: K, text?: string, cls?: string): HTMLElementTagNameMap[K] {
  const node = document.createElement(tag);
  if (text !== undefined) node.textContent = text;
  if (cls) node.className = cls;
  return node;
}

export function link(href: string, text: string, cls?: string): HTMLAnchorElement {
  const a = el("a", text, cls);
  a.href = href;
  return a;
}

export function safeHTTPSURL(raw?: string): string | undefined {
  if (!raw) return undefined;
  try {
    const u = new URL(raw);
    return u.protocol === "https:" ? u.toString() : undefined;
  } catch {
    return undefined;
  }
}

export function externalLink(rawURL: string | undefined, text: string, cls?: string): HTMLAnchorElement | undefined {
  const safe = safeHTTPSURL(rawURL);
  if (!safe) return undefined;
  const a = link(safe, text, cls);
  a.target = "_blank";
  a.rel = "noopener noreferrer";
  return a;
}

async function refusalCode(r: Response): Promise<string> {
  try {
    const body = (await r.json()) as { error?: unknown };
    if (typeof body.error === "string" && /^[a-z][a-z0-9_]{0,47}$/.test(body.error)) return body.error;
  } catch { /* non-JSON refusal */ }
  return `HTTP ${r.status}`;
}

const routineReasons: Record<string, string> = {
  mode_requires_phase2: "Mode requires Phase 2 (propose and draft are not enabled yet).",
  mode_widening: "Requested mode cannot widen the routine's configured mode.",
  invalid_mode: "Invalid run mode.",
  bad_idempotency_key: "Invalid idempotency key.",
  idempotency_conflict: "A different manual run already used that idempotency key.",
  kill_switch: "Factory kill switch is active.",
  repository_not_owned: "This factory does not own the repository.",
  routine_disabled: "This routine is disabled.",
  open_cap: "Open PR limit reached for this routine.",
  budget_exhausted: "Daily upkeep budget is exhausted.",
  probe_empty: "Probe found no actionable work.",
  probe_refused: "Probe findings were refused by strict validation.",
  not_a_maintainer: "Only a maintainer of this repository (with write access) may do this.",
  repository_permission: "GitHub write permission on this repository is required.",
  private_code_unacknowledged: "Private repository code must be acknowledged on the Repository before manual runs.",
  not_linked: "Link your GitHub account on the rooms page first.",
  bad_reason: "Pause reason must be 200 characters or fewer with no control characters.",
  routines_unavailable: "Routine controls are unavailable right now. Retry.",
  routine_state_unavailable: "Routine state store is unavailable right now. Retry.",
  tasks_unavailable: "Task history could not be read right now. Retry.",
  access_unverified: "GitHub access could not be verified right now. Retry.",
  link_unverified: "Your GitHub link could not be verified right now. Retry.",
  cross_origin: "Request origin was refused.",
  bad_repository: "Invalid repository name.",
  bad_routine: "Invalid routine name.",
  not_found: "Routine not found, or not readable with your GitHub access.",
};

export function explainRoutineError(code: string): string {
  return routineReasons[code] ?? explain(code);
}

export function newIdempotencyKey(): string {
  if (typeof globalThis.crypto?.randomUUID === "function") {
    return globalThis.crypto.randomUUID();
  }
  return `${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 10)}`;
}

export async function fetchRoutines(repo?: string, get: typeof fetch = fetch): Promise<RoutineOverview[]> {
  const path = repo ? `/api/routines?repo=${encodeURIComponent(repo)}` : "/api/routines";
  const r = await api(path, { credentials: "same-origin" }, get);
  if (!r.ok) throw new Error(await refusalCode(r));
  const body = (await r.json()) as { routines?: RoutineOverview[] };
  return body.routines ?? [];
}

export async function fetchRoutineDetail(owner: string, name: string, routine: string, get: typeof fetch = fetch): Promise<RoutineDetail> {
  const path = `/api/routines/${encodeURIComponent(owner)}/${encodeURIComponent(name)}/${encodeURIComponent(routine)}`;
  const r = await api(path, { credentials: "same-origin" }, get);
  if (!r.ok) throw new Error(await refusalCode(r));
  return (await r.json()) as RoutineDetail;
}

export async function triggerRoutineRun(
  owner: string,
  name: string,
  routine: string,
  mode: string,
  get: typeof fetch = fetch,
  idempotencyKey = newIdempotencyKey(),
): Promise<RoutineRunCreated> {
  const path = `/api/routines/${encodeURIComponent(owner)}/${encodeURIComponent(name)}/${encodeURIComponent(routine)}/runs`;
  const payload = mode ? { idempotencyKey, mode } : { idempotencyKey };
  const r = await api(
    path,
    {
      method: "POST",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(payload),
    },
    get,
  );
  if (!r.ok) throw new Error(await refusalCode(r));
  return (await r.json()) as RoutineRunCreated;
}

export async function pauseRoutine(
  owner: string,
  name: string,
  routine: string,
  reason = "",
  get: typeof fetch = fetch,
): Promise<void> {
  const path = `/api/routines/${encodeURIComponent(owner)}/${encodeURIComponent(name)}/${encodeURIComponent(routine)}/pause`;
  const body = JSON.stringify(reason ? { reason } : {});
  const r = await api(
    path,
    {
      method: "POST",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json" },
      body,
    },
    get,
  );
  if (!r.ok) throw new Error(await refusalCode(r));
}

export async function resumeRoutine(
  owner: string,
  name: string,
  routine: string,
  get: typeof fetch = fetch,
): Promise<void> {
  const path = `/api/routines/${encodeURIComponent(owner)}/${encodeURIComponent(name)}/${encodeURIComponent(routine)}/resume`;
  const r = await api(
    path,
    {
      method: "POST",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json" },
      body: "{}",
    },
    get,
  );
  if (!r.ok) throw new Error(await refusalCode(r));
}

export function sparklineOutcome(day: RoutineSparklineDay): SparklineOutcome {
  if (day.escalated > 0) return "escalated";
  if (day.closed > 0) return "closed";
  if (day.merged > 0) return "merged";
  if (day.noOp > 0) return "no-op";
  return "empty";
}

export function renderSparkline(days: RoutineSparklineDay[]): HTMLElement {
  const list = el("ol", undefined, "routine-sparkline");
  list.setAttribute("aria-label", "14-day outcomes");
  const normalized = days.slice(-14);
  for (const d of normalized) {
    const li = el("li", undefined, "spark-day");
    li.dataset.outcome = sparklineOutcome(d);
    li.title = `${d.date}: ${d.merged} merged, ${d.closed} closed, ${d.noOp} no-op, ${d.escalated} escalated`;
    list.append(li);
  }
  return list;
}

export function routineState(r: Pick<RoutineOverview, "mode" | "paused" | "breakerState">): string {
  if (r.mode === "disabled") return "disabled";
  if (r.paused) return "paused";
  if (r.breakerState === "half_open") return "half_open";
  return "active";
}

export function formatLocalDateTime(iso: string): string {
  const ms = Date.parse(iso);
  if (Number.isNaN(ms)) return iso;
  return new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "short" }).format(new Date(ms));
}

export function timeElement(iso: string | null | undefined): HTMLElement | string {
  if (!iso) return "—";
  const t = el("time", formatLocalDateTime(iso));
  t.dateTime = iso;
  t.title = iso;
  return t;
}

export function formatMergeRate(r: RoutineOverview): string {
  if (r.mergeRate === null || r.mergeRate === undefined) return "—";
  return `${Math.round(r.mergeRate * 100)}% · ${r.rollingMerged}/${r.rollingMerged + r.rollingClosed}`;
}

export function formatUSDPerMerged(r: RoutineOverview): string {
  if (r.usdPerMerged === null || r.usdPerMerged === undefined) return "—";
  return `$${r.usdPerMerged.toFixed(2)}`;
}

export function renderRoutineMetrics(r: RoutineOverview): HTMLElement {
  const dl = el("dl", undefined, "routine-metrics");
  const add = (label: string, value: string | Node) => {
    const row = el("div", undefined, "routine-metric");
    row.append(el("dt", label), el("dd"));
    row.lastElementChild!.append(value);
    dl.append(row);
  };
  add("Merge rate", formatMergeRate(r));
  add("$/merged PR", formatUSDPerMerged(r));
  add("Open PRs", `${r.openCount}/${r.maxOpenPRs}`);
  add("Next run", timeElement(r.nextRunAt));
  return dl;
}

export function renderRoutineBadges(r: RoutineOverview): HTMLElement {
  const wrap = el("div", undefined, "routine-badges");
  const mode = el("span", r.mode, "mode-badge");
  mode.dataset.mode = r.mode;
  const st = routineState(r);
  const state = el("span", st, "routine-state");
  state.dataset.state = st;
  const breakerVal = r.breakerState || "ok";
  const breaker = el("span", `breaker: ${breakerVal}`, "routine-breaker");
  breaker.dataset.breaker = breakerVal;
  wrap.append(mode, state, breaker);
  if (r.paused && r.pausedReason) {
    wrap.append(el("span", `Paused: ${r.pausedReason}`, "routine-paused-reason"));
  }
  return wrap;
}

export function renderRoutineActions(
  r: RoutineOverview,
  get: typeof fetch,
  notice: HTMLElement,
  reload: () => Promise<void>,
): HTMLElement {
  const [owner = "", name = ""] = r.repository.split("/");
  const bar = el("div", undefined, "routine-actions");

  const modeSelect = el("select", undefined, "routine-mode-select");
  modeSelect.setAttribute("aria-label", "Run mode");
  for (const [val, label] of [
    ["", `default (${r.mode})`],
    ["pr", "pr"],
    ["propose", "propose"],
    ["draft", "draft"],
  ] as const) {
    const opt = el("option", label);
    opt.value = val;
    modeSelect.append(opt);
  }
  modeSelect.disabled = !r.canWrite || r.mode === "disabled";

  const runBtn = el("button", "Run now", "primary");
  runBtn.type = "button";
  runBtn.dataset.act = "run-now";
  runBtn.disabled = !r.canWrite || r.mode === "disabled";

  const toggleBtn = el("button", r.paused ? "Resume" : "Pause");
  toggleBtn.type = "button";
  toggleBtn.dataset.act = r.paused ? "resume" : "pause";
  toggleBtn.disabled = !r.canWrite;

  const setBusy = (busy: boolean) => {
    modeSelect.disabled = busy || !r.canWrite || r.mode === "disabled";
    runBtn.disabled = busy || !r.canWrite || r.mode === "disabled";
    toggleBtn.disabled = busy || !r.canWrite;
  };

  runBtn.onclick = async () => {
    setBusy(true);
    notice.replaceChildren();
    notice.dataset.tone = "";
    try {
      const created = await triggerRoutineRun(owner, name, r.routine, modeSelect.value, get);
      await reload();
      notice.dataset.tone = "ok";
      notice.append(`Started ${created.task} (`, link(`/r/${encodeURIComponent(created.room)}`, `room ${created.room}`), ").");
    } catch (e) {
      if (e instanceof SignInRequired) return;
      notice.dataset.tone = "bad";
      notice.textContent = explainRoutineError(e instanceof Error ? e.message : String(e));
      setBusy(false);
    }
  };

  toggleBtn.onclick = async () => {
    setBusy(true);
    notice.replaceChildren();
    notice.dataset.tone = "";
    try {
      if (r.paused) {
        await resumeRoutine(owner, name, r.routine, get);
        await reload();
        notice.dataset.tone = "ok";
        notice.textContent = `Resumed ${r.repository} · ${r.routine}.`;
      } else {
        await pauseRoutine(owner, name, r.routine, "", get);
        await reload();
        notice.dataset.tone = "ok";
        notice.textContent = `Paused ${r.repository} · ${r.routine}.`;
      }
    } catch (e) {
      if (e instanceof SignInRequired) return;
      notice.dataset.tone = "bad";
      notice.textContent = explainRoutineError(e instanceof Error ? e.message : String(e));
      setBusy(false);
    }
  };

  bar.append(modeSelect, runBtn, toggleBtn);
  const edit = externalLink(r.promptEditURL, "Edit prompt on GitHub ↗", "routine-edit-link");
  if (edit) bar.append(edit);
  return bar;
}

function renderOverviewCard(
  r: RoutineOverview,
  get: typeof fetch,
  notice: HTMLElement,
  reload: () => Promise<void>,
): HTMLElement {
  const [owner = "", name = ""] = r.repository.split("/");
  const card = el("article", undefined, "routine-card");
  card.dataset.repository = r.repository;
  card.dataset.state = routineState(r);
  card.dataset.mode = r.mode;

  const head = el("div", undefined, "routine-card-head");
  const titleWrap = el("div", undefined, "routine-title-wrap");
  const detailPath = `/routines/${encodeURIComponent(owner)}/${encodeURIComponent(name)}/${encodeURIComponent(r.routine)}`;
  titleWrap.append(link(detailPath, r.routine, "routine-title"), el("span", r.repository, "routine-repo"));
  head.append(titleWrap, renderRoutineBadges(r));
  card.append(head);

  if (r.description) {
    card.append(el("p", r.description, "routine-desc"));
  }
  const sparkWrap = el("div", undefined, "routine-spark-row");
  sparkWrap.append(el("span", "14d", "routine-spark-label"), renderSparkline(r.sparkline));
  card.append(sparkWrap, renderRoutineMetrics(r), renderRoutineActions(r, get, notice, reload));
  return card;
}

async function mountOverview(app: HTMLElement, get: typeof fetch, theme?: ThemeController): Promise<void> {
  const main = settingsFrame(app, "Routines", "/routines", theme);
  document.title = "Maku routines";
  const notice = el("p", "", "routine-notice");
  notice.setAttribute("role", "status");
  main.append(notice);

  const body = el("div", undefined, "routines-body");
  main.append(body);

  let selectedRepo = "";
  let selectedState = "";
  let selectedMode = "";

  const load = async () => {
    let routines: RoutineOverview[];
    try {
      routines = await fetchRoutines(undefined, get);
    } catch (e) {
      if (e instanceof SignInRequired) {
        body.replaceChildren(el("p", "signing in again…", "notice"));
        return;
      }
      body.replaceChildren(el("p", explainRoutineError(e instanceof Error ? e.message : String(e)), "notice"));
      return;
    }

    if (routines.length === 0) {
      body.replaceChildren(el("p", "No routines are configured on repositories you can read."));
      return;
    }

    const repos = [...new Set(routines.map((r) => r.repository))].sort();
    const modes = [...new Set(["pr", "disabled", ...routines.map((r) => r.mode)])];

    const filters = el("div", undefined, "routines-filters");
    const repoSelect = el("select");
    repoSelect.name = "repository";
    repoSelect.setAttribute("aria-label", "Filter by repository");
    repoSelect.append(el("option", "All repositories"));
    (repoSelect.firstElementChild as HTMLOptionElement).value = "";
    for (const repo of repos) {
      const opt = el("option", repo);
      opt.value = repo;
      repoSelect.append(opt);
    }
    repoSelect.value = repos.includes(selectedRepo) ? selectedRepo : "";

    const stateSelect = el("select");
    stateSelect.name = "state";
    stateSelect.setAttribute("aria-label", "Filter by state");
    for (const [val, label] of [
      ["", "All states"],
      ["active", "active"],
      ["paused", "paused"],
      ["half_open", "half_open"],
      ["disabled", "disabled"],
    ] as const) {
      const opt = el("option", label);
      opt.value = val;
      stateSelect.append(opt);
    }
    stateSelect.value = selectedState;

    const modeSelect = el("select");
    modeSelect.name = "mode";
    modeSelect.setAttribute("aria-label", "Filter by mode");
    const allModeOpt = el("option", "All modes");
    allModeOpt.value = "";
    modeSelect.append(allModeOpt);
    for (const m of modes) {
      const opt = el("option", m);
      opt.value = m;
      modeSelect.append(opt);
    }
    modeSelect.value = modes.includes(selectedMode) ? selectedMode : "";

    filters.append(repoSelect, stateSelect, modeSelect);

    const list = el("section", undefined, "routines-list");
    const cards = routines.map((r) => renderOverviewCard(r, get, notice, load));
    const emptyMsg = el("p", "No routines match the selected filters.", "routines-empty");
    emptyMsg.hidden = true;
    list.append(...cards, emptyMsg);

    const applyFilters = () => {
      selectedRepo = repoSelect.value;
      selectedState = stateSelect.value;
      selectedMode = modeSelect.value;
      let visible = 0;
      for (const c of cards) {
        const show =
          (!selectedRepo || c.dataset.repository === selectedRepo) &&
          (!selectedState || c.dataset.state === selectedState) &&
          (!selectedMode || c.dataset.mode === selectedMode);
        c.hidden = !show;
        if (show) visible++;
      }
      emptyMsg.hidden = visible > 0;
    };

    repoSelect.onchange = applyFilters;
    stateSelect.onchange = applyFilters;
    modeSelect.onchange = applyFilters;
    body.replaceChildren(filters, list);
    applyFilters();
  };

  await load();
}

export async function mountRoutines(
  app: HTMLElement,
  path: string,
  get: typeof fetch = fetch,
  theme?: ThemeController,
): Promise<void> {
  if (path === "/routines") {
    return mountOverview(app, get, theme);
  }
  const m = path.match(/^\/routines\/([^/]+)\/([^/]+)\/([^/]+)$/);
  if (m) {
    let owner: string;
    let name: string;
    let routine: string;
    try {
      owner = decodeURIComponent(m[1]);
      name = decodeURIComponent(m[2]);
      routine = decodeURIComponent(m[3]);
    } catch {
      const main = settingsFrame(app, "Routines", "/routines", theme);
      document.title = "Maku · Routine not found";
      main.append(
        link("/routines", "← All routines", "routine-back"),
        el("p", "Invalid routine URL.", "notice"),
      );
      return;
    }
    return mountRoutineDetail(app, owner, name, routine, get, theme);
  }
}
