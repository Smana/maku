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

  it("appears when the settings API is available", async () => {
    const host = document.createElement("header");
    const get = () => Promise.resolve(new Response("{}", { status: 200 }));
    const a = mountSettingsLink(host, "/settings", get as typeof fetch);
    expect(a.hidden).toBe(true);
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(a.hidden).toBe(false);
  });

  it("stays hidden when the settings API is unavailable", async () => {
    const host = document.createElement("header");
    const get = () => Promise.resolve(new Response("", { status: 404 }));
    const a = mountSettingsLink(host, "/settings", get as typeof fetch);
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(a.hidden).toBe(true);
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

describe("the Settings link's colours", () => {
  it("uses the header's foreground, never a surface colour (it was invisible on v0.40.2's first candidate)", async () => {
    const { readFileSync } = await import("node:fs");
    const { join } = await import("node:path");
    const css = readFileSync(join(__dirname, "../src/app.css"), "utf8");
    const rule = css.match(/\.page-header \.settings-link \{[^}]*\}/)?.[0] ?? "";
    expect(rule).toMatch(/color: var\(--brand-header-foreground\)/);
    expect(rule).not.toMatch(/--muted/);
  });
});
