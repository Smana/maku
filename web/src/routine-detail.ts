// SPDX-License-Identifier: Apache-2.0

import { SignInRequired } from "./api";
import { CRON_PRESETS, presetForCron } from "./cron-preview";
import {
  el,
  explainRoutineError,
  externalLink,
  fetchRoutineDetail,
  formatUSD,
  link,
  renderBreadcrumb,
  renderFetchError,
  renderRoutineActions,
  renderRoutineBadges,
  renderRoutineMetrics,
  renderSkeleton,
  renderSparkline,
  renderSparklineLegend,
  sparklineSummaryText,
  timeElement,
  type RoutineDetail,
  type RoutineTaskRun,
  type SkipRecord,
} from "./routines";
import { settingsFrame } from "./settings";
import type { ThemeController } from "./theme";

const skipExplanations: Record<string, string> = {
  open_cap: "Open PR cap reached for this routine.",
  upkeep_budget: "Daily upkeep budget exhausted.",
  breaker_paused: "Yield breaker paused this routine.",
  probe_empty: "Probe found no actionable work.",
  probe_refused: "Probe findings were refused by strict validation.",
  prompt_invalid: "Prompt validation refused the routine prompt.",
  cooldown: "Routine is inside its cooldown window after a closed or reverted PR.",
  paused_manual: "Routine is operationally paused.",
  kill_switch: "Factory kill switch is active.",
  not_owned: "Repository is not owned by this factory.",
  disabled: "Routine mode is disabled.",
};

const outcomeLabels: Record<string, string> = {
  accepted: "merged",
  merged_human: "merged (edited)",
  no_op: "no-op",
  closed: "closed",
  reverted: "reverted",
  failed: "failed",
  stopped: "stopped",
  rejected: "rejected",
};

function shortSHA(sha: string): string {
  return sha.length > 12 ? sha.slice(0, 12) : sha;
}

function formatCronFact(expr: string): string {
  const id = presetForCron(expr);
  const preset = CRON_PRESETS.find((p) => p.id === id && p.cron !== "");
  return preset ? `${preset.label} (${expr})` : expr;
}

function renderRunRow(run: RoutineTaskRun, prevRun: RoutineTaskRun | undefined): HTMLElement {
  const row = el("article", undefined, "routine-run-row");
  const head = el("div", undefined, "routine-run-head");
  const outcomeCode = run.outcome || run.phase;
  const badge = el("span", outcomeLabels[outcomeCode] ?? outcomeCode, "routine-outcome");
  badge.dataset.outcome = outcomeCode;
  head.append(el("span", run.name, "routine-run-name"), badge);
  if (run.reason) {
    head.append(el("span", run.reason, "routine-run-reason"));
  }
  row.append(head);

  const meta = el("dl", undefined, "routine-run-meta");
  const addMeta = (label: string, val: string | Node) => {
    const item = el("div", undefined, "routine-run-meta-item");
    item.append(el("dt", label), el("dd"));
    item.lastElementChild!.append(val);
    meta.append(item);
  };

  addMeta("Created", timeElement(run.createdAt));

  const prAnchor = externalLink(run.prURL, run.prNumber ? `#${run.prNumber}` : "Pull request");
  addMeta("PR", prAnchor ?? "—");

  addMeta("Room", run.roomRef ? link(`/r/${encodeURIComponent(run.roomRef)}`, run.roomRef) : "—");

  const promptCell = el("span", undefined, "routine-prompt-cell");
  const shaChanged = Boolean(prevRun?.promptSHA && run.promptSHA && run.promptSHA !== prevRun.promptSHA);
  const shaBadge = el("code", run.promptSHA ? shortSHA(run.promptSHA) : "—", "prompt-sha-badge");
  if (run.promptSHA) shaBadge.title = run.promptSHA;
  if (shaChanged) {
    shaBadge.dataset.promptChanged = "true";
  }
  promptCell.append(shaBadge);
  if (shaChanged) {
    promptCell.append(el("span", "prompt changed", "prompt-changed-note"));
  }
  addMeta("Prompt SHA", promptCell);

  addMeta("Requested by", run.requestedBy || "—");
  addMeta("Cost", formatUSD(run.costUSD));

  row.append(meta);
  return row;
}

function renderSkipRow(sk: SkipRecord): HTMLElement {
  const row = el("li", undefined, "routine-skip-row");
  const head = el("div", undefined, "routine-skip-head");
  head.append(timeElement(sk.at), el("code", sk.reason, "routine-skip-code"));
  if (sk.slotKey) {
    head.append(el("span", `slot ${sk.slotKey}`, "routine-skip-slot"));
  }
  row.append(head);
  const desc = skipExplanations[sk.reason];
  if (desc) {
    row.append(el("p", desc, "routine-skip-desc"));
  }
  return row;
}

export async function mountRoutineDetail(
  app: HTMLElement,
  owner: string,
  name: string,
  routine: string,
  get: typeof fetch = fetch,
  theme?: ThemeController,
): Promise<void> {
  const repo = `${owner}/${name}`;
  const main = settingsFrame(app, `${repo} · ${routine}`, "/routines", theme);
  document.title = `Maku · ${repo} · ${routine}`;

  const crumbs = renderBreadcrumb([
    { label: "Routines", href: "/routines" },
    { label: `${repo} · ${routine}` },
  ]);
  const notice = el("p", "", "routine-notice");
  notice.setAttribute("role", "status");
  notice.setAttribute("aria-live", "polite");
  notice.tabIndex = -1;
  const body = el("div", undefined, "routine-detail-body");
  main.append(crumbs, notice, body);

  const load = async () => {
    body.replaceChildren(renderSkeleton(3));
    let d: RoutineDetail;
    try {
      d = await fetchRoutineDetail(owner, name, routine, get);
    } catch (e) {
      if (e instanceof SignInRequired) {
        body.replaceChildren(el("p", "signing in again…", "notice"));
        return;
      }
      body.replaceChildren(renderFetchError(explainRoutineError(e instanceof Error ? e.message : String(e)), () => void load()));
      return;
    }

    const summary = el("section", undefined, "routine-card routine-detail-summary");
    if (d.breakerState === "paused" || d.breakerState === "tripped") {
      summary.dataset.attention = "breaker";
    } else if (d.paused || d.breakerState === "half_open") {
      summary.dataset.attention = "paused";
    }

    const head = el("div", undefined, "routine-card-head");
    const info = el("div", undefined, "routine-title-wrap");
    info.append(el("h2", d.routine, "routine-detail-title"), el("span", d.repository, "routine-repo"));
    head.append(info, renderRoutineBadges(d));
    summary.append(head);

    if (d.description) {
      summary.append(el("p", d.description, "routine-desc"));
    }

    const facts = el("div", undefined, "routine-detail-facts");
    if (d.workflow) facts.append(el("span", `workflow: ${d.workflow}`, "routine-fact"));
    if (d.probeKind) facts.append(el("span", `probe: ${d.probeKind}`, "routine-fact"));
    if (d.cron) facts.append(el("span", `schedule: ${formatCronFact(d.cron)}`, "routine-fact"));
    if (d.maxCostUSD > 0) facts.append(el("span", `max cost: ${formatUSD(d.maxCostUSD)}`, "routine-fact"));
    if (d.cooldown) facts.append(el("span", `cooldown: ${d.cooldown}`, "routine-fact"));
    if (d.skill) facts.append(el("span", `skill: ${d.skill}`, "routine-fact"));
    if (d.paused && d.pausedBy) {
      const pauseSpan = el("span", undefined, "routine-fact");
      pauseSpan.append(`paused by ${d.pausedBy}`);
      if (d.pausedAt) pauseSpan.append(" · ", timeElement(d.pausedAt));
      facts.append(pauseSpan);
    }
    if (d.promptSHA) {
      const sha = el("code", `prompt ${shortSHA(d.promptSHA)}`, "prompt-sha-badge");
      sha.title = d.promptSHA;
      facts.append(sha);
    }
    summary.append(facts);

    const sparkWrap = el("div", undefined, "routine-spark-row");
    sparkWrap.append(
      el("span", "14d", "routine-spark-label"),
      renderSparkline(d.sparkline),
      el("span", sparklineSummaryText(d.sparkline), "routine-spark-summary"),
    );
    summary.append(
      sparkWrap,
      renderSparklineLegend(),
      renderRoutineMetrics(d),
      renderRoutineActions(d, get, notice, load, {
        showGitHubLink: true,
        onFocusRestore: () => notice.focus(),
      }),
    );

    if (d.promptExists && d.repoPromptText) {
      const repoPromptDet = el("details", undefined, "routine-prompt");
      repoPromptDet.append(
        el("summary", `Repository prompt (${d.promptPath || `.github/agent-factory/upkeep/${d.routine}.md`})`),
        el("pre", d.repoPromptText, "routine-prompt-text"),
      );
      summary.append(repoPromptDet);
    }

    if (d.defaultPrompt) {
      const det = el("details", undefined, "routine-prompt");
      det.append(el("summary", "Default prompt"), el("pre", d.defaultPrompt, "routine-prompt-text"));
      summary.append(det);
    }

    const runsSection = el("section", undefined, "routine-runs");
    runsSection.append(el("h2", "Run history"));
    if (d.runs.length === 0) {
      runsSection.append(el("p", "No runs recorded yet."));
    } else {
      const list = el("div", undefined, "routine-runs-list");
      for (let i = 0; i < d.runs.length; i++) {
        list.append(renderRunRow(d.runs[i], d.runs[i + 1]));
      }
      runsSection.append(list);
    }

    const skipsSection = el("section", undefined, "routine-skips");
    skipsSection.append(el("h2", "Skip & refusal diagnostics"));
    if (d.skips.length === 0) {
      skipsSection.append(el("p", "No recent skips recorded."));
    } else {
      const list = el("ul", undefined, "routine-skips-list");
      for (const sk of d.skips) {
        list.append(renderSkipRow(sk));
      }
      skipsSection.append(list);
    }

    body.replaceChildren(summary, runsSection, skipsSection);
  };

  await load();
}
