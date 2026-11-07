// SPDX-License-Identifier: Apache-2.0

// The coordination panel: a parent's family (flow.coordination, already filtered for this viewer
// by the broker) drawn beside the run rail, never inside it. It reuses the rail's glyphs, persona
// labels and tones but none of the splitter's part vocabulary: a family is not a split. Every
// string goes in as text; only a C2 room id or a GitHub issue/PR URL becomes a link.

import { el } from "./controls";
import { personaLabel, tone } from "./flow";
import { glyph, glyphFor } from "./glyphs";
import type { CoordinationConfirmation, CoordinationEdge, CoordinationFlow, CoordinationNode, CoordinationView } from "./summary";

const roomId = /^[a-z2-7]{8}$/;
const githubURL = /^https:\/\/github\.com\/[A-Za-z0-9._-]+\/[A-Za-z0-9._-]+\/(issues|pull)\/\d+$/;

function link(text: string, href: string, ok: boolean): HTMLElement {
  return ok ? el("a", { href, textContent: text, rel: "noopener noreferrer" }) : el("span", { textContent: text });
}

function label(n: CoordinationNode): string {
  return n.kind === "integration" ? `${n.id.replace(/^integration:/, "")} integration` : n.childID ?? n.id;
}

function state(n: CoordinationNode): string {
  if (n.phase) return n.phase;
  if (n.requiredConfirmation) return `awaiting ${n.requiredConfirmation} confirmation`;
  return n.kind === "integration" ? "not started" : "planned";
}

// renderCoordinationNode is one child or integration. A node that cannot start yet is a disabled
// button; a filed child links to its own room and its GitHub issue or PR.
export function renderCoordinationNode(n: CoordinationNode): HTMLLIElement {
  const li = el("li", { className: "rail-coordination-node" });
  li.dataset.node = n.id;
  li.dataset.kind = n.kind;
  const role = n.roles[0] ?? "";
  li.dataset.tone = tone(role);
  const b = el("button", { type: "button", disabled: n.disabled, className: "rail-coordination-head" });
  b.append(glyph(glyphFor(role, role)), el("span", { className: "rail-coordination-name", textContent: label(n) }));
  li.append(b);
  const meta = el("div", { className: "rail-coordination-meta" });
  if (n.repository) meta.append(el("span", { className: "rail-coordination-repo", textContent: n.repository }));
  meta.append(el("span", { className: "rail-coordination-state", textContent: [state(n), n.reason].filter(Boolean).join(" · ") }));
  meta.append(el("span", { className: "rail-coordination-roles", textContent: n.roles.map(personaLabel).join(" → ") }));
  li.append(meta);
  const links = el("div", { className: "rail-coordination-links" });
  if (n.room) links.append(link("room", `/r/${n.room}`, roomId.test(n.room)));
  if (n.issueURL) links.append(link("issue", n.issueURL, githubURL.test(n.issueURL)));
  if (n.prURL) links.append(link("PR", n.prURL, githubURL.test(n.prURL)));
  if (links.childElementCount) li.append(links);
  return li;
}

function renderEdge(e: CoordinationEdge, names: Map<string, string>): HTMLLIElement {
  const li = el("li", { className: "rail-coordination-edge" });
  li.dataset.kind = e.kind;
  li.dataset.state = e.state;
  const what = e.kind === "artifact" ? e.name ?? "" : "integration";
  li.textContent = `${names.get(e.from) ?? e.from} → ${names.get(e.to) ?? e.to}: ${what} · ${e.state}`;
  return li;
}

export interface CoordinationPanel { el: HTMLElement; set(f?: CoordinationFlow, v?: CoordinationView): HTMLElement }

// The page's two hands: confirm posts one confirmation, refresh reads the summary again. A
// confirmation's answer is never applied here: the next summary is what the page shows.
export interface CoordinationControls {
  confirm(c: CoordinationConfirmation): Promise<void>;
  // recover performs the parent's one proposed recovery; absent, the panel offers none.
  recover?(r: { code: string; planVersion: number }): Promise<void>;
  refresh(): void;
}

function confirmation(v: CoordinationView, part: Pick<CoordinationConfirmation, "parent" | "child">): CoordinationConfirmation {
  return { plan: v.plan, planHash: v.planHash, ...part, planning: { attemptID: "", effectiveRequester: "",
    reservedMicroUSD: v.budget.planningReservedMicroUSD ?? 0, settledMicroUSD: v.budget.planningSettledMicroUSD ?? 0 } };
}

// mountCoordinationPanel appends a hidden panel to root; set draws a family, or hides the panel
// when the room is not a coordination parent. With controls and a fully readable view, a node that
// waits for a confirmation offers it, bound to the exact plan, hash and fingerprint shown.
export function mountCoordinationPanel(root: HTMLElement, controls?: CoordinationControls): CoordinationPanel {
  const section = el("section", { className: "rail-coordination", hidden: true });
  section.setAttribute("aria-label", "Coordination");
  root.append(section);
  return {
    el: section,
    set(f, v) {
      section.hidden = !f;
      if (!f) { section.replaceChildren(); return section; }
      const status = el("p", { className: "rail-coordination-status" });
      status.setAttribute("role", "status");
      const decide = controls && v && v.restrictedChildren === 0 && f.restrictedChildren === 0 && v.planHash === f.planHash ? controls : undefined;
      const press = (label: string, name: string, run: () => Promise<void>): HTMLButtonElement => {
        const b = el("button", { type: "button", textContent: label, className: "rail-coordination-act" });
        b.dataset.act = name;
        b.onclick = () => {
          b.disabled = true;
          status.textContent = "";
          run().catch((e: unknown) => { status.textContent = `Not recorded: ${e instanceof Error ? e.message : String(e)}`; })
            .finally(() => decide!.refresh());
        };
        return b;
      };
      const act = (label: string, name: string, c: CoordinationConfirmation) => press(label, name, () => decide!.confirm(c));
      const head = el("h2", { className: "rail-coordination-title", textContent: `Coordination · plan v${f.planVersion} ${f.planHash.slice(0, 7)}` });
      const nodes = el("ol", { className: "rail-coordination-nodes" });
      nodes.append(...f.nodes.map((n) => {
        const li = renderCoordinationNode(n);
        const fp = v?.children.find((c) => c.id === n.childID)?.fingerprint;
        if (decide && v && n.requiredConfirmation === "child" && n.childID && fp) {
          li.append(act(`Authorize ${n.childID}`, "coordination-authorize",
            confirmation(v, { child: { childID: n.childID, childFingerprint: fp, planVersion: v.plan.version } })));
        }
        return li;
      }));
      const names = new Map(f.nodes.map((n) => [n.id, label(n)]));
      const edges = el("ul", { className: "rail-coordination-edges" });
      edges.append(...f.edges.map((e) => renderEdge(e, names)));
      section.replaceChildren(head);
      if (decide && v && f.nodes.some((n) => n.requiredConfirmation === "parent")) {
        section.append(act(`Confirm plan v${v.plan.version}`, "coordination-confirm", confirmation(v, { parent: { planHash: v.planHash } })));
      }
      const rec = v?.recovery;
      if (decide?.recover && rec && /^[a-z][a-z_]{0,47}$/.test(rec.code)) {
        const r = { code: rec.code, planVersion: rec.planVersion };
        section.append(press(`Recover: ${rec.code}`, "coordination-recover", () => decide.recover!(r)));
      }
      section.append(nodes, status);
      if (f.edges.length) section.append(edges);
      if (f.restrictedChildren > 0) {
        const n = f.restrictedChildren;
        section.append(el("p", { className: "rail-coordination-restricted", textContent: `${n} ${n === 1 ? "child" : "children"} you cannot read` }));
      }
      return section;
    },
  };
}
