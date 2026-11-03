// SPDX-License-Identifier: Apache-2.0

package ledger

import (
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// now is the tests' clock: the day ledger it names is agent-factory-ledger-usd-20260927, as in
// the API's tests.
var now = time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)

// scheme knows ConfigMaps, the ledger's one kind.
func scheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = corev1.AddToScheme(s)
	return s
}
