// SPDX-License-Identifier: Apache-2.0

package meter

import "testing"

func TestOffReadsNothing(t *testing.T) {
	var src Source = Off{}
	got, err := src.RunUsage(t.Context())
	if err != nil || len(got) != 0 {
		t.Fatalf("RunUsage = %v, %v; want no usage and no error", got, err)
	}
	if s := (Off{}).String(); s != "off (demo)" {
		t.Fatalf("String = %q", s)
	}
}
