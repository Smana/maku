// SPDX-License-Identifier: Apache-2.0

import { api, SignInRequired } from "./api";
import {
  el,
  explainRoutineError,
  formatUSD,
  refusalCode,
  renderBreadcrumb,
  renderFetchError,
  renderSkeleton,
} from "./routines";
import { settingsFrame } from "./settings";
import type { ThemeController } from "./theme";

export interface RoutineBreaker {
  window: number;
  minSamples: number;
  minMergeRate: number;
  maxConsecutiveClosed: number;
  maxConsecutiveFailures: number;
}

export interface CatalogConsumer {
  repository: string;
  mode: string;
  paused: boolean;
}

export interface CatalogRoutine {
  name: string;
  description?: string;
  catalog: boolean;
  workflow?: string;
  dataClass?: string;
  defaultPrompt?: string;
  probeKind: string;
  requireConsecutive?: number;
  maxFindings?: number;
  requires?: string[];
  maxOpenPRs: number;
  maxCostUSD: number;
  cooldown?: string;
  breaker: RoutineBreaker;
  protectedPaths?: string[];
  egress?: string[];
  probeWorkflow?: string;
  probeArtifact?: string;
  probeFile?: string;
  probeAuthor?: string;
  oracleClass?: string;
  consumerCount: number;
  consumers: CatalogConsumer[];
}

export interface CatalogRepoCapability {
  repository: string;
  canWrite: boolean;
  egress?: string[];
  boundRoutines: string[];
}

export interface RoutineCatalogResponse {
  routines: CatalogRoutine[];
  repositories: string[];
  capabilities: CatalogRepoCapability[];
  uiWrite: boolean;
  upkeepDailyUSD: number;
}

export async function fetchRoutineCatalog(get: typeof fetch = fetch): Promise<RoutineCatalogResponse> {
  const r = await api("/api/routines/catalog", { credentials: "same-origin" }, get);
  if (!r.ok) throw new Error(await refusalCode(r));
  return (await r.json()) as RoutineCatalogResponse;
}

function editHrefFor(repo: string, routine: string): string {
  const [owner = "", name = ""] = repo.split("/");
  return `/routines/${encodeURIComponent(owner)}/${encodeURIComponent(name)}/${encodeURIComponent(routine)}/edit?from=catalog`;
}

function setArrowLabel(anchor: HTMLAnchorElement, label: string): void {
  anchor.replaceChildren(label, " ", el("span", "→", "routine-arrow"));
  anchor.lastElementChild!.setAttribute("aria-hidden", "true");
}

function renderCatalogCard(
  r: CatalogRoutine,
  getRepo: () => string,
  capByRepo: Map<string, CatalogRepoCapability>,
): { card: HTMLElement; syncRepo: () => void } {
  const card = el("article", undefined, "routine-card routine-catalog-card");
  card.dataset.routine = r.name;
  card.dataset.catalog = String(r.catalog);
  card.dataset.search = `${r.name} ${r.description ?? ""} ${r.probeKind ?? ""} ${r.workflow ?? ""}`.toLowerCase();

  const head = el("div", undefined, "routine-card-head");
  const titleWrap = el("div", undefined, "routine-title-wrap");
  titleWrap.append(el("h2", r.name, "routine-catalog-title"));

  const badges = el("div", undefined, "routine-badges");
  const catBadge = el("span", r.catalog ? "catalog" : "custom", "routine-catalog-badge");
  catBadge.dataset.catalog = String(r.catalog);
  badges.append(catBadge);

  const probeBadge = el("span", `probe: ${r.probeKind || "none"}`, "routine-fact-badge");
  badges.append(probeBadge);
  if (r.workflow) {
    badges.append(el("span", `workflow: ${r.workflow}`, "routine-fact-badge"));
  }
  if (r.dataClass) {
    badges.append(el("span", r.dataClass, "routine-fact-badge"));
  }
  const boundBadge = el("span", "bound on repository", "routine-bound-badge");
  boundBadge.hidden = true;
  badges.append(boundBadge);

  head.append(titleWrap, badges);
  card.append(head);

  if (r.description) {
    card.append(el("p", r.description, "routine-desc"));
  }

  const dl = el("dl", undefined, "routine-metrics");
  const addMetric = (label: string, value: string) => {
    const row = el("div", undefined, "routine-metric");
    row.append(el("dt", label), el("dd", value));
    dl.append(row);
  };
  addMetric("Max open PRs", String(r.maxOpenPRs));
  addMetric("Max cost / run", formatUSD(r.maxCostUSD));
  addMetric("Cooldown", r.cooldown || "none");
  addMetric("Consumers", String(r.consumerCount));
  card.append(dl);

  const specs = el("div", undefined, "routine-catalog-specs");
  if (r.requires && r.requires.length > 0) {
    const reqLine = el("div", undefined, "routine-catalog-spec-row");
    reqLine.append(el("span", "Requires: ", "routine-spec-label"));
    const chips = el("span", undefined, "routine-requires-list");
    for (const req of r.requires) {
      chips.append(el("code", req, "routine-req-chip"));
    }
    reqLine.append(chips);
    specs.append(reqLine);
  }
  if (r.egress && r.egress.length > 0) {
    const egLine = el("div", undefined, "routine-catalog-spec-row");
    egLine.append(el("span", "Egress: ", "routine-spec-label"), el("span", r.egress.join(", ")));
    specs.append(egLine);
  }
  const fixedParams: string[] = [];
  if (r.probeWorkflow) fixedParams.push(`probeWorkflow: ${r.probeWorkflow}`);
  if (r.probeArtifact) fixedParams.push(`probeArtifact: ${r.probeArtifact}`);
  if (r.probeFile) fixedParams.push(`probeFile: ${r.probeFile}`);
  if (r.probeAuthor) fixedParams.push(`probeAuthor: ${r.probeAuthor}`);
  if (r.oracleClass) fixedParams.push(`oracleClass: ${r.oracleClass}`);
  if (fixedParams.length > 0) {
    const pLine = el("div", undefined, "routine-catalog-spec-row");
    pLine.append(el("span", "Parameters: ", "routine-spec-label"), el("span", fixedParams.join(" · ")));
    specs.append(pLine);
  }
  const minPct = Math.round(r.breaker.minMergeRate * 100);
  const brLine = el("div", undefined, "routine-catalog-spec-row");
  brLine.append(
    el("span", "Breaker: ", "routine-spec-label"),
    el(
      "span",
      `window ${r.breaker.window} · min ${minPct}% merge (${r.breaker.minSamples} samples) · ${r.breaker.maxConsecutiveClosed} closed / ${r.breaker.maxConsecutiveFailures} failed`,
    ),
  );
  specs.append(brLine);
  card.append(specs);

  if (r.consumers.length > 0) {
    const consumersDet = el("details", undefined, "routine-consumers");
    consumersDet.append(el("summary", `Active consumers (${r.consumers.length})`));
    const ul = el("ul", undefined, "routine-consumers-list");
    for (const c of r.consumers) {
      ul.append(el("li", `${c.repository} · ${c.mode}${c.paused ? " · paused" : ""}`));
    }
    consumersDet.append(ul);
    card.append(consumersDet);
  }

  if (r.defaultPrompt) {
    const det = el("details", undefined, "routine-prompt");
    det.append(el("summary", "Default prompt"), el("pre", r.defaultPrompt, "routine-prompt-text"));
    card.append(det);
  }

  const actions = el("div", undefined, "routine-actions");
  let ctaLink: HTMLAnchorElement | undefined;
  const syncRepo = () => {
    const repo = getRepo();
    const cap = capByRepo.get(repo);
    const isBound = Boolean(cap?.boundRoutines?.includes(r.name));
    const canWrite = cap?.canWrite ?? false;
    boundBadge.hidden = !isBound;
    if (!repo) {
      if (ctaLink) {
        ctaLink.remove();
        ctaLink = undefined;
      }
      return;
    }
    if (!ctaLink) {
      ctaLink = el("a", undefined, "routine-catalog-enable");
      actions.append(ctaLink);
    }
    ctaLink.href = editHrefFor(repo, r.name);
    ctaLink.dataset.readonly = String(!canWrite);
    const baseLabel = !canWrite
      ? "View configuration"
      : isBound
        ? "Configure on repository"
        : "Enable on repository";
    setArrowLabel(ctaLink, baseLabel);
    ctaLink.setAttribute("aria-label", `${baseLabel}: ${r.name} on ${repo}`);
  };

  syncRepo();
  card.append(actions);
  return { card, syncRepo };
}

export async function mountRoutineCatalog(
  app: HTMLElement,
  get: typeof fetch = fetch,
  theme?: ThemeController,
): Promise<void> {
  const main = settingsFrame(app, "Routine catalog", "/routines", theme);
  document.title = "Maku · Routine catalog";

  const crumbs = renderBreadcrumb([
    { label: "Routines", href: "/routines" },
    { label: "Catalog" },
  ]);
  const notice = el("p", "", "routine-notice");
  notice.setAttribute("role", "status");
  notice.setAttribute("aria-live", "polite");
  const body = el("div", undefined, "routine-catalog-body");
  main.append(crumbs, notice, body);

  const load = async () => {
    body.replaceChildren(renderSkeleton(3));
    let resp: RoutineCatalogResponse;
    try {
      resp = await fetchRoutineCatalog(get);
    } catch (e) {
      if (e instanceof SignInRequired) {
        body.replaceChildren(el("p", "signing in again…", "notice"));
        return;
      }
      body.replaceChildren(renderFetchError(explainRoutineError(e instanceof Error ? e.message : String(e)), () => void load()));
      return;
    }

    const capByRepo = new Map<string, CatalogRepoCapability>();
    for (const cap of resp.capabilities ?? []) {
      capByRepo.set(cap.repository, cap);
    }

    const controls = el("section", undefined, "routine-catalog-controls");
    const pickerRow = el("div", undefined, "routine-catalog-picker");
    const pickerLabel = el("label", undefined, "routine-catalog-picker");
    pickerLabel.append(el("span", "Target repository", "routine-spec-label"));
    const repoSelect = el("select");
    repoSelect.name = "targetRepository";
    repoSelect.setAttribute("aria-label", "Target repository");

    if (resp.repositories.length === 0) {
      const opt = el("option", "No readable repositories");
      opt.value = "";
      repoSelect.append(opt);
      repoSelect.disabled = true;
    } else {
      for (const repo of resp.repositories) {
        const opt = el("option", repo);
        opt.value = repo;
        repoSelect.append(opt);
      }
    }
    pickerLabel.append(repoSelect);

    const searchInput = el("input", undefined, "routine-catalog-search");
    searchInput.type = "search";
    searchInput.name = "catalogSearch";
    searchInput.autocomplete = "off";
    searchInput.placeholder = "Filter catalog routines…";
    searchInput.setAttribute("aria-label", "Filter catalog routines");

    pickerRow.append(pickerLabel, searchInput);

    const metaBar = el("div", undefined, "routine-catalog-meta");
    metaBar.append(
      el("span", `Upkeep daily budget: ${formatUSD(resp.upkeepDailyUSD)}`, "routine-fact-badge"),
      el("span", resp.uiWrite ? "Direct cluster save (uiWrite)" : "GitOps YAML export", "routine-fact-badge"),
    );

    const repoStatus = el("p", "", "routine-catalog-repo-status");
    repoStatus.setAttribute("aria-live", "polite");
    controls.append(pickerRow, metaBar, repoStatus);

    const updateRepoStatus = () => {
      const repo = repoSelect.value;
      if (!repo) {
        repoStatus.textContent = "No readable repositories are configured.";
        return;
      }
      const cap = capByRepo.get(repo);
      const parts: string[] = [];
      if (cap) {
        parts.push(
          cap.canWrite
            ? "Maintainer write access verified."
            : "Read-only on this repository (maintainer write access required to preview or save).",
        );
        if (cap.egress && cap.egress.length > 0) {
          parts.push(`Repository egress: ${cap.egress.join(", ")}.`);
        }
        parts.push(`Bound routines: ${cap.boundRoutines.length}.`);
      }
      repoStatus.textContent = parts.join(" ");
    };

    if (resp.routines.length === 0) {
      updateRepoStatus();
      body.replaceChildren(controls, el("p", "No routines are served by this factory."));
      return;
    }

    const grid = el("section", undefined, "routine-catalog-grid");
    const cards = resp.routines.map((rt) => renderCatalogCard(rt, () => repoSelect.value, capByRepo));
    for (const c of cards) {
      grid.append(c.card);
    }

    const emptyMsg = el("div", undefined, "routines-empty");
    const clearBtn = el("button", "Clear filter");
    clearBtn.type = "button";
    emptyMsg.append(el("span", "No catalog routines match your search."), clearBtn);
    emptyMsg.hidden = true;

    const applySearch = () => {
      const words = searchInput.value.trim().toLowerCase().split(/\s+/).filter(Boolean);
      let visible = 0;
      for (const c of cards) {
        const show = words.length === 0 || words.every((w) => (c.card.dataset.search ?? "").includes(w));
        c.card.hidden = !show;
        if (show) visible++;
      }
      emptyMsg.hidden = visible > 0;
    };

    clearBtn.onclick = () => {
      searchInput.value = "";
      applySearch();
      searchInput.focus();
    };
    searchInput.oninput = applySearch;
    searchInput.onkeydown = (e) => {
      if (e.key === "Escape" && searchInput.value) {
        searchInput.value = "";
        applySearch();
      }
    };

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

    repoSelect.onchange = () => {
      updateRepoStatus();
      for (const c of cards) c.syncRepo();
    };
    updateRepoStatus();

    body.replaceChildren(controls, grid, emptyMsg);
  };

  await load();
}
