// SPDX-License-Identifier: Apache-2.0

// What the settings pages share to hand a manifest to a maintainer: a value written as YAML, and
// copy and download of a text. A repository's file is read only for its bindings (B15); its
// classes are a policy's, edited on the policy's page.

import type { Binding } from "./settings";

// PolicyFile is a repository's .github/agent-factory.yaml: its factory and its bindings (B15),
// which the page shows read-only.
export interface PolicyFile { factory: string; mergePolicies?: Binding[] }

function scalar(v: unknown): string {
  return typeof v === "string" ? JSON.stringify(v) : String(v);
}

function isScalar(v: unknown): boolean {
  return v === null || typeof v !== "object";
}

// toYAML writes a policy file as YAML: block mappings, scalar lists in flow, every string
// double-quoted (JSON's escapes are YAML's), so a pattern is never read as anything else.
export function toYAML(v: unknown, indent = 0): string {
  const pad = " ".repeat(indent);
  const lines: string[] = [];
  if (Array.isArray(v)) {
    for (const item of v) {
      if (isScalar(item)) { lines.push(`${pad}- ${scalar(item)}`); continue; }
      const sub = toYAML(item, indent + 2).split("\n");
      sub[0] = `${pad}- ${sub[0].slice(indent + 2)}`;
      lines.push(...sub);
    }
    return lines.join("\n");
  }
  for (const [k, val] of Object.entries(v as Record<string, unknown>)) {
    if (val === undefined || val === null) continue;
    if (isScalar(val)) lines.push(`${pad}${k}: ${scalar(val)}`);
    else if (Array.isArray(val) && val.every(isScalar)) lines.push(`${pad}${k}: [${val.map(scalar).join(", ")}]`);
    else if (!Array.isArray(val) && Object.keys(val as object).length === 0) continue;
    else lines.push(`${pad}${k}:`, toYAML(val, indent + 2));
  }
  return lines.join("\n");
}

function el<K extends keyof HTMLElementTagNameMap>(tag: K, text?: string, cls?: string): HTMLElementTagNameMap[K] {
  const e = document.createElement(tag);
  if (text !== undefined) e.textContent = text;
  if (cls) e.className = cls;
  return e;
}

// fileActions copies text or downloads it as the file at path. The label says it was copied:
// the change of word, not a motion, is the signal.
export function fileActions(text: () => string, path: string, label = "Copy"): HTMLElement[] {
  const copy = el("button", label, "copy");
  copy.type = "button";
  let reset: ReturnType<typeof setTimeout> | undefined;
  copy.onclick = async () => {
    try {
      await navigator.clipboard.writeText(text());
      copy.textContent = "Copied";
    } catch {
      copy.textContent = "Copy failed: select the text";
    }
    clearTimeout(reset);
    reset = setTimeout(() => { copy.textContent = label; }, 1500);
  };
  const download = el("a", "Download", "download");
  download.setAttribute("download", path.split("/").at(-1)!);
  download.href = "#";
  // The href is set at the click, so the download is the text as it is then.
  download.onclick = () => { download.href = `data:application/yaml;charset=utf-8,${encodeURIComponent(text())}`; };
  return [copy, download];
}
