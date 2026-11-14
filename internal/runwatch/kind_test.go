// SPDX-License-Identifier: Apache-2.0

package runwatch

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func kindClaim(apiVersion, ns, name string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion(apiVersion)
	u.SetKind("AgentRun")
	u.SetNamespace(ns)
	u.SetName(name)
	return u
}

// An install that configures nothing gets the run-controller's kind, as the chart renders it (D5).
func TestDefaultKindIsTheRunControllers(t *testing.T) {
	k := Kind{}.WithDefaults()
	if g := k.GVK(); g.Group != "maku.ogenki.io" || g.Version != "v1alpha1" || g.Kind != "AgentRun" {
		t.Fatalf("GVK %v", g)
	}
	if k.Namespace != "agents" || k.ClaimName("abcdefgh") != "run-abcdefgh" {
		t.Fatalf("namespace %q claim %q", k.Namespace, k.ClaimName("abcdefgh"))
	}
	if err := k.Validate(); err != nil {
		t.Fatalf("the default kind is refused: %v", err)
	}
	if m := k.RunSubject().FindStringSubmatch("system:serviceaccount:agents:run-abcdefgh"); len(m) != 2 || m[1] != "abcdefgh" {
		t.Fatalf("subject match %v", m)
	}
}

// D9: a kind answers for itself, whatever kind the process was configured with.
func TestAKindAnswersForItself(t *testing.T) {
	native := Kind{Group: "maku.ogenki.io", Namespace: "runs", NamePrefix: "run-"}
	if g := native.GVK(); g != (schema.GroupVersionKind{Group: "maku.ogenki.io", Version: "v1alpha1", Kind: "AgentRun"}) {
		t.Fatalf("GVK %v", g)
	}
	if n := native.ClaimName("abcdefgh"); n != "run-abcdefgh" {
		t.Fatalf("claim name %q", n)
	}
	if m := native.RunSubject().FindStringSubmatch("system:serviceaccount:runs:run-abcdefgh"); len(m) != 2 || m[1] != "abcdefgh" {
		t.Fatalf("subject match %v", m)
	}
	if native.RunSubject().MatchString("system:serviceaccount:agents:run-abcdefgh") {
		t.Fatal("another kind's subject matched")
	}
	if r, ok := native.FromUnstructured(kindClaim("maku.ogenki.io/v1alpha1", "runs", "run-abcdefgh")); !ok || r.ID != "abcdefgh" {
		t.Fatalf("native claim: ok=%v id=%q", ok, r.ID)
	}
	for _, u := range []*unstructured.Unstructured{
		kindClaim("maku.ogenki.io/v1alpha1", "runs", "task-abcdefgh"),  // another kind's prefix
		kindClaim("maku.ogenki.io/v1alpha1", "agents", "run-abcdefgh"), // another namespace
	} {
		if _, ok := native.FromUnstructured(u); ok {
			t.Errorf("%s/%s read as a native run", u.GetNamespace(), u.GetName())
		}
	}
	if _, ok := DefaultKind.FromUnstructured(kindClaim("maku.ogenki.io/v1alpha1", "runs", "run-abcdefgh")); ok {
		t.Fatal("the default kind read another namespace's run")
	}
}

// An empty field takes the default; a malformed one is refused. The config packages call both
// at load, which is where Configure's checks went.
func TestWithDefaultsAndValidate(t *testing.T) {
	if got := (Kind{Group: "maku.ogenki.io", NamePrefix: "run-"}).WithDefaults(); got != (Kind{Group: "maku.ogenki.io", Namespace: "agents", NamePrefix: "run-"}) {
		t.Fatalf("an empty field takes the default: %+v", got)
	}
	for _, k := range []Kind{{Group: "ogenki"}, {Group: "agents.example.com"}, {Namespace: "Agents"}, {NamePrefix: "run"}, {NamePrefix: "-run-"}} {
		if err := k.WithDefaults().Validate(); err == nil {
			t.Errorf("%+v: want an error", k)
		}
	}
}

// cloud-native-ref's Crossplane AgentRun is refused by name, with its replacement (v4 step 8, D5).
func TestTheXRGroupIsRefused(t *testing.T) {
	err := Kind{Group: "cloud.ogenki.io", NamePrefix: "xplane-run-"}.WithDefaults().Validate()
	if err == nil {
		t.Fatal("the XR's group was accepted")
	}
	for _, want := range []string{"cloud.ogenki.io", "removed in v4 step 8 (D5)", "maku.ogenki.io", "upgrading.md#the-xr-run-kind-and-octo-sts"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%q does not name %q", err, want)
		}
	}
}
