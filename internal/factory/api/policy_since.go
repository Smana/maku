// SPDX-License-Identifier: Apache-2.0

package api

import (
	"strconv"
	"strings"
	"time"
)

func (s *Server) policySince(value string) string {
	if value != "" && !strings.HasSuffix(value, "d") {
		return value
	}
	return policySince(s.Now(), value)
}

func policySince(now time.Time, value string) string {
	if value == "" {
		return now.AddDate(0, 0, -30).UTC().Format(time.DateOnly)
	}
	if days, ok := strings.CutSuffix(value, "d"); ok {
		if n, err := strconv.Atoi(days); err == nil && n > 0 {
			return now.AddDate(0, 0, -n).UTC().Format(time.DateOnly)
		}
	}
	return value
}
