// SPDX-License-Identifier: Apache-2.0

// A filter field over a list the page already drew: a table's rows, a page's class cards. The query
// lives in `?q=` so a filtered page can be shared and survives a reload.

const MIN_ITEMS = 4; // fewer than that is nothing to filter

// filterField wires a search input over items: each item's text (from text(item)) is matched,
// case-insensitive, against every whitespace-separated word of the query; hidden items get the
// `hidden` attribute. It returns the field's container (label, input, count, empty state), which
// is empty when there are too few items to filter, and `apply`, to call after the items are redrawn.
export function filterField<T extends HTMLElement>(opts: {
  label: string;
  items: () => T[];
  text: (item: T) => string;
  noun: [string, string];
}): { box: HTMLElement; apply: () => void } {
  const box = document.createElement("div");
  box.className = "filter";
  if (opts.items().length < MIN_ITEMS) return { box, apply: () => {} };

  const input = document.createElement("input");
  input.type = "search";
  input.id = `filter-${Math.random().toString(36).slice(2, 8)}`;
  input.autocomplete = "off";
  input.value = new URLSearchParams(location.search).get("q") ?? "";
  const label = document.createElement("label");
  label.htmlFor = input.id;
  label.textContent = opts.label;
  const count = document.createElement("span");
  count.className = "filter-count";
  count.setAttribute("aria-live", "polite");
  const empty = document.createElement("p");
  empty.className = "filter-empty";
  empty.hidden = true;
  box.append(label, input, count, empty);

  const apply = () => {
    const words = input.value.toLowerCase().split(/\s+/).filter(Boolean);
    const items = opts.items();
    let shown = 0;
    for (const it of items) {
      const t = opts.text(it).toLowerCase();
      it.hidden = !words.every((w) => t.includes(w));
      if (!it.hidden) shown++;
    }
    count.textContent = `${shown} of ${items.length} ${items.length === 1 ? opts.noun[0] : opts.noun[1]}`;
    empty.replaceChildren();
    empty.hidden = shown > 0;
    if (shown === 0) {
      const clear = document.createElement("button");
      clear.type = "button";
      clear.textContent = "Clear";
      clear.onclick = () => { input.value = ""; apply(); input.focus(); };
      empty.append(`No ${opts.noun[0]} matches "${input.value.trim()}". `, clear);
    }
    const u = new URL(location.href);
    if (input.value) u.searchParams.set("q", input.value); else u.searchParams.delete("q");
    history.replaceState(history.state, "", u);
  };

  input.addEventListener("input", apply);
  input.addEventListener("keydown", (e) => {
    if (e.key === "Escape" && input.value) { input.value = ""; apply(); }
  });
  // One listener per mounted field: a re-rendered page drops its field, and the old listener with it.
  const slash = (e: KeyboardEvent) => {
    if (!input.isConnected) { document.removeEventListener("keydown", slash); return; }
    const t = e.target;
    if (e.key !== "/" || e.ctrlKey || e.metaKey || e.altKey) return;
    if (t instanceof HTMLElement && (["INPUT", "TEXTAREA", "SELECT"].includes(t.tagName) || t.isContentEditable)) return;
    e.preventDefault();
    input.focus();
  };
  document.addEventListener("keydown", slash);
  apply();
  return { box, apply };
}
