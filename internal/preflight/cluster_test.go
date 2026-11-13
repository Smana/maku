// SPDX-License-Identifier: Apache-2.0

package preflight

import (
	"context"
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	nodev1 "k8s.io/api/node/v1"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/version"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

type fixedVersion struct {
	v   version.Info
	err error
}

func (f fixedVersion) ServerVersion() (*version.Info, error) { return &f.v, f.err }

func crd(name string, served ...string) *apiextv1.CustomResourceDefinition {
	c := &apiextv1.CustomResourceDefinition{ObjectMeta: metav1.ObjectMeta{Name: name}}
	for _, v := range served {
		c.Spec.Versions = append(c.Spec.Versions, apiextv1.CustomResourceDefinitionVersion{Name: v, Served: true})
	}
	return c
}

func scheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := apiextv1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return s
}

// results runs the cluster checks and keys them check/code.
func results(t *testing.T, c client.Client, v Versioner, cfg ClusterConfig) map[string]Result {
	t.Helper()
	out := map[string]Result{}
	for _, r := range ClusterChecks(t.Context(), c, v, cfg) {
		out[r.Check+"/"+r.Code] = r
	}
	return out
}

func healthy() ([]client.Object, ClusterConfig) {
	objs := []client.Object{
		crd("rooms.maku.ogenki.io", "v1alpha1"),
		&nodev1.RuntimeClass{ObjectMeta: metav1.ObjectMeta{Name: "gvisor"}, Handler: "runsc"},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "rooms-broker-dsn", Namespace: "maku"}, Data: map[string][]byte{"uri": []byte("postgresql://x")}},
	}
	return objs, ClusterConfig{MinKubernetes: "1.30", CRDs: []CRD{{Name: "rooms.maku.ogenki.io", Version: "v1alpha1"}},
		RuntimeClass: "gvisor", Secrets: []SecretRef{{Seam: "databaseBroker", Namespace: "maku", Name: "rooms-broker-dsn", Keys: []string{"uri"}}}}
}

func TestClusterChecksPass(t *testing.T) {
	objs, cfg := healthy()
	c := fake.NewClientBuilder().WithScheme(scheme(t)).WithObjects(objs...).Build()
	got := results(t, c, fixedVersion{v: version.Info{Major: "1", Minor: "32"}}, cfg)
	for _, id := range []string{"kubernetes/", "crds/", "runtimeclass/", "secrets/"} {
		if r, ok := got[id]; !ok || r.Outcome != Pass {
			t.Errorf("%s: %+v (all %v)", id, r, got)
		}
	}
}

// One test per code of IN2's four cluster rows.
func TestClusterChecksRefuse(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(objs []client.Object, cfg *ClusterConfig, v *fixedVersion, f *interceptor.Funcs) []client.Object
		want   string // check/code
		msg    string // a substring of the message
		notMsg string // never in it
	}{
		{"1.29 is refused", func(o []client.Object, _ *ClusterConfig, v *fixedVersion, _ *interceptor.Funcs) []client.Object {
			v.v = version.Info{Major: "1", Minor: "29"}
			return o
		}, "kubernetes/kubernetes_too_old", "1.29", ""},
		{"a GKE minor with a plus", func(o []client.Object, _ *ClusterConfig, v *fixedVersion, _ *interceptor.Funcs) []client.Object {
			v.v = version.Info{Major: "1", Minor: "29+"}
			return o
		}, "kubernetes/kubernetes_too_old", "1.29", ""},
		{"no version is undecided", func(o []client.Object, _ *ClusterConfig, v *fixedVersion, _ *interceptor.Funcs) []client.Object {
			v.err = errors.New("connection refused")
			return o
		}, "kubernetes/undecided", "", ""},
		{"a missing CRD", func(o []client.Object, cfg *ClusterConfig, _ *fixedVersion, _ *interceptor.Funcs) []client.Object {
			cfg.CRDs = append(cfg.CRDs, CRD{Name: "tasks.maku.ogenki.io", Version: "v1alpha1"})
			return o
		}, "crds/crd_missing", "tasks.maku.ogenki.io", ""},
		{"a CRD at an old served version", func(o []client.Object, _ *ClusterConfig, _ *fixedVersion, _ *interceptor.Funcs) []client.Object {
			o[0] = crd("rooms.maku.ogenki.io", "v1alpha0")
			return o
		}, "crds/crd_version", "v1alpha1", ""},
		{"a missing RuntimeClass", func(o []client.Object, cfg *ClusterConfig, _ *fixedVersion, _ *interceptor.Funcs) []client.Object {
			cfg.RuntimeClass = "kata"
			return o
		}, "runtimeclass/runtimeclass_missing", "kata", ""},
		{"unsafeAllowRunc warns", func(o []client.Object, cfg *ClusterConfig, _ *fixedVersion, _ *interceptor.Funcs) []client.Object {
			cfg.RuntimeClass, cfg.UnsafeAllowRunc = "", true
			return o
		}, "runtimeclass/unsafe_allow_runc", "", ""},
		{"a missing Secret", func(o []client.Object, _ *ClusterConfig, _ *fixedVersion, _ *interceptor.Funcs) []client.Object {
			return o[:2]
		}, "secrets/secret_missing", "rooms-broker-dsn", ""},
		{"a Secret without a documented key names the key, never a value", func(o []client.Object, _ *ClusterConfig, _ *fixedVersion, _ *interceptor.Funcs) []client.Object {
			o[2] = &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "rooms-broker-dsn", Namespace: "maku"},
				Data: map[string][]byte{"url": []byte("sekrit-value")}}
			return o
		}, "secrets/secret_key_missing", "uri", "sekrit-value"},
		{"an empty key is missing", func(o []client.Object, _ *ClusterConfig, _ *fixedVersion, _ *interceptor.Funcs) []client.Object {
			o[2] = &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "rooms-broker-dsn", Namespace: "maku"}, Data: map[string][]byte{"uri": nil}}
			return o
		}, "secrets/secret_key_missing", "uri", ""},
		{"an RBAC denial is undecided, not missing", func(o []client.Object, _ *ClusterConfig, _ *fixedVersion, f *interceptor.Funcs) []client.Object {
			f.Get = func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if _, ok := obj.(*corev1.Secret); ok {
					return apierrors.NewForbidden(schema.GroupResource{Resource: "secrets"}, key.Name, errors.New("denied"))
				}
				return c.Get(ctx, key, obj, opts...)
			}
			return o
		}, "secrets/undecided", "rooms-broker-dsn", ""},
		{"an RBAC denial on the RuntimeClass is undecided", func(o []client.Object, _ *ClusterConfig, _ *fixedVersion, f *interceptor.Funcs) []client.Object {
			f.Get = func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if _, ok := obj.(*nodev1.RuntimeClass); ok {
					return apierrors.NewForbidden(schema.GroupResource{Resource: "runtimeclasses"}, key.Name, errors.New("denied"))
				}
				return c.Get(ctx, key, obj, opts...)
			}
			return o
		}, "runtimeclass/undecided", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			objs, cfg := healthy()
			v := fixedVersion{v: version.Info{Major: "1", Minor: "32"}}
			var f interceptor.Funcs
			objs = c.mutate(objs, &cfg, &v, &f)
			cl := fake.NewClientBuilder().WithScheme(scheme(t)).WithObjects(objs...).WithInterceptorFuncs(f).Build()
			got := results(t, cl, v, cfg)
			r, ok := got[c.want]
			if !ok {
				t.Fatalf("no %s in %v", c.want, got)
			}
			if !strings.Contains(r.Message, c.msg) || (c.notMsg != "" && strings.Contains(r.Message, c.notMsg)) {
				t.Fatalf("message %q", r.Message)
			}
		})
	}
}

// Every cluster result holds to the catalog.
func TestClusterResultsAreCatalogued(t *testing.T) {
	objs, cfg := healthy()
	cl := fake.NewClientBuilder().WithScheme(scheme(t)).WithObjects(objs...).Build()
	var r Report
	for _, res := range ClusterChecks(t.Context(), cl, fixedVersion{v: version.Info{Major: "1", Minor: "30"}}, cfg) {
		if err := r.Add(res); err != nil {
			t.Fatal(err)
		}
	}
}
