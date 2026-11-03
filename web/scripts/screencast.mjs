// SPDX-License-Identifier: Apache-2.0

// screencast.mjs OUT.webp: the README's screencast, recorded from the offline preview's room
// list and the live demo, in light mode. Needs a Playwright install (not a web/ dependency)
// and img2webp (libwebp) on PATH:
//
//   PLAYWRIGHT_MODULES=/path/to/node_modules node web/scripts/screencast.mjs docs/assets/maku-room.webp
//
// Recorded at 1280×900, so the room page keeps its rail beside the conversation (it folds to
// dots below 1100 px), and scaled to 960×675 by the device pixel ratio.

import { spawn, execFileSync } from "node:child_process";
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { createRequire } from "node:module";
import { fileURLToPath } from "node:url";

const modules = process.env.PLAYWRIGHT_MODULES;
if (!modules) throw new Error("set PLAYWRIGHT_MODULES to a node_modules holding playwright");
const { chromium } = createRequire(join(modules, "noop.js"))("playwright");

const out = resolve(process.argv[2] ?? "maku-room.webp");
const web = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const frameMs = 120;
const listMs = 12_000; // past one 10 s refresh of the room list
const demoMaxMs = 60_000; // the approval card is held on screen, whatever the speed
const holdMs = 3_000; // the ending (the Next box, the rail's last state) stays on screen

const run = (cmd, args, env = {}) => execFileSync(cmd, args, { cwd: web, stdio: "inherit", env: { ...process.env, ...env } });

function serve(port, root) {
  const env = { ...process.env, PORT: String(port), ...(root ? { SERVE_ROOT: root } : {}) };
  return spawn("node", ["preview/serve.mjs"], { cwd: web, env, stdio: "ignore" });
}

async function waitFor(url) {
  for (let i = 0; i < 50; i++) {
    try { if ((await fetch(url)).ok) return; } catch { /* not up yet */ }
    await new Promise((r) => setTimeout(r, 200));
  }
  throw new Error(`no server at ${url}`);
}

async function record(page, ms, frames, dir, done = async () => false, follow = false) {
  const end = Date.now() + ms;
  let last = Date.now();
  while (Date.now() < end && !(await done())) {
    // The newest rows stay in view, as a reader following the run would keep them.
    if (follow) await page.evaluate(() => window.scrollTo(0, document.documentElement.scrollHeight));
    const file = join(dir, `f${String(frames.length).padStart(5, "0")}.png`);
    await page.screenshot({ path: file });
    const now = Date.now();
    const wait = Math.max(0, frameMs - (now - last));
    await new Promise((r) => setTimeout(r, wait));
    frames.push({ file, ms: Math.max(frameMs, Date.now() - last) });
    last = Date.now();
  }
}

// SCREENCAST_FRAMES=DIR keeps the PNG frames there, to look at before committing the WebP.
const kept = process.env.SCREENCAST_FRAMES;
const dir = kept ? resolve(kept) : mkdtempSync(join(tmpdir(), "maku-screencast-"));
if (kept) mkdirSync(dir, { recursive: true });
const demoSite = join(dir, "demo-site");
run("npm", ["run", "preview:build"]);
run("bash", ["scripts/demo-site.sh", demoSite]);
const servers = [serve(4181), serve(4182, demoSite)];
try {
  await waitFor("http://127.0.0.1:4181/preview/list.html");
  await waitFor("http://127.0.0.1:4182/");
  const browser = await chromium.launch(process.env.CHROME_CHANNEL ? { channel: process.env.CHROME_CHANNEL } : {});
  const page = await browser.newPage({ viewport: { width: 1280, height: 900 }, deviceScaleFactor: 0.75, colorScheme: "light" });
  const frames = [];

  await page.goto("http://127.0.0.1:4181/preview/list.html?theme=light");
  await record(page, listMs, frames, dir);

  await page.goto(`http://127.0.0.1:4182/?theme=light&speed=${process.env.DEMO_SPEED ?? 8}`);
  const finished = async () => /(\d+) \/ \1 events/.test(await page.locator(".page-dock .status").innerText());
  await record(page, demoMaxMs, frames, dir, finished, true);
  await page.evaluate(() => window.scrollTo(0, 0)); // the ending reads from the Next box down
  await record(page, holdMs, frames, dir);
  await browser.close();

  const args = ["-loop", "0", "-lossy", "-q", "70", "-m", "4"];
  for (const f of frames) args.push("-d", String(f.ms), f.file);
  args.push("-o", out);
  const list = join(dir, "args.txt");
  writeFileSync(list, args.join("\n"));
  execFileSync("img2webp", [list], { stdio: "inherit" });
  console.log(`screencast: ${out} (${frames.length} frames)`);
} finally {
  for (const s of servers) s.kill();
  if (!kept) rmSync(dir, { recursive: true, force: true });
}
