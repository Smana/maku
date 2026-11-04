// SPDX-License-Identifier: Apache-2.0

// screencast.mjs OUT.webp: the README's screencast, recorded from the offline preview's room
// list and the live demo, in light mode, the run rail as its subject. Needs a Playwright install
// (not a web/ dependency), and ImageMagick's magick or, on macOS, sips:
//
//   PLAYWRIGHT_MODULES=/path/to/node_modules [CHROME_PATH=/usr/bin/chromium] \
//     node web/scripts/screencast.mjs docs/assets/maku-room.webp
//
// Recorded in a 1100×800 window, the narrowest that keeps the rail beside the conversation (it
// folds to dots below 1100 px), at device scale 2. A camera then crops each frame, wide for the
// room list, the steer and the approval, close on the rail while a node draws and through the
// finished task's Replay, and scales it to 800×582, the width the README shows it at. Frames are
// assembled by img2webp (libwebp) if on PATH, else by magick.

import { spawn, execFileSync, spawnSync } from "node:child_process";
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
const W = 1100, H = 800, scale = 2; // the page, in CSS px, and its device scale
const outW = 800, outH = 582;
const aspect = outW / outH;
const wide = { x: 0, y: 0, w: W, h: W / aspect };
const defaultFrameMs = 120;
const replayMaxMs = 20_000; // Replay's own cap
const listMs = 2_500;
const demoMaxMs = 60_000; // the approval card is held on screen, whatever the speed
const holdMs = 2_000; // the ending (the Next box, the rail's last state) stays on screen
const drawMs = 2_600; // the camera stays on the rail this long after a node appears
const steerHoldMs = 1_800; // a steer is one row in a fast replay: it holds that long

const has = (tool) => spawnSync(tool, ["--version"], { stdio: "ignore" }).error === undefined;
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

// onRail is the crop that holds the rail whole, as close as its height allows, at the page's right.
function onRail(page) {
  return page.evaluate(({ W, H, aspect }) => {
    const rail = document.querySelector(".rail");
    const r = rail.getBoundingClientRect();
    const h = Math.min(Math.max(rail.scrollHeight + 28, 340), W / aspect, H);
    return { x: W - h * aspect, y: Math.max(0, Math.min(r.y - 12, H - h)), w: h * aspect, h };
  }, { W, H, aspect });
}

// spotlight fades the conversation while the camera is close on the rail, so a crop's cut-off text
// does not compete with it.
const spotlightCSS = ".spot .room-body > :not(.rail), .spot .page-dock { opacity: 0.2; } .room-body > :not(.rail), .page-dock { transition: opacity 0.4s; }";
const spotlight = (page, on) => page.evaluate((on) => document.documentElement.classList.toggle("spot", on), on);

// record shoots the page until ms pass or done; at says where the camera looks for each frame, and
// may hold a frame on screen longer.
async function record(page, ms, frames, dir, { done = async () => false, scroll = async () => {}, at = async () => ({ crop: wide }) } = {}) {
  const end = Date.now() + ms;
  let last = Date.now();
  while (Date.now() < end && !(await done())) {
    await scroll();
    const file = join(dir, `f${String(frames.length).padStart(5, "0")}.jpg`);
    await page.screenshot({ path: file, type: "jpeg", quality: 92 });
    const { crop, hold = 0 } = await at();
    const wait = Math.max(0, defaultFrameMs - (Date.now() - last));
    await new Promise((r) => setTimeout(r, wait));
    frames.push({ file, crop, ms: Math.max(defaultFrameMs, Date.now() - last) + hold });
    last = Date.now();
  }
}

// SCREENCAST_FRAMES=DIR keeps the frames there, to look at before committing the WebP.
const kept = process.env.SCREENCAST_FRAMES;
// Chrome's own screencast: a frame per paint, at its paint time, so short transitions stay
// smooth where a screenshot loop stutters.
async function paints(page, ms, frames, dir, done, crop) {
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
    frames.push({ file: shot[i].file, crop, ms: Math.max(16, Math.round(next - shot[i].at)) });
  }
}

// settleCamera turns each frame's target into where the camera is by then: it eases towards a new
// target over about a second, so a zoom reads as a move and not a cut.
function settleCamera(frames) {
  const c = { ...wide };
  for (const f of frames) {
    const a = 1 - Math.exp(-f.ms / 280);
    for (const k of ["x", "y", "w", "h"]) c[k] += (f.crop[k] - c[k]) * a;
    f.crop = { x: Math.round(c.x * scale), y: Math.round(c.y * scale), w: Math.round(c.w * scale), h: Math.round(c.h * scale) };
  }
}

function cropAndScale(f) {
  const { x, y, w, h } = f.crop;
  if (has("magick")) {
    execFileSync("magick", [f.file, "-crop", `${w}x${h}+${x}+${y}`, "+repage", "-resize", `${outW}x${outH}!`, "-quality", "92", f.file]);
  } else {
    execFileSync("sips", ["--cropToHeightWidth", String(h), String(w), "--cropOffset", String(y), String(x), f.file], { stdio: "ignore" });
    execFileSync("sips", ["--resampleHeightWidth", String(outH), String(outW), f.file], { stdio: "ignore" });
  }
}

function assemble(frames, dir) {
  if (has("img2webp")) {
    const args = ["-loop", "0", "-lossy", "-q", "62", "-m", "4"];
    for (const f of frames) args.push("-d", String(f.ms), f.file);
    args.push("-o", out);
    const list = join(dir, "args.txt");
    writeFileSync(list, args.join("\n"));
    execFileSync("img2webp", [list], { stdio: "inherit" });
    return;
  }
  // magick counts delays in 1/100 s: the remainder carries over, so the length stays exact.
  const args = ["-loop", "0"];
  let owed = 0;
  for (const f of frames) {
    const cs = Math.max(2, Math.round((f.ms + owed) / 10));
    owed += f.ms - cs * 10;
    args.push("-delay", String(cs), f.file);
  }
  args.push("-define", "webp:lossless=false", "-define", "webp:method=4", "-quality", "62", out);
  execFileSync("magick", args, { stdio: "inherit" });
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
  // 887 = an 800 px page plus headless Chrome's window frame.
  const browser = await chromium.launch({
    args: [`--window-size=${W},887`, `--force-device-scale-factor=${scale}`],
    ...(process.env.CHROME_PATH ? { executablePath: process.env.CHROME_PATH } : {}),
    ...(process.env.CHROME_CHANNEL ? { channel: process.env.CHROME_CHANNEL } : {}),
  });
  const page = await browser.newPage({ viewport: null, colorScheme: "light" });
  const frames = [];

  await page.goto("http://127.0.0.1:4181/preview/list.html?theme=light");
  await record(page, listMs, frames, dir);

  await page.goto(`http://127.0.0.1:4182/?theme=light&speed=${process.env.DEMO_SPEED ?? 24}`);
  await page.addStyleTag({ content: spotlightCSS });
  const finished = async () => /(\d+) \/ \1 events/.test(await page.locator(".page-dock .status").innerText());
  // The newest rows stay in view, as a reader following the run would keep them; a held approval's
  // card is at the top of the page.
  const scroll = async () => {
    const held = await page.locator("a.approval-call").first().isVisible().catch(() => false);
    await page.evaluate((held) => window.scrollTo(0, held ? 0 : document.documentElement.scrollHeight), held);
  };
  // The camera goes to the rail when a node appears, and comes back for the conversation.
  let nodes = 0, onRailUntil = 0, steered = false;
  const camera = async () => {
    const n = await page.locator('.rail-node:not([data-state="ghost"])').count();
    if (n > nodes) { nodes = n; onRailUntil = Date.now() + drawMs; }
    const steer = !steered && await page.locator(".view-chat").getByText("Don't call external APIs").first().isVisible().catch(() => false);
    if (steer) steered = true;
    const close = Date.now() < onRailUntil;
    await spotlight(page, close);
    return { crop: close ? await onRail(page) : wide, hold: steer ? steerHoldMs : 0 };
  };
  await record(page, demoMaxMs, frames, dir, { done: finished, at: camera, scroll });
  await page.evaluate(() => window.scrollTo(0, 0)); // the ending reads from the top of the rail down
  await spotlight(page, true);
  const end = await onRail(page);
  await record(page, holdMs, frames, dir, { at: async () => ({ crop: end }) });

  // A finished task replays its rail: the diagram drawn node by node, close up.
  await page.goto("http://127.0.0.1:4181/preview/full.html?finished&theme=light");
  await page.addStyleTag({ content: spotlightCSS });
  const replay = page.locator(".rail-replay", { hasText: "Replay" });
  await replay.waitFor();
  await spotlight(page, true);
  const close = await onRail(page);
  await record(page, 1_200, frames, dir, { at: async () => ({ crop: close }) });
  await replay.click();
  await page.waitForTimeout(100);
  await paints(page, replayMaxMs, frames, dir, () => replay.isVisible(), close);
  await record(page, holdMs, frames, dir, { at: async () => ({ crop: close }) });
  // The card names every model that served a step.
  await page.locator('.rail-node:has(.rail-persona:text-is("implementer")) .rail-button').first().hover();
  await record(page, holdMs, frames, dir, { at: async () => ({ crop: close }) });
  await browser.close();

  settleCamera(frames);
  for (const f of frames) cropAndScale(f);
  assemble(frames, dir);
  const total = frames.reduce((n, f) => n + f.ms, 0);
  console.log(`screencast: ${out} (${frames.length} frames, ${(total / 1000).toFixed(1)} s)`);
} finally {
  for (const s of servers) s.kill();
  if (!kept) rmSync(dir, { recursive: true, force: true });
}
