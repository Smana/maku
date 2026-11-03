// SPDX-License-Identifier: Apache-2.0

package runwatch

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func restore(t *testing.T) { t.Cleanup(func() { _ = Configure(DefaultKind) }) }

func kindClaim(apiVersion, ns, name string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion(apiVersion)
	u.SetKind("AgentRun")
	u.SetNamespace(ns)
	u.SetName(name)
	return u
}

// cloud-native-ref configures nothing: every reader keeps today's kind.
func TestDefaultKindIsCloudNativeRefs(t *testing.T) {
	if g := GVK(); g.Group != "cloud.ogenki.io" || g.Version != "v1alpha1" || g.Kind != "AgentRun" {
		t.Fatalf("GVK %v", g)
	}
	if Namespace() != "agents" || ClaimPrefix() != "xplane-run-" {
		t.Fatalf("namespace %q prefix %q", Namespace(), ClaimPrefix())
	}
	if m := RunSubject().FindStringSubmatch("system:serviceaccount:agents:xplane-run-abcdefgh"); len(m) != 2 || m[1] != "abcdefgh" {
		t.Fatalf("subject match %v", m)
	}
}

func TestConfigureNativeKind(t *testing.T) {
	restore(t)
	if err := Configure(Kind{Group: "maku.ogenki.io", NamePrefix: "run-"}); err != nil {
		t.Fatal(err)
	}
	if Current() != (Kind{Group: "maku.ogenki.io", Namespace: "agents", NamePrefix: "run-"}) {
		t.Fatalf("an empty field takes the default: %+v", Current())
	}
	if r, ok := FromUnstructured(kindClaim("maku.ogenki.io/v1alpha1", "agents", "run-abcdefgh")); !ok || r.ID != "abcdefgh" {
		t.Fatalf("native claim: ok=%v id=%q", ok, r.ID)
	}
	if _, ok := FromUnstructured(kindClaim("maku.ogenki.io/v1alpha1", "agents", "xplane-run-abcdefgh")); ok {
		t.Fatal("the old prefix must not parse once configured")
	}
	if RunSubject().MatchString("system:serviceaccount:agents:xplane-run-abcdefgh") {
		t.Fatal("subject pattern follows the prefix")
	}
}

func TestConfigureRefusesAndKeepsTheKind(t *testing.T) {
	restore(t)
	for _, k := range []Kind{{Group: "ogenki"}, {Namespace: "Agents"}, {NamePrefix: "run"}, {NamePrefix: "-run-"}} {
		if err := Configure(k); err == nil {
			t.Errorf("%+v: want an error", k)
		}
	}
	if Current() != DefaultKind {
		t.Fatalf("a refused Configure changed the kind: %+v", Current())
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
	if native.RunSubject().MatchString("system:serviceaccount:agents:xplane-run-abcdefgh") {
		t.Fatal("another kind's subject matched")
	}
	if r, ok := native.FromUnstructured(kindClaim("maku.ogenki.io/v1alpha1", "runs", "run-abcdefgh")); !ok || r.ID != "abcdefgh" {
		t.Fatalf("native claim: ok=%v id=%q", ok, r.ID)
	}
	for _, u := range []*unstructured.Unstructured{
		kindClaim("maku.ogenki.io/v1alpha1", "runs", "xplane-run-abcdefgh"), // another kind's prefix
		kindClaim("maku.ogenki.io/v1alpha1", "agents", "run-abcdefgh"),      // another namespace
	} {
		if _, ok := native.FromUnstructured(u); ok {
			t.Errorf("%s/%s read as a native run", u.GetNamespace(), u.GetName())
		}
	}
	if _, ok := DefaultKind.FromUnstructured(kindClaim("maku.ogenki.io/v1alpha1", "runs", "run-abcdefgh")); ok {
		t.Fatal("the default kind read a native run")
	}
}
