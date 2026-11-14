// SPDX-License-Identifier: Apache-2.0

// A binding's parameters (B4), as the bindings editor offers them, and the policy's meaning keys it
// shows read-only beside them.

// BindingField is one parameter a binding supplies (B4): fill-once, additive or a cap. The policy's
// meaning fields are never a binding's: the binding editor shows them, read-only.
export interface BindingField { key: string; label: string; kind: "list" | "number" | "text" | "approval" }

export const BINDING_FIELDS: BindingField[] = [
  { key: "allowedFiles", label: "Files it may change (fill once)", kind: "list" },
  { key: "companionFiles", label: "Files that may change with the entry (fill once)", kind: "list" },
  { key: "requiredLabel", label: "Label it must carry (fill once)", kind: "text" },
  { key: "oracleWorkflow", label: "Workflow reporting the oracle's findings (fill once)", kind: "text" },
  { key: "holdoutChecks", label: "Checks that must pass (added to the policy's)", kind: "list" },
  { key: "protectedFiles", label: "Files it must never touch (added to the policy's)", kind: "list" },
  { key: "requiresApproval", label: "Changes that need a maintainer's approval (added to the policy's; one block of patterns per rule, a blank line between)", kind: "approval" },
  { key: "maxPatchFiles", label: "Most files changed (at most the policy's)", kind: "number" },
  { key: "maxModifiedLines", label: "Most lines changed (at most the policy's)", kind: "number" },
  { key: "maxPerDay", label: "Most merges a day (at most the policy's)", kind: "number" },
];

// MEANING_KEYS are a policy's meaning fields (B4): the policy's, shown read-only in a binding edit.
export const MEANING_KEYS = ["description", "author", "harness", "harnessAllowed", "escalation", "checks", "testFirst", "diffShape",
  "modifyOnly", "oracle", "releaseAgeCheck", "monitor"];
