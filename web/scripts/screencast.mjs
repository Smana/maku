// SPDX-License-Identifier: Apache-2.0

// screencast.mjs OUT.webp: the README's screencast, recorded from the offline preview's room
// list and the live demo, in light mode. Needs a Playwright install (not a web/ dependency)
// and img2webp (libwebp) on PATH:
//
//   PLAYWRIGHT_MODULES=/path/to/node_modules node web/scripts/screencast.mjs docs/assets/maku-room.webp
//
// Recorded at 1280×900, so the room page keeps its rail beside the conversation (it folds to
// dots below 1100 px), and every frame scaled to 960 px wide (macOS sips).

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
const defaultFrameMs = 120;
const replayMaxMs = 20_000; // Replay's own cap
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

async function record(page, ms, frames, dir, done = async () => false, follow = false, frameMs = defaultFrameMs) {
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
// Chrome's own screencast: a frame per paint, at its paint time, so short transitions stay
// smooth where a screenshot loop (one shot per ~100 ms) stutters.
async function paints(page, ms, frames, dir, done) {
  const cdp = await page.context().newCDPSession(page);
  const shot = [];
  cdp.on("Page.screencastFrame", ({ data, metadata, sessionId }) => {
    const file = join(dir, `p${String(frames.length + shot.length).padStart(5, "0")}.jpg`);
    writeFileSync(file, Buffer.from(data, "base64"));
    shot.push({ file, at: metadata.timestamp * 1000 });
    void cdp.send("Page.screencastFrameAck", { sessionId });
  });
  await cdp.send("Page.startScreencast", { format: "jpeg", quality: 92, everyNthFrame: 1 });
  const end = Date.now() + ms;
  while (Date.now() < end && !(await done())) await new Promise((r) => setTimeout(r, 100));
  await cdp.send("Page.stopScreencast");
  await cdp.detach();
  for (let i = 0; i < shot.length; i++) {
    const next = shot[i + 1]?.at ?? shot[i].at + 40;
    frames.push({ file: shot[i].file, ms: Math.max(16, Math.round(next - shot[i].at)) });
  }
}

const dir = kept ? resolve(kept) : mkdtempSync(join(tmpdir(), "maku-screencast-"));
if (kept) mkdirSync(dir, { recursive: true });
const demoSite = join(dir, "demo-site");
run("npm", ["run", "preview:build"]);
run("bash", ["scripts/demo-site.sh", demoSite]);
const servers = [serve(4181), serve(4182, demoSite)];
try {
  await waitFor("http://127.0.0.1:4181/preview/list.html");
  await waitFor("http://127.0.0.1:4182/");
  // No viewport emulation: the screencast captures the real window, so screenshots must too.
  // 988 = a 900 px page plus headless Chrome's 88 px of window frame: frames scale to 960×675.
  const launch = { args: ["--window-size=1280,988"], ...(process.env.CHROME_CHANNEL ? { channel: process.env.CHROME_CHANNEL } : {}) };
  const browser = await chromium.launch(launch);
  const page = await browser.newPage({ viewport: null, colorScheme: "light" });
  const frames = [];

  await page.goto("http://127.0.0.1:4181/preview/list.html?theme=light");
  await record(page, listMs, frames, dir);

  await page.goto(`http://127.0.0.1:4182/?theme=light&speed=${process.env.DEMO_SPEED ?? 8}`);
  const finished = async () => /(\d+) \/ \1 events/.test(await page.locator(".page-dock .status").innerText());
  await record(page, demoMaxMs, frames, dir, finished, true);
  await page.evaluate(() => window.scrollTo(0, 0)); // the ending reads from the Next box down
  await record(page, holdMs, frames, dir);

  // A finished task replays its rail: the diagram drawn node by node.
  await page.goto("http://127.0.0.1:4181/preview/full.html?finished&theme=light");
  const replay = page.locator(".rail-replay", { hasText: "Replay" });
  await replay.waitFor();
  await record(page, 1_000, frames, dir);
  await replay.click();
  const replayed = async () => replay.isVisible();
  await page.waitForTimeout(200);
  await paints(page, replayMaxMs, frames, dir, replayed);
  await record(page, holdMs, frames, dir);
  // The card names every model that served a step.
  await page.locator('.rail-node[data-persona="implementer"] .rail-button, .rail-node:has(.rail-persona:text-is("implementer")) .rail-button').first().hover();
  await record(page, holdMs, frames, dir);
  await browser.close();

  for (const f of frames) execFileSync("sips", ["--resampleWidth", "960", f.file], { stdio: "ignore" });
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
