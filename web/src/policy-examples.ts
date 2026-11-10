// SPDX-License-Identifier: Apache-2.0

// The gallery's words for each template in policy-templates.json: its level and why a maintainer
// would start from it. The templates themselves stay in the JSON, so the factory's own check
// (mergepolicy.TestEveryEditorTemplatePassesCheck) keeps every example valid.

export interface Example { name: string; level: "simple" | "advanced"; why: string }

export const EXAMPLES: Example[] = [
  { name: "docs-broad", level: "simple",
    why: "Let agents fix documentation without a human merge: small, docs-only changes whose links still resolve, with sensitive pages kept out." },
  { name: "deps-patch", level: "simple",
    why: "Let Renovate's patch bumps merge on their own once the release is old enough, as long as nothing is added or deleted." },
  { name: "generated-files", level: "simple",
    why: "Let a regeneration land when it only rewrites generated files and regenerating them again gives the same result." },
  { name: "deps-minor", level: "advanced",
    why: "Watch Renovate's minor bumps in shadow to learn how often they are safe. The factory keeps this shape in shadow only." },
  { name: "docs-or-tests", level: "advanced",
    why: "One class for two kinds of small change, docs or tests, each with its own check, using an any of two branches." },
  { name: "infra-with-approval", level: "advanced",
    why: "Small infrastructure changes that pass validation, never production or security paths, and a maintainer's yes for network changes." },
];
