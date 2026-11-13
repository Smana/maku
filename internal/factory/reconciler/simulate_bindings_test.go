// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"maps"
	"slices"
	"testing"

	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/mergepolicy"
)

// boundAsFile is the conformance config's classes split into class-shape policies and the
// repository's bindings (B4), then through the stage-4 file's round trip: Export, Marshal, Parse
// and ValidateFile, as the factory reads a file in the binding shape (B15).
func boundAsFile(t *testing.T, c *config.Config) mergepolicy.Snapshot {
	t.Helper()
	rc := *c.RepositoryFor("")
	c.Policies = map[string]config.ServedPolicy{}
	rc.Bindings = nil
	for _, name := range slices.Sorted(maps.Keys(rc.Classes)) {
		src := rc.Classes[name]
		cl := *src.DeepCopy()
		b := config.Binding{Name: name, Mode: config.ModeOff}
		switch {
		case cl.Live:
			b.Mode = config.ModeLive
		case cl.Shadow:
			b.Mode = config.ModeShadow
		}
		cl.Live, cl.Shadow = false, false
		if cl.Oracle != nil {
			b.OracleWorkflow, cl.Oracle.Workflow = cl.Oracle.Workflow, ""
		}
		if cl.Monitor != nil && cl.Monitor.Flux != nil {
			rc.Merge.FluxSource, cl.Monitor.Flux.Source = cl.Monitor.Flux.Source, ""
		}
		c.Policies[name] = config.ServedPolicy{Spec: config.PolicySpec{Class: cl}, Generation: 1}
		rc.Bindings = append(rc.Bindings, b)
	}
	f, err := mergepolicy.Export(&rc, c)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := mergepolicy.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	if f, err = mergepolicy.Parse(raw); err != nil {
		t.Fatalf("%v\n%s", err, raw)
	}
	rep, err := mergepolicy.ValidateFile(f, &rc, c)
	if err != nil || len(rep.BindingErrors) != 0 {
		t.Fatalf("%v %+v\n%s", err, rep.BindingErrors, raw)
	}
	if d := mergepolicy.Compare(&rc, f, c); len(d) != 0 {
		t.Fatalf("divergences %q", d)
	}
	return mergepolicy.Snapshot{Hash: "0123456789abcdef", HasClasses: true, File: f, Classes: rep.Classes}
}

// Stage 2 and 3 for the binding shape (B15): the central classes, written as a binding file,
// decide every conformance fixture as the central config does, and dual evaluation agrees.
func TestConformanceOfABindingFile(t *testing.T) {
	c := conformanceCfg()
	snap := boundAsFile(t, c)
	for _, row := range Simulate(c, snap.Classes, conformanceCases(t)) {
		if row.Change != ChangeUnchanged {
			t.Errorf("%s: central %+v, binding file %+v", row.Name, row.Central, row.Repository)
		}
	}
	for _, cs := range conformanceCases(t) {
		in := cs.Inputs
		in.Cfg = c
		if _, got := dualOf(DecideArm(in), in, snap, true); got != "agree" {
			t.Errorf("%s: dual=%s", cs.Name, got)
		}
	}
}
