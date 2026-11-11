// SPDX-License-Identifier: Apache-2.0

package summary

import (
	"strconv"
	"time"
)

// units is d as its two largest units, floored: the first always, the second zero when there is none
// (under a minute, under an hour) or it is nothing. A negative d reads as 0.
func units(d time.Duration) (a time.Duration, au string, b time.Duration, bu string) {
	d = max(0, d)
	switch {
	case d < time.Minute:
		return d / time.Second, "s", 0, ""
	case d < time.Hour:
		return d / time.Minute, "min", 0, ""
	case d < 24*time.Hour:
		return d / time.Hour, "h", d % time.Hour / time.Minute, "min"
	}
	return d / (24 * time.Hour), "d", d % (24 * time.Hour) / time.Hour, "h"
}

// Span is a length of time as its two largest units: "40 s", "12 min", "5 h 47 min", "37 d 17 h".
// A zero second unit is left out. The web's span (web/src/duration.ts) is the same rule;
// internal/roomview/testdata/durations.json pins both. It lives here so the run status and the
// room views, which import this package, share the one implementation.
func Span(d time.Duration) string {
	a, au, b, bu := units(d)
	s := strconv.FormatInt(int64(a), 10) + " " + au
	if b > 0 {
		s += " " + strconv.FormatInt(int64(b), 10) + " " + bu
	}
	return s
}

// Ago is how long ago d was: the largest unit only, "just now" under a minute. There is no date
// cap: a month old reads "37 d ago".
func Ago(d time.Duration) string {
	if d < time.Minute {
		return "just now"
	}
	a, au, _, _ := units(d)
	return strconv.FormatInt(int64(a), 10) + " " + au + " ago"
}
