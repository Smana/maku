import { describe, expect, it } from "vitest";
import { mountSettingsLink } from "../src/settingslink";
import { mountRoom } from "../src/room";
import { listRooms } from "../src/view";
import { FakeSocket } from "./fakes";

const theme = { mode: "light" as const, cycle: () => "light" as const };

describe("mountSettingsLink", () => {
  it("appends one labelled link to /settings", () => {
    const host = document.createElement("header");
    const a = mountSettingsLink(host);
    expect(host.querySelectorAll("a.settings-link")).toHaveLength(1);
    expect(a.getAttribute("href")).toBe("/settings");
    expect(a.textContent).toContain("Settings");
    expect(a.querySelector("svg")?.getAttribute("aria-hidden")).toBe("true");
  });
});

describe("the Settings link in the page headers", () => {
  it("is in the room page's header", () => {
    const app = document.createElement("div");
    mountRoom(app, "3kq7x2ma", theme, { socket: (url) => new FakeSocket(url), random: () => 0 });
    expect(app.querySelector(".page-header a.settings-link")?.getAttribute("href")).toBe("/settings");
  });

  it("is in the rooms list's header", async () => {
    const app = document.createElement("div");
    await listRooms(app, (() => Promise.resolve(new Response(JSON.stringify([])))) as typeof fetch, {}, theme);
    expect(app.querySelector(".page-header a.settings-link")?.getAttribute("href")).toBe("/settings");
  });
});
