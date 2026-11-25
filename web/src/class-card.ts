// SPDX-License-Identifier: Apache-2.0

// One merge class on its repository's page: its name and mode, where its conditions are, its
// binding, its evidence toward promotion, and its own promotion request and exception. What serves the class (the Repository) is the page header's to say, once. The
// broker decides every write: a button here is only a convenience it would also refuse.
// Runtime values come from settings-api.ts; settings.ts is imported for its types only, as it
// imports this module.

import { modeMeaning } from "./policy-describe";
import { patternDetails, policyWords } from "./policy-words";
import { blockerActions, explain, LADDER_LEGEND, policyState, post, timeText, trackRecord } from "./settings-api";
import type { Binding, Catalogue, CatalogueClass, CatalogueProfile, ClassReport, Evidence, Promotion, Proposal, RepositoryPage } from "./settings";

export const READING = "Reading evidence from GitHub…";

// now is the clock a card reads relative times against; Date.now when unset.
export interface CardContext { page: RepositoryPage; get: typeof fetch; say: HTMLElement; reload: () => void; main: HTMLElement; now?: () => number }

function el<K extends keyof HTMLElementTagNameMap>(tag: K, text?: string, cls?: string): HTMLElementTagNameMap[K] {
  const e = document.createElement(tag);
  if (text !== undefined) e.textContent = text;
  if (cls) e.className = cls;
  return e;
}

// The factory's unproposed codes, in words; an unknown code is shown with spaces.
const unproposedWords: Record<string, string> = {
  not_shadow: "the class is not in shadow",
  not_in_repository_policy: "the class is not declared in the repository's policy file",
};

// evidenceBar is a class's clean decisions toward the minimum; eligible says whether it may promote
// (an approved exception can make it so where the evidence alone does not).
export function evidenceBar(e: Evidence, eligible = e.eligible): HTMLElement {
  const box = el("div", undefined, "evidence-bar");
  box.setAttribute("role", "meter");
  box.setAttribute("aria-valuemin", "0");
  box.setAttribute("aria-valuenow", String(Math.min(e.clean, e.minimum)));
  box.setAttribute("aria-valuemax", String(e.minimum));
  box.setAttribute("aria-label", "clean decisions toward promotion");
  // A blocked class is not one step from promotion, however many clean decisions it has: its bar
  // never reads as full.
  const blocked = !eligible && (e.blockers ?? []).length > 0;
  if (blocked) box.dataset.blocked = "true";
  const fill = el("span", undefined, "evidence-fill");
  fill.style.width = `${Math.min(blocked ? 95 : 100, Math.round((100 * e.clean) / Math.max(1, e.minimum)))}%`;
  const track = el("span", undefined, "evidence-track");
  track.append(fill);
  box.append(track, el("span", e.clean > e.minimum
    ? `${e.minimum} of ${e.minimum} clean decisions (${e.clean} recorded)`
    : `${e.clean} of ${e.minimum} clean decisions to promote`, "evidence-count"));
  return box;
}

// generationLine is the shadow decisions merged unchanged per policy generation (Q6, PR13):
// "gen 4: 3 clean · gen 3: 17 clean · before the binding: 12"; empty without a split. Promotion
// counts them all.
export function generationLine(e: Evidence): string {
  return (e.shadow.byGeneration ?? []).map((g) => {
    const counts = `${g.mergedUnchanged} clean${g.changed ? `, ${g.changed} changed` : ""}`;
    return g.before ? `before the binding: ${counts}` : `gen ${g.generation}: ${counts}`;
  }).join(" \u00b7 ");
}

// bindingRows links the reusable definition; the repository page reports a refusal once.
function bindingRows(card: HTMLElement, c: ClassReport, ctx: CardContext) {
  const r = ctx.page.report;
  const bound = r.bindings?.some((x) => x.name === c.class) ||
    (policyState(r) === "repository" && r.policy?.mergePolicies?.some((x) => x.name === c.class));
  if (!bound) return;
  const line = el("p", "Definition: ", "binding-policy");
  const a = el("a", c.class);
  a.href = `/settings/merge-policies/policies/${encodeURIComponent(c.class)}`;
  line.append(a);
  card.append(line);
}

// detail is a class's track record as one line of words.
const detail = (e: Evidence) => {
  const p = el("p", undefined, "evidence-detail");
  p.append(trackRecord(e));
  return p;
};

const ladder = (e: Evidence) => {
  const ol = el("ol", undefined, "evidence-ladder");
  ol.setAttribute("aria-label", "clean merges in a row, and the bad rate each proves");
  ol.title = LADDER_LEGEND;
  for (const s of e.ladder) {
    const li = el("li", `${s.merges} merges${s.reached ? ", reached" : ""}`);
    li.dataset.reached = String(s.reached);
    ol.append(li);
  }
  return ol;
};

// lastDecisionWords reads the factory's "would_merge:clean" as "would merge (clean)".
function lastDecisionWords(code: string): string {
  const [what, why] = code.split(":", 2);
  return `${what.replaceAll("_", " ")}${why ? ` (${why.replaceAll("_", " ")})` : ""}`;
}

// rolesOf is the roles a workflow runs, in run order: its resolved path (a scoper, a tester, custom
// read roles included; Maku #698, #704) without CI and the merge gate, else its template's roles
// from a broker that predates the path.
function rolesOf(p: CatalogueProfile): string[] {
  if (p.path?.length) return p.path.filter((r) => r !== "CI" && r !== "merge gate");
  return p.roles ?? [];
}

// profileLine is one workflow in words: its roles, description, ceiling and tests-first flag (no tier).
// The text is set as text, never as markup: a description is a maintainer's.
function profileLine(cat: Catalogue, name: string): string {
  const p = cat.profiles[name];
  if (!p) return `${name}: not in the factory's list of workflows`;
  const roles = rolesOf(p);
  const parts = [p.offered && roles.length ? `roles ${roles.join(" \u2192 ")}` : "roles chosen at triage"];
  if (p.description) parts.push(p.description);
  if (p.ceilingUSD && p.ceilingUSD > 0) parts.push(`up to $${p.ceilingUSD.toFixed(2)}`);
  if (p.testFirst) parts.push("tests first");
  return `${name}: ${parts.join(" \u00b7 ")}`;
}

// escalationWords is where a workflow's failed run steps, or "" without an escalation.
function escalationWords(p: CatalogueProfile | undefined): string {
  const e = p?.escalation;
  if (!e || (!e.outOfBudget && !e.notConverged)) return "";
  const to = [e.outOfBudget && `out of budget goes to ${e.outOfBudget}`, e.notConverged && `not converged goes to ${e.notConverged}`].filter(Boolean);
  return ` \u00b7 ${to.join("; ")}`;
}

// profilesRow is a class's Workflows row: the repository's default, then every other workflow a
// maintainer may pin, the same on every class since the workflow left the class (WF4), each with
// where its failed run escalates. Read-only (factory resources D9); undefined for a class the
// catalogue does not offer at intake, or no catalogue.
export function profilesRow(cat: Catalogue | null | undefined, cls: string): HTMLElement | undefined {
  const c: CatalogueClass | undefined = cat?.classes.find((x) => x.name === cls);
  if (!cat || !c) return undefined;
  const row = el("section", undefined, "class-profiles");
  row.append(el("h4", "Workflows"));
  const list = el("ul", undefined, "class-profile-list");
  const def = cat.defaultWorkflow;
  if (def) list.append(el("li", `Default ${profileLine(cat, def)}${escalationWords(cat.profiles[def])}`));
  for (const name of Object.keys(cat.profiles).sort()) {
    if (name !== def) list.append(el("li", `${profileLine(cat, name)}${escalationWords(cat.profiles[name])}`));
  }
  row.append(list);
  return row;
}

export function classCard(c: ClassReport, ctx: CardContext): HTMLElement {
  const now = () => (ctx.now ?? Date.now)();
  const card = el("article", undefined, "class-card");
  card.id = `class-${c.class}`;
  const head = el("header", undefined, "class-card-head");
  const m = modeMeaning(c.mode);
  // The badge is the word; what it means is the page's legend, and this tooltip.
  const badge = el("span", m.label, "mode-badge");
  badge.dataset.mode = c.mode;
  badge.title = m.meaning;
  head.append(el("h3", c.class), badge);
  card.append(head);

  const r = ctx.page.report;
  const binding = r.bindings?.find((x) => x.name === c.class)?.parameters;
  const spec = ctx.page.policySpecs?.[c.class] ?? {};
  card.append(el("p", policyWords(spec, binding), "policy-sentence"));
  const patterns = binding?.allowedFiles?.length ? binding.allowedFiles : (Array.isArray(spec.allowedFiles) ? spec.allowedFiles as string[] : []);
  if (patterns.length) card.append(patternDetails(patterns));
  bindingRows(card, c, ctx);
  const last = r.repositoryStatus?.status.classes?.find((x) => x.name === c.class);
  if (last?.lastDecision) {
    const p = el("p", `Last decision: ${lastDecisionWords(last.lastDecision)}`, "class-last");
    if (last.lastDecisionAt) p.append(", ", timeText(last.lastDecisionAt, now()));
    card.append(p);
  }

  const e = c.evidence;
  const act = el("div", undefined, "class-actions");
  const certainty = (e: Evidence, extras: Node[] = []) => {
    const fold = el("details", undefined, "policy-certainty");
    fold.append(el("summary", "How sure is this?"), ladder(e), detail(e), ...extras);
    return fold;
  };
  const words = (code: string) => unproposedWords[code] ?? code.replaceAll("_", " ");
  const notPromotable = () => {
    if (c.mode !== "shadow" || !c.unproposed) return;
    act.textContent = `Not promotable here: ${words(c.unproposed)}.`;
  };
  if (!e) {
    if (r.evidenceAt) card.append(el("p", "Evidence for this class is still being read.", "evidence-reading"));
    notPromotable();
  } else if (c.mode === "live") {
    card.append(el("p", `Live \u00b7 ${e.clean} clean decisions recorded.`, "evidence-live"), certainty(e));
  } else if (c.mode === "off" || c.mode === "disabled") {
    card.append(e.clean === 0 ? el("p", "Off: no decisions recorded.", "evidence-detail") : certainty(e));
  } else {
    const exc = ctx.page.exceptions.find((x) => x.class === c.class && x.state === "approved" && Date.parse(x.expiresAt ?? "") > Date.now());
    const eligible = e.eligible || (!!exc && e.clean >= exc.minimum && e.bad === 0 && e.drill === "passed");
    const extras: Node[] = [];
    const gens = generationLine(e);
    if (gens) extras.push(el("p", gens, "evidence-generations"));
    const reviewers = (e.shadow.reviewers ?? []).map((x) => `${x.model} ${x.mergedUnchanged}/${x.mergedUnchanged + x.changed}`).join(", ");
    if (reviewers) extras.push(el("p", `Reviewers: ${reviewers}`, "evidence-reviewers"));
    card.append(evidenceBar(e, eligible), certainty(e, extras));
    if (r.repositoryObject) {
      const where = policyState(r) === "repository" ? ".github/agent-factory.yaml" : r.repositoryManifestPath ?? "the Repository manifest";
      card.append(el("p", `To promote, set mode: live on the binding in ${where} and merge the change.`, "promote-where"));
    }
    card.append(el("p", eligible
      ? (e.eligible ? "Eligible to promote: enough clean decisions, no bad outcome, drill passed." : `Eligible to promote under exception ${exc!.id} (${exc!.minimum} clean decisions required).`)
      : `Blocks promotion: ${blockerActions(e).join(", ") || "no reason given"}.`, "evidence-eligibility"));
    // The broker refuses a second request while one is open (promotion_open): offer neither then.
    const open = ctx.page.promotions.some((p) => p.class === c.class && (p.state === "requested" || p.state === "pr_opened"));
    if (open) act.textContent = "";
    else if (c.proposal && ctx.page.you.maintainer && eligible) {
      const b = el("button", "Promote to live", "primary");
      b.type = "button";
      b.onclick = async () => {
        b.disabled = true;
        try {
          const out = (await post("/api/policy/promotions", { repository: r.repository, class: c.class, ...(exc && !e.eligible ? { exceptionId: exc.id } : {}) }, ctx.get)) as { promotion: Promotion; proposal: Proposal; command?: string };
          if (out.promotion.pullRequestURL) showPromotionPR(ctx.main, out.promotion.pullRequestURL);
          else showProposal(ctx.main, out.proposal, out.command);
        } catch (err) { ctx.say.textContent = explain(err instanceof Error ? err.message : String(err)); b.disabled = false; }
      };
      act.append(b);
    } else if (c.proposal && ctx.page.you.maintainer && !eligible) {
      const b = el("button", "Ask an operator for an exception");
      b.type = "button";
      b.onclick = () => exceptionForm(act, r.repository, c.class, r.minimum, ctx);
      act.append(b);
    } else notPromotable();
  }
  const profiles = profilesRow(ctx.page.catalogue, c.class);
  if (profiles) card.append(profiles);
  card.append(act);
  const ps = promotionsOf(c.class, ctx);
  if (ps) card.append(ps);
  const xs = exceptionsOf(c.class, ctx);
  if (xs) card.append(xs);
  return card;
}

function promotionsOf(cls: string, ctx: CardContext): HTMLElement | undefined {
  const box = el("div", undefined, "class-promotions");
  const ps = ctx.page.promotions.filter((p) => p.class === cls);
  if (ps.length === 0) return undefined;
  for (const p of ps) {
    const line = el("p", `Promotion requested by ${p.requestedBy} `);
    line.append(timeText(p.requestedAt, (ctx.now ?? Date.now)()), `: ${p.state === "pr_opened" ? "pull request opened" : p.state.replaceAll("_", " ")}`);
    if (p.pullRequest) {
      const a = el("a", ` #${p.pullRequest}`);
      a.href = p.pullRequestURL || `https://github.com/${p.repository}/pull/${p.pullRequest}`;
      line.append(a);
    }
    line.append(` (${p.exceptionId ? `under exception ${p.exceptionId}, ` : ""}threshold ${p.threshold})`);
    box.append(line);
  }
  return box;
}

function exceptionsOf(cls: string, ctx: CardContext): HTMLElement | undefined {
  const box = el("div", undefined, "class-exceptions");
  const xs = ctx.page.exceptions.filter((y) => y.class === cls);
  if (xs.length === 0) return undefined;
  for (const x of xs) {
    const line = el("p", `Exception: ${x.minimum} clean decisions instead of ${ctx.page.report.minimum}, ${x.state}` + (x.decidedBy ? ` by ${x.decidedBy}` : ""));
    const now = (ctx.now ?? Date.now)();
    if (x.expiresAt) line.append(Date.parse(x.expiresAt) < now ? ", expired " : " until ", timeText(x.expiresAt, now));
    line.append(`. \u201c${x.justification}\u201d`);
    box.append(line);
    if (ctx.page.you.operator && x.state === "requested") {
      const act = el("span", undefined, "class-actions");
      for (const d of ["approved", "refused"]) {
        const b = el("button", d === "approved" ? "Approve" : "Refuse");
        b.type = "button";
        b.onclick = async () => {
          try { await post(`/api/policy/exceptions/${x.id}`, { decision: d }, ctx.get); ctx.reload(); } catch (err) { ctx.say.textContent = explain(err instanceof Error ? err.message : String(err)); }
        };
        act.append(b);
      }
      box.append(act);
    }
  }
  return box;
}

// showProposal shows the one-line change and, without gitops, the command that opens it.
export function showProposal(main: HTMLElement, p: Proposal, command = "") {
  const box = el("section", undefined, "proposal");
  box.append(el("h2", "Your promotion pull request"));
  const lead = el("p", `Change line ${p.line} of ${p.path} from mode: shadow to mode: live, as below, then propose it as a pull request`);
  if (p.editURL) {
    const a = el("a", "edit the file on GitHub");
    a.href = p.editURL;
    lead.append(": ", a);
  } else lead.append(" to the repository Flux applies it from.");
  box.append(lead, el("pre", p.file, "proposal-file"));
  if (command) box.append(el("pre", command, "proposal-command"));
  const copy = el("button", "Copy the file");
  copy.type = "button";
  copy.onclick = () => void navigator.clipboard?.writeText(p.file);
  box.append(copy);
  main.prepend(box);
}

function showPromotionPR(main: HTMLElement, url: string) {
  const box = el("section", undefined, "proposal");
  box.append(el("h2", "Your promotion pull request"));
  const lead = el("p");
  const a = el("a", "Open the promotion pull request");
  a.href = url;
  lead.append(a, ". The factory opened it with the one-line mode change and its supporting decisions. A maintainer still merges it.");
  box.append(lead);
  main.prepend(box);
}

function exceptionForm(at: HTMLElement, repository: string, cls: string, minimum: number, ctx: CardContext) {
  const min = el("input");
  min.type = "number";
  min.min = "1";
  min.max = String(minimum - 1);
  min.setAttribute("aria-label", "minimum clean decisions");
  const why = el("textarea");
  why.maxLength = 2000;
  why.setAttribute("aria-label", "justification");
  why.placeholder = "Why this class may go live on fewer clean decisions (20 to 2,000 characters)";
  const b = el("button", "Ask");
  b.type = "button";
  b.onclick = async () => {
    try {
      await post("/api/policy/exceptions", { repository, class: cls, minimum: Number(min.value), justification: why.value }, ctx.get);
      ctx.reload();
    } catch (err) { ctx.say.textContent = explain(err instanceof Error ? err.message : String(err)); }
  };
  at.replaceChildren(min, why, b);
}
