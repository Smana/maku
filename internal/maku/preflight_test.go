// SPDX-License-Identifier: Apache-2.0

package maku

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/Smana/maku/internal/preflight"
)

const (
	passLines = `{"check":"kubernetes","component":"","outcome":"pass","code":"","message":"Kubernetes 1.35","seconds":0.01}
{"check":"database","component":"broker","outcome":"pass","code":"","message":"rooms_broker: Postgres 16","seconds":0.2}
`
	failLines = `{"check":"kubernetes","component":"","outcome":"pass","code":"","message":"Kubernetes 1.35","seconds":0.01}
{"check":"secrets","component":"","outcome":"fail","code":"secret_missing","message":"dsn is missing","seconds":0.01}
`
)

func testCronJob(release, ns string) *batchv1.CronJob {
	return &batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{Name: release + "-preflight", Namespace: ns, UID: "cj-uid",
			Labels: map[string]string{"app.kubernetes.io/name": "preflight", "app.kubernetes.io/part-of": "maku"}},
		Spec: batchv1.CronJobSpec{Schedule: "@yearly", JobTemplate: batchv1.JobTemplateSpec{Spec: batchv1.JobSpec{
			ActiveDeadlineSeconds: new(int64(330)),
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{RestartPolicy: corev1.RestartPolicyNever,
				Containers: []corev1.Container{{Name: "preflight", Image: "maku-run-controller:v1"}}}},
		}}},
	}
}

// fakeCluster runs a created Job at once: its pod, with log lines, and its status, succeeded or failed.
type fakeCluster struct {
	c       client.WithWatch
	created *batchv1.Job
	deleted []string
}

// summaryOf is the pod's own termination summary of lines, as the runner writes it.
func summaryOf(lines string) string {
	var rep preflight.Report
	for _, l := range strings.Split(strings.TrimSpace(lines), "\n") {
		var r preflight.Result
		if json.Unmarshal([]byte(l), &r) == nil && r.Check != "" {
			_ = rep.Add(r)
		}
	}
	return rep.Summary()
}

// newFakeCluster's pod terminates with term as its termination message (empty: a crash, none).
func newFakeCluster(t *testing.T, succeed bool, term string, objs ...client.Object) *fakeCluster {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	f := &fakeCluster{}
	f.c = fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).WithStatusSubresource(&batchv1.Job{}).
		WithInterceptorFuncs(interceptor.Funcs{
			Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
				if err := c.Create(ctx, obj, opts...); err != nil {
					return err
				}
				job, ok := obj.(*batchv1.Job)
				if !ok {
					return nil
				}
				f.created = job.DeepCopy()
				pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: job.Name + "-x", Namespace: job.Namespace,
					Labels: map[string]string{"batch.kubernetes.io/job-name": job.Name}},
					Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{Name: "preflight",
						State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Message: term}}}}}}
				if err := c.Create(ctx, pod); err != nil {
					return err
				}
				if succeed {
					job.Status.Succeeded = 1
				} else {
					job.Status.Failed = 1
				}
				return c.Status().Update(ctx, job)
			},
			Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
				f.deleted = append(f.deleted, obj.GetName())
				return c.Delete(ctx, obj, opts...)
			},
		}).Build()
	return f
}

func (f *fakeCluster) cluster(lines string) PreflightCluster {
	return PreflightCluster{Client: f.c, Logs: func(_ context.Context, ns, pod string) ([]byte, error) {
		if !strings.HasPrefix(pod, f.created.Name) || ns != f.created.Namespace {
			return nil, errors.New("no such pod")
		}
		return []byte(lines), nil
	}}
}

func runPreflight(t *testing.T, lines string, succeed bool, o PreflightOptions, objs ...client.Object) (*fakeCluster, string, error) {
	t.Helper()
	return runPreflightTerm(t, lines, summaryOf(lines), succeed, o, objs...)
}

func runPreflightTerm(t *testing.T, lines, term string, succeed bool, o PreflightOptions, objs ...client.Object) (*fakeCluster, string, error) {
	t.Helper()
	f := newFakeCluster(t, succeed, term, objs...)
	o.Poll = time.Millisecond
	o.NoOutside = true
	var out bytes.Buffer
	err := RunPreflight(t.Context(), f.cluster(lines), o, &out)
	return f, out.String(), err
}

func exitCode(err error) int {
	var e *ExitError
	if errors.As(err, &e) {
		return e.Code
	}
	if err != nil {
		return -1
	}
	return 0
}

// No CronJob: the release has no preflight, and the command says how to get one.
func TestPreflightNoCronJob(t *testing.T) {
	_, _, err := runPreflight(t, passLines, true, PreflightOptions{Namespace: "maku"})
	if err == nil || !strings.Contains(err.Error(), "preflight.enabled is off in this release: set it, or run helm test") {
		t.Fatalf("%v", err)
	}
	if exitCode(err) != 2 {
		t.Fatalf("exit %d", exitCode(err))
	}
}

// A passing run: a Job from the CronJob, owned by it, named maku-preflight-<rand>; the report is
// printed grouped by seam with its summary; the Job is deleted; exit 0.
func TestPreflightPass(t *testing.T) {
	f, out, err := runPreflight(t, passLines, true, PreflightOptions{Namespace: "maku"}, testCronJob("af", "maku"))
	if err != nil {
		t.Fatal(err)
	}
	j := f.created
	if j == nil || !strings.HasPrefix(j.Name, "maku-preflight-") || len(j.Name) <= len("maku-preflight-") {
		t.Fatalf("the Job: %+v", j)
	}
	if len(j.OwnerReferences) != 1 || j.OwnerReferences[0].Kind != "CronJob" || j.OwnerReferences[0].Name != "af-preflight" || j.OwnerReferences[0].UID != "cj-uid" {
		t.Fatalf("owner: %+v", j.OwnerReferences)
	}
	if j.Spec.Template.Spec.Containers[0].Image != "maku-run-controller:v1" {
		t.Fatalf("the Job's pod is not the CronJob's: %+v", j.Spec.Template.Spec)
	}
	for _, want := range []string{"SEAM", "database", "preflight: 0 fail, 0 warn, 2 pass, 1 skip"} {
		if !strings.Contains(out, want) {
			t.Fatalf("no %q in:\n%s", want, out)
		}
	}
	if len(f.deleted) != 1 || f.deleted[0] != j.Name {
		t.Fatalf("deleted %v", f.deleted)
	}
}

// A failed pod: its report is printed and the exit is 1; the Job is deleted all the same, unless --keep.
func TestPreflightFail(t *testing.T) {
	f, out, err := runPreflight(t, failLines, false, PreflightOptions{Namespace: "maku"}, testCronJob("af", "maku"))
	if exitCode(err) != 1 {
		t.Fatalf("exit %d: %v", exitCode(err), err)
	}
	if !strings.Contains(out, "secret_missing") || !strings.Contains(out, "1 fail") {
		t.Fatalf("the report:\n%s", out)
	}
	if len(f.deleted) != 1 {
		t.Fatalf("deleted %v", f.deleted)
	}
	f, _, _ = runPreflight(t, failLines, false, PreflightOptions{Namespace: "maku", Keep: true}, testCronJob("af", "maku"))
	if len(f.deleted) != 0 {
		t.Fatalf("--keep deleted %v", f.deleted)
	}
}

// --json prints the pod's lines verbatim; --strict fails on a warning.
func TestPreflightJSONAndStrict(t *testing.T) {
	_, out, err := runPreflight(t, passLines, true, PreflightOptions{Namespace: "maku", JSON: true}, testCronJob("af", "maku"))
	skip := `{"check":"exposure","component":"cli","outcome":"skip","code":"","message":"--no-outside","seconds":0}` + "\n"
	if err != nil || out != passLines+skip {
		t.Fatalf("%v\n%q", err, out)
	}
	warn := passLines + `{"check":"runtimeclass","component":"","outcome":"warn","code":"unsafe_allow_runc","message":"no sandbox","seconds":0}` + "\n"
	if _, _, err := runPreflight(t, warn, true, PreflightOptions{Namespace: "maku"}, testCronJob("af", "maku")); err != nil {
		t.Fatalf("a warning without --strict: %v", err)
	}
	if _, _, err := runPreflight(t, warn, true, PreflightOptions{Namespace: "maku", Strict: true}, testCronJob("af", "maku")); exitCode(err) != 1 {
		t.Fatalf("a warning with --strict: %v", err)
	}
}

// Two releases in one namespace: --release picks one, and without it the command names both.
func TestPreflightRelease(t *testing.T) {
	two := []client.Object{testCronJob("af", "maku"), testCronJob("other", "maku")}
	if _, _, err := runPreflight(t, passLines, true, PreflightOptions{Namespace: "maku"}, two...); err == nil || !strings.Contains(err.Error(), "--release") {
		t.Fatalf("%v", err)
	}
	f, _, err := runPreflight(t, passLines, true, PreflightOptions{Namespace: "maku", Release: "other"}, two...)
	if err != nil || f.created.OwnerReferences[0].Name != "other-preflight" {
		t.Fatalf("%v", err)
	}
	if _, _, err := runPreflight(t, passLines, true, PreflightOptions{Namespace: "maku", Release: "nope"}, two...); exitCode(err) != 2 {
		t.Fatalf("%v", err)
	}
	_ = apierrors.IsNotFound
}

// A pod that crashes (OOM-killed, past its deadline) after printing some passes left a partial
// report: no termination summary, or one that is not its lines'. It never passes: exit 2.
func TestPreflightPartialReport(t *testing.T) {
	for name, term := range map[string]string{
		"no summary":            "",
		"a Go panic's log tail": "panic: runtime error: invalid memory address",
		"another run's summary": "preflight: 0 fail, 0 warn, 9 pass, 0 skip",
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := runPreflightTerm(t, passLines, term, false, PreflightOptions{Namespace: "maku"}, testCronJob("af", "maku"))
			if exitCode(err) != 2 || !strings.Contains(err.Error(), "the preflight did not finish") {
				t.Fatalf("exit %d: %v", exitCode(err), err)
			}
		})
	}
}

// --json writes the results the report accepted, never the log's other lines: a line holding a
// credential, or one that is not a result, does not reach stdout.
func TestPreflightJSONOnlyAccepted(t *testing.T) {
	lines := passLines + "panic: something\n" + `{"check":"secrets","component":"","outcome":"fail","code":"secret_missing","message":"postgres://u:p@h/db","seconds":0}` + "\n"
	_, out, err := runPreflightTerm(t, lines, "preflight: 1 fail, 0 warn, 2 pass, 0 skip", false, PreflightOptions{Namespace: "maku", JSON: true}, testCronJob("af", "maku"))
	if exitCode(err) != 1 || !strings.Contains(out, `"check":"secrets","component":"","outcome":"fail","code":"undecided"`) {
		t.Fatalf("a withheld result is a fail undecided: exit %d\n%s", exitCode(err), out)
	}
	if strings.Contains(out, "panic") || strings.Contains(out, "postgres://") {
		t.Fatalf("--json wrote a line the report did not accept:\n%s", out)
	}
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		var r preflight.Result
		if err := json.Unmarshal([]byte(l), &r); err != nil || r.Check == "" {
			t.Fatalf("not a result: %q", l)
		}
	}
}
