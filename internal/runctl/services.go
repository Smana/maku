// SPDX-License-Identifier: Apache-2.0

package runctl

import (
	"slices"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	v1alpha1 "github.com/Smana/maku/api/v1alpha1"
)

// ServicesConfig is the service sidecars' resources, each sidecar's: the chart's, never a
// Repository's (as the browser's, C10).
type ServicesConfig struct {
	// Resources are one sidecar's requests and limits; each key left out takes its default
	// (requests cpu 100m, memory 256Mi; limits cpu 1, memory 1Gi).
	Resources corev1.ResourceRequirements `json:"resources,omitzero"`
}

var servicesDefaults = corev1.ResourceRequirements{
	Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m"), corev1.ResourceMemory: resource.MustParse("256Mi")},
	Limits:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1"), corev1.ResourceMemory: resource.MustParse("1Gi")},
}

func (s *ServicesConfig) applyDefaults() {
	fill := func(list *corev1.ResourceList, defaults corev1.ResourceList) {
		if *list == nil {
			*list = corev1.ResourceList{}
		}
		for k, v := range defaults {
			if _, ok := (*list)[k]; !ok {
				(*list)[k] = v.DeepCopy()
			}
		}
	}
	fill(&s.Resources.Requests, servicesDefaults.Requests)
	fill(&s.Resources.Limits, servicesDefaults.Limits)
}

// The service sidecars' loopback addresses, as the harness hands them to the checks.
const (
	postgresURL = "postgres://maku@127.0.0.1:5432/maku?sslmode=disable"
	redisURL    = "redis://127.0.0.1:6379/0"
)

// serviceSidecars are a run's opt-in loopback services (developer review gap 3, owner decision D3),
// on the browser's pattern: native sidecars (C6) with no workspace, no secret, no token and no
// proxy, listening on 127.0.0.1 only, their data in an emptyDir that ends with the pod. Each is ready
// before the harness starts. The env is the harness's: where its checks find each service.
func serviceSidecars(r *v1alpha1.AgentRun, c Config) ([]corev1.Container, []corev1.Volume, []corev1.EnvVar) {
	var ctrs []corev1.Container
	var vols []corev1.Volume
	var env []corev1.EnvVar
	always := corev1.ContainerRestartPolicyAlways
	sidecar := func(name, image string, args []string, probe []string, mounts ...corev1.VolumeMount) corev1.Container {
		health := corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: probe}}
		return corev1.Container{
			Name: name, Image: image, Args: args, RestartPolicy: &always,
			StartupProbe:    &corev1.Probe{ProbeHandler: health, PeriodSeconds: 1, FailureThreshold: 120, TimeoutSeconds: 3},
			ReadinessProbe:  &corev1.Probe{ProbeHandler: health, PeriodSeconds: 10, TimeoutSeconds: 3},
			LivenessProbe:   &corev1.Probe{ProbeHandler: health, PeriodSeconds: 20, FailureThreshold: 3, TimeoutSeconds: 3},
			Resources:       *c.Services.Resources.DeepCopy(),
			SecurityContext: containerSecurity(),
			VolumeMounts:    mounts,
		}
	}
	if slices.Contains(r.Spec.Services, "postgres") {
		pg := sidecar("postgres", c.Images.Postgres,
			[]string{"-c", "listen_addresses=127.0.0.1", "-c", "fsync=off", "-c", "full_page_writes=off"},
			[]string{"pg_isready", "-h", "127.0.0.1", "-U", "maku", "-d", "maku"},
			corev1.VolumeMount{Name: "postgres-data", MountPath: "/var/lib/postgresql/data"},
			corev1.VolumeMount{Name: "postgres-run", MountPath: "/var/run/postgresql"},
			corev1.VolumeMount{Name: "postgres-tmp", MountPath: "/tmp"})
		// trust on loopback alone: nothing off the pod reaches it, and no secret enters the run.
		pg.Env = []corev1.EnvVar{{Name: "POSTGRES_USER", Value: "maku"}, {Name: "POSTGRES_DB", Value: "maku"},
			{Name: "POSTGRES_HOST_AUTH_METHOD", Value: "trust"}, {Name: "PGDATA", Value: "/var/lib/postgresql/data/pgdata"}}
		ctrs = append(ctrs, pg)
		vols = append(vols, emptyDir("postgres-data", "2Gi", false), emptyDir("postgres-run", "16Mi", false), emptyDir("postgres-tmp", "64Mi", false))
		env = append(env, corev1.EnvVar{Name: "DATABASE_URL", Value: postgresURL})
	}
	if slices.Contains(r.Spec.Services, "redis") {
		ctrs = append(ctrs, sidecar("redis", c.Images.Redis,
			[]string{"redis-server", "--bind", "127.0.0.1", "--protected-mode", "yes", "--save", "", "--appendonly", "no", "--dir", "/data"},
			[]string{"redis-cli", "-h", "127.0.0.1", "ping"},
			corev1.VolumeMount{Name: "redis-data", MountPath: "/data"}))
		vols = append(vols, emptyDir("redis-data", "512Mi", false))
		env = append(env, corev1.EnvVar{Name: "REDIS_URL", Value: redisURL})
	}
	return ctrs, vols, env
}
