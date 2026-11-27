// SPDX-License-Identifier: Apache-2.0

// "How merge policies decide": the docs section "Holds, agreed, signal-checked", rendered on the
// policy pages (correctness signals, guidance surface 1). The text is the docs', generated into the
// bundle by hack/gen-policy-explainer.go: the page keeps no second copy.

import { HOW_POLICIES_DECIDE } from "./generated/how-policies-decide";
import { markdown } from "./render";

export const DOCS_PAGE = "https://maku.ogenki.io/docs/use/merge-policies/";
export const DOCS_SECTION = `${DOCS_PAGE}#holds-agreed-signal-checked`;

// explainerSource is the section ready for the page: the diagram, which the page does not draw,
// becomes a link to it, and the docs' relative links point at the docs.
export function explainerSource(src = HOW_POLICIES_DECIDE): string {
  return src
    .replace(/```mermaid[\s\S]*?```/g, `The diagram of the flow is on [the docs page](${DOCS_SECTION}).`)
    .replace(/\]\(#([^)]+)\)/g, `](${DOCS_PAGE}#$1)`)
    .replace(/\]\((?!https?:)([^)#]+?)(#[^)]*)?\)/g, (_, path: string, anchor = "") => {
      const slug = path.replace(/\.md$/, "/");
      return `](https://maku.ogenki.io/docs/${slug}${anchor})`;
    });
}

// policyExplainer is the folded explainer, with a link to the docs section it renders.
export function policyExplainer(): HTMLDetailsElement {
  const box = document.createElement("details");
  box.className = "policy-explainer";
  const summary = document.createElement("summary");
  summary.textContent = "How merge policies decide";
  const link = document.createElement("a");
  link.href = DOCS_SECTION;
  link.textContent = "Read it in the docs";
  const p = document.createElement("p");
  p.append(link);
  box.append(summary, markdown(explainerSource()), p);
  return box;
}
