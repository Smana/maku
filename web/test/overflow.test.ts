// SPDX-License-Identifier: Apache-2.0
import { spawn, execFileSync, type ChildProcess } from "node:child_process";
import { readFileSync } from "node:fs";
import { createRequire } from "node:module";
import { join } from "node:path";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { LONG_COMMAND } from "../preview/long-fixtures";

// The room page never scrolls sideways: a v0.40.0 room showed its whole column shifted
// left. jsdom lays nothing out, so the stylesheet's guards are asserted here, and a real browser
// measures full.html?long when PLAYWRIGHT_MODULES points at a node_modules holding playwright.
const web = join(__dirname, "..");
const css = readFileSync(join(web, "src/app.css"), "utf8");
const rule = (sel: string) => {
  const esc = sel.replace(/[.*+?^${}()|[\]\\#]/g, "\\$&");
  const m = css.match(new RegExp(`(?:^|\\n)\\s*${esc}\\s*{([^}]*)}`));
  expect(m, `rule ${sel}`).not.toBeNull();
  return m![1];
};

describe("no sideways scroll, by the stylesheet", () => {
  // clip, not hidden: hidden makes #app a scroll box, which a scrollIntoView can still shift and
  // which would end the header's and the dock's stickiness.
  it("#app clips what a line pushes past the window", () => expect(rule("#app")).toMatch(/overflow-x:\s*clip/));
  it("a call's command line ellipsizes inside its row", () => expect(rule(".t-cmd")).toMatch(/text-overflow:\s*ellipsis/));
  it("a pre wraps anywhere", () => expect(rule("pre")).toMatch(/overflow-wrap:\s*anywhere/));
  it("a needs card wraps an inline command", () => expect(rule(".needs-other li")).toMatch(/overflow-wrap:\s*anywhere/));
});

const modules = process.env.PLAYWRIGHT_MODULES;
describe.skipIf(!modules)("no sideways scroll, measured (PLAYWRIGHT_MODULES)", () => {
  const port = 4300 + Math.floor(Math.random() * 500);
  let server: ChildProcess;
  type Browser = { newPage(o: object): Promise<{ goto(u: string): Promise<unknown>; waitForTimeout(ms: number): Promise<void>; evaluate<T>(f: () => T): Promise<T>; close(): Promise<void> }>; close(): Promise<void> };
  let browser: Browser;
  beforeAll(async () => {
    execFileSync("npm", ["run", "-s", "preview:build"], { cwd: web, stdio: "ignore" });
    server = spawn(process.execPath, ["preview/serve.mjs"], { cwd: web, env: { ...process.env, PORT: String(port) }, stdio: "ignore" });
    const { chromium } = createRequire(join(modules!, "noop.js"))("playwright");
    browser = await chromium.launch(process.env.CHROME_PATH ? { executablePath: process.env.CHROME_PATH } : {});
    await new Promise((r) => setTimeout(r, 300));
  }, 60_000);
  afterAll(async () => { await browser?.close(); server?.kill(); });

  it.each([390, 1366, 2560].flatMap((w) => ["long&running", "long&finished", "long&briefs"].map((q) => [w, q] as const)))(
    "%i px, ?%s", async (width, q) => {
      const page = await browser.newPage({ viewport: { width, height: 900 } });
      await page.goto(`http://127.0.0.1:${port}/preview/full.html?${q}`);
      await page.waitForTimeout(800);
      // Every fold open: the long command, its output and the heredoc are all laid out.
      const r = await page.evaluate(() => {
        document.querySelectorAll("details").forEach((d) => { d.open = true; });
        const d = document.documentElement;
        return { scroll: d.scrollWidth, client: d.clientWidth, shown: document.body.textContent!.includes("deploy/base/metrics-stack/alerting") };
      });
      await page.close();
      expect(r.shown).toBe(true);
      expect(r.scroll).toBe(r.client);
    }, 30_000);
});

it("the long fixture carries the line that broke the page", () => expect(LONG_COMMAND).toMatch(/^sed -i .s\|https:\/\/git\.example\.com\//));
