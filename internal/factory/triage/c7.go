// SPDX-License-Identifier: Apache-2.0

package triage

import "context"

// C7Request and C7Response are the programme's C7 contract, verbatim. No C7 service is wired since
// triage.classifierURL was removed (WF9); a test drives the tier through Classifier.
type C7Request struct {
	Text      string `json:"text"`
	Ref       string `json:"ref"`
	DataClass string `json:"dataClass"`
}

// C7Shadow is a classifier consulted in shadow: recorded, never acted on.
type C7Shadow struct {
	Classifier string  `json:"classifier"`
	Tier       string  `json:"tier"`
	Confidence float64 `json:"confidence"`
}

// C7Response is the classifier's verdict: the tier, how sure it is, and what it fell back to.
type C7Response struct {
	Tier       string     `json:"tier"`
	Confidence float64    `json:"confidence"`
	Classifier string     `json:"classifier"`
	Fallback   string     `json:"fallback"`
	Shadow     []C7Shadow `json:"shadow"`
}

// Classifier is C7: one call per task, and one that never blocks (an error falls back to static).
type Classifier interface {
	Classify(ctx context.Context, r C7Request) (C7Response, error)
}
