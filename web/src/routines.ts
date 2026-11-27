// SPDX-License-Identifier: Apache-2.0

import { api, SignInRequired } from "./api";
import { mountRoutineCatalog, type CatalogRoutine, type RoutineBreaker } from "./routine-catalog";
import { mountRoutineDetail } from "./routine-detail";
import { mountRoutineEditor } from "./routine-editor";
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
  dataClass?: string;
  probeKind?: string;
  cron?: string;
  paused: boolean;
  declarativePaused?: boolean;
  pausedReason?: string;
  pausedBy?: string;
  pausedAt?: string;
  maxOpenPRs: number;
  maxCostUSD: number;
  cooldown?: string;
  egress?: string[];
  repoPrompt?: boolean;
  uiWrite?: boolean;
  definedIn?: string;
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
  breaker?: RoutineBreaker;
  probeWorkflow?: string;
  probeArtifact?: string;
  probeFile?: string;
  probeAuthor?: string;
  oracleClass?: string;
  maxFindings?: number;
  requireConsecutive?: number;
  promptPath?: string;
  promptExists?: boolean;
  repoPromptText?: string;
  spec?: CatalogRoutine;
  repoEgress?: string[];
  upkeepDailyUSD?: number;
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

const usdFormatter = new Intl.NumberFormat("en-US", {
  style: "currency",
  currency: "USD",
  minimumFractionDigits: 2,
  maximumFractionDigits: 2,
});

export function formatUSD(amount: number): string {
  return usdFormatter.format(amount);
}

export function isRoutines(path: string): boolean {
  return /^\/routines(?:\/catalog|\/[^/]+\/[^/]+\/[^/]+(?:\/edit)?)?$/.test(path);
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

export function externalLink(rawURL: string | undefined, text: string, cls?: string, ariaLabel?: string): HTMLAnchorElement | undefined {
  const safe = safeHTTPSURL(rawURL);
  if (!safe) return undefined;
  const a = el("a", undefined, cls);
  a.href = safe;
  a.target = "_blank";
  a.rel = "noopener noreferrer";
  if (text.endsWith(" ↗")) {
    a.append(text.slice(0, -2), " ", el("span", "↗", "routine-arrow"));
    a.lastElementChild!.setAttribute("aria-hidden", "true");
  } else {
    a.textContent = text;
  }
  if (ariaLabel) a.setAttribute("aria-label", ariaLabel);
  return a;
}

export function renderSkeleton(rows = 3): HTMLElement {
  const wrap = el("div", undefined, "skeleton");
  wrap.setAttribute("aria-busy", "true");
  wrap.setAttribute("aria-label", "Loading…");
  for (let i = 0; i < rows; i++) {
    wrap.append(el("div", undefined, "sk-msg"));
  }
  return wrap;
}

export function renderFetchError(message: string, onRetry: () => void): HTMLElement {
  const box = el("div", undefined, "routine-fetch-error");
  const p = el("p", message, "notice");
  p.setAttribute("role", "alert");
  const retry = el("button", "Retry");
  retry.type = "button";
  retry.dataset.act = "retry";
  retry.onclick = onRetry;
  box.append(p, retry);
  return box;
}

export function renderBreadcrumb(crumbs: { label: string; href?: string }[]): HTMLElement {
  const nav = el("nav", undefined, "routine-breadcrumb");
  nav.setAttribute("aria-label", "Breadcrumb");
  crumbs.forEach((c, idx) => {
    if (idx > 0) {
      const sep = el("span", "/", "routine-breadcrumb-sep");
      sep.setAttribute("aria-hidden", "true");
      nav.append(sep);
    }
    if (c.href) {
      nav.append(link(c.href, c.label));
    } else {
      const cur = el("span", c.label);
      cur.setAttribute("aria-current", "page");
      nav.append(cur);
    }
  });
  return nav;
}

export async function refusalCode(r: Response): Promise<string> {
  try {
    const body = (await r.json()) as { error?: unknown };
    if (typeof body.error === "string" && /^[a-z][a-z0-9_]{0,47}$/.test(body.error)) return body.error;
  } catch { /* non-JSON refusal */ }
  return `HTTP ${r.status}`;
}

const routineReasons: Record<string, string> = {
  mode_requires_phase2: "Selected mode is not enabled on this factory (mode_requires_phase2).",
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
  prompt_invalid: "Routine prompt failed strict validation.",
  skill_missing: "Bound Agent Skill SKILL.md was not found on the repository's default branch.",
  skill_invalid: "Bound Agent Skill SKILL.md failed strict validation.",
  defined_twice: "This routine is already defined in Repository.spec.routines[] or schedules[].",
  over_routine: "Binding cannot widen the Routine's ceilings or dataClass.",
  missing_parameter: "A required routine parameter is missing.",
  parameter_twice: "A fill-once parameter is already set on the Routine and cannot be repeated in the binding.",
  missing_ref: "Referenced Routine does not exist in this factory.",
  invalid_binding: "Routine binding configuration is invalid.",
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
  bad_json: "Request JSON is invalid or contains an unknown field.",
  unsupported_media_type: "Request must use Content-Type: application/json.",
  bad_window: "Digest window must be between 1d and 30d.",
  not_permitted: "Your account is not permitted to access routine controls.",
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

export function sparklineSummaryText(days: RoutineSparklineDay[]): string {
  let merged = 0;
  let closed = 0;
  let escalated = 0;
  let noOp = 0;
  for (const d of days.slice(-14)) {
    merged += d.merged;
    closed += d.closed;
    escalated += d.escalated;
    noOp += d.noOp;
  }
  const parts: string[] = [];
  if (merged > 0) parts.push(`${merged} merged`);
  if (closed > 0) parts.push(`${closed} closed`);
  if (escalated > 0) parts.push(`${escalated} escalated`);
  if (noOp > 0) parts.push(`${noOp} no-op`);
  return parts.length > 0 ? parts.join(" · ") : "No runs in 14d";
}

export function renderSparklineLegend(): HTMLElement {
  const ul = el("ul", undefined, "routine-spark-legend");
  ul.setAttribute("aria-label", "14-day outcome legend");
  for (const [outcome, label] of [
    ["merged", "Merged"],
    ["closed", "Closed"],
    ["escalated", "Escalated"],
    ["no-op", "No-op"],
    ["empty", "Idle"],
  ] as const) {
    const li = el("li");
    const swatch = el("span", undefined, "spark-day");
    swatch.dataset.outcome = outcome;
    swatch.setAttribute("aria-hidden", "true");
    li.append(swatch, el("span", label));
    ul.append(li);
  }
  return ul;
}

export function renderSparkline(days: RoutineSparklineDay[]): HTMLElement {
  const normalized = days.slice(-14);
  const list = el("ol", undefined, "routine-sparkline");
  list.setAttribute("aria-label", `14-day outcomes: ${sparklineSummaryText(normalized)}`);
  for (const d of normalized) {
    const li = el("li", undefined, "spark-day");
    const out = sparklineOutcome(d);
    li.dataset.outcome = out;
    const detail = `${d.date}: ${d.merged} merged, ${d.closed} closed, ${d.noOp} no-op, ${d.escalated} escalated`;
    li.title = detail;
    li.setAttribute("aria-label", detail);
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

export function formatStateLabel(state: string): string {
  if (state === "half_open") return "half-open";
  return state;
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
  return formatUSD(r.usdPerMerged);
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
  const state = el("span", formatStateLabel(st), "routine-state");
  state.dataset.state = st;
  const breakerVal = r.breakerState || "ok";
  const breakerLabel = breakerVal === "paused" ? "breaker: paused" : `breaker: ${formatStateLabel(breakerVal)}`;
  const breaker = el("span", breakerLabel, "routine-breaker");
  breaker.dataset.breaker = breakerVal;
  wrap.append(mode, state, breaker);
  if (r.paused && r.pausedReason) {
    wrap.append(el("span", `Paused: ${r.pausedReason}`, "routine-paused-reason"));
  }
  return wrap;
}

const MODE_RANK: Record<string, number> = {
  propose: 1,
  draft: 2,
  pr: 3,
};

export interface RoutineActionsOptions {
  showGitHubLink?: boolean;
  onFocusRestore?: () => void;
}

export function renderRoutineActions(
  r: RoutineOverview,
  get: typeof fetch,
  notice: HTMLElement,
  reload: () => Promise<void>,
  opts: RoutineActionsOptions = {},
): HTMLElement {
  const [owner = "", name = ""] = r.repository.split("/");
  const idLabel = `${r.repository} ${r.routine}`;
  const container = el("div", undefined, "routine-actions-wrap");
  const bar = el("div", undefined, "routine-actions");
  const confirmHost = el("div", undefined, "routine-inline-confirm-host");

  const modeSelect = el("select", undefined, "routine-mode-select");
  modeSelect.setAttribute("aria-label", `Run mode for ${idLabel}`);
  const configuredRank = MODE_RANK[r.mode] ?? 3;
  for (const [val, label] of [
    ["", `default (${r.mode})`],
    ["propose", "propose"],
    ["draft", "draft"],
    ["pr", "pr"],
  ] as const) {
    const opt = el("option", label);
    opt.value = val;
    if (val && (MODE_RANK[val] ?? 0) > configuredRank) {
      opt.disabled = true;
      opt.textContent = `${label} (widens)`;
    }
    modeSelect.append(opt);
  }
  modeSelect.disabled = !r.canWrite || r.mode === "disabled";

  const runBtn = el("button", "Run now");
  runBtn.type = "button";
  runBtn.dataset.act = "run-now";
  runBtn.setAttribute("aria-label", `Run now: ${idLabel}`);
  runBtn.disabled = !r.canWrite || r.mode === "disabled";

  const declarativelyPaused = Boolean(r.declarativePaused);
  const toggleBtn = el("button", r.paused ? "Resume" : "Pause");
  toggleBtn.type = "button";
  toggleBtn.dataset.act = r.paused ? "resume" : "pause";
  toggleBtn.setAttribute("aria-label", `${r.paused ? "Resume" : "Pause"} ${idLabel}`);
  toggleBtn.disabled = !r.canWrite || (r.paused && declarativelyPaused);
  if (r.paused && declarativelyPaused) {
    toggleBtn.title = "Paused in Repository configuration; edit YAML or Configure to resume.";
  }

  const setBusy = (busy: boolean, activeBtn?: HTMLButtonElement, busyLabel?: string) => {
    modeSelect.disabled = busy || !r.canWrite || r.mode === "disabled";
    runBtn.disabled = busy || !r.canWrite || r.mode === "disabled";
    toggleBtn.disabled = busy || !r.canWrite || (r.paused && declarativelyPaused);
    if (activeBtn) {
      activeBtn.setAttribute("aria-busy", String(busy));
      if (busy && busyLabel) activeBtn.textContent = busyLabel;
    }
  };

  const announceNotice = (tone: "ok" | "bad", ...children: (string | Node)[]) => {
    notice.dataset.tone = tone;
    notice.replaceChildren(...children);
  };

  const executeRun = async (confirmBtn?: HTMLButtonElement) => {
    setBusy(true, confirmBtn ?? runBtn, "Starting…");
    notice.replaceChildren();
    notice.dataset.tone = "";
    try {
      const created = await triggerRoutineRun(owner, name, r.routine, modeSelect.value, get);
      confirmHost.replaceChildren();
      await reload();
      announceNotice("ok", `Started ${created.task} (`, link(`/r/${encodeURIComponent(created.room)}`, `room ${created.room}`), ").");
      opts.onFocusRestore?.();
    } catch (e) {
      if (e instanceof SignInRequired) return;
      announceNotice("bad", explainRoutineError(e instanceof Error ? e.message : String(e)));
      setBusy(false, confirmBtn ?? runBtn);
      if (confirmBtn) confirmBtn.textContent = "Confirm run";
      runBtn.textContent = "Run now";
    }
  };

  runBtn.onclick = () => {
    confirmHost.replaceChildren();
    const box = el("div", undefined, "routine-inline-confirm");
    const chosenMode = modeSelect.value || r.mode;
    const costNote = r.maxCostUSD > 0 ? ` (up to ${formatUSD(r.maxCostUSD)})` : "";
    box.append(el("span", `Start manual ${chosenMode} run for ${r.routine}${costNote}?`));
    const confirmBtn = el("button", "Confirm run", "primary");
    confirmBtn.type = "button";
    confirmBtn.dataset.act = "confirm-run";
    const cancelBtn = el("button", "Cancel");
    cancelBtn.type = "button";
    cancelBtn.dataset.act = "cancel-confirm";
    cancelBtn.onclick = () => {
      confirmHost.replaceChildren();
      runBtn.focus();
    };
    confirmBtn.onclick = () => void executeRun(confirmBtn);
    box.append(confirmBtn, cancelBtn);
    confirmHost.append(box);
    confirmBtn.focus();
  };

  const executePause = async (reason: string, confirmBtn?: HTMLButtonElement) => {
    setBusy(true, confirmBtn ?? toggleBtn, "Pausing…");
    notice.replaceChildren();
    notice.dataset.tone = "";
    try {
      await pauseRoutine(owner, name, r.routine, reason.trim(), get);
      confirmHost.replaceChildren();
      await reload();
      announceNotice("ok", `Paused ${r.repository} · ${r.routine}.`);
      opts.onFocusRestore?.();
    } catch (e) {
      if (e instanceof SignInRequired) return;
      announceNotice("bad", explainRoutineError(e instanceof Error ? e.message : String(e)));
      setBusy(false, confirmBtn ?? toggleBtn);
      if (confirmBtn) confirmBtn.textContent = "Confirm pause";
      toggleBtn.textContent = "Pause";
    }
  };

  toggleBtn.onclick = async () => {
    if (r.paused) {
      setBusy(true, toggleBtn, "Resuming…");
      notice.replaceChildren();
      notice.dataset.tone = "";
      try {
        await resumeRoutine(owner, name, r.routine, get);
        await reload();
        announceNotice("ok", `Resumed ${r.repository} · ${r.routine}.`);
        opts.onFocusRestore?.();
      } catch (e) {
        if (e instanceof SignInRequired) return;
        announceNotice("bad", explainRoutineError(e instanceof Error ? e.message : String(e)));
        setBusy(false, toggleBtn);
        toggleBtn.textContent = "Resume";
      }
      return;
    }

    confirmHost.replaceChildren();
    const box = el("div", undefined, "routine-inline-confirm");
    const reasonInput = el("input");
    reasonInput.type = "text";
    reasonInput.name = "pauseReason";
    reasonInput.maxLength = 200;
    reasonInput.autocomplete = "off";
    reasonInput.placeholder = "Optional pause reason…";
    reasonInput.setAttribute("aria-label", `Pause reason for ${idLabel}`);
    const confirmBtn = el("button", "Confirm pause", "primary");
    confirmBtn.type = "button";
    confirmBtn.dataset.act = "confirm-pause";
    const cancelBtn = el("button", "Cancel");
    cancelBtn.type = "button";
    cancelBtn.dataset.act = "cancel-confirm";
    cancelBtn.onclick = () => {
      confirmHost.replaceChildren();
      toggleBtn.focus();
    };
    confirmBtn.onclick = () => void executePause(reasonInput.value, confirmBtn);
    reasonInput.onkeydown = (ev) => {
      if (ev.key === "Enter") {
        ev.preventDefault();
        void executePause(reasonInput.value, confirmBtn);
      } else if (ev.key === "Escape") {
        confirmHost.replaceChildren();
        toggleBtn.focus();
      }
    };
    box.append(reasonInput, confirmBtn, cancelBtn);
    confirmHost.append(box);
    reasonInput.focus();
  };

  bar.append(modeSelect, runBtn, toggleBtn);
  if (r.paused && declarativelyPaused) {
    bar.append(el("span", "Paused in config", "routine-declarative-note"));
  }
  const configureHref = `/routines/${encodeURIComponent(owner)}/${encodeURIComponent(name)}/${encodeURIComponent(r.routine)}/edit`;
  const cfgLink = link(configureHref, "Configure", "routine-configure-link");
  cfgLink.setAttribute("aria-label", `Configure ${idLabel}`);
  bar.append(cfgLink);
  if (opts.showGitHubLink) {
    const edit = externalLink(r.promptEditURL, "Edit prompt on GitHub ↗", "routine-edit-link", `Edit prompt for ${idLabel} on GitHub`);
    if (edit) bar.append(edit);
  }
  container.append(bar, confirmHost);
  return container;
}

function attentionRank(r: RoutineOverview): number {
  if (r.breakerState === "paused" || r.breakerState === "tripped") return 0;
  if (r.paused) return 1;
  if (r.breakerState === "half_open") return 2;
  if (r.mode === "disabled") return 4;
  return 3;
}

export function sortRoutinesByAttention(list: RoutineOverview[]): RoutineOverview[] {
  return [...list].sort((a, b) => {
    const diff = attentionRank(a) - attentionRank(b);
    if (diff !== 0) return diff;
    const repoCmp = a.repository.localeCompare(b.repository);
    if (repoCmp !== 0) return repoCmp;
    return a.routine.localeCompare(b.routine);
  });
}

function renderOverviewCard(
  r: RoutineOverview,
  get: typeof fetch,
  notice: HTMLElement,
  reload: () => Promise<void>,
  onFocusRestore: () => void,
): HTMLElement {
  const [owner = "", name = ""] = r.repository.split("/");
  const card = el("article", undefined, "routine-card");
  card.dataset.repository = r.repository;
  card.dataset.routine = r.routine;
  card.dataset.state = routineState(r);
  card.dataset.mode = r.mode;
  card.dataset.breaker = r.breakerState || "ok";
  card.dataset.search = `${r.repository} ${r.routine} ${r.description ?? ""} ${r.probeKind ?? ""} ${r.workflow ?? ""}`.toLowerCase();

  if (r.breakerState === "paused" || r.breakerState === "tripped") {
    card.dataset.attention = "breaker";
  } else if (r.paused || r.breakerState === "half_open") {
    card.dataset.attention = "paused";
  }

  const head = el("div", undefined, "routine-card-head");
  const titleWrap = el("div", undefined, "routine-title-wrap");
  const detailPath = `/routines/${encodeURIComponent(owner)}/${encodeURIComponent(name)}/${encodeURIComponent(r.routine)}`;
  const heading = el("h2", undefined, "routine-title-heading");
  heading.append(link(detailPath, r.routine, "routine-title"));
  titleWrap.append(heading, el("span", r.repository, "routine-repo"));
  head.append(titleWrap, renderRoutineBadges(r));
  card.append(head);

  if (r.description) {
    card.append(el("p", r.description, "routine-desc"));
  }
  const sparkWrap = el("div", undefined, "routine-spark-row");
  sparkWrap.append(
    el("span", "14d", "routine-spark-label"),
    renderSparkline(r.sparkline),
    el("span", sparklineSummaryText(r.sparkline), "routine-spark-summary"),
  );
  card.append(
    sparkWrap,
    renderRoutineMetrics(r),
    renderRoutineActions(r, get, notice, reload, { showGitHubLink: false, onFocusRestore }),
  );
  return card;
}

function syncOverviewQuery(params: { repo: string; state: string; mode: string; q: string }): void {
  if (typeof location === "undefined" || typeof history === "undefined") return;
  try {
    const u = new URL(location.href);
    for (const [k, v] of Object.entries(params)) {
      if (v) u.searchParams.set(k, v);
      else u.searchParams.delete(k);
    }
    history.replaceState(history.state, "", u);
  } catch { /* non-URL environment */ }
}

async function mountOverview(app: HTMLElement, get: typeof fetch, theme?: ThemeController, search = ""): Promise<void> {
  const main = settingsFrame(app, "Routines", "/routines", theme);
  document.title = "Maku routines";
  const notice = el("p", "", "routine-notice");
  notice.setAttribute("role", "status");
  notice.setAttribute("aria-live", "polite");
  notice.tabIndex = -1;
  main.append(notice);

  const body = el("div", undefined, "routines-body");
  main.append(body);

  const initParams = new URLSearchParams(search);
  let selectedRepo = initParams.get("repo") ?? "";
  let selectedState = initParams.get("state") ?? "";
  let selectedMode = initParams.get("mode") ?? "";
  let searchQuery = initParams.get("q") ?? "";
  let focusKey = "";

  const load = async () => {
    body.replaceChildren(renderSkeleton(3));
    let routines: RoutineOverview[];
    try {
      routines = await fetchRoutines(undefined, get);
    } catch (e) {
      if (e instanceof SignInRequired) {
        body.replaceChildren(el("p", "signing in again…", "notice"));
        return;
      }
      body.replaceChildren(renderFetchError(explainRoutineError(e instanceof Error ? e.message : String(e)), () => void load()));
      return;
    }

    const toolbar = el("div", undefined, "routines-toolbar");
    toolbar.append(link("/routines/catalog", "Browse catalog", "routine-catalog-cta"));

    if (routines.length === 0) {
      const emptyCard = el("section", undefined, "routines-empty-card");
      emptyCard.append(
        el("h2", "No routines configured yet"),
        el("p", "Upkeep routines run scheduled maintenance (dead links, doc drift, failing Renovate PRs, CI findings) inside bounded cost and PR caps."),
        el("p", "Browse the starter catalog to bind a routine to one of your repositories."),
      );
      body.replaceChildren(toolbar, emptyCard);
      return;
    }

    const ordered = sortRoutinesByAttention(routines);
    const repos = [...new Set(ordered.map((r) => r.repository))].sort();
    const modes = [...new Set(["propose", "draft", "pr", "disabled", ...ordered.map((r) => r.mode)])];

    const filters = el("div", undefined, "routines-filters");
    const searchInput = el("input");
    searchInput.type = "search";
    searchInput.name = "search";
    searchInput.autocomplete = "off";
    searchInput.placeholder = "Search routines or repos…";
    searchInput.setAttribute("aria-label", "Search routines");
    searchInput.value = searchQuery;

    const repoSelect = el("select");
    repoSelect.name = "repository";
    repoSelect.setAttribute("aria-label", "Filter by repository");
    const allRepoOpt = el("option", "All repositories");
    allRepoOpt.value = "";
    repoSelect.append(allRepoOpt);
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
      ["breaker_paused", "breaker paused"],
      ["half_open", "half-open"],
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

    const countEl = el("span", "", "routines-filter-count");
    countEl.setAttribute("aria-live", "polite");

    filters.append(searchInput, repoSelect, stateSelect, modeSelect, countEl);

    const list = el("section", undefined, "routines-list");
    const cards = ordered.map((r) =>
      renderOverviewCard(r, get, notice, load, () => {
        focusKey = `${r.repository}::${r.routine}`;
      }),
    );

    const emptyMsg = el("div", undefined, "routines-empty");
    const clearBtn = el("button", "Clear filters");
    clearBtn.type = "button";
    clearBtn.dataset.act = "clear-filters";
    emptyMsg.append(el("span", "No routines match the selected filters."), clearBtn);
    emptyMsg.hidden = true;
    list.append(...cards, emptyMsg);

    const applyFilters = (pushURL = true) => {
      selectedRepo = repoSelect.value;
      selectedState = stateSelect.value;
      selectedMode = modeSelect.value;
      searchQuery = searchInput.value;
      const words = searchQuery.trim().toLowerCase().split(/\s+/).filter(Boolean);
      let visible = 0;
      for (const c of cards) {
        const matchesState =
          !selectedState ||
          (selectedState === "breaker_paused"
            ? c.dataset.breaker === "paused" || c.dataset.breaker === "tripped"
            : c.dataset.state === selectedState);
        const matchesSearch = words.length === 0 || words.every((w) => (c.dataset.search ?? "").includes(w));
        const show =
          (!selectedRepo || c.dataset.repository === selectedRepo) &&
          matchesState &&
          (!selectedMode || c.dataset.mode === selectedMode) &&
          matchesSearch;
        c.hidden = !show;
        if (show) visible++;
      }
      countEl.textContent = `${visible} of ${cards.length} ${cards.length === 1 ? "routine" : "routines"}`;
      emptyMsg.hidden = visible > 0;
      if (pushURL) {
        syncOverviewQuery({ repo: selectedRepo, state: selectedState, mode: selectedMode, q: searchQuery.trim() });
      }
    };

    clearBtn.onclick = () => {
      searchInput.value = "";
      repoSelect.value = "";
      stateSelect.value = "";
      modeSelect.value = "";
      applyFilters();
      searchInput.focus();
    };
    searchInput.oninput = () => applyFilters();
    searchInput.onkeydown = (e) => {
      if (e.key === "Escape" && searchInput.value) {
        searchInput.value = "";
        applyFilters();
      }
    };
    repoSelect.onchange = () => applyFilters();
    stateSelect.onchange = () => applyFilters();
    modeSelect.onchange = () => applyFilters();

    const slashHandler = (e: KeyboardEvent) => {
      if (!searchInput.isConnected) {
        document.removeEventListener("keydown", slashHandler);
        return;
      }
      if (e.key !== "/" || e.ctrlKey || e.metaKey || e.altKey) return;
      const t = e.target;
      if (t instanceof HTMLElement && (["INPUT", "TEXTAREA", "SELECT"].includes(t.tagName) || t.isContentEditable)) return;
      e.preventDefault();
      searchInput.focus();
    };
    document.addEventListener("keydown", slashHandler);

    body.replaceChildren(toolbar, filters, renderSparklineLegend(), list);
    applyFilters(false);

    if (focusKey) {
      const target = cards.find((c) => `${c.dataset.repository}::${c.dataset.routine}` === focusKey);
      focusKey = "";
      target?.querySelector<HTMLElement>(".routine-title")?.focus();
    }
  };

  await load();
}

export async function mountRoutines(
  app: HTMLElement,
  path: string,
  get: typeof fetch = fetch,
  theme?: ThemeController,
  search = typeof location !== "undefined" ? location.search : "",
): Promise<void> {
  if (path === "/routines") {
    return mountOverview(app, get, theme, search);
  }
  if (path === "/routines/catalog") {
    return mountRoutineCatalog(app, get, theme);
  }
  const editMatch = path.match(/^\/routines\/([^/]+)\/([^/]+)\/([^/]+)\/edit$/);
  if (editMatch) {
    let owner: string;
    let name: string;
    let routine: string;
    try {
      owner = decodeURIComponent(editMatch[1]);
      name = decodeURIComponent(editMatch[2]);
      routine = decodeURIComponent(editMatch[3]);
    } catch {
      const main = settingsFrame(app, "Routines", "/routines", theme);
      document.title = "Maku · Routine not found";
      main.append(
        renderBreadcrumb([{ label: "Routines", href: "/routines" }, { label: "Not found" }]),
        el("p", "Invalid routine URL.", "notice"),
      );
      return;
    }
    return mountRoutineEditor(app, owner, name, routine, get, theme, search);
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
        renderBreadcrumb([{ label: "Routines", href: "/routines" }, { label: "Not found" }]),
        el("p", "Invalid routine URL.", "notice"),
      );
      return;
    }
    return mountRoutineDetail(app, owner, name, routine, get, theme);
  }
}
