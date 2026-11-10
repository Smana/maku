// SPDX-License-Identifier: Apache-2.0

package statuswriter

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// keepTimes is apimeta's rule: the stored lastTransitionTime stays while Type and Status do,
// whatever the reason and message say; a new status takes the desired time.
func TestKeepTimesFollowsTypeAndStatus(t *testing.T) {
	then := metav1.NewTime(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC))
	stored := []metav1.Condition{
		{Type: "Accepted", Status: metav1.ConditionFalse, Reason: "Invalid", Message: "one", LastTransitionTime: then},
		{Type: "Connected", Status: metav1.ConditionTrue, Reason: "Healthy", LastTransitionTime: then},
	}
	desired := []metav1.Condition{
		{Type: "Accepted", Status: metav1.ConditionFalse, Reason: "DefinedTwice", Message: "two", LastTransitionTime: now},
		{Type: "Connected", Status: metav1.ConditionFalse, Reason: "Unhealthy", LastTransitionTime: now},
	}
	got := keepTimes(stored, desired)
	if !got[0].LastTransitionTime.Equal(&then) {
		t.Errorf("Accepted stayed False: time %v, want the stored %v", got[0].LastTransitionTime, then)
	}
	if !got[1].LastTransitionTime.Equal(&now) {
		t.Errorf("Connected turned False: time %v, want now", got[1].LastTransitionTime)
	}
}
