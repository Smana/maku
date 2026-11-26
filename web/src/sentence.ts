// SPDX-License-Identifier: Apache-2.0

// The one sentence the factory builds about a policy (correctness signals CS7): what it merges,
// what checks its meaning, what is left open. The page prints it, never rebuilds it: a factory
// that sends none gets the line UNAVAILABLE.

export type Signal = "links-only" | "semver-patch" | "semver-minor" | "oracle" | "contract" | "none";

export interface Sentence {
  signal: Signal | string; prose: boolean; lead: string; when?: string; checkedBy: string; notChecked?: string;
  onceLive?: string; after?: string; undo?: string; wayOut?: string; text: string;
}

export const UNAVAILABLE = "This factory does not say what checks this policy's meaning. Update Maku to see it.";

// sentenceNode renders the sentence's Markdown subset: **bold** as <strong>, `code` as <code>, the
// rest as text. Nothing is parsed as HTML.
export function sentenceNode(text: string, cls = "policy-sentence", tag: "p" | "span" = "p"): HTMLElement {
  const p = document.createElement(tag);
  p.className = cls;
  for (const part of text.split(/(\*\*[^*]+\*\*|`[^`]+`)/)) {
    if (!part) continue;
    if (part.startsWith("**") && part.endsWith("**") && part.length > 4) {
      const b = document.createElement("strong");
      b.append(...inline(part.slice(2, -2)));
      p.append(b);
    } else p.append(...inline(part));
  }
  return p;
}

function inline(text: string): Node[] {
  return text.split(/(`[^`]+`)/).filter(Boolean).map((x) => {
    if (x.length > 2 && x.startsWith("`") && x.endsWith("`")) {
      const c = document.createElement("code");
      c.textContent = x.slice(1, -1);
      return c;
    }
    return document.createTextNode(x);
  });
}

// meaningNode is the sentence, or the line that says this factory sends none.
export function meaningNode(s: Sentence | undefined | null, cls = "policy-sentence"): HTMLElement {
  if (s?.text) return sentenceNode(s.text, cls);
  const p = document.createElement("p");
  p.className = `${cls} notice`;
  p.textContent = UNAVAILABLE;
  return p;
}

// checkedByLine is the "Meaning checked by" line of the page, with the way to add a signal.
export function checkedByLine(s: Sentence | undefined | null): HTMLElement | undefined {
  if (!s?.text) return undefined;
  const words = s.wayOut ? `**Meaning checked by:** ${s.checkedBy}. ${s.wayOut}` : `**Meaning checked by:** ${s.checkedBy}.`;
  return sentenceNode(words, "policy-meaning");
}
