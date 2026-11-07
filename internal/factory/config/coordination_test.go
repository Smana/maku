// SPDX-License-Identifier: Apache-2.0

package config

import (
	"strings"
	"testing"
)

// MR16: an omitted coordination block is off, public only, two children.
func TestCoordinationConfigDefaultsOff(t *testing.T) {
	c, err := Parse([]byte(good))
	if err != nil {
		t.Fatal(err)
	}
	if c.Coordination.Mode != CoordinationOff || !c.Coordination.EffectivePublicOnly() || c.Coordination.MaxChildren != 2 {
		t.Fatalf("defaults %+v", c.Coordination)
	}
	for _, mode := range []string{"plan", "execute"} {
		c, err := Parse([]byte(good + "coordination: {mode: " + mode + "}\n"))
		if err != nil {
			t.Fatalf("mode %s: %v", mode, err)
		}
		if c.Coordination.Mode != mode || !c.Coordination.EffectivePublicOnly() || c.Coordination.MaxChildren != 2 {
			t.Errorf("mode %s: %+v", mode, c.Coordination)
		}
	}
	// YAML 1.1 reads an unquoted off as false: it is still off.
	for _, block := range []string{"coordination: {mode: off}\n", "coordination: {mode: \"off\"}\n", "coordination:\n  mode: off\n  publicOnly: true\n"} {
		c, err := Parse([]byte(good + block))
		if err != nil || c.Coordination.Mode != CoordinationOff {
			t.Errorf("%q: %v %+v", block, err, c)
		}
	}
	// execute loads: readiness is dynamic, so the execute gate refuses at every admission instead.
	if _, err := Parse([]byte(good + "coordination: {mode: execute, publicOnly: true, maxChildren: 2}\n")); err != nil {
		t.Fatal(err)
	}
}

// MR1, MR16: any other mode, child count or publicOnly: false, and an unknown or duplicate key,
// fail startup.
func TestCoordinationConfigRefusals(t *testing.T) {
	for name, c := range map[string]struct{ block, want string }{
		"a bogus mode":       {"coordination: {mode: bogus}\n", "coordination.mode"},
		"one child":          {"coordination: {maxChildren: 1}\n", "coordination.maxChildren"},
		"three children":     {"coordination: {maxChildren: 3}\n", "coordination.maxChildren"},
		"private families":   {"coordination: {publicOnly: false}\n", "coordination.publicOnly"},
		"an unknown key":     {"coordination: {mode: plan, slack: true}\n", "unknown field"},
		"a duplicate key":    {"coordination:\n  mode: plan\n  mode: execute\n", "mode"},
		"a duplicate block":  {"coordination: {mode: plan}\ncoordination: {mode: execute}\n", "coordination"},
		"a mode in capitals": {"coordination: {mode: Execute}\n", "coordination.mode"},
		"a mode of on":       {"coordination: {mode: on}\n", "coordination.mode"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Parse([]byte(good + c.block))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("got %v, want an error naming %q", err, c.want)
			}
		})
	}
}
