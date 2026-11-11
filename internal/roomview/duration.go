// SPDX-License-Identifier: Apache-2.0

package roomview

import (
	"time"

	"github.com/Smana/maku/internal/summary"
)

// spanText and agoText are the one duration rule (summary.Span, summary.Ago), which
// testdata/durations.json pins here and on the web.
func spanText(d time.Duration) string { return summary.Span(d) }
func agoText(d time.Duration) string  { return summary.Ago(d) }
