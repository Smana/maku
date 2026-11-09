// SPDX-License-Identifier: Apache-2.0

package resources

import (
	"strings"
	"testing"
)

func TestDecodeRefusesUnknownFields(t *testing.T) {
	_, err := Decode([]byte(`apiVersion: maku.ogenki.io/v1alpha1
kind: MergePolicy
metadata: {name: x, namespace: agent-system}
spec:
  repositoryRef: x
  classes: {docs: {live: true, typo: 1}}
`))
	if err == nil || !strings.Contains(err.Error(), "typo") {
		t.Fatalf("an unknown field must be refused, naming it: %v", err)
	}
}

func TestDecodeRefusesOtherKinds(t *testing.T) {
	if _, err := Decode([]byte("apiVersion: v1\nkind: ConfigMap\nmetadata: {name: x}\n")); err == nil {
		t.Fatal("a ConfigMap is not a factory resource")
	}
}

func TestDecodeKeepsTheRawSpec(t *testing.T) {
	objs, err := Decode([]byte(`apiVersion: maku.ogenki.io/v1alpha1
kind: Factory
metadata: {name: aws-0, namespace: agent-system, generation: 4}
spec: {caps: {activeTasks: 3}}
`))
	if err != nil || len(objs) != 1 {
		t.Fatal(err)
	}
	caps := objs[0].Spec["caps"].(map[string]any)
	if _, set := caps["concurrentRuns"]; set || objs[0].Generation != 4 || objs[0].Kind != KindFactory {
		t.Fatalf("Decode must keep an omitted field omitted: %+v", objs[0])
	}
}

func TestDecodeSkipsEmptyDocuments(t *testing.T) {
	objs, err := Decode([]byte("# only a comment\n---  \n\n---\napiVersion: maku.ogenki.io/v1alpha1\nkind: Factory\nmetadata: {name: a}\nspec: {}\n---\n# end\n"))
	if err != nil || len(objs) != 1 {
		t.Fatalf("%v %+v", err, objs)
	}
}

func TestDecodeFillsTheNamespace(t *testing.T) {
	objs, err := Decode([]byte("apiVersion: maku.ogenki.io/v1alpha1\nkind: Factory\nmetadata: {name: a, namespace: agent-system}\nspec: {}\n"))
	if err != nil || len(objs) != 1 || objs[0].Namespace != "agent-system" {
		t.Fatalf("%v %+v", err, objs)
	}
}

func TestDecodeRefusesAnotherAPIVersion(t *testing.T) {
	_, err := Decode([]byte("apiVersion: maku.ogenki.io/v1beta1\nkind: Factory\nmetadata: {name: aws-0}\nspec: {}\n"))
	if err == nil || !strings.Contains(err.Error(), `Factory "aws-0"`) || !strings.Contains(err.Error(), "maku.ogenki.io/v1alpha1") {
		t.Fatalf("err = %v", err)
	}
}
