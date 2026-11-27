// SPDX-License-Identifier: Apache-2.0

package runctl

import (
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"

	v1alpha1 "github.com/Smana/maku/api/v1alpha1"
)

const (
	postgresImage = "docker.io/library/postgres:17@sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	redisImage    = "docker.io/library/redis:8@sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
)

func withServices(svcs ...v1alpha1.AgentRunService) func(*v1alpha1.AgentRun) {
	return func(r *v1alpha1.AgentRun) { r.Spec.Services = svcs }
}

// Developer review gap 3, owner decision D3: a run that asks for postgres and redis gets each as a
// native sidecar on loopback, ready before the harness, and the harness's checks find them.
func TestServiceSidecarsAreLoopbackNativeSidecars(t *testing.T) {
	c := testConfig(t)
	c.Images.Postgres, c.Images.Redis = postgresImage, redisImage
	r := run("run-aaaaaaaa", withServices("postgres", "redis"))
	if why := c.Invalid(r); why != "" {
		t.Fatal(why)
	}
	p := Pod(r, c, testModel, "aaaaaaaa")
	byName := map[string]corev1.Container{}
	for _, ctr := range p.Spec.InitContainers {
		byName[ctr.Name] = ctr
	}
	for name, bind := range map[string]string{"postgres": "listen_addresses=127.0.0.1", "redis": "127.0.0.1"} {
		ctr, ok := byName[name]
		switch {
		case !ok:
			t.Fatalf("no %s sidecar", name)
		case ctr.RestartPolicy == nil || *ctr.RestartPolicy != corev1.ContainerRestartPolicyAlways:
			t.Errorf("%s is not a native sidecar", name)
		case !slices.Contains(ctr.Args, bind):
			t.Errorf("%s does not bind loopback: %v", name, ctr.Args)
		case len(ctr.Ports) != 0 || len(ctr.EnvFrom) != 0:
			t.Errorf("%s exposes a port or reads a secret", name)
		case ctr.StartupProbe == nil || ctr.SecurityContext == nil || *ctr.SecurityContext.ReadOnlyRootFilesystem != true:
			t.Errorf("%s: probe or security context", name)
		}
		for _, e := range ctr.Env {
			if e.ValueFrom != nil {
				t.Errorf("%s reads %s from a secret", name, e.Name)
			}
		}
	}
	env := harnessEnv(p)
	if !strings.HasPrefix(env["DATABASE_URL"], "postgres://maku@127.0.0.1:5432/") || env["REDIS_URL"] != "redis://127.0.0.1:6379/0" {
		t.Errorf("harness env %q %q", env["DATABASE_URL"], env["REDIS_URL"])
	}
}

func TestAServiceWithoutItsImageIsRefused(t *testing.T) {
	c := testConfig(t)
	if why := c.Invalid(run("run-aaaaaaaa", withServices("postgres"))); !strings.Contains(why, "images.postgres") {
		t.Errorf("%q", why)
	}
	if why := c.Invalid(run("run-aaaaaaaa", withServices("redis"))); !strings.Contains(why, "images.redis") {
		t.Errorf("%q", why)
	}
	if p := Pod(run("run-aaaaaaaa"), c, testModel, "aaaaaaaa"); slices.ContainsFunc(p.Spec.InitContainers, func(c corev1.Container) bool { return c.Name == "postgres" }) {
		t.Error("a run without services gets none")
	}
}
