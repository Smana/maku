// SPDX-License-Identifier: Apache-2.0

package api

import "testing"

func TestRunRequestHarnessValidation(t *testing.T) {
	base := RunRequest{Role: "implementer", DataClass: "public"}
	base.Task.Text = "fix it"

	for _, h := range []string{"", "openhands", "opencode"} {
		in := base
		in.Harness = h
		if got := validate(in); got != "" {
			t.Errorf("harness %q: validate = %q, want empty", h, got)
		}
	}
	bad := base
	bad.Harness = "other"
	if got := validate(bad); got != "bad_harness" {
		t.Fatalf("invalid harness: validate = %q, want bad_harness", got)
	}
}
