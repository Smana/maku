import { afterEach, describe, expect, it, vi } from "vitest";
import { api, fetchEvents, fetchNeeds, nav, SignInRequired } from "../src/api";
import { listRooms, newRoomForm } from "../src/view";

const answer = (status: number, body = "") => (() => Promise.resolve(new Response(body, { status }))) as typeof fetch;

describe("api", () => {
  afterEach(() => vi.restoreAllMocks());

  // An expired session that oauth2-proxy could not refresh answers 401: without a
  // sign-in the page would sit dead until a reload.
  it("sends a 401 to sign in again, back to the current page", async () => {
    const go = vi.spyOn(nav, "go").mockImplementation(() => {});
    history.replaceState(null, "", "/r/3kq7x2ma?x=1");
    await expect(api("/api/rooms", {}, answer(401))).rejects.toBeInstanceOf(SignInRequired);
    expect(go).toHaveBeenCalledWith("/oauth2/start?rd=%2Fr%2F3kq7x2ma%3Fx%3D1");
  });

  it("passes any other status through", async () => {
    const go = vi.spyOn(nav, "go").mockImplementation(() => {});
    expect((await api("/api/rooms", {}, answer(503))).status).toBe(503);
    expect(go).not.toHaveBeenCalled();
  });

  it("signs in from the room list too", async () => {
    const go = vi.spyOn(nav, "go").mockImplementation(() => {});
    history.replaceState(null, "", "/");
    const el = document.createElement("div");
    await listRooms(el, answer(401));
    expect(go).toHaveBeenCalledWith("/oauth2/start?rd=%2F");
    expect(el.textContent).toMatch(/signing in/);
  });
});

describe("newRoomForm", () => {
  afterEach(() => vi.restoreAllMocks());

  function submit(get: typeof fetch, dataClass = "public", repository = "Smana/a") {
    const form = newRoomForm(get);
    (form.elements.namedItem("dataClass") as HTMLSelectElement).value = dataClass;
    (form.elements.namedItem("repository") as HTMLInputElement).value = repository;
    form.dispatchEvent(new Event("submit", { cancelable: true }));
    return form;
  }

  it("creates a room and opens it", async () => {
    const go = vi.spyOn(nav, "go").mockImplementation(() => {});
    const calls: [string, RequestInit][] = [];
    const get = ((url: string, init: RequestInit) => {
      calls.push([url, init]);
      return Promise.resolve(new Response(JSON.stringify({ id: "3kq7x2ma" }), { status: 201 }));
    }) as unknown as typeof fetch;
    submit(get, "internal", "Smana/cloud-native-ref");
    await vi.waitFor(() => expect(go).toHaveBeenCalledWith("/r/3kq7x2ma"));
    expect(calls[0][0]).toBe("/api/rooms");
    expect(calls[0][1].method).toBe("POST");
    expect(JSON.parse(String(calls[0][1].body))).toEqual({ dataClass: "internal", repository: "Smana/cloud-native-ref" });
  });

  // D7: who can read the repository can read the room, so the broker refuses a room without one.
  it("sends nothing without a repository", async () => {
    vi.spyOn(nav, "go").mockImplementation(() => {});
    const posts: string[] = [];
    const get = ((_: string, init: RequestInit) => {
      posts.push(String(init.body));
      return Promise.resolve(new Response(JSON.stringify({ id: "3kq7x2ma" }), { status: 201 }));
    }) as unknown as typeof fetch;
    const form = submit(get, "public", "  ");
    await Promise.resolve();
    expect(posts).toEqual([]);
    expect(form.querySelector(".notice")?.textContent).toMatch(/repository first/);
    expect((form.elements.namedItem("repository") as HTMLInputElement).required).toBe(true);
  });

  it("says why a room was not created", async () => {
    const go = vi.spyOn(nav, "go").mockImplementation(() => {});
    // #146: only a 503 says retry for an outage; a 500 points at the broker's logs.
    for (const [status, copy] of [[400, /not accepted/], [404, /cannot read that repository/], [409, /already exists/], [429, /too many/i],
      [500, /see its logs/], [503, /cannot be created/], [502, /502/]] as const) {
      const form = submit(answer(status));
      await vi.waitFor(() => expect(form.querySelector(".notice")?.textContent).toMatch(copy));
    }
    // An answer that is not a room id never becomes a navigation.
    const form = submit(answer(201, JSON.stringify({ id: "../oauth2/sign_out" })));
    await vi.waitFor(() => expect(form.querySelector(".notice")?.textContent).toMatch(/unexpected/));
    expect(go).not.toHaveBeenCalled();
  });
});

describe("fetchNeeds", () => {
  it("asks the route for one room's list and returns it", async () => {
    const page = { apiVersion: "needs/v1", cursor: "2026-11-04T19:10:00Z", items: [{ kind: "hold", id: "r", what: "w", url: "u" }] };
    let url = "";
    const get = (async (u: string) => { url = u; return new Response(JSON.stringify(page), { status: 200 }); }) as unknown as typeof fetch;
    expect((await fetchNeeds("3kq7x2ma", undefined, get)).items).toHaveLength(1);
    expect(url).toBe("/api/needs?room=3kq7x2ma");
    await fetchNeeds("3kq7x2ma", "2026-11-04T19:10:00Z", get);
    expect(url).toBe("/api/needs?room=3kq7x2ma&since=2026-11-04T19%3A10%3A00Z");
  });

  // Review Focus 3: the route's 400 names the format, and the panel says the route's own sentence.
  it("relays the route's refusal and its outage", async () => {
    for (const [status, copy] of [[400, /RFC 3339/], [503, /cannot be verified/], [502, /502/]] as const) {
      await expect(fetchNeeds("3kq7x2ma", undefined, answer(status))).rejects.toThrow(copy);
    }
  });

  // A broker that predates needs/v1 answers something else: the panel says it cannot read it rather
  // than rendering an empty list, which would read as nothing needs you.
  it("refuses an answer in a format the page does not know", async () => {
    await expect(fetchNeeds("3kq7x2ma", undefined, answer(200, JSON.stringify({ apiVersion: "summary/v1" })))).rejects.toThrow(/does not know/);
  });
});

describe("fetchEvents", () => {
  it("asks for one page after a seq and returns its events", async () => {
    let url = "";
    const get = (async (u: string) => { url = u; return new Response(JSON.stringify({ events: [{ seq: 4 }] }), { status: 200 }); }) as unknown as typeof fetch;
    expect(await fetchEvents("3kq7x2ma", 3, 50, get)).toEqual([{ seq: 4 }]);
    expect(url).toBe("/api/rooms/3kq7x2ma/events?after=3&limit=50");
  });
  it("throws on an error status", async () => {
    const get = (async () => new Response("", { status: 503 })) as unknown as typeof fetch;
    await expect(fetchEvents("3kq7x2ma", 0, 50, get)).rejects.toThrow("503");
  });
});
