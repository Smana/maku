// SPDX-License-Identifier: Apache-2.0

// A run's screenshots (harness screenshots phase B): the chat card, the run's gallery and the
// viewer. The broker keeps the images and serves them to the room's readers only, at
// /api/rooms/{room}/screenshots/{digest}; the page loads nothing else. An image's src is built
// here from a room id and a hex digest, both checked, so no payload can point an <img> anywhere
// else: never a data: or blob: URL, never another origin (the CSP's img-src 'self' agrees).
// Every string a payload carries is written as text.

import { api } from "./api";

// Shot is one stored screenshot's meta: GET /api/rooms/{id}/screenshots' row and the screenshot
// event's payload share these keys.
export interface Shot {
  run: string; n: number; digest: string; mime?: string; width: number; height: number; bytes?: number;
  source?: string; pageUrl?: string; vision: boolean;
}

const digestRe = /^[0-9a-f]{64}$/;
const roomRe = /^[a-z0-9][a-z0-9-]{0,62}$/;

// shotSrc is the image's same-origin path, or undefined when the room or digest is not one.
export function shotSrc(room: string, digest: string): string | undefined {
  if (!roomRe.test(room) || !digestRe.test(digest)) return undefined;
  return `/api/rooms/${room}/screenshots/${digest}`;
}

// loopbackPath is a loopback http URL's path, the only part a card shows; anything else is "".
function loopbackPath(raw: unknown): string {
  if (typeof raw !== "string") return "";
  try {
    const u = new URL(raw);
    const h = u.hostname;
    if (u.protocol !== "http:" || !(h === "localhost" || h === "[::1]" || /^127\.\d+\.\d+\.\d+$/.test(h))) return "";
    return u.pathname || "/";
  } catch {
    return "";
  }
}

const dim = (v: unknown) => (Number.isInteger(v) && (v as number) > 0 && (v as number) <= 10_000 ? (v as number) : 0);

// shotFromPayload reads a screenshot event's payload, or a list row, into a Shot: undefined when its
// digest or number is not one. A page URL that is not loopback http is dropped.
export function shotFromPayload(p: any): Shot | undefined {
  if (!p || typeof p !== "object" || typeof p.digest !== "string" || !digestRe.test(p.digest)) return undefined;
  if (!Number.isInteger(p.n) || p.n < 1 || p.n > 100) return undefined;
  const path = loopbackPath(p.pageUrl);
  return { run: typeof p.run === "string" ? p.run : "", n: p.n, digest: p.digest, mime: typeof p.mime === "string" ? p.mime : undefined,
    width: dim(p.width), height: dim(p.height), bytes: typeof p.bytes === "number" ? p.bytes : undefined,
    source: typeof p.source === "string" ? p.source : undefined, pageUrl: path ? String(p.pageUrl) : undefined, vision: p.vision === true };
}

// fetchShots is the room's screenshots, as its readers may see them; a broker without the route is none.
export async function fetchShots(room: string, get: typeof fetch = fetch): Promise<Shot[]> {
  const r = await api(`/api/rooms/${encodeURIComponent(room)}/screenshots`, {}, get);
  if (r.status === 404) return [];
  if (!r.ok) throw new Error(`The room's screenshots are unavailable: ${r.status}`);
  const rows = (await r.json()) as unknown;
  return Array.isArray(rows) ? rows.map(shotFromPayload).filter((s): s is Shot => !!s) : [];
}

function el<K extends keyof HTMLElementTagNameMap>(tag: K, cls: string, text?: string): HTMLElementTagNameMap[K] {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (text !== undefined) e.textContent = text;
  return e;
}

const size = (s: Shot) => (s.width && s.height ? `${s.width}×${s.height}` : "");

// caption is "Screenshot n of the run · <page path> · <W>×<H>", the parts it has.
export function caption(s: Shot): string {
  return [`Screenshot ${s.n} of the run`, loopbackPath(s.pageUrl), size(s)].filter(Boolean).join(" · ");
}

function alt(s: Shot): string {
  const path = loopbackPath(s.pageUrl);
  return `Screenshot ${s.n} of run ${s.run || "?"}${path ? `, page ${path}` : ""}`;
}

const blind = () => el("p", "shot-blind", "Model could not see this");

function image(room: string, s: Shot, lazy: boolean): HTMLImageElement | undefined {
  const src = shotSrc(room, s.digest);
  if (!src) return undefined;
  const img = el("img", "shot-img");
  img.alt = alt(s);
  if (s.width && s.height) { img.width = s.width; img.height = s.height; }
  if (lazy) img.setAttribute("loading", "lazy");
  img.setAttribute("decoding", "async");
  img.src = src;
  return img;
}

// thumb is a button showing s's thumbnail, opening the viewer on list at s.
function thumb(room: string, s: Shot, list: () => Shot[]): HTMLButtonElement | undefined {
  const img = image(room, s, true);
  if (!img) return undefined;
  const b = el("button", "shot-thumb");
  b.type = "button";
  b.dataset.n = String(s.n);
  b.setAttribute("aria-label", `Open ${alt(s)}`);
  b.append(img);
  b.onclick = () => {
    const all = list();
    openViewer(room, all, Math.max(0, all.findIndex((x) => x.digest === s.digest)), b);
  };
  return b;
}

// screenshotCard is a screenshot event's row in the chat: its caption, its lazy thumbnail and, on a
// text-only model, that the model could not see it (C15). list is the run's shots the chat holds,
// for the viewer's previous and next.
export function screenshotCard(room: string, seq: number, s: Shot, list: () => Shot[]): HTMLElement {
  const card = el("figure", "shot-card");
  card.dataset.seq = String(seq);
  const t = thumb(room, s, list);
  if (t) card.append(t);
  const cap = el("figcaption", "shot-caption", caption(s));
  card.append(cap);
  if (!s.vision) card.append(blind());
  return card;
}

// mountGallery fills el with a run's thumbnails, a wrapping grid in order of n. It hides while empty.
export function mountGallery(el0: HTMLElement, room: string): { set(shots: Shot[]): void; add(s: Shot): void } {
  el0.classList.add("shots");
  let shots: Shot[] = [];
  const render = () => {
    el0.hidden = shots.length === 0;
    const head = el("p", "shots-head", `Screenshots · ${shots.length}`);
    const grid = el("ul", "shots-grid");
    for (const s of shots) {
      const t = thumb(room, s, () => shots);
      if (!t) continue;
      const li = el("li", "shots-item");
      li.append(t);
      if (!s.vision) li.append(blind());
      grid.append(li);
    }
    el0.replaceChildren(head, grid);
  };
  const order = () => shots.sort((a, b) => a.n - b.n);
  render();
  return {
    set(next) {
      shots = [];
      for (const s of next) if (!shots.some((x) => x.digest === s.digest)) shots.push(s);
      order();
      render();
    },
    add(s) {
      if (shots.some((x) => x.digest === s.digest)) return;
      shots.push(s);
      order();
      render();
    },
  };
}

// openViewer shows shots[index] full size in a modal <dialog>, with previous and next (the buttons
// and the arrow keys). Escape or Close removes it and returns the focus to opener.
export function openViewer(room: string, shots: Shot[], index: number, opener?: HTMLElement): HTMLDialogElement {
  const d = el("dialog", "shot-viewer");
  d.setAttribute("aria-label", "Screenshot viewer");
  const bar = el("div", "shot-viewer-bar");
  const count = el("span", "shot-viewer-count");
  const prev = el("button", "shot-prev", "Previous");
  const next = el("button", "shot-next", "Next");
  const close = el("button", "shot-close", "Close");
  for (const b of [prev, next, close]) b.type = "button";
  bar.append(count, prev, next, close);
  const fig = el("figure", "shot-viewer-figure");
  d.append(bar, fig);
  let i = Math.min(Math.max(index, 0), shots.length - 1);
  const show = () => {
    const s = shots[i];
    count.textContent = `${i + 1} of ${shots.length}`;
    prev.disabled = i === 0;
    next.disabled = i === shots.length - 1;
    const img = image(room, s, false);
    fig.replaceChildren(...(img ? [img] : []), el("figcaption", "shot-caption", caption(s)), ...(s.vision ? [] : [blind()]));
  };
  const step = (by: number) => {
    const j = i + by;
    if (j < 0 || j >= shots.length) return;
    i = j;
    show();
  };
  let done = false;
  const shut = () => {
    if (done) return;
    done = true;
    if (d.open && typeof d.close === "function") d.close();
    d.remove();
    opener?.focus();
  };
  prev.onclick = () => step(-1);
  next.onclick = () => step(1);
  close.onclick = shut;
  d.addEventListener("keydown", (e) => {
    if (e.key === "ArrowLeft") { e.preventDefault(); step(-1); }
    else if (e.key === "ArrowRight") { e.preventDefault(); step(1); }
    else if (e.key === "Escape") { e.preventDefault(); shut(); }
  });
  // The browser's own Escape on a modal dialog closes it: tidy up the same way.
  d.addEventListener("close", shut);
  show();
  document.body.append(d);
  if (typeof d.showModal === "function") {
    try { d.showModal(); } catch { d.setAttribute("open", ""); }
  } else d.setAttribute("open", "");
  close.focus();
  return d;
}
