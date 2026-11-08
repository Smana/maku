import { readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import { fileYAML, type PolicyFile, templates } from "../src/policy-editor";
import { canonical, comments, fileShape, highlight, keyFix, locate, parsePolicy, type Shape, tokenize } from "../src/policy-yaml";

// The goldens are shared with the Go side (internal/factory/mergepolicy/ui_golden_test.go), which
// parses each one strictly and checks full.yaml names every field of the file's Go types.
// Not new URL(…, import.meta.url): Vite rewrites that pattern as an asset import.
const golden = (name: string) => join(__dirname, "../../internal/factory/mergepolicy/testdata/ui", `${name}.yaml`);
function matchGolden(name: string, got: string) {
  if (process.env.UPDATE_GOLDEN) writeFileSync(golden(name), got);
  expect(got).toBe(readFileSync(golden(name), "utf8"));
}

// fullPolicy sets every field the file knows, in a scrambled key order.
const fullPolicy = (): PolicyFile => ({
  classes: {
    "z-oracle": { monitor: { followUpWindow: "168h", verification: { within: "24h" }, flux: { within: "1h", source: "flux-system/flux-system" }, oracleRuns: 2 },
      limits: { maxPerDay: 3 }, requiresApproval: [{ changedFiles: ["^docs/architecture/.+$"] }],
      conditions: { deny: [{ protectedFiles: ["^docs/security/.+$"] }],
        any: [[{ requiredLabel: "safe" }], [{ companionFiles: ["^CHANGELOG\\.md$"] }]],
        all: [{ oracle: { maxFindings: 4, workflow: "detect.yaml", probe: "links" } }, { allowedFiles: ["^docs/.+$"] }, { modifyOnly: true },
          { maxPatchFiles: 5 }, { maxModifiedLines: 50 }, { diffShape: "links-only" }, { holdoutChecks: ["links 🔗"] }, { releaseAgeCheck: "renovate/stability-days" }] },
      harness: "docs-standard", adopt: { branches: ["^renovate/"], logins: ["renovate[bot]"] }, author: "adopt", mode: "shadow" },
    "a-docs": { harness: "docs-standard", author: "agents", mode: "live" },
  },
  factory: "aws-0",
});

function keyPaths(v: unknown, s: Shape | null, at: string, out: { known: string[]; unknown: string[] }) {
  if (Array.isArray(v)) { for (const x of v) keyPaths(x, s?.items ?? null, `${at}[]`, out); return; }
  if (!v || typeof v !== "object") return;
  for (const [k, x] of Object.entries(v)) {
    const p = s?.values ? `${at}.*` : `${at}.${k}`;
    (s?.values || (s?.fields && k in s.fields) ? out.known : out.unknown).push(p);
    keyPaths(x, s?.values ?? s?.fields?.[k] ?? null, p, out);
  }
}

describe("the expert view's YAML", () => {
  it("writes the full file in the schema's key order, class names sorted, whatever order it was built in", () => {
    matchGolden("full", fileYAML(fullPolicy()));
  });
  it("knows every key the full file sets: the order list and the Go types cannot drift apart", () => {
    const out = { known: [] as string[], unknown: [] as string[] };
    keyPaths(fullPolicy(), fileShape, "", out);
    expect(out.unknown).toEqual([]);
  });
  it("writes each template as the editor starts it", () => {
    for (const [name, t] of Object.entries(templates)) {
      matchGolden(`template-${name}`, fileYAML({ factory: "aws-0", classes: { [name]: { ...t(), harness: "docs-standard" } } }));
    }
  });
  it("round-trips: the text the form writes parses back to the same file", () => {
    const files = [fullPolicy(), ...Object.entries(templates).map(([n, t]) => ({ factory: "aws-0", classes: { [n]: { ...t(), harness: "p" } } }))];
    for (const f of files) {
      const text = fileYAML(f);
      const back = parsePolicy(text);
      expect(back.ok && back.file).toEqual(canonical(f));
      expect(back.ok && fileYAML(back.file)).toBe(text);
    }
  });
  it("reads a hand-written file: comments, block lists, plain scalars", () => {
    const p = parsePolicy("# ours\nfactory: aws-0\nclasses:\n  docs-links:\n    mode: shadow # for now\n    conditions:\n      all:\n        - allowedFiles:\n            - ^docs/.+$\n        - maxPatchFiles: 3\n");
    expect(p).toEqual({ ok: true, file: { factory: "aws-0", classes: { "docs-links": { mode: "shadow",
      conditions: { all: [{ allowedFiles: ["^docs/.+$"] }, { maxPatchFiles: 3 }] } } } } });
  });
  it("keeps a date-like or yes-like value a string, as the file's strings are", () => {
    const p = parsePolicy("factory: aws-0\nclasses:\n  a:\n    harness: 2026-01-01\n    mode: yes\n");
    expect(p.ok && p.file.classes?.a).toEqual({ harness: "2026-01-01", mode: "yes" });
  });
  it("refuses text that is not a policy mapping, with the line", () => {
    expect(parsePolicy("factory: aws-0\nclasses:\n  a:\n    mode: shadow\n  mode: x\n    harness: p\n")).toMatchObject({ ok: false, line: 6 });
    expect(parsePolicy("factory: aws-0\nfactory: gcp-0\n")).toMatchObject({ ok: false, line: 2, message: expect.stringContaining("duplicated") });
    expect(parsePolicy("- a\n")).toMatchObject({ ok: false, line: 1 });
    expect(parsePolicy("factory: aws-0\nclasses: [a]\n")).toMatchObject({ ok: false, line: 2 });
  });
});

const text = 'factory: "aws-0"\nclasses:\n  docs-links:\n    mode: "shadow"\n    harness: "p"\n    conditions:\n      all:\n        - maxPatchFiles: 30\n' +
  '  deps-patch:\n    mode: "shadow"\n    harness: "q"\n    limits:\n      maxPerDay: 9\n';

describe("an error from the factory's check", () => {
  it.each([
    ["mergepolicy: not YAML: error converting YAML to JSON: yaml: line 7: did not find expected key", 7],
    ["factory: is read-only here: it must stay aws-0", 1],
    ["class deps-patch: guardrail: harness \"q\" is not in harnessProfiles", 11],
    ["mergepolicy: class docs-links: mode \"x\" is not live, shadow or off", 4],
    ["class docs-links: guardrail: maxPatchFiles 30 is over the guardrail's 10", 8],
    ["class deps-patch: limits.maxPerDay is 0 (the guardrail's) or more", 13],
    ["class deps-patch: an oracle class needs a monitor", 9],
    ["mergepolicy: key \"maxPerDay\" is not a field: keys are matched exactly, in their documented case", 13],
    ["\"deps-patch\" is not a class name", 9],
  ])("points at its line: %s", (msg, line) => {
    expect(locate(text, msg)).toBe(line);
  });
  it.each([
    ["mergepolicy: classes.docs-links.conditions.all[0]: key \"maxFiles\" is not a field (did you mean \"maxPatchFiles\"?); keys are matched exactly", 8],
    ["mergepolicy: classes.deps-patch.limits: key \"maxPerDay\" is not a field; one of x", 13],
    ["mergepolicy: classes.deps-patch: key \"harness\" is not a field; one of x", 11],
    ["mergepolicy: key \"factory\" is not a field (did you mean \"classes\"?); one of classes, factory", 1],
  ])("follows a key error's path to its line: %s", (msg, line) => {
    expect(locate(text, msg)).toBe(line);
  });
  it("follows a path through list items and stops at the deepest place the text has", () => {
    const t = "factory: aws-0\nclasses:\n  a:\n    conditions:\n      all:\n        - allowedFiles: [x]\n        - maxPatchFiles: 3\n" +
      "        - oracle:\n            probe: p\n";
    expect(locate(t, 'classes.a.conditions.all[1]: key "maxPatchFiles" is not a field')).toBe(7);
    expect(locate(t, 'classes.a.conditions.all[2].oracle: key "probe" is not a field')).toBe(9);
    expect(locate(t, 'classes.a.conditions.all[5]: key "zz" is not a field')).toBe(5);
  });
  it.each([
    "the proposal is the file as it is",
    "class revert is required",
    "at most 400 patterns in the file, not 401",
  ])("points nowhere when the text holds no such place: %s", (msg) => {
    expect(locate(text, msg)).toBeUndefined();
  });
});

describe("the highlighter", () => {
  it("splits a line into key, punctuation, value and comment", () => {
    expect(tokenize('    - allowedFiles: ["^a$"] # why')).toEqual([
      { t: "    ", c: "" }, { t: "- ", c: "y-punct" }, { t: "allowedFiles", c: "y-key" }, { t: ":", c: "y-punct" },
      { t: " ", c: "" }, { t: '["^a$"]', c: "y-str" }, { t: " # why", c: "y-comment" }]);
    expect(tokenize("  maxPerDay: 2").map((x) => x.c)).toEqual(["", "y-key", "y-punct", "", "y-num"]);
    expect(tokenize("  modifyOnly: true").at(-1)).toEqual({ t: "true", c: "y-bool" });
    expect(tokenize("# a comment")).toEqual([{ t: "# a comment", c: "y-comment" }]);
    expect(tokenize('  "#": "a # b"').map((x) => x.t)).toEqual(["  ", '"#"', ":", " ", '"a # b"']);
  });
  it("renders the text unchanged, one span a line, and marks the lines asked", () => {
    const code = highlight("a: 1\nb: 2\n", new Set([2]));
    expect(code.textContent).toBe("a: 1\nb: 2\n");
    expect([...code.querySelectorAll(".y-line")].map((l) => l.classList.contains("y-error"))).toEqual([false, true]);
  });
});

describe("a key error's fix", () => {
  it("reads the key, the one meant first, then the rest of the list", () => {
    expect(keyFix('mergepolicy: classes.a.conditions.all[0]: key "maxFiles" is not a field (did you mean "maxPatchFiles"?); keys are matched exactly, in their documented case: one of allowedFiles, maxPatchFiles, modifyOnly'))
      .toEqual({ key: "maxFiles", keys: ["maxPatchFiles", "allowedFiles", "modifyOnly"] });
    expect(keyFix('classes.a.monitor: key "followUp" is not a field; keys are matched exactly, in their documented case: one of flux, followUpWindow'))
      .toEqual({ key: "followUp", keys: ["flux", "followUpWindow"] });
    expect(keyFix("class a: harness is required")).toBeUndefined();
  });
});

describe("comments", () => {
  it("counts the comments a rewrite would drop, not a # inside a string", () => {
    expect(comments('# top\nfactory: aws-0 # inline\nclasses:\n  a:\n    holdoutChecks: ["links #1"]\n')).toBe(2);
    expect(comments('factory: "aws-0"\n')).toBe(0);
  });
});
