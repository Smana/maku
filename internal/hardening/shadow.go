// SPDX-License-Identifier: Apache-2.0

package hardening

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
)

// ShadowVerdict is what the threat check would have done with a write a run made itself (HR1, HR3).
type ShadowVerdict string

// The verdicts. A protected path that needs approval would wait for a maintainer under H3, by
// design, so it is never counted as a refusal.
const (
	WouldApply  ShadowVerdict = "would_apply"
	WouldRefuse ShadowVerdict = "would_refuse"
	WouldWait   ShadowVerdict = "would_wait"
)

// ObservedWrite is one write a run made on GitHub with its own token, as the shadow pass read it
// back, shaped as the output request the run would have sent (HR1).
//   - Payload is the output type's JSON for pr, comment, labels and issue: a PR body with the
//     harness footer already stripped (StripHarnessFooter), a comment with its target.
//   - A push has no bundle to read back: Payload is whatever identifies it (its ref and SHAs), only
//     digested, and Files and Added are the pushed diff's paths and added lines and commit messages.
//   - Object is the GitHub URL written to.
type ObservedWrite struct {
	Type    OutputType
	Payload json.RawMessage
	Object  string
	Files   []string
	Added   []string
	// DiffUnavailable: a push whose diff could not be read whole; T4 records diff_unavailable.
	DiffUnavailable bool
}

// ShadowResult is one output_shadowed record's facts. Layer and Reason are the verdict's finding,
// empty on would_apply; Audit is every other finding, T5's included, in layer order; Report counts
// what T2 would have rewritten, which is never a refusal.
type ShadowResult struct {
	Type    OutputType
	Object  string
	Digest  string
	Verdict ShadowVerdict
	Layer   Layer
	Reason  Refusal
	Audit   []Finding
	Report  TextReport
}

// shadowBundle stands in for a push's bundle, which an observed push does not have: T1 then only
// counts the push against its cap. The bundle's ref and tree are the H3 applier's checks.
var shadowBundle = json.RawMessage(`{"bundle":"AA=="}`)

// Shadow checks w as the request the run would have sent, every layer in audit whatever c's modes
// (T5 at most audit, and off when c has it off), counting it against n. The first T1–T4 finding
// decides the verdict; T5 never does (HR3).
func Shadow(ctx context.Context, c Checker, n *Counts, w ObservedWrite, ownIssue int, canary string) ShadowResult {
	t5 := ModeOff
	if c.Layers.Classifier.AtLeast(ModeAudit) {
		t5 = ModeAudit
	}
	c.Layers = Layers{Schema: ModeAudit, Sanitise: ModeAudit, Secrets: ModeAudit, Paths: ModeAudit, Classifier: t5}
	req := Request{Type: w.Type, Payload: w.Payload, OwnIssue: ownIssue, Canary: canary, Files: w.Files, Added: w.Added,
		DiffUnavailable: w.DiffUnavailable}
	if w.Type == Push {
		req.Payload = shadowBundle
	}
	v := c.Check(ctx, n, req)
	r := ShadowResult{Type: w.Type, Object: w.Object, Digest: canonicalDigest(w.Payload), Verdict: WouldApply, Report: v.Report}
	for _, f := range v.Audit {
		if r.Layer != "" || f.Layer == LayerClassifier {
			r.Audit = append(r.Audit, f)
			continue
		}
		r.Layer, r.Reason, r.Verdict = f.Layer, f.Refusal, WouldRefuse
		if f.Refusal == RefuseNeedsApproval {
			r.Verdict = WouldWait
		}
	}
	return r
}

// canonicalDigest is Digest of p re-encoded by encoding/json, which sorts object keys, with no
// HTML escaping, so the spelling of the observed JSON never moves it. A payload that is not JSON is
// digested as read.
func canonicalDigest(p json.RawMessage) string {
	var v any
	dec := json.NewDecoder(bytes.NewReader(p))
	dec.UseNumber() // a number keeps its digits: float64 would round a large one
	if err := dec.Decode(&v); err != nil || dec.Decode(&struct{}{}) != io.EOF {
		return Digest(p)
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false) // <, > and & as written, as any other JSON encoder writes them
	if err := enc.Encode(v); err != nil {
		return Digest(p)
	}
	return Digest(bytes.TrimSuffix(b.Bytes(), []byte("\n")))
}
