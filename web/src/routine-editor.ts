// SPDX-License-Identifier: Apache-2.0

import { api, SignInRequired } from "./api";
import {
  CRON_PRESETS,
  formatLocalDateTimeDate,
  formatUTCDateTime,
  nextRuns,
  presetForCron,
  validateCron,
} from "./cron-preview";
import { fileActions } from "./policy-editor";
import {
  fetchRoutineCatalog,
  type CatalogRepoCapability,
  type CatalogRoutine,
  type RoutineBreaker,
  type RoutineCatalogResponse,
} from "./routine-catalog";
import {
  el,
  explainRoutineError,
  externalLink,
  fetchRoutineDetail,
  formatUSD,
  refusalCode,
  renderBreadcrumb,
  renderFetchError,
  renderSkeleton,
  safeHTTPSURL,
  type RoutineDetail,
} from "./routines";
import { settingsFrame } from "./settings";
import type { ThemeController } from "./theme";

export const MAX_PROMPT_BYTES = 4096;
export const MAX_PROMPT_SECTIONS = 5;

export interface PromptValidation {
  valid: boolean;
  bytes: number;
  sections: number;
  hasVerify: boolean;
  verifyItems: number;
  error?: string;
}

export function validatePromptText(text: string): PromptValidation {
  const bytes = new TextEncoder().encode(text).length;
  if (bytes > MAX_PROMPT_BYTES) {
    return {
      valid: false,
      bytes,
      sections: 0,
      hasVerify: false,
      verifyItems: 0,
      error: `${bytes} bytes, at most ${MAX_PROMPT_BYTES}`,
    };
  }
  for (const ch of text) {
    const cp = ch.codePointAt(0)!;
    if ((cp <= 0x1f && cp !== 0x0a && cp !== 0x09) || (cp >= 0x7f && cp <= 0x9f) || /\p{Cf}/u.test(ch)) {
      return {
        valid: false,
        bytes,
        sections: 0,
        hasVerify: false,
        verifyItems: 0,
        error: `control or invisible character U+${cp.toString(16).toUpperCase().padStart(4, "0")}`,
      };
    }
  }

  let sections = 0;
  let verifyItems = 0;
  let inSection = "";
  let hasVerify = false;
  const lines = text.split("\n");
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i];
    if (line.startsWith("## ")) {
      sections++;
      inSection = line.replace(/[ \t]+$/, "");
      if (inSection === "## Verify") hasVerify = true;
    } else if (line.startsWith("# ") && i === 0) {
      // Optional top-level title on line 1.
    } else if (inSection === "" && line.trim() !== "") {
      return {
        valid: false,
        bytes,
        sections,
        hasVerify,
        verifyItems,
        error: `line ${i + 1} stands before the first section`,
      };
    } else if (inSection === "## Verify" && (line.startsWith("- ") || line.startsWith("* ")) && line.slice(2).trim() !== "") {
      verifyItems++;
    }
  }

  if (sections > MAX_PROMPT_SECTIONS) {
    return {
      valid: false,
      bytes,
      sections,
      hasVerify,
      verifyItems,
      error: `${sections} sections, at most ${MAX_PROMPT_SECTIONS}`,
    };
  }
  if (!hasVerify) {
    return {
      valid: false,
      bytes,
      sections,
      hasVerify,
      verifyItems,
      error: "no ## Verify section",
    };
  }
  if (verifyItems === 0) {
    return {
      valid: false,
      bytes,
      sections,
      hasVerify,
      verifyItems,
      error: "## Verify lists no check",
    };
  }

  return { valid: true, bytes, sections, hasVerify, verifyItems };
}

export interface RoutineBindingPayload {
  name: string;
  mode: string;
  paused?: boolean;
  cron?: string;
  repoPrompt?: boolean;
  skill?: string;
  workflow?: string;
  dataClass?: string;
  probeWorkflow?: string;
  probeArtifact?: string;
  probeFile?: string;
  probeAuthor?: string;
  oracleClass?: string;
  protectedPaths?: string[];
  egress?: string[];
  maxOpenPRs?: number;
  maxCostUSD?: number;
  cooldown?: string;
  maxFindings?: number;
  breaker?: Partial<RoutineBreaker>;
}

export interface RoutinePreviewResponse {
  valid: boolean;
  errors?: string[];
  reason?: string;
  promptValid?: boolean;
  promptError?: string;
  yaml?: string;
  manifest?: string;
  manifestPath?: string;
}

const FILL_ONCE_KEYS = [
  "probeWorkflow",
  "probeArtifact",
  "probeFile",
  "probeAuthor",
  "oracleClass",
] as const;

type FillOnceKey = (typeof FILL_ONCE_KEYS)[number];

const FILL_ONCE_LABELS: Record<FillOnceKey, { label: string; placeholder: string }> = {
  probeWorkflow: { label: "Probe workflow file", placeholder: "code-scan.yml…" },
  probeArtifact: { label: "Probe artifact", placeholder: "findings…" },
  probeFile: { label: "Probe findings file", placeholder: "findings.json…" },
  probeAuthor: { label: "Probe PR author", placeholder: "renovate[bot]…" },
  oracleClass: { label: "Oracle merge policy class", placeholder: "docs-links…" },
};

const MODE_CARDS: { value: "propose" | "draft" | "pr" | "disabled"; title: string; summary: string }[] = [
  {
    value: "propose",
    title: "Propose",
    summary: "Read-only triage files a factory/proposed issue when work is found (silent on NoOp).",
  },
  {
    value: "draft",
    title: "Draft PR",
    summary: "Opens a draft pull request; reviewer and checks run while draft, 48h reminders stay silent.",
  },
  {
    value: "pr",
    title: "Pull request",
    summary: "Opens a pull request. Auto-merge still requires a live MergePolicy.",
  },
  {
    value: "disabled",
    title: "Disabled",
    summary: "Decommissioned binding stays in config without scheduling or manual runs.",
  },
];

function extractBranchFromEditURL(editURL: string | undefined): string {
  if (!editURL) return "main";
  const m = editURL.match(/\/edit\/([^/]+)\//);
  return m ? decodeURIComponent(m[1]) : "main";
}

function githubPromptURL(
  owner: string,
  name: string,
  promptPath: string,
  promptExists: boolean | undefined,
  promptEditURL: string | undefined,
  isBound: boolean,
  draftValue = "",
): { url: string; label: string; isNew: boolean } {
  const branch = extractBranchFromEditURL(promptEditURL);
  const encOwner = encodeURIComponent(owner);
  const encName = encodeURIComponent(name);
  if (promptExists === false || (!isBound && promptExists === undefined)) {
    const base = `https://github.com/${encOwner}/${encName}/new/${encodeURIComponent(branch)}?filename=${encodeURIComponent(promptPath)}`;
    const withValue = draftValue ? `${base}&value=${encodeURIComponent(draftValue)}` : base;
    return {
      url: withValue.length <= 4000 ? withValue : base,
      label: "Create on GitHub ↗",
      isNew: true,
    };
  }
  const fallback = `https://github.com/${encOwner}/${encName}/edit/${encodeURIComponent(branch)}/${promptPath.split("/").map(encodeURIComponent).join("/")}`;
  return {
    url: safeHTTPSURL(promptEditURL) ?? fallback,
    label: "Edit on GitHub ↗",
    isNew: false,
  };
}

function renderPreviewOutput(pv: RoutinePreviewResponse): Node[] {
  const nodes: Node[] = [];
  if (!pv.valid) {
    const msg = pv.reason
      ? `Configuration check refused (${explainRoutineError(pv.reason)}):`
      : "Configuration check refused:";
    nodes.push(el("p", msg, "notice"));
    if (pv.errors && pv.errors.length > 0) {
      const ul = el("ul", undefined, "preview-errors");
      for (const err of pv.errors) {
        ul.append(el("li", err));
      }
      nodes.push(ul);
    }
  }

  if (pv.promptValid === false) {
    nodes.push(el("p", `Prompt check failed: ${pv.promptError || "invalid prompt"}`, "preview-prompt-error"));
  } else if (pv.promptValid === true) {
    nodes.push(el("p", "Prompt check passed.", "preview-prompt-ok"));
  }

  if (pv.valid) {
    const text = pv.manifest ?? pv.yaml ?? "";
    const path = pv.manifestPath ?? "repository.yaml";
    const actions = el("p", undefined, "file-actions");
    actions.append(...fileActions(() => text, path, `Copy ${path}`));
    nodes.push(actions, el("pre", text, "routine-yaml-preview"));
  }
  return nodes;
}

function configureCodeInput(inp: HTMLInputElement): void {
  inp.spellcheck = false;
  inp.autocomplete = "off";
}

export async function mountRoutineEditor(
  app: HTMLElement,
  owner: string,
  name: string,
  routine: string,
  get: typeof fetch = fetch,
  theme?: ThemeController,
  search = "",
): Promise<void> {
  const repo = `${owner}/${name}`;
  const main = settingsFrame(app, `Configure · ${repo} · ${routine}`, "/routines", theme);
  document.title = `Maku · Configure ${repo} · ${routine}`;

  const fromCatalog = new URLSearchParams(search).get("from") === "catalog";
  const crumbs = fromCatalog
    ? renderBreadcrumb([
        { label: "Routines", href: "/routines" },
        { label: "Catalog", href: "/routines/catalog" },
        { label: `${repo} · ${routine}` },
      ])
    : renderBreadcrumb([
        { label: "Routines", href: "/routines" },
        {
          label: `${repo} · ${routine}`,
          href: `/routines/${encodeURIComponent(owner)}/${encodeURIComponent(name)}/${encodeURIComponent(routine)}`,
        },
        { label: "Configure" },
      ]);

  const notice = el("p", "", "routine-notice");
  notice.setAttribute("role", "status");
  notice.setAttribute("aria-live", "polite");
  const body = el("div", undefined, "routine-editor-body");
  main.append(crumbs, notice, body);

  const load = async () => {
    body.replaceChildren(renderSkeleton(4));
    const [detailRes, catalogRes] = await Promise.allSettled([
      fetchRoutineDetail(owner, name, routine, get),
      fetchRoutineCatalog(get),
    ]);

    let detail: RoutineDetail | undefined;
    let catalog: RoutineCatalogResponse | undefined;
    if (catalogRes.status === "fulfilled") {
      catalog = catalogRes.value;
    }

    if (detailRes.status === "fulfilled") {
      detail = detailRes.value;
    } else {
      if (detailRes.reason instanceof SignInRequired) {
        body.replaceChildren(el("p", "signing in again…", "notice"));
        return;
      }
      const errCode = detailRes.reason instanceof Error ? detailRes.reason.message : String(detailRes.reason);
      if (errCode !== "not_found") {
        body.replaceChildren(renderFetchError(explainRoutineError(errCode), () => void load()));
        return;
      }
      if (catalogRes.status === "rejected") {
        if (catalogRes.reason instanceof SignInRequired) {
          body.replaceChildren(el("p", "signing in again…", "notice"));
          return;
        }
        body.replaceChildren(renderFetchError(explainRoutineError(errCode), () => void load()));
        return;
      }
    }

    const spec: CatalogRoutine | undefined =
      detail?.spec ?? catalog?.routines.find((r) => r.name === routine);
    const cap: CatalogRepoCapability | undefined = catalog?.capabilities.find(
      (c) => c.repository.toLowerCase() === repo.toLowerCase(),
    );

    if (!detail && (!spec || !cap)) {
      body.replaceChildren(el("p", explainRoutineError("not_found"), "notice"));
      return;
    }

    const isBound = Boolean(detail);
    const canWrite = detail?.canWrite ?? cap?.canWrite ?? false;
    const uiWrite = detail?.uiWrite ?? catalog?.uiWrite ?? false;
    const definedIn = detail?.definedIn;
    const upkeepDailyUSD = detail?.upkeepDailyUSD ?? catalog?.upkeepDailyUSD ?? 0;
    const repoEgress = detail?.repoEgress ?? cap?.egress ?? [];
    const promptPath = detail?.promptPath || `.github/agent-factory/upkeep/${routine}.md`;
    const initialRepoPromptText = detail?.repoPromptText ?? "";

    const editor = el("form", undefined, "routine-editor");
    editor.onsubmit = (ev) => ev.preventDefault();

    // Header metadata card.
    const metaCard = el("section", undefined, "routine-card routine-editor-summary");
    const head = el("div", undefined, "routine-card-head");
    const titleWrap = el("div", undefined, "routine-title-wrap");
    titleWrap.append(el("h2", routine, "routine-detail-title"), el("span", repo, "routine-repo"));
    const badges = el("div", undefined, "routine-badges");
    badges.append(
      el("span", isBound ? `source: ${definedIn ?? "gitops"}` : "new binding", "routine-fact-badge"),
      el("span", `probe: ${spec?.probeKind ?? detail?.probeKind ?? "none"}`, "routine-fact-badge"),
      el("span", uiWrite ? "Direct cluster save (uiWrite)" : "YAML export mode", "routine-fact-badge"),
    );
    head.append(titleWrap, badges);
    metaCard.append(head);
    if (detail?.description || spec?.description) {
      metaCard.append(el("p", detail?.description ?? spec?.description ?? "", "routine-desc"));
    }
    if (!canWrite) {
      metaCard.append(el("p", "Maintainer write access on this repository is required to preview or save changes.", "notice"));
    }
    editor.append(metaCard);

    // 1. Delivery mode cards + declarative pause.
    const modeSection = el("fieldset", undefined, "routine-editor-section");
    modeSection.append(el("legend", "Delivery mode"));
    const modeGroup = el("div", undefined, "routine-mode-cards");
    modeGroup.setAttribute("role", "radiogroup");
    modeGroup.setAttribute("aria-label", "Delivery mode");

    let currentMode = detail?.mode ?? "propose";
    const modeInputs: HTMLInputElement[] = [];
    for (const mc of MODE_CARDS) {
      const cardLabel = el("label", undefined, "routine-mode-card");
      const radio = el("input");
      radio.type = "radio";
      radio.name = "mode";
      radio.value = mc.value;
      radio.checked = currentMode === mc.value;
      radio.disabled = !canWrite;
      const textWrap = el("span", undefined, "routine-mode-card-text");
      textWrap.append(el("strong", mc.title), el("span", mc.summary, "routine-mode-card-desc"));
      cardLabel.append(radio, textWrap);
      modeGroup.append(cardLabel);
      modeInputs.push(radio);
    }

    const pausedLabel = el("label", undefined, "routine-editor-check");
    const pausedInput = el("input");
    pausedInput.type = "checkbox";
    pausedInput.name = "paused";
    pausedInput.checked = Boolean(detail?.declarativePaused);
    pausedInput.disabled = !canWrite;
    pausedLabel.append(
      pausedInput,
      el("span", "Paused declaratively (temporarily halts cron while keeping mode and manual runs available)"),
    );

    modeSection.append(modeGroup, pausedLabel);
    editor.append(modeSection);

    // 2. Schedule & frequency presets + live Next 3 runs.
    const schedSection = el("fieldset", undefined, "routine-editor-section");
    schedSection.append(el("legend", "Schedule (UTC)"));
    const schedRow = el("div", undefined, "routine-editor-grid");

    const presetField = el("label", undefined, "editor-field");
    presetField.append(el("span", "Frequency preset"));
    const presetSelect = el("select");
    presetSelect.name = "cronPreset";
    presetSelect.setAttribute("aria-label", "Frequency preset");
    presetSelect.disabled = !canWrite;
    for (const p of CRON_PRESETS) {
      const opt = el("option", p.cron ? `${p.label} (${p.cron})` : p.label);
      opt.value = p.id;
      presetSelect.append(opt);
    }
    presetField.append(presetSelect);

    const initialCron = detail?.cron ?? "0 3 * * 1,4";
    const cronErrorId = `cron-err-${Math.random().toString(36).slice(2, 8)}`;
    const cronField = el("label", undefined, "editor-field");
    cronField.append(el("span", "5-field UTC cron"));
    const cronInput = el("input");
    cronInput.type = "text";
    cronInput.name = "cron";
    cronInput.value = initialCron;
    cronInput.placeholder = "0 3 * * 1,4…";
    cronInput.disabled = !canWrite;
    configureCodeInput(cronInput);
    cronInput.setAttribute("aria-label", "5-field UTC cron");
    cronInput.setAttribute("aria-describedby", cronErrorId);
    cronField.append(cronInput);
    presetSelect.value = presetForCron(initialCron);

    const disabledHint = el(
      "p",
      "Schedule is inactive while mode is disabled or paused declaratively.",
      "routine-editor-hint",
    );
    const syncDisabledHint = () => {
      disabledHint.hidden = currentMode !== "disabled" && !pausedInput.checked;
    };
    syncDisabledHint();

    const previewWrap = el("div", undefined, "cron-preview-box");
    const previewLabel = el("p", "Next 3 scheduled runs", "routine-spec-label");
    const previewError = el("p", "", "cron-preview-error");
    previewError.id = cronErrorId;
    previewError.setAttribute("aria-live", "polite");
    previewError.hidden = true;
    const nextList = el("ol", undefined, "cron-next-runs");
    previewWrap.append(previewLabel, previewError, nextList);

    const refreshCronPreview = (): string | null => {
      const expr = cronInput.value;
      const err = validateCron(expr);
      if (err) {
        cronInput.setAttribute("aria-invalid", "true");
        previewError.hidden = false;
        previewError.textContent = err;
        nextList.replaceChildren();
        return err;
      }
      try {
        const runs = nextRuns(expr, new Date(), 3);
        cronInput.removeAttribute("aria-invalid");
        previewError.hidden = true;
        previewError.textContent = "";
        nextList.replaceChildren(
          ...runs.map((d) => {
            const li = el("li");
            li.append(el("span", formatLocalDateTimeDate(d), "cron-run-local"), el("code", formatUTCDateTime(d), "cron-run-utc"));
            return li;
          }),
        );
        return null;
      } catch (e) {
        const msg = e instanceof Error ? e.message : String(e);
        cronInput.setAttribute("aria-invalid", "true");
        previewError.hidden = false;
        previewError.textContent = msg;
        nextList.replaceChildren();
        return msg;
      }
    };

    presetSelect.onchange = () => {
      const found = CRON_PRESETS.find((p) => p.id === presetSelect.value);
      if (found && found.cron) {
        cronInput.value = found.cron;
        refreshCronPreview();
      }
    };
    cronInput.oninput = () => {
      presetSelect.value = presetForCron(cronInput.value);
      refreshCronPreview();
    };
    refreshCronPreview();

    schedRow.append(presetField, cronField);
    schedSection.append(schedRow, disabledHint, previewWrap);
    editor.append(schedSection);

    // 3. Bounds, parameters & yield breaker (essential fields visible; optional overrides folded).
    const boundsSection = el("fieldset", undefined, "routine-editor-section");
    boundsSection.append(el("legend", "Bounds, parameters & yield breaker"));

    const ceilingMaxPRs = spec?.maxOpenPRs ?? detail?.maxOpenPRs ?? 1;
    const ceilingMaxCost = spec?.maxCostUSD ?? detail?.maxCostUSD ?? 2.0;

    const maxPRsField = el("label", undefined, "editor-field");
    maxPRsField.append(el("span", `Max open PRs (ceiling: ${ceilingMaxPRs})`));
    const maxPRsInput = el("input");
    maxPRsInput.type = "number";
    maxPRsInput.name = "maxOpenPRs";
    maxPRsInput.min = "1";
    maxPRsInput.max = String(Math.min(5, ceilingMaxPRs || 5));
    maxPRsInput.value = String(detail?.maxOpenPRs ?? ceilingMaxPRs ?? 1);
    maxPRsInput.disabled = !canWrite;
    maxPRsField.append(maxPRsInput);

    const maxCostField = el("label", undefined, "editor-field");
    maxCostField.append(
      el("span", `Max cost / run USD (ceiling: ${formatUSD(ceilingMaxCost)} · daily upkeep: ${formatUSD(upkeepDailyUSD)})`),
    );
    const maxCostInput = el("input");
    maxCostInput.type = "number";
    maxCostInput.name = "maxCostUSD";
    maxCostInput.min = "0";
    maxCostInput.step = "0.01";
    maxCostInput.value = String(detail?.maxCostUSD ?? ceilingMaxCost ?? 2.0);
    maxCostInput.disabled = !canWrite;
    maxCostField.append(maxCostInput);

    boundsSection.append(maxPRsField, maxCostField);

    const probeKind = spec?.probeKind ?? detail?.probeKind ?? "none";

    // Fill-once parameters: required ones stay visible; optional ones go into Advanced fold.
    const fillOnceInputs = new Map<FillOnceKey, HTMLInputElement>();
    const optionalFillOnceFields: HTMLElement[] = [];
    const fixedReadonly: string[] = [];
    const reqSet = new Set(spec?.requires ?? []);
    for (const key of FILL_ONCE_KEYS) {
      const specVal = spec?.[key];
      if (specVal) {
        fixedReadonly.push(`${key}: ${specVal}`);
        continue;
      }
      const needByProbe =
        (key === "probeWorkflow" && probeKind === "actions-findings") ||
        (key === "oracleClass" && probeKind === "dead-links");
      const isRequired = reqSet.has(key) || needByProbe;
      const curVal = detail?.[key] ?? "";
      if (!isRequired && !curVal && probeKind === "none") continue;
      const meta = FILL_ONCE_LABELS[key];
      const f = el("label", undefined, "editor-field");
      f.append(el("span", `${meta.label}${isRequired ? " (required)" : ""}`));
      const inp = el("input");
      inp.type = "text";
      inp.name = key;
      inp.placeholder = meta.placeholder;
      inp.value = curVal;
      inp.disabled = !canWrite;
      configureCodeInput(inp);
      f.append(inp);
      if (isRequired) {
        boundsSection.append(f);
      } else {
        optionalFillOnceFields.push(f);
      }
      fillOnceInputs.set(key, inp);
    }
    if (fixedReadonly.length > 0) {
      boundsSection.append(
        el("p", `Fixed on Routine (not repeated in binding): ${fixedReadonly.join(" · ")}`, "routine-editor-hint"),
      );
    }

    // Advanced overrides fold (cooldown, workflow, dataClass, maxFindings, optional fill-once, egress).
    const advancedDetails = el("details", undefined, "routine-advanced-details");
    advancedDetails.append(el("summary", "Advanced bounds, workflow & egress (optional)"));

    const cooldownField = el("label", undefined, "editor-field");
    cooldownField.append(el("span", `Cooldown (blank inherits ${spec?.cooldown || "none"})`));
    const cooldownInput = el("input");
    cooldownInput.type = "text";
    cooldownInput.name = "cooldown";
    cooldownInput.placeholder = `${spec?.cooldown || "24h"}…`;
    cooldownInput.value = detail?.cooldown ?? "";
    cooldownInput.disabled = !canWrite;
    configureCodeInput(cooldownInput);
    cooldownField.append(cooldownInput);

    const workflowField = el("label", undefined, "editor-field");
    workflowField.append(el("span", `Workflow (blank inherits ${spec?.workflow || "default"})`));
    const workflowInput = el("input");
    workflowInput.type = "text";
    workflowInput.name = "workflow";
    workflowInput.placeholder = `${spec?.workflow || "quick"}…`;
    workflowInput.value = detail?.workflow && detail.workflow !== spec?.workflow ? detail.workflow : "";
    workflowInput.disabled = !canWrite;
    configureCodeInput(workflowInput);
    workflowField.append(workflowInput);

    const dataClassField = el("label", undefined, "editor-field");
    dataClassField.append(el("span", `Data class (Routine: ${spec?.dataClass || "default"})`));
    const dataClassSelect = el("select");
    dataClassSelect.name = "dataClass";
    dataClassSelect.setAttribute("aria-label", "Data class");
    dataClassSelect.disabled = !canWrite;
    for (const [val, label] of [
      ["", `Inherit (${spec?.dataClass || "default"})`],
      ["public", "public"],
      ["internal", "internal"],
    ] as const) {
      const opt = el("option", label);
      opt.value = val;
      if (spec?.dataClass === "internal" && val === "public") {
        opt.disabled = true;
      }
      dataClassSelect.append(opt);
    }
    dataClassSelect.value =
      detail?.dataClass && detail.dataClass !== spec?.dataClass ? detail.dataClass : "";
    dataClassField.append(dataClassSelect);

    advancedDetails.append(cooldownField, workflowField, dataClassField);

    let maxFindingsInput: HTMLInputElement | undefined;
    if (probeKind !== "none") {
      const mfCeiling = spec?.maxFindings || 16;
      const mfField = el("label", undefined, "editor-field");
      mfField.append(el("span", `Max findings (ceiling: ${mfCeiling})`));
      maxFindingsInput = el("input");
      maxFindingsInput.type = "number";
      maxFindingsInput.name = "maxFindings";
      maxFindingsInput.min = "1";
      maxFindingsInput.max = String(Math.min(16, mfCeiling));
      maxFindingsInput.value = detail?.maxFindings ? String(detail.maxFindings) : "";
      maxFindingsInput.disabled = !canWrite;
      mfField.append(maxFindingsInput);
      advancedDetails.append(mfField);
    }

    for (const f of optionalFillOnceFields) {
      advancedDetails.append(f);
    }

    const egressCheckboxes: { value: string; input: HTMLInputElement }[] = [];
    const egressCandidates = [...new Set([...(spec?.egress ?? []), ...repoEgress, ...(detail?.egress ?? [])])];
    if (egressCandidates.length > 0) {
      const egWrap = el("div", undefined, "routine-egress-group");
      egWrap.append(el("span", "Egress allowlists", "routine-spec-label"));
      const egList = el("div", undefined, "routine-egress-options");
      const initialEgress = new Set(
        detail?.egress ?? (spec?.egress?.length ? spec.egress.filter((e) => repoEgress.includes(e)) : []),
      );
      const repoSet = new Set(repoEgress);
      const specEgressSet = spec?.egress?.length ? new Set(spec.egress) : undefined;
      for (const eg of egressCandidates) {
        const lbl = el("label", undefined, "routine-editor-check");
        const cb = el("input");
        cb.type = "checkbox";
        cb.name = "egress";
        cb.value = eg;
        const allowed = repoSet.has(eg) && (!specEgressSet || specEgressSet.has(eg));
        cb.disabled = !canWrite || !allowed;
        cb.checked = allowed && initialEgress.has(eg);
        lbl.append(cb, el("span", allowed ? eg : `${eg} (unavailable)`));
        egList.append(lbl);
        egressCheckboxes.push({ value: eg, input: cb });
      }
      egWrap.append(egList);
      advancedDetails.append(egWrap);
    }

    if (
      Boolean(cooldownInput.value) ||
      Boolean(workflowInput.value) ||
      Boolean(dataClassSelect.value) ||
      Boolean(maxFindingsInput?.value) ||
      optionalFillOnceFields.some((f) => Boolean(f.querySelector("input")?.value))
    ) {
      advancedDetails.open = true;
    }
    boundsSection.append(advancedDetails);

    // Yield breaker overrides (collapsible).
    const rtBreaker = spec?.breaker ?? detail?.breaker ?? {
      window: 10,
      minSamples: 5,
      minMergeRate: 0.25,
      maxConsecutiveClosed: 3,
      maxConsecutiveFailures: 3,
    };
    const curBreaker = detail?.breaker;
    const breakerDetails = el("details", undefined, "routine-breaker-details");
    breakerDetails.append(el("summary", "Yield breaker overrides (optional)"));
    const brWindow = el("input");
    const brSamples = el("input");
    const brRate = el("input");
    const brClosed = el("input");
    const brFailed = el("input");
    for (const [inp, nameAttr, label, min, max, step, placeholder, initial] of [
      [brWindow, "breakerWindow", `Window (Routine: ${rtBreaker.window})`, "1", "100", "1", String(rtBreaker.window), curBreaker && curBreaker.window !== rtBreaker.window ? String(curBreaker.window) : ""],
      [brSamples, "breakerMinSamples", `Min samples (Routine: ${rtBreaker.minSamples})`, "1", "100", "1", String(rtBreaker.minSamples), curBreaker && curBreaker.minSamples !== rtBreaker.minSamples ? String(curBreaker.minSamples) : ""],
      [brRate, "breakerMinMergeRate", `Min merge rate 0..1 (Routine min: ${rtBreaker.minMergeRate})`, String(rtBreaker.minMergeRate), "1", "0.05", String(rtBreaker.minMergeRate), curBreaker && curBreaker.minMergeRate !== rtBreaker.minMergeRate ? String(curBreaker.minMergeRate) : ""],
      [brClosed, "breakerMaxConsecutiveClosed", `Max consecutive closed (ceiling: ${rtBreaker.maxConsecutiveClosed})`, "1", String(rtBreaker.maxConsecutiveClosed), "1", String(rtBreaker.maxConsecutiveClosed), curBreaker && curBreaker.maxConsecutiveClosed !== rtBreaker.maxConsecutiveClosed ? String(curBreaker.maxConsecutiveClosed) : ""],
      [brFailed, "breakerMaxConsecutiveFailures", `Max consecutive failures (ceiling: ${rtBreaker.maxConsecutiveFailures})`, "1", String(rtBreaker.maxConsecutiveFailures), "1", String(rtBreaker.maxConsecutiveFailures), curBreaker && curBreaker.maxConsecutiveFailures !== rtBreaker.maxConsecutiveFailures ? String(curBreaker.maxConsecutiveFailures) : ""],
    ] as const) {
      inp.type = "number";
      inp.name = nameAttr;
      inp.min = min;
      inp.max = max;
      inp.step = step;
      inp.placeholder = placeholder;
      inp.value = initial;
      inp.disabled = !canWrite;
      const f = el("label", undefined, "editor-field");
      f.append(el("span", label), inp);
      breakerDetails.append(f);
    }
    boundsSection.append(breakerDetails);

    // Read-only Routine meaning <details>.
    if (spec) {
      const specDet = el("details", undefined, "routine-spec-details");
      specDet.append(el("summary", "Routine definition (read-only)"));
      const ul = el("ul", undefined, "routine-spec-list");
      ul.append(
        el("li", `Probe: ${spec.probeKind || "none"}${spec.requireConsecutive ? ` (requireConsecutive: ${spec.requireConsecutive})` : ""}`),
        el("li", `Ceilings: maxOpenPRs ${spec.maxOpenPRs}, maxCostUSD ${formatUSD(spec.maxCostUSD)}`),
      );
      if (spec.protectedPaths?.length) {
        ul.append(el("li", `Protected paths: ${spec.protectedPaths.join(", ")}`));
      }
      specDet.append(ul);
      boundsSection.append(specDet);
    }

    editor.append(boundsSection);

    // 4. Split-view Prompt & Skill panel.
    const promptSection = el("fieldset", undefined, "routine-editor-section");
    promptSection.append(el("legend", `Repository prompt & skill (${promptPath})`));

    const repoPromptLabel = el("label", undefined, "routine-editor-check");
    const repoPromptInput = el("input");
    repoPromptInput.type = "checkbox";
    repoPromptInput.name = "repoPrompt";
    repoPromptInput.checked = detail ? detail.repoPrompt !== false : true;
    repoPromptInput.disabled = !canWrite;
    repoPromptLabel.append(repoPromptInput, el("span", `Load ${promptPath} from default branch when present`));

    const skillField = el("label", undefined, "editor-field");
    skillField.append(el("span", "Optional Agent Skill (.agents/skills/<skill>/SKILL.md)"));
    const skillInput = el("input");
    skillInput.type = "text";
    skillInput.name = "skill";
    skillInput.placeholder = "docs-link-fixer…";
    skillInput.value = detail?.skill ?? "";
    skillInput.disabled = !canWrite;
    configureCodeInput(skillInput);
    skillField.append(skillInput);

    const ghInfo = githubPromptURL(owner, name, promptPath, detail?.promptExists, detail?.promptEditURL, isBound, initialRepoPromptText);
    const ghLink = externalLink(ghInfo.url, ghInfo.label, "routine-github-prompt-link");

    const split = el("div", undefined, "routine-prompt-split");
    const leftPane = el("div", undefined, "routine-prompt-pane");
    leftPane.append(
      el("h3", "Default prompt (Routine fallback)"),
      el("pre", detail?.defaultPrompt ?? spec?.defaultPrompt ?? "(none)", "routine-prompt-text"),
    );

    const rightPane = el("div", undefined, "routine-prompt-pane");
    const rightHead = el("div", undefined, "routine-prompt-pane-head");
    rightHead.append(el("h3", `Repo prompt draft (${promptPath} · saved on GitHub)`));
    const rightActions = el("div", undefined, "routine-prompt-pane-actions");
    const copyPromptBtn = el("button", "Copy prompt");
    copyPromptBtn.type = "button";
    copyPromptBtn.dataset.act = "copy-prompt";
    let copyTimer: ReturnType<typeof setTimeout> | undefined;
    copyPromptBtn.onclick = async () => {
      try {
        await navigator.clipboard.writeText(promptTextarea.value);
        copyPromptBtn.textContent = "Copied";
      } catch {
        copyPromptBtn.textContent = "Copy failed";
      }
      clearTimeout(copyTimer);
      copyTimer = setTimeout(() => {
        copyPromptBtn.textContent = "Copy prompt";
      }, 1500);
    };
    rightActions.append(copyPromptBtn);
    if (ghLink) rightActions.append(ghLink);
    rightHead.append(rightActions);

    const promptStatusId = `prompt-status-${Math.random().toString(36).slice(2, 8)}`;
    const promptTextarea = el("textarea");
    promptTextarea.name = "repoPromptText";
    promptTextarea.rows = 10;
    promptTextarea.spellcheck = false;
    promptTextarea.readOnly = !canWrite;
    promptTextarea.placeholder = "## Goal\nDescribe the routine goal.\n\n## Verify\n- Run verification check\n";
    promptTextarea.setAttribute("aria-label", "Repository prompt markdown");
    promptTextarea.setAttribute("aria-describedby", promptStatusId);
    promptTextarea.value = initialRepoPromptText;

    const promptStatus = el("p", "", "routine-prompt-status");
    promptStatus.id = promptStatusId;
    promptStatus.setAttribute("aria-live", "polite");

    const updatePromptStatus = () => {
      const val = promptTextarea.value;
      if (ghLink && ghInfo.isNew) {
        const nextGH = githubPromptURL(owner, name, promptPath, detail?.promptExists, detail?.promptEditURL, isBound, val);
        const safe = safeHTTPSURL(nextGH.url);
        if (safe) ghLink.href = safe;
      }
      if (!val) {
        promptTextarea.removeAttribute("aria-invalid");
        promptStatus.dataset.valid = "empty";
        promptStatus.textContent =
          "0 / 4096 B · Optional in preview; custom prompts require a ## Verify section with bulleted checks (- or *), at most 5 sections.";
        return;
      }
      const res = validatePromptText(val);
      promptStatus.dataset.valid = String(res.valid);
      if (res.valid) {
        promptTextarea.removeAttribute("aria-invalid");
        promptStatus.textContent = `${res.bytes} / ${MAX_PROMPT_BYTES} B · Valid prompt (${res.sections}/${MAX_PROMPT_SECTIONS} sections, ## Verify present)`;
      } else {
        promptTextarea.setAttribute("aria-invalid", "true");
        promptStatus.textContent = `${res.bytes} / ${MAX_PROMPT_BYTES} B · Invalid: ${res.error}`;
      }
    };
    promptTextarea.oninput = updatePromptStatus;
    updatePromptStatus();

    rightPane.append(rightHead, promptTextarea, promptStatus);
    split.append(leftPane, rightPane);
    promptSection.append(repoPromptLabel, skillField, split);
    editor.append(promptSection);

    // 5. Preview, Reset & Conditional Save actions.
    const actionsBar = el("div", undefined, "editor-actions routine-editor-actions");
    const previewBtn = el("button", "Check & Generate YAML", "primary");
    previewBtn.type = "button";
    previewBtn.dataset.act = "preview";
    previewBtn.disabled = !canWrite;
    actionsBar.append(previewBtn);

    let saveBtn: HTMLButtonElement | undefined;
    if (uiWrite) {
      saveBtn = el("button", "Save to cluster");
      saveBtn.type = "button";
      saveBtn.dataset.act = "save";
      saveBtn.disabled = !canWrite || definedIn === "gitops";
      actionsBar.append(saveBtn);
    }

    const resetBtn = el("button", "Reset");
    resetBtn.type = "button";
    resetBtn.dataset.act = "reset";
    resetBtn.disabled = !canWrite;
    actionsBar.append(resetBtn);

    const previewOut = el("div", undefined, "routine-preview-output");
    previewOut.setAttribute("aria-live", "polite");

    let isDirty = false;
    const beforeUnload = (ev: BeforeUnloadEvent) => {
      if (!editor.isConnected || !isDirty) return;
      ev.preventDefault();
      ev.returnValue = "";
    };
    if (typeof window !== "undefined") {
      window.addEventListener("beforeunload", beforeUnload);
    }

    const onFormEdited = () => {
      isDirty = true;
      previewOut.replaceChildren();
    };
    editor.addEventListener("input", onFormEdited);
    editor.addEventListener("change", onFormEdited);

    for (const radio of modeInputs) {
      radio.onchange = () => {
        if (radio.checked) currentMode = radio.value;
        syncDisabledHint();
      };
    }
    pausedInput.onchange = syncDisabledHint;

    resetBtn.onclick = () => {
      isDirty = false;
      void load();
    };

    const buildBinding = (): RoutineBindingPayload => {
      const b: RoutineBindingPayload = {
        name: routine,
        mode: currentMode,
      };
      if (pausedInput.checked) b.paused = true;
      const cronVal = cronInput.value.trim();
      if (cronVal) b.cron = cronVal;
      if (!repoPromptInput.checked) b.repoPrompt = false;

      const skillVal = skillInput.value.trim();
      if (skillVal) b.skill = skillVal;
      const wfVal = workflowInput.value.trim();
      if (wfVal) b.workflow = wfVal;
      if (dataClassSelect.value) b.dataClass = dataClassSelect.value;

      const maxPRsVal = Number(maxPRsInput.value);
      if (maxPRsInput.value.trim() !== "" && !Number.isNaN(maxPRsVal)) b.maxOpenPRs = maxPRsVal;
      const maxCostVal = Number(maxCostInput.value);
      if (maxCostInput.value.trim() !== "" && !Number.isNaN(maxCostVal)) b.maxCostUSD = maxCostVal;

      const cooldownVal = cooldownInput.value.trim();
      if (cooldownVal) b.cooldown = cooldownVal;

      if (maxFindingsInput && maxFindingsInput.value.trim() !== "") {
        const mf = Number(maxFindingsInput.value);
        if (!Number.isNaN(mf)) b.maxFindings = mf;
      }

      for (const [key, inp] of fillOnceInputs.entries()) {
        const v = inp.value.trim();
        if (v) b[key] = v;
      }

      const checkedEgress = egressCheckboxes.filter((e) => e.input.checked).map((e) => e.value);
      if (checkedEgress.length > 0) b.egress = checkedEgress;

      const br: Partial<RoutineBreaker> = {};
      if (brWindow.value.trim() !== "") br.window = Number(brWindow.value);
      if (brSamples.value.trim() !== "") br.minSamples = Number(brSamples.value);
      if (brRate.value.trim() !== "") br.minMergeRate = Number(brRate.value);
      if (brClosed.value.trim() !== "") br.maxConsecutiveClosed = Number(brClosed.value);
      if (brFailed.value.trim() !== "") br.maxConsecutiveFailures = Number(brFailed.value);
      if (Object.keys(br).length > 0) b.breaker = br;

      return b;
    };

    const ensureCronValid = (): boolean => {
      if (currentMode === "disabled") return true;
      const err = refreshCronPreview();
      if (err) {
        notice.dataset.tone = "bad";
        notice.textContent = `Fix the schedule before continuing: ${err}`;
        cronInput.focus();
        return false;
      }
      return true;
    };

    previewBtn.onclick = async () => {
      notice.replaceChildren();
      notice.dataset.tone = "";
      if (!ensureCronValid()) return;
      previewBtn.disabled = true;
      previewBtn.setAttribute("aria-busy", "true");
      previewBtn.textContent = "Checking…";
      try {
        const binding = buildBinding();
        const promptText = promptTextarea.value;
        const payload = promptText ? { binding, promptText } : { binding };
        const r = await api(
          `/api/routines/${encodeURIComponent(owner)}/${encodeURIComponent(name)}/preview`,
          {
            method: "POST",
            credentials: "same-origin",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify(payload),
          },
          get,
        );
        if (!r.ok) throw new Error(await refusalCode(r));
        const pv = (await r.json()) as RoutinePreviewResponse;
        previewOut.replaceChildren(...renderPreviewOutput(pv));
      } catch (e) {
        if (e instanceof SignInRequired) return;
        notice.dataset.tone = "bad";
        notice.textContent = explainRoutineError(e instanceof Error ? e.message : String(e));
      } finally {
        previewBtn.disabled = !canWrite;
        previewBtn.removeAttribute("aria-busy");
        previewBtn.textContent = "Check & Generate YAML";
      }
    };

    if (saveBtn) {
      saveBtn.onclick = async () => {
        notice.replaceChildren();
        notice.dataset.tone = "";
        if (!ensureCronValid()) return;
        saveBtn!.disabled = true;
        saveBtn!.setAttribute("aria-busy", "true");
        saveBtn!.textContent = "Saving…";
        try {
          const binding = buildBinding();
          const r = await api(
            `/api/routines/${encodeURIComponent(owner)}/${encodeURIComponent(name)}/${encodeURIComponent(routine)}`,
            {
              method: "PUT",
              credentials: "same-origin",
              headers: { "Content-Type": "application/json" },
              body: JSON.stringify(binding),
            },
            get,
          );
          if (!r.ok) throw new Error(await refusalCode(r));
          isDirty = false;
          const promptEdited = promptTextarea.value !== initialRepoPromptText;
          notice.dataset.tone = "ok";
          notice.textContent = promptEdited
            ? `Saved ${repo} · ${routine} to cluster. Note: your prompt draft is not written to GitHub — copy or commit it via GitHub.`
            : `Saved ${repo} · ${routine} to cluster.`;
        } catch (e) {
          if (e instanceof SignInRequired) return;
          notice.dataset.tone = "bad";
          notice.textContent = explainRoutineError(e instanceof Error ? e.message : String(e));
        } finally {
          saveBtn!.disabled = !canWrite || definedIn === "gitops";
          saveBtn!.removeAttribute("aria-busy");
          saveBtn!.textContent = "Save to cluster";
        }
      };
    }

    editor.append(actionsBar);
    if (uiWrite && definedIn === "gitops") {
      editor.append(
        el("p", "Managed in GitOps repository config. Export YAML and commit via pull request instead.", "routine-editor-hint"),
      );
    }
    editor.append(previewOut);
    body.replaceChildren(editor);
  };

  await load();
}
