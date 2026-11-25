// SPDX-License-Identifier: Apache-2.0

package intake

import (
	"slices"
	"testing"
)

func TestParseDependsOn(t *testing.T) {
	for name, tc := range map[string]struct {
		body string
		self int
		want []int32
	}{
		"none": {
			body: "plain issue body",
			self: 10,
			want: nil,
		},
		"single": {
			body: "Waits for #12\n\n<!-- maku depends-on: #12 -->",
			self: 13,
			want: []int32{12},
		},
		"multiple dedup and self excluded": {
			body: "<!-- maku depends-on: #12, #13, #12, #15 -->",
			self: 15,
			want: []int32{12, 13},
		},
		"capped at four": {
			body: "<!-- maku depends-on: #1, #2, #3, #4, #5, #6 -->",
			self: 99,
			want: []int32{1, 2, 3, 4},
		},
		"case insensitive spacing": {
			body: "<!--   MAKU depends-on:   #7, #8  -->",
			self: 9,
			want: []int32{7, 8},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if got := ParseDependsOn(tc.body, tc.self); !slices.Equal(got, tc.want) {
				t.Fatalf("ParseDependsOn() = %v, want %v", got, tc.want)
			}
		})
	}
}
