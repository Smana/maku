// SPDX-License-Identifier: Apache-2.0

// One merge class on its repository's page: its name and mode (with what the mode means), the
// class in words, its evidence toward promotion, and its own promotion request and exception. The
// broker decides every write: a button here is only a convenience it would also refuse.
// Runtime values come from settings-api.ts; settings.ts is imported for its types only, as it
// imports this module.

import { describeClass, modeMeaning } from "./policy-describe";
import type { PolicyFile } from "./policy-editor";
import { centralManifest, evidenceLine, explain, post, servedFrom } from "./settings-api";
import type { ClassReport, Evidence, Promotion, Proposal, RepositoryPage } from "./settings";

export const READING = "Reading evidence from GitHub…";

export interface CardContext { page: RepositoryPage; get: typeof fetch; say: HTMLElement; reload: () => void; main: HTMLElement; policy?: PolicyFile }

function el<K extends keyof HTMLElementTagNameMap>(tag: K, text?: string, cls?: string): HTMLElementTagNameMap[K] {
  const e = document.createElement(tag);
  if (text !== undefined) e.textContent = text;
  if (cls) e.className = cls;
  return e;
}

// The factory sends "repository" (the policy file's class) or "central"; anything else is shown as
// sent. A central class is the MergePolicy's while that serves the repository (servedFrom).
function sourceLine(c: ClassReport, r: RepositoryPage["report"]): string {
  if (c.source === "repository") return "Declared in the repository's .github/agent-factory.yaml";
  if (c.source !== "central") return c.source;
  return centralManifest(r) !== undefined ? `Served from ${servedFrom(r)}` : "Set by the factory's central config";
}

// The factory's blocker codes (internal/factory/evidence: BlockBelowMinimum, BlockBadOutcome,
// BlockNoDrill) and its unproposed codes, in words; an unknown code is shown with spaces.
const blockerWords: Record<string, string> = {
  below_minimum: "fewer clean decisions than the minimum",
  bad_outcome: "a bad outcome was recorded",
  no_drill: "the revert drill has not passed",
  drill_not_passed: "the revert drill has not passed",
};
const unproposedWords: Record<string, string> = {
  not_shadow: "the class is not in shadow",
  not_in_repository_policy: "the class is not declared in the repository's policy file",
};

export function evidenceBar(e: Evidence): HTMLElement {
  const box = el("div", undefined, "evidence-bar");
  box.setAttribute("role", "meter");
  box.setAttribute("aria-valuemin", "0");
  box.setAttribute("aria-valuenow", String(Math.min(e.clean, e.minimum)));
  box.setAttribute("aria-valuemax", String(e.minimum));
  box.setAttribute("aria-label", "clean decisions toward promotion");
  // A blocked class is not one step from promotion, however many clean decisions it has: its bar
  // never reads as full.
  const blocked = !e.eligible && (e.blockers ?? []).length > 0;
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

const ladder = (e: Evidence) => {
  const ol = el("ol", undefined, "evidence-ladder");
  ol.setAttribute("aria-label", "widening steps");
  for (const s of e.ladder) {
    const li = el("li", `${s.merges} merges${s.reached ? ", reached" : ""}`);
    li.dataset.reached = String(s.reached);
    ol.append(li);
  }
  return ol;
};

export function classCard(c: ClassReport, ctx: CardContext): HTMLElement {
  const card = el("article", undefined, "class-card");
  card.id = `class-${c.class}`;
  const head = el("header", undefined, "class-card-head");
  const m = modeMeaning(c.mode);
  const badge = el("span", `${m.label} · ${m.meaning}`, "mode-badge");
  badge.dataset.mode = c.mode;
  head.append(el("h3", c.class), badge);
  card.append(head, el("p", sourceLine(c, ctx.page.report), "class-source"));

  const policy = ctx.policy?.classes?.[c.class];
  if (policy) {
    const s = describeClass(policy, ctx.page.report.guardrails);
    const when = el("ul", undefined, "class-when");
    for (const w of s.when) when.append(el("li", w));
    card.append(el("p", s.lead, "class-lead"), when, ...s.also.map((a) => el("p", a, "class-also")));
  } else card.append(el("p", "This class's conditions are set in the factory's config and are not shown here.", "class-lead"));

  const r = ctx.page.report;
  const e = c.evidence;
  const act = el("div", undefined, "class-actions");
  // A live class has been promoted and an off one decides nothing: neither is moving toward
  // promotion, so neither shows the bar, the ladder or a promotion button.
  const words = (code: string) => unproposedWords[code] ?? code.replaceAll("_", " ");
  const notPromotable = c.unproposed && c.mode !== "live" ? `Not promotable here: ${words(c.unproposed)}.` : "";
  if (!e) {
    // The page says once that evidence is being read; a card speaks only for a class added since
    // the last read, when the rest of the page already has its evidence.
    if (r.evidenceAt) card.append(el("p", "Evidence for this class is still being read.", "evidence-reading"));
    act.textContent = notPromotable;
  } else if (c.mode === "live") {
    card.append(el("p", `Live \u00b7 ${e.clean} clean decisions recorded.`, "evidence-live"), el("p", evidenceLine(e), "evidence-detail"));
  } else if (c.mode === "off") {
    card.append(el("p", e.clean === 0 ? "Off: no decisions recorded." : evidenceLine(e), "evidence-detail"));
    act.textContent = notPromotable;
  } else {
    card.append(evidenceBar(e), ladder(e), el("p", evidenceLine(e), "evidence-detail"));
    const reviewers = (e.shadow.reviewers ?? []).map((x) => `${x.model} ${x.mergedUnchanged}/${x.mergedUnchanged + x.changed}`).join(", ");
    if (reviewers) card.append(el("p", `Reviewers: ${reviewers}`, "evidence-reviewers"));
    const exc = ctx.page.exceptions.find((x) => x.class === c.class && x.state === "approved" && Date.parse(x.expiresAt ?? "") > Date.now());
    const eligible = e.eligible || (!!exc && e.clean >= exc.minimum && e.bad === 0 && e.drill === "passed");
    card.append(el("p", eligible
      ? (e.eligible ? "Eligible to promote: enough clean decisions, no bad outcome, drill passed." : `Eligible to promote under exception ${exc!.id} (${exc!.minimum} clean decisions required).`)
      : `Not yet eligible: ${(e.blockers ?? []).map((k) => blockerWords[k] ?? k.replaceAll("_", " ")).join(", ") || "no reason given"}.`, "evidence-eligibility"));
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
    } else act.textContent = notPromotable;
  }
  card.append(act, promotionsOf(c.class, ctx), exceptionsOf(c.class, ctx));
  return card;
}

function promotionsOf(cls: string, ctx: CardContext): HTMLElement {
  const box = el("div", undefined, "class-promotions");
  const ps = ctx.page.promotions.filter((p) => p.class === cls);
  if (ps.length === 0) { box.append(el("p", "No promotion requested.", "class-empty")); return box; }
  for (const p of ps) {
    const line = el("p", `Promotion requested by ${p.requestedBy} on ${p.requestedAt}: ${p.state === "pr_opened" ? "pull request opened" : p.state.replaceAll("_", " ")}`);
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

function exceptionsOf(cls: string, ctx: CardContext): HTMLElement {
  const box = el("div", undefined, "class-exceptions");
  for (const x of ctx.page.exceptions.filter((y) => y.class === cls)) {
    box.append(el("p", `Exception: ${x.minimum} clean decisions instead of ${ctx.page.report.minimum}, ${x.state}` +
      (x.decidedBy ? ` by ${x.decidedBy}` : "") + (x.expiresAt ? ` until ${x.expiresAt}` : "") + `. \u201c${x.justification}\u201d`));
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
  const lead = el("p", `Change line ${p.line} of ${p.path} from mode: shadow to mode: live, as below, then propose it as a pull request: `);
  const a = el("a", "edit the file on GitHub");
  a.href = p.editURL;
  lead.append(a);
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
