// SPDX-License-Identifier: Apache-2.0

package intake_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Smana/maku/internal/intake"
)

// CD18: the model call fails three ways, and each is one reason of the closed list with the one
// sentence this package holds for it — never a vendor's error text and never a reason a call site
// spelled itself. The field-and-bound diagnostic rides the sentence that has a place for it.
func TestTheSentinelsMapToTheClosedRefusals(t *testing.T) {
	sentences := intake.Sentences()
	for name, tc := range map[string]struct {
		err    error
		reason string
		detail string
	}{
		"a reply that is not the contract's shape": {
			err:    fmt.Errorf("%w: issues: 4, the contract asks for 1 to 3", intake.ErrInvalid),
			reason: intake.ReasonPlannerInvalid,
			detail: "issues: 4, the contract asks for 1 to 3",
		},
		"a call that outlived its deadline": {
			err:    fmt.Errorf("%w: the call outlived its 20s deadline", intake.ErrTimeout),
			reason: intake.ReasonPlannerTimeout,
		},
		"a gateway the broker could not reach": {
			err:    fmt.Errorf("%w: the gateway answered 503", intake.ErrUnavailable),
			reason: intake.ReasonPlannerUnavailable,
		},
	} {
		r, ok := intake.RefusalOf(tc.err)
		if !ok || r.Reason != tc.reason {
			t.Fatalf("%s: %+v %v", name, r, ok)
		}
		if want := sentences[tc.reason]; tc.detail == "" && r.Sentence != want {
			t.Errorf("%s: sentence %q, the closed list's is %q", name, r.Sentence, want)
		}
		if tc.detail != "" && !strings.Contains(r.Sentence, tc.detail) {
			t.Errorf("%s: %q does not name the field and the bound", name, r.Sentence)
		}
		if !strings.HasPrefix(r.Error(), tc.reason) {
			t.Errorf("%s: %q does not read as its reason", name, r.Error())
		}
	}
	// An error that is none of the three is not a refusal: the caller answers its own.
	if r, ok := intake.RefusalOf(errors.New("something else")); ok || r != nil {
		t.Fatalf("%+v %v", r, ok)
	}
}

// A caller that decides a reason from something other than a model call builds its refusal here, so
// its sentence is the closed list's and not one it wrote.
func TestAReasonAloneIsARefusal(t *testing.T) {
	for _, reason := range []string{intake.ReasonNotLinked, intake.ReasonDailyPlansReached, intake.ReasonRepositoryUnverified} {
		r := intake.ReasonRefusal(reason)
		if r.Reason != reason || r.Sentence != intake.Sentences()[reason] {
			t.Errorf("%s: %+v", reason, r)
		}
	}
}
