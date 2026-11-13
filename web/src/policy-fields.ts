// SPDX-License-Identifier: Apache-2.0

// The class editor's fields, grouped by the question each answers. A field is shown only where the
// factory accepts it (docs/merge-policies.md, Reference): the adopted leaves on an adopted class,
// companionFiles with an added-entry shape, harnessAllowed and escalation on an off class. Labels
// are words; the key stays beside them in mono, so a reader of the YAML finds it.

import type { ClassPolicy } from "./policy-editor";

export type Group = "intent" | "who" | "touch" | "size" | "pass" | "ask" | "limits";
export interface Field {
  key: string; label: string; help: string; anchor: string; group: Group;
  kind: "list" | "number" | "text" | "bool" | "shape";
  shown(c: ClassPolicy): boolean;
}

export const DOCS = "https://github.com/Smana/maku/blob/main/docs/merge-policies.md";
export const docsLink = (anchor: string) => `${DOCS}#${anchor}`;

export const GROUPS: { id: Group; title: string; add: string }[] = [
  { id: "intent", title: "What it is for", add: "+ Describe what this class is for" },
  { id: "who", title: "Who writes it", add: "" },
  { id: "touch", title: "What it may touch", add: "Add file rules" },
  { id: "size", title: "How big it may be", add: "Add a size limit" },
  { id: "pass", title: "What must pass", add: "Add a check" },
  { id: "ask", title: "When to ask a maintainer", add: "Add an approval rule" },
  { id: "limits", title: "Limits and follow-up", add: "Add a daily cap or a follow-up" },
];

const always = () => true;
const adopted = (c: ClassPolicy) => c.author === "adopt";
const off = (c: ClassPolicy) => c.mode === "off";
const shape = (c: ClassPolicy) => c.conditions?.all?.find((l) => "diffShape" in l)?.diffShape;

export const FIELDS: Field[] = [
  { key: "description", label: "What it is for", group: "intent", kind: "text", anchor: "description", shown: always,
    help: "One or two sentences a maintainer reads first on the class's card; the gate never reads it." },
  { key: "adopt.logins", label: "Bot accounts it adopts", group: "who", kind: "list", anchor: "a-class", shown: adopted,
    help: "The GitHub App logins whose pull requests this class decides, such as renovate[bot]. One per line." },
  { key: "adopt.branches", label: "Their branch prefixes", group: "who", kind: "list", anchor: "a-class", shown: adopted,
    help: "^ plus a literal prefix, such as ^renovate/. A pull request on another branch is not this class's." },
  { key: "harnessAllowed", label: "Other harness profiles a run may use", group: "who", kind: "list", anchor: "central-config", shown: off,
    help: "Profiles the scoper may pick for this class's runs. Only an off or review class takes them." },
  { key: "escalation.outOfBudget", label: "Profile when a run runs out of budget", group: "who", kind: "text", anchor: "central-config", shown: off,
    help: "One of the profiles above. Empty: the task goes to a human." },
  { key: "escalation.notConverged", label: "Profile when review does not converge", group: "who", kind: "text", anchor: "central-config", shown: off,
    help: "One of the profiles above. Empty: the task goes to a human." },
  { key: "allowedFiles", label: "Files it may change", group: "touch", kind: "list", anchor: "leaves", shown: always,
    help: "Regular expressions, one per line, from ^ to $. A pull request that changes any other file is not this class's." },
  { key: "companionFiles", label: "Files that may change with the entry", group: "touch", kind: "list", anchor: "leaves", shown: (c) => shape(c) === "added-entry",
    help: "Regular expressions for the files an added entry may also touch, anchored like the allowed files." },
  { key: "protectedFiles", label: "Files it must never touch", group: "touch", kind: "list", anchor: "leaves", shown: always,
    help: "Regular expressions, one per line. A match refuses the pull request, on top of the factory's own protected paths." },
  { key: "maxPatchFiles", label: "Most files changed", group: "size", kind: "number", anchor: "leaves", shown: always,
    help: "At most the factory's cap. Empty: the cap applies." },
  { key: "maxModifiedLines", label: "Most lines changed", group: "size", kind: "number", anchor: "leaves", shown: always,
    help: "Additions plus deletions, as GitHub counts them. At most the factory's cap. Empty: the cap applies." },
  { key: "modifyOnly", label: "Only modify existing files", group: "size", kind: "bool", anchor: "leaves", shown: always,
    help: "Refuses a pull request that adds, deletes or renames a file." },
  { key: "diffShape", label: "Shape of the change", group: "size", kind: "shape", anchor: "leaves", shown: always,
    help: "links-only for an agents class; the version bumps and added-entry for an adopted one." },
  { key: "holdoutChecks", label: "Checks that must pass", group: "pass", kind: "list", anchor: "leaves", shown: always,
    help: "GitHub Actions check names, one per line, green on the pull request's head. Their workflow is a protected path." },
  { key: "releaseAgeCheck", label: "Release-age status", group: "pass", kind: "text", anchor: "leaves", shown: adopted,
    help: "A commit status the adopted bot posts once a release is old enough, such as renovate/stability-days." },
  { key: "requiredLabel", label: "Label it must carry", group: "pass", kind: "text", anchor: "leaves", shown: adopted,
    help: "A label the adopted bot sets, 1 to 50 characters." },
  { key: "changedFiles", label: "Changes that need a maintainer's approval", group: "ask", kind: "list", anchor: "a-class", shown: always,
    help: "Regular expressions, one per line. A match sends the pull request to a maintainer, even in a live class." },
  { key: "maxPerDay", label: "Most merges a day", group: "limits", kind: "number", anchor: "a-class", shown: always,
    help: "This class's own daily cap, at most the factory's. Empty: the factory's cap." },
  { key: "followUpWindow", label: "Watch after merge for", group: "limits", kind: "text", anchor: "a-class", shown: always,
    help: "How long the post-merge monitor follows a merge, 1h to 720h, such as 168h. Empty: no monitor." },
];

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
