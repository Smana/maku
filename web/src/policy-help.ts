// SPDX-License-Identifier: Apache-2.0

// What each field of a merge policy does, in one line: what happens when it is exceeded or unmet,
// with the reason a maintainer will read (correctness signals, guidance surface 1). And the
// fields the editor does not edit, read-only, in plain words.

export const FIELD_HELP: Record<string, string> = {
  description: "What the policy is for, in plain words. The gate never reads it.",
  allowedFiles: "A pull request that touches any other file waits for a maintainer (`file_not_allowed`).",
  protectedFiles: "One matching file sends the whole pull request to a maintainer (`file_protected`).",
  maxPatchFiles: "Above this, the pull request waits for a maintainer (`patch_too_large`).",
  maxModifiedLines: "Above this, the pull request waits for a maintainer (`patch_too_large`). Lines are additions plus deletions.",
  maxPerDay: "After this many, the next one waits until tomorrow (`auto_merge_cap`).",
  modifyOnly: "A file added, deleted or renamed sends the pull request to a maintainer (`file_added_or_deleted`).",
  diffShape: "Every changed line must have this shape, or the policy does not apply (`class_mismatch`). A shape is what lets Maku check the meaning.",
  holdoutChecks: "Each must report success from GitHub Actions on this exact commit. Skipped counts as failed (`holdout_red`). A check says something still works, not that the change is right.",
  requiresApproval: "A matching file asks a maintainer even when the policy is live (`approval_required`).",
  requires: "Tasks touching these files run these stages. A plan and tests together are what let Maku check the meaning of a configuration change.",
  evidence: "How many decisions a maintainer must agree with before promotion: 5 when a shape or an oracle checks the meaning, else 20.",
  monitor: "What Maku watches after a merge, and for how long. Silence here is quiet, not right.",
  oracle: "A scheduled workflow names the defects. The pull request must fix exactly those (`oracle_unrequested`, `oracle_finding_open`).",
  author: "Whose pull requests this policy takes: the factory's agents, or another bot's by login and branch.",
  companionFiles: "Files that may grow beside the one added entry; any other change does not fit the shape (`class_mismatch`).",
  requiredLabel: "An adopted pull request without this label, set by an adopted login, is not taken.",
  releaseAgeCheck: "The commit status that holds the release age; until it passes, the pull request waits.",
};

const STAGES: Record<string, string> = {
  plan: "a plan you approve", tests: "tests written before the change", judge: "a completion judge", review: "a review",
};

const natural = (xs: string[]) => xs.length < 2 ? xs[0] ?? "" : `${xs.slice(0, -1).join(", ")} and ${xs.at(-1)}`;

// durationWords reads a Go duration ("2h0m0s", "30m", "168h") as "2 h", "30 min", "7 days".
export function durationWords(v: unknown): string {
  if (typeof v !== "string") return "";
  const n = (u: string) => Number(v.match(new RegExp(`(\\d+(?:\\.\\d+)?)${u}`))?.[1] ?? 0);
  const s = n("h") * 3600 + n("m(?!s)") * 60 + n("s");
  if (s <= 0) return v;
  if (s < 3600) return `${Math.round(s / 60)} min`;
  if (s >= 7 * 86400 && s % 86400 === 0) return `${s / 86400} days`;
  return `${Math.round(s / 3600)} h`;
}

// readOnlyWords is a field the editor does not edit, in plain words; "" when it says nothing.
export function readOnlyWords(key: string, value: unknown): string {
  const o = (value ?? {}) as Record<string, unknown>;
  switch (key) {
    case "requires": {
      const stages = Array.isArray(value) ? value.map((x) => STAGES[String(x)] ?? String(x)) : [];
      return stages.length ? `Tasks that touch these files run: ${natural(stages)}` : "";
    }
    case "evidence":
      return value === "low" ? "5 agreed decisions before promotion" : "20 agreed decisions before promotion";
    case "monitor": {
      const parts: string[] = [];
      const flux = o.flux as Record<string, unknown> | undefined;
      const verification = o.verification as Record<string, unknown> | undefined;
      if (flux?.within) parts.push(`watches Flux for ${durationWords(flux.within)}`);
      if (verification?.within) parts.push(`waits ${durationWords(verification.within)} for RunLore's verdict`);
      if (typeof o.oracleRuns === "number" && o.oracleRuns > 0) parts.push(`reads the detector's next ${o.oracleRuns} runs`);
      if (o.followUpWindow) parts.push(`reads follow-up fixes for ${durationWords(o.followUpWindow)}`);
      return parts.length ? `After a merge, ${natural(parts)}.` : "";
    }
    case "oracle":
      return `Detector \`${String(o.probe ?? "")}\`, at most ${Number(o.maxFindings ?? 0)} findings a task, reported by ${o.workflow ? `\`${String(o.workflow)}\`` : "the binding's workflow"}`;
    case "author": {
      const adopt = o.adopt as { logins?: string[]; branches?: string[] } | undefined;
      if (!adopt) return "The factory's agents";
      const branches = (adopt.branches ?? []).map((b) => b.replace(/^\^/, ""));
      return `Pull requests of ${natural(adopt.logins ?? [])} from branches starting ${natural(branches)}`;
    }
  }
  return "";
}

// READ_ONLY are the fields the form does not edit, shown read-only with their words.
export const READ_ONLY = ["requires", "evidence", "monitor", "oracle", "author"];
