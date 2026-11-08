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
