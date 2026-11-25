// SPDX-License-Identifier: Apache-2.0

// A binding's parameters and the policy's read-only meaning. Fields appear only where the policy
// makes them valid: repository-specific fill-once values are not offered when the policy sets them.
export type BindingGroup = "essential" | "shape" | "expert";
export interface BindingField { key: string; label: string; kind: "list" | "number" | "text" | "approval"; group: BindingGroup }

export const BINDING_FIELDS: BindingField[] = [
  { key: "allowedFiles", label: "Files it may change", kind: "list", group: "essential" },
  { key: "requiredLabel", label: "Label it must carry", kind: "text", group: "essential" },
  { key: "maxPerDay", label: "Most merges a day (at most the policy's)", kind: "number", group: "essential" },
  { key: "companionFiles", label: "Files that may change with the entry", kind: "list", group: "shape" },
  { key: "oracleWorkflow", label: "Workflow reporting the oracle's findings", kind: "text", group: "shape" },
  { key: "holdoutChecks", label: "Checks that must pass (added to the policy's)", kind: "list", group: "shape" },
  { key: "protectedFiles", label: "Files it must never touch (added to the policy's)", kind: "list", group: "expert" },
  { key: "requiresApproval", label: "Changes that need a maintainer's approval (one block of patterns per rule, a blank line between)", kind: "approval", group: "expert" },
  { key: "maxPatchFiles", label: "Most files changed (at most the policy's)", kind: "number", group: "expert" },
  { key: "maxModifiedLines", label: "Most lines changed (at most the policy's)", kind: "number", group: "expert" },
];

export function bindingFieldValid(key: string, meaning: Record<string, unknown> | undefined): boolean {
  const set = (k: string) => {
    const v = meaning?.[k];
    return v !== undefined && v !== "" && v !== false && (!Array.isArray(v) || v.length > 0);
  };
  if (key === "companionFiles") return meaning?.diffShape === "added-entry" && !set(key);
  if (key === "oracleWorkflow") return set("oracle");
  if (key === "requiredLabel") return !set(key) && (set("author") || meaning?.diffShape === "added-entry");
  return true;
}

export const MEANING_KEYS = ["description", "allowedFiles", "companionFiles", "requiredLabel", "protectedFiles", "requiresApproval",
  "modifyOnly", "maxPatchFiles", "maxModifiedLines", "maxPerDay", "evidence", "diffShape", "holdoutChecks", "oracle", "monitor",
  "author", "releaseAgeCheck"];
