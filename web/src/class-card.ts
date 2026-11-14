// SPDX-License-Identifier: Apache-2.0

// One merge class on its repository's page: its name and mode, where its conditions are, its
// binding, its evidence toward promotion, and its own promotion request and exception. What serves the class (the Repository) is the page header's to say, once. The
// broker decides every write: a button here is only a convenience it would also refuse.
// Runtime values come from settings-api.ts; settings.ts is imported for its types only, as it
// imports this module.

import { modeMeaning } from "./policy-describe";
import { blockerActions, explain, LADDER_LEGEND, policyState, post, timeText, trackRecord } from "./settings-api";
import type { Binding, Catalogue, CatalogueClass, ClassReport, Evidence, Promotion, Proposal, RepositoryPage } from "./settings";

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

// parameterWords are what a binding supplies, one per line, as "key: value".
function parameterWords(b: Binding): string[] {
  const out: string[] = [];
  for (const [k, v] of Object.entries(b)) {
    if (k === "name" || k === "mode" || v === undefined) continue;
    if (k === "requiresApproval") out.push(`requiresApproval: ${(v as { changedFiles: string[] }[]).map((a) => a.changedFiles.join(", ")).join("; ")}`);
    else out.push(`${k}: ${Array.isArray(v) ? v.join(", ") : String(v)}`);
  }
  return out;
}

// bindingRows are a bound class's binding (B16): its policy, linked, the generation it serves, the
// parameters this repository supplies, and why it is refused when it is.
function bindingRows(card: HTMLElement, c: ClassReport, ctx: CardContext) {
  const r = ctx.page.report;
  const b = r.bindings?.find((x) => x.name === c.class);
  if (!b) {
    // Under repository authority the binding is the file's (B15): its policy and what it supplies.
    const f = policyState(r) === "repository" ? r.policy?.mergePolicies?.find((x) => x.name === c.class) : undefined;
    if (!f) return;
    const line = el("p", "Policy ", "binding-policy");
    const a = el("a", c.class);
    a.href = `/settings/merge-policies/policies/${encodeURIComponent(c.class)}`;
    line.append(a, " \u00b7 Bound in .github/agent-factory.yaml");
    card.append(line);
    const params = parameterWords(f);
    if (params.length) {
      const ul = el("ul", undefined, "binding-parameters");
      for (const p of params) ul.append(el("li", p));
      card.append(el("p", "This repository supplies:", "binding-parameters-lead"), ul);
    }
    return;
  }
  const st = r.repositoryStatus?.status.classes?.find((x) => x.name === c.class);
  const line = el("p", "Policy ", "binding-policy");
  const a = el("a", c.class);
  a.href = `/settings/merge-policies/policies/${encodeURIComponent(c.class)}`;
  line.append(a);
  const gen = st?.policyGeneration ?? b.policyGeneration;
  if (b.accepted && gen) line.append(` \u00b7 Policy generation ${gen}`);
  card.append(line);
  if (!b.accepted) {
    const why = [b.reason ?? st?.reason, st?.message].filter(Boolean).join(": ");
    card.append(el("p", `Refused: ${why || "no reason given"}`, "binding-refused"),
      el("p", gen ? `Serving generation ${gen}` : "Not serving: the class falls back to review", "binding-serving"));
  }
  const params = parameterWords(b.parameters);
  if (params.length) {
    const ul = el("ul", undefined, "binding-parameters");
    for (const p of params) ul.append(el("li", p));
    card.append(el("p", "This repository supplies:", "binding-parameters-lead"), ul);
  }
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

// profileLine is one profile in words: its roles (or that they are chosen at triage) and its tier.
// The text is set as text, never as markup: a description is a maintainer's.
function profileLine(cat: Catalogue, name: string): string {
  const p = cat.profiles[name];
  if (!p) return `${name}: not in the factory's list of profiles`;
  const parts = [p.offered && p.roles?.length ? `roles ${p.roles.join(", ")}` : "roles chosen at triage"];
  if (p.tier) parts.push(`tier ${p.tier}`);
  if (p.testFirst) parts.push("tests first");
  return `${name}: ${parts.join(" \u00b7 ")}`;
}

// profilesRow is a class's Profiles row: the floor it runs under, the variants a maintainer may pin
// or the scoper may recommend, and where each failed signal escalates. Read-only (factory resources
// D9); undefined for a class the catalogue does not offer at intake, or no catalogue.
export function profilesRow(cat: Catalogue | null | undefined, cls: string): HTMLElement | undefined {
  const c: CatalogueClass | undefined = cat?.classes.find((x) => x.name === cls);
  if (!cat || !c) return undefined;
  const row = el("section", undefined, "class-profiles");
  row.append(el("h4", "Profiles"));
  const list = el("ul", undefined, "class-profile-list");
  list.append(el("li", c.floor ? `Floor ${profileLine(cat, c.floor)}` : "Floor: none declared, the factory runs the class as configured"));
  for (const v of c.allowed ?? []) list.append(el("li", `Variant ${profileLine(cat, v)}`));
  row.append(list);
  const e = c.escalation;
  if (e && (e.outOfBudget || e.notConverged)) {
    const to = [e.outOfBudget && `out of budget goes to ${e.outOfBudget}`, e.notConverged && `not converged goes to ${e.notConverged}`].filter(Boolean);
    row.append(el("p", `Escalation: ${to.join("; ")}.`, "class-escalation"));
  }
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
  if (r.repositoryObject) {
    card.append(el("p", "This class's conditions are its MergePolicy's: open the policy's page.", "class-summary"));
  } else {
    card.append(el("p", "This class's conditions are set in the factory's config and are not shown here.", "class-summary"));
  }
  bindingRows(card, c, ctx);
  const last = r.repositoryStatus?.status.classes?.find((x) => x.name === c.class);
  if (last?.lastDecision) {
    const p = el("p", `Last decision: ${lastDecisionWords(last.lastDecision)}`, "class-last");
    if (last.lastDecisionAt) p.append(", ", timeText(last.lastDecisionAt, now()));
    card.append(p);
  }

  const e = c.evidence;
  const act = el("div", undefined, "class-actions");
  // A live class has been promoted and an off one decides nothing: neither is moving toward
  // promotion, so neither shows the bar, the ladder or a promotion button, nor says it cannot.
  const words = (code: string) => unproposedWords[code] ?? code.replaceAll("_", " ");
  const notPromotable = () => {
    if (c.mode !== "shadow" || !c.unproposed) return;
    act.textContent = `Not promotable here: ${words(c.unproposed)}.`;
  };
  if (!e) {
    // The page says once that evidence is being read; a card speaks only for a class added since
    // the last read, when the rest of the page already has its evidence.
    if (r.evidenceAt) card.append(el("p", "Evidence for this class is still being read.", "evidence-reading"));
    notPromotable();
  } else if (c.mode === "live") {
    card.append(el("p", `Live \u00b7 ${e.clean} clean decisions recorded.`, "evidence-live"), detail(e));
  } else if (c.mode === "off") {
    card.append(e.clean === 0 ? el("p", "Off: no decisions recorded.", "evidence-detail") : detail(e));
  } else {
    const exc = ctx.page.exceptions.find((x) => x.class === c.class && x.state === "approved" && Date.parse(x.expiresAt ?? "") > Date.now());
    const eligible = e.eligible || (!!exc && e.clean >= exc.minimum && e.bad === 0 && e.drill === "passed");
    card.append(evidenceBar(e, eligible), ladder(e), detail(e));
    const gens = generationLine(e);
    if (gens) card.append(el("p", gens, "evidence-generations"));
    if (r.repositoryObject) {
      // B16: where a binding's promotion is made, by authority.
      const where = policyState(r) === "repository" ? ".github/agent-factory.yaml" : r.repositoryManifestPath ?? "the Repository manifest";
      card.append(el("p", `To promote, set mode: live on the binding in ${where} and merge the change.`, "promote-where"));
    }
    const reviewers = (e.shadow.reviewers ?? []).map((x) => `${x.model} ${x.mergedUnchanged}/${x.mergedUnchanged + x.changed}`).join(", ");
    if (reviewers) card.append(el("p", `Reviewers: ${reviewers}`, "evidence-reviewers"));
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
          const out = (await post("/api/policy/promotions", { repository: r.repository, class: c.class, ...(exc && !e.eligible ? { exceptionId: exc.id } : {}) }, ctx.get)) as { proposal: Proposal };
          showProposal(ctx.main, out.proposal);
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
      a.href = `https://github.com/${p.repository}/pull/${p.pullRequest}`;
      line.append(a);
    }
    line.append(` (${p.exceptionId ? `under exception ${p.exceptionId}, ` : ""}threshold ${p.threshold})`);
    const act = el("span", undefined, "class-actions");
    if (ctx.page.you.maintainer && (p.state === "requested" || p.state === "pr_opened")) promotionActions(act, p, ctx);
    box.append(line, act);
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

// showProposal shows the one-line change and where to submit it: GitHub's edit page takes no
// prefilled content for an existing file, so the file is shown whole, its changed line named.
export function showProposal(main: HTMLElement, p: Proposal) {
  const box = el("section", undefined, "proposal");
  box.append(el("h2", "Your promotion pull request"));
  const lead = el("p", `Change line ${p.line} of ${p.path} from mode: shadow to mode: live, as below, then propose it as a pull request`);
  if (p.editURL) {
    const a = el("a", "edit the file on GitHub");
    a.href = p.editURL;
    lead.append(": ", a);
  } else lead.append(" to the repository Flux applies it from."); // a Repository manifest (B5)
  box.append(lead, el("pre", p.file, "proposal-file"));
  const copy = el("button", "Copy the file");
  copy.type = "button";
  copy.onclick = () => void navigator.clipboard?.writeText(p.file);
  box.append(copy);
  main.prepend(box);
}

function promotionActions(act: HTMLElement, p: Promotion, ctx: CardContext) {
  const send = async (body: unknown) => {
    try { await post(`/api/policy/promotions/${p.id}`, body, ctx.get); ctx.reload(); } catch (err) { ctx.say.textContent = explain(err instanceof Error ? err.message : String(err)); }
  };
  if (p.state === "requested") {
    const n = el("input");
    n.type = "number";
    n.min = "1";
    n.placeholder = "PR #";
    n.setAttribute("aria-label", "pull request number");
    const b = el("button", "Record the pull request");
    b.type = "button";
    b.onclick = () => void send({ state: "pr_opened", pullRequest: Number(n.value) });
    act.append(n, b);
  } else {
    const m = el("button", "Merged");
    m.type = "button";
    m.onclick = () => void send({ state: "merged" });
    act.append(m);
  }
  const a = el("button", "Abandon");
  a.type = "button";
  a.onclick = () => void send({ state: "abandoned" });
  act.append(a);
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
