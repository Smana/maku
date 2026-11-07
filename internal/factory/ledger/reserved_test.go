// SPDX-License-Identifier: Apache-2.0

package ledger

import (
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestReservedByRunReadsAllOutstandingDays(t *testing.T) {
	cm := func(name string, data map[string]string) *corev1.ConfigMap {
		return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "f"}, Data: data}
	}
	c := fake.NewClientBuilder().WithScheme(scheme()).WithObjects(
		cm(Prefix+"20260926", map[string]string{Reserved + "runaaaaa": `{"principal":"p","maxMicroUSD":300000}`, Settled + "runbbbbb": "5"}),
		cm(Prefix+"20260927", map[string]string{Reserved + "runaaaaa": `{"principal":"p","maxMicroUSD":200000}`, Reserved + "runccccc": `{"principal":"p","maxMicroUSD":100}`}),
		cm("unrelated", map[string]string{Reserved + "runzzzzz": `{"maxMicroUSD":999}`}),
	).Build()
	got, err := ReservedByRun(t.Context(), c, "f")
	if err != nil {
		t.Fatal(err)
	}
	// runbbbbb's reservation was released: only its settled entry remains, so it holds nothing.
	if want := map[string]int64{"runaaaaa": 500000, "runccccc": 100}; !reflect.DeepEqual(got, want) {
		t.Fatalf("%v, want %v", got, want)
	}

	bad := fake.NewClientBuilder().WithScheme(scheme()).WithObjects(
		cm(Prefix+"20260927", map[string]string{Reserved + "runaaaaa": `{"maxMicroUSD":"lots"}`})).Build()
	if _, err := ReservedByRun(t.Context(), bad, "f"); err == nil {
		t.Fatal("a malformed amount was summed")
	}
}
