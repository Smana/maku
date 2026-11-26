import { afterEach, describe, expect, it, vi } from "vitest";
import { ChatView } from "../src/chat";
import type { RoomEvent } from "../src/conn";
import { mountViews } from "../src/roomview";
import { mountRunView } from "../src/runview";
import { renderEvent } from "../src/render";
import { fetchShots, mountGallery, openViewer, shotFromPayload, shotSrc, type Shot } from "../src/shots";
import type { FlowNode } from "../src/summary";

const room = "3kq7x2ma";
const digest = (c: string) => c.repeat(64);

const shot = (n: number, over: Partial<Shot> = {}): Shot => ({ run: "aaaa1111", n, digest: digest(String(n % 10)), mime: "image/png",
  width: 1280, height: 800, bytes: 1000, source: "browser", pageUrl: `http://127.0.0.1:3000/page${n}`, vision: true, ...over });

const shotEv = (seq: number, s: Shot, roomId = room): RoomEvent => ({ v: 1, id: String(seq), seq, roomId, runId: s.run,
  actor: { kind: "agent", id: `agent:${s.run}`, role: "implementer" }, type: "screenshot", origin: "harness",
  ts: "2026-11-26T10:00:00Z", redactions: [], payload: { ...s } });

afterEach(() => {
  document.body.replaceChildren();
  history.replaceState(null, "", "/");
});

// Every <img> under root: its src must be this origin's /api/rooms/ path, and nothing else.
function srcs(root: ParentNode): string[] {
  return [...root.querySelectorAll("img")].map((i) => i.getAttribute("src") ?? "");
}

describe("shotSrc", () => {
  it("is the room's same-origin API path", () => {
    expect(shotSrc(room, digest("a"))).toBe(`/api/rooms/${room}/screenshots/${digest("a")}`);
  });
  it.each([
    ["a digest that is not hex", room, "../summary"],
    ["an upper-case digest", room, digest("A")],
    ["a short digest", room, "abc"],
    ["a room with a slash", "a/b", digest("a")],
    ["a room that is a URL", "https://evil.test", digest("a")],
    ["an empty room", "", digest("a")],
  ])("refuses %s", (_, r, d) => expect(shotSrc(r, d)).toBeUndefined());
});

describe("shotFromPayload", () => {
  it("drops a page URL that is not loopback http", () => {
    expect(shotFromPayload({ ...shot(1), pageUrl: "javascript:alert(1)" })?.pageUrl).toBeUndefined();
    expect(shotFromPayload({ ...shot(1), pageUrl: "https://evil.test/x" })?.pageUrl).toBeUndefined();
  });
  it("refuses a payload without a valid digest or n", () => {
    expect(shotFromPayload({ ...shot(1), digest: "data:image/png;base64,AAAA" })).toBeUndefined();
    expect(shotFromPayload({ ...shot(1), n: 0 })).toBeUndefined();
    expect(shotFromPayload(null)).toBeUndefined();
  });
});

describe("the chat card", () => {
  it("renders one card with the image's same-origin src and alt text", () => {
    const main = document.createElement("div");
    const chat = new ChatView(main);
    chat.apply(shotEv(5, shot(2, { pageUrl: "http://127.0.0.1:3000/login?token=x" })));
    const cards = main.querySelectorAll(".shot-card");
    expect(cards).toHaveLength(1);
    const card = cards[0] as HTMLElement;
    expect(card.dataset.seq).toBe("5");
    expect(card.textContent).toContain("Screenshot 2 of the run · /login · 1280×800");
    const img = card.querySelector("img")!;
    expect(img.getAttribute("src")).toBe(`/api/rooms/${room}/screenshots/${digest("2")}`);
    expect(img.alt).toBe("Screenshot 2 of run aaaa1111, page /login");
    expect(img.getAttribute("loading")).toBe("lazy");
    expect(img.getAttribute("decoding")).toBe("async");
    expect(card.textContent).not.toContain("Model could not see this");
  });

  it("says when the model could not see the image (C15)", () => {
    const main = document.createElement("div");
    new ChatView(main).apply(shotEv(5, shot(1, { vision: false })));
    expect(main.querySelector(".shot-card")!.textContent).toContain("Model could not see this");
  });

  it("renders no image for a digest or room it cannot trust", () => {
    const main = document.createElement("div");
    const chat = new ChatView(main);
    chat.apply(shotEv(5, { ...shot(1), digest: "https://evil.test/x.png" }));
    chat.apply(shotEv(6, shot(2), "https://evil.test"));
    expect(srcs(main)).toEqual([]);
  });

  it("opens the viewer on the run's shots the chat holds", () => {
    const main = document.createElement("div");
    document.body.append(main);
    const chat = new ChatView(main);
    chat.apply(shotEv(5, shot(1)));
    chat.apply(shotEv(6, shot(2)));
    main.querySelectorAll<HTMLButtonElement>(".shot-thumb")[1].click();
    const d = document.querySelector("dialog.shot-viewer")!;
    expect(d.querySelector(".shot-viewer-count")!.textContent).toBe("2 of 2");
  });

  it("the raw log shows the meta, never an image", () => {
    const row = renderEvent(shotEv(5, shot(3)));
    expect(row.textContent).toContain("screenshot 3");
    expect(row.querySelector("img")).toBeNull();
  });
});

describe("the gallery", () => {
  it("orders thumbnails by n", () => {
    const el = document.createElement("section");
    const g = mountGallery(el, room);
    g.set([shot(3), shot(1), shot(2)]);
    g.add(shot(2)); // a repeat changes nothing
    expect([...el.querySelectorAll<HTMLElement>(".shot-thumb")].map((b) => b.dataset.n)).toEqual(["1", "2", "3"]);
    g.add(shot(4));
    expect(el.querySelectorAll(".shot-thumb")).toHaveLength(4);
    expect(el.hidden).toBe(false);
  });

  it("is hidden while it holds nothing", () => {
    const el = document.createElement("section");
    mountGallery(el, room).set([]);
    expect(el.hidden).toBe(true);
  });
});

describe("the viewer", () => {
  it("opens, steps, closes with Escape and returns focus", () => {
    const el = document.createElement("section");
    document.body.append(el);
    const g = mountGallery(el, room);
    g.set([shot(1), shot(2), shot(3, { vision: false })]);
    const thumbs = el.querySelectorAll<HTMLButtonElement>(".shot-thumb");
    thumbs[1].focus();
    thumbs[1].click();
    const d = document.querySelector<HTMLDialogElement>("dialog.shot-viewer")!;
    expect(d.open).toBe(true);
    const count = () => d.querySelector(".shot-viewer-count")!.textContent;
    const src = () => d.querySelector("img")!.getAttribute("src");
    expect(count()).toBe("2 of 3");
    expect(src()).toBe(shotSrc(room, digest("2")));
    d.querySelector<HTMLButtonElement>(".shot-next")!.click();
    expect(count()).toBe("3 of 3");
    expect(d.textContent).toContain("Model could not see this");
    expect(d.querySelector<HTMLButtonElement>(".shot-next")!.disabled).toBe(true);
    d.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowLeft", bubbles: true }));
    expect(count()).toBe("2 of 3");
    d.querySelector<HTMLButtonElement>(".shot-prev")!.click();
    expect(count()).toBe("1 of 3");
    expect(d.querySelector<HTMLButtonElement>(".shot-prev")!.disabled).toBe(true);
    d.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true }));
    expect(document.querySelector("dialog.shot-viewer")).toBeNull();
    expect(document.activeElement).toBe(thumbs[1]);
  });

  it("the close button closes it too", () => {
    const opener = document.createElement("button");
    document.body.append(opener);
    const d = openViewer(room, [shot(1)], 0, opener);
    d.querySelector<HTMLButtonElement>(".shot-close")!.click();
    expect(d.isConnected).toBe(false);
    expect(document.activeElement).toBe(opener);
  });
});

describe("fetchShots", () => {
  it("reads the room's list and drops what it cannot trust", async () => {
    const get = vi.fn(async () => new Response(JSON.stringify([shot(1), { ...shot(2), digest: "x" }]), { status: 200 }));
    const got = await fetchShots(room, get as unknown as typeof fetch);
    expect(get).toHaveBeenCalledWith(`/api/rooms/${room}/screenshots`, expect.objectContaining({ credentials: "same-origin" }));
    expect(got.map((s) => s.n)).toEqual([1]);
  });
  it("a 404 is no screenshots", async () => {
    const get = vi.fn(async () => new Response("no", { status: 404 }));
    expect(await fetchShots(room, get as unknown as typeof fetch)).toEqual([]);
  });
});

describe("the run view's gallery", () => {
  const node = (run: string, fromSeq: number, toSeq: number, screenshots?: number): FlowNode => ({ run, persona: "implementer",
    role: "implementer", round: 1, fromSeq, toSeq, tools: { ok: 0, failed: 0 }, screenshots });

  function setup(list: Shot[]) {
    const main = document.createElement("main");
    document.body.append(main);
    mountViews(main);
    const shots = vi.fn(async (run: string) => list.filter((s) => s.run === run));
    const rv = mountRunView(main, { load: async () => [], views: (el) => mountViews(el), chosen: () => {}, shots: { room, load: shots } });
    return { main, rv, shots };
  }

  it("shows the run's screenshots above its events, in order of n", async () => {
    const { main, rv, shots } = setup([shot(2), shot(1), shot(1, { run: "bbbb2222", digest: digest("b") })]);
    rv.set([node("aaaa1111", 10, 20, 2)]);
    await rv.select("aaaa1111");
    await Promise.resolve();
    expect(shots).toHaveBeenCalledWith("aaaa1111");
    const g = main.querySelector<HTMLElement>(".run-view .shots")!;
    expect(g.hidden).toBe(false);
    expect(g.compareDocumentPosition(main.querySelector(".run-view .view-chat")!) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect([...g.querySelectorAll<HTMLElement>(".shot-thumb")].map((b) => b.dataset.n)).toEqual(["1", "2"]);
    expect(srcs(main).every((s) => s.startsWith(`/api/rooms/${room}/screenshots/`))).toBe(true);
  });

  it("adds a live screenshot of the open run", async () => {
    const { main, rv } = setup([]);
    rv.set([node("aaaa1111", 10, 0)]);
    await rv.select("aaaa1111");
    rv.apply(shotEv(11, shot(1)));
    rv.apply(shotEv(12, shot(1, { run: "bbbb2222", digest: digest("b") })));
    expect(main.querySelectorAll(".run-view .shots .shot-thumb")).toHaveLength(1);
  });

  it("#screenshots opens the latest run that took any", async () => {
    history.replaceState(null, "", "/r/3kq7x2ma#screenshots");
    const { main, rv } = setup([shot(1), shot(1, { run: "bbbb2222", digest: digest("b") })]);
    rv.set([node("aaaa1111", 10, 20, 1), node("bbbb2222", 21, 30, 1), node("cccc3333", 31, 0)]);
    await vi.waitFor(() => expect(main.querySelector(".run-view-title")?.textContent).toContain("bbbb2222"));
    await vi.waitFor(() => expect(main.querySelectorAll(".run-view .shots .shot-thumb")).toHaveLength(1));
  });
});
