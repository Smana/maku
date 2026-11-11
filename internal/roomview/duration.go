// SPDX-License-Identifier: Apache-2.0

package roomview

import (
	"strconv"
	"strings"
	"time"
)

// spanText is a length of time as its two largest units: "40 s", "12 min", "5 h 47 min", "37 d 17 h".
// A zero second unit is left out, each unit is floored and a negative d reads as 0. The web's
// span (web/src/duration.ts) is the same rule; testdata/durations.json pins both.
func spanText(d time.Duration) string {
	d = max(0, d)
	n := func(v time.Duration, unit string) string { return strconv.FormatInt(int64(v), 10) + " " + unit }
	two := func(a time.Duration, au string, b time.Duration, bu string) string {
		if b == 0 {
			return n(a, au)
		}
		return n(a, au) + " " + n(b, bu)
	}
	switch {
	case d < time.Minute:
		return n(d/time.Second, "s")
	case d < time.Hour:
		return n(d/time.Minute, "min")
	case d < 24*time.Hour:
		return two(d/time.Hour, "h", d%time.Hour/time.Minute, "min")
	}
	return two(d/(24*time.Hour), "d", d%(24*time.Hour)/time.Hour, "h")
}

// agoText is how long ago d was: the largest unit only, "just now" under a minute. There is no
// date cap: a month old reads "37 d ago".
func agoText(d time.Duration) string {
	if d < time.Minute {
		return "just now"
	}
	s := spanText(d)
	if i := strings.Index(s, " "); i >= 0 {
		if j := strings.Index(s[i+1:], " "); j >= 0 {
			s = s[:i+1+j]
		}
	}
	return s + " ago"
}
