// SPDX-License-Identifier: Apache-2.0

package envelope

import (
	"encoding/json"
	"strings"
	"testing"
)

// output_requested, output_applied and output_refused carry one Output record (hardening UH4): an
// id, a type, a digest, and the GitHub object written or one bounded reason.
func TestOutputRecordValidate(t *testing.T) {
	d := strings.Repeat("a", 64)
	ok := map[MessageKind]OutputRecord{
		KindOutputRequested: {ID: "0123456789abcdef", Type: "pr", Digest: d, Bytes: 40},
		KindOutputApplied:   {ID: "0123456789abcdef", Type: "pr", Digest: d, Object: "https://github.com/o/r/pull/7"},
		KindOutputRefused:   {ID: "0123456789abcdef", Type: "push", Digest: d, Reason: "protected_path_blocked"},
	}
	for k, r := range ok {
		if err := r.Validate(k); err != nil {
			t.Errorf("%s: %v", k, err)
		}
	}
	for name, tc := range map[string]struct {
		k MessageKind
		r OutputRecord
	}{
		"a short id":          {KindOutputRequested, OutputRecord{ID: "x", Type: "pr", Digest: d}},
		"no type":             {KindOutputRequested, OutputRecord{ID: "0123456789abcdef", Digest: d}},
		"a bad digest":        {KindOutputRequested, OutputRecord{ID: "0123456789abcdef", Type: "pr", Digest: "x"}},
		"a reason on request": {KindOutputRequested, OutputRecord{ID: "0123456789abcdef", Type: "pr", Digest: d, Reason: "x"}},
		"applied, no object":  {KindOutputApplied, OutputRecord{ID: "0123456789abcdef", Type: "pr", Digest: d}},
		"applied off GitHub":  {KindOutputApplied, OutputRecord{ID: "0123456789abcdef", Type: "pr", Digest: d, Object: "https://evil.example/x"}},
		"refused, prose":      {KindOutputRefused, OutputRecord{ID: "0123456789abcdef", Type: "pr", Digest: d, Reason: "Because I said so"}},
		"refused, no reason":  {KindOutputRefused, OutputRecord{ID: "0123456789abcdef", Type: "pr", Digest: d}},
		"another kind":        {KindChat, OutputRecord{ID: "0123456789abcdef", Type: "pr", Digest: d}},
	} {
		t.Run(name, func(t *testing.T) {
			if err := tc.r.Validate(tc.k); err == nil {
				t.Fatal("valid")
			}
		})
	}
	b, _ := json.Marshal(MessagePayload{Kind: KindOutputRequested, Delivery: DeliveryNone, Output: &OutputRecord{ID: "0123456789abcdef", Type: "pr", Digest: d}})
	if !strings.Contains(string(b), `"output":{"id":"0123456789abcdef","type":"pr","digest":"`) {
		t.Fatalf("%s", b)
	}
}

// output_shadowed is what the factory would have done with an observed write (hardening HR2,
// HR3): an object always, a reason when it would refuse or wait, and up to five audit findings,
// which only an applied or shadowed record may carry.
func TestOutputShadowedValidate(t *testing.T) {
	d := strings.Repeat("a", 64)
	base := func(mut func(*OutputRecord)) OutputRecord {
		r := OutputRecord{ID: "0123456789abcdef", Type: "pr", Digest: d, Object: "https://github.com/o/r/pull/7"}
		mut(&r)
		return r
	}
	five := []OutputFinding{
		{Layer: "t2_text", Reason: "rewritten"},
		{Layer: "t3_secrets", Reason: "secret_found"},
		{Layer: "t4_paths", Reason: "protected_path_blocked"},
		{Layer: "t5_classifier", Reason: "injection_suspected"},
		{Layer: "t1_schema", Reason: "output_cap"},
	}
	for _, tc := range []struct {
		name  string
		k     MessageKind
		r     OutputRecord
		valid bool
	}{
		{"shadowed, would apply", KindOutputShadowed, base(func(*OutputRecord) {}), true},
		{"shadowed, would refuse", KindOutputShadowed, base(func(r *OutputRecord) { r.Reason = "output_schema" }), true},
		{"shadowed, would wait", KindOutputShadowed, base(func(r *OutputRecord) { r.Reason = "would_wait" }), true},
		{"shadowed with five findings", KindOutputShadowed, base(func(r *OutputRecord) { r.Audit = five }), true},
		{"applied with a finding", KindOutputApplied, base(func(r *OutputRecord) { r.Audit = five[:1] }), true},
		{"shadowed, no object", KindOutputShadowed, base(func(r *OutputRecord) { r.Object = "" }), false},
		{"shadowed off GitHub", KindOutputShadowed, base(func(r *OutputRecord) { r.Object = "https://evil.example/x" }), false},
		{"shadowed, prose reason", KindOutputShadowed, base(func(r *OutputRecord) { r.Reason = "Looks bad" }), false},
		{"six findings", KindOutputShadowed, base(func(r *OutputRecord) { r.Audit = append(append([]OutputFinding{}, five...), five[0]) }), false},
		{"a finding's layer is not t1 to t5", KindOutputShadowed, base(func(r *OutputRecord) { r.Audit = []OutputFinding{{Layer: "t6_other", Reason: "x"}} }), false},
		{"a finding's layer has no name", KindOutputShadowed, base(func(r *OutputRecord) { r.Audit = []OutputFinding{{Layer: "t1", Reason: "x"}} }), false},
		{"a finding's reason is prose", KindOutputShadowed, base(func(r *OutputRecord) { r.Audit = []OutputFinding{{Layer: "t1_schema", Reason: "Too long"}} }), false},
		{"findings on a request", KindOutputRequested, OutputRecord{ID: "0123456789abcdef", Type: "pr", Digest: d, Audit: five[:1]}, false},
		{"findings on a refusal", KindOutputRefused, OutputRecord{ID: "0123456789abcdef", Type: "pr", Digest: d, Reason: "output_schema", Audit: five[:1]}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.r.Validate(tc.k); (err == nil) != tc.valid {
				t.Fatalf("Validate = %v, want valid %v", err, tc.valid)
			}
		})
	}
	b, _ := json.Marshal(OutputRecord{ID: "0123456789abcdef", Type: "pr", Digest: d, Audit: five[:1]})
	if !strings.Contains(string(b), `"audit":[{"layer":"t2_text","reason":"rewritten"}]`) {
		t.Fatalf("%s", b)
	}
}
