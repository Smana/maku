// SPDX-License-Identifier: Apache-2.0

package maku

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Smana/maku/internal/preflight"
)

// errPreflightOff is a release without the preflight's CronJob (maku.preflight.enabled false).
var errPreflightOff = errors.New("preflight.enabled is off in this release: set it, or run helm test")

// PreflightCluster is what `maku preflight` needs of the cluster: the API (CronJobs, Jobs, Pods,
// routes) and a pod's log.
type PreflightCluster struct {
	Client client.Client
	Logs   func(ctx context.Context, namespace, pod string) ([]byte, error)
}

// PreflightOptions are the command's flags.
type PreflightOptions struct {
	Namespace string
	Release   string // empty: the namespace's one preflight CronJob
	Strict    bool   // fail on a warning too
	JSON      bool   // print the JSON lines, the pod's verbatim, instead of the table
	Keep      bool   // keep the Job (and its pod) for kubectl
	NoOutside bool   // skip the outside checks (exposure from this machine)
	Timeout   time.Duration
	Poll      time.Duration
	Outside   OutsideDeps
}

// RunPreflight is `maku preflight` (v4 D24, IN5): it creates a Job from the release's preflight
// CronJob, waits for it, reads its pod's JSON lines, adds the outside checks run from this machine,
// prints the report and deletes the Job unless Keep. A fail (or, with Strict, a warn) exits 1; a
// release without the preflight, or a Job that leaves no report, exits 2.
func RunPreflight(ctx context.Context, c PreflightCluster, o PreflightOptions, out io.Writer) error {
	cj, err := preflightCronJob(ctx, c.Client, o.Namespace, o.Release)
	if err != nil {
		return &ExitError{Code: 2, Err: err}
	}
	job, err := createPreflightJob(ctx, c.Client, cj)
	if err != nil {
		return &ExitError{Code: 2, Err: err}
	}
	if !o.Keep {
		defer func() {
			// The pod goes with it; a canceled command still cleans up.
			dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			defer cancel()
			_ = c.Client.Delete(dctx, job, client.PropagationPolicy(metav1.DeletePropagationBackground))
		}()
	}
	timeout := o.Timeout
	if timeout == 0 {
		timeout = 6 * time.Minute
		if d := cj.Spec.JobTemplate.Spec.ActiveDeadlineSeconds; d != nil {
			timeout = time.Duration(*d)*time.Second + time.Minute
		}
	}
	if err := waitJob(ctx, c.Client, job, timeout, cmpPoll(o.Poll)); err != nil {
		return &ExitError{Code: 2, Err: err}
	}
	pod, err := jobPod(ctx, c.Client, job)
	if err != nil {
		return &ExitError{Code: 2, Err: err}
	}
	raw, err := c.Logs(ctx, job.Namespace, pod)
	if err != nil {
		return &ExitError{Code: 2, Err: fmt.Errorf("read %s's log: %w", pod, err)}
	}
	var rep preflight.Report
	lines := 0
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		var res preflight.Result
		if json.Unmarshal(sc.Bytes(), &res) != nil || res.Check == "" {
			continue // a line that is not a result (a Go panic, a log line) is not the report's
		}
		if rep.Add(res) == nil {
			lines++
		}
	}
	if lines == 0 {
		return &ExitError{Code: 2, Err: fmt.Errorf("the preflight Job %s left no report: kubectl -n %s logs %s", job.Name, job.Namespace, pod)}
	}
	var outside []preflight.Result
	if o.NoOutside {
		outside = []preflight.Result{{Check: "exposure", Component: string(preflight.InCLI), Outcome: preflight.Skip, Message: "--no-outside"}}
	} else {
		outside = OutsideChecks(ctx, c.Client, o.Namespace, o.Outside)
	}
	for _, r := range outside {
		_ = rep.Add(r)
	}
	if o.JSON {
		if _, err := out.Write(raw); err != nil {
			return err
		}
		enc := json.NewEncoder(out)
		for _, r := range outside {
			if err := enc.Encode(r); err != nil {
				return err
			}
		}
	} else {
		if err := rep.WriteText(out); err != nil {
			return err
		}
		if _, err := fmt.Fprintln(out, "\n"+rep.Summary()); err != nil {
			return err
		}
	}
	if rep.Exit(o.Strict) != 0 {
		return &ExitError{Code: 1, Err: errors.New(rep.Summary())}
	}
	return nil
}

func cmpPoll(p time.Duration) time.Duration {
	if p == 0 {
		return 2 * time.Second
	}
	return p
}

// preflightCronJob finds the release's CronJob, <release>-preflight, or the namespace's only one.
func preflightCronJob(ctx context.Context, c client.Client, ns, release string) (*batchv1.CronJob, error) {
	if release != "" {
		var cj batchv1.CronJob
		err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: release + "-preflight"}, &cj)
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("release %s in namespace %s: %w", release, ns, errPreflightOff)
		}
		return &cj, err
	}
	var list batchv1.CronJobList
	if err := c.List(ctx, &list, client.InNamespace(ns), client.MatchingLabels{"app.kubernetes.io/name": "preflight"}); err != nil {
		return nil, err
	}
	switch len(list.Items) {
	case 0:
		return nil, fmt.Errorf("namespace %s: %w", ns, errPreflightOff)
	case 1:
		return &list.Items[0], nil
	}
	var names []string
	for _, cj := range list.Items {
		names = append(names, strings.TrimSuffix(cj.Name, "-preflight"))
	}
	slices.Sort(names)
	return nil, fmt.Errorf("namespace %s holds the preflight of %s: name one with --release", ns, strings.Join(names, ", "))
}

// createPreflightJob creates maku-preflight-<rand> from the CronJob's template, owned by it, as
// `kubectl create job --from=cronjob/…` does.
func createPreflightJob(ctx context.Context, c client.Client, cj *batchv1.CronJob) (*batchv1.Job, error) {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	tpl := cj.Spec.JobTemplate
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name: "maku-preflight-" + hex.EncodeToString(b), Namespace: cj.Namespace,
			Labels:      tpl.Labels,
			Annotations: map[string]string{"cronjob.kubernetes.io/instantiate": "manual"},
			OwnerReferences: []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "CronJob", Name: cj.Name, UID: cj.UID,
				Controller: new(true)}},
		},
		Spec: *tpl.Spec.DeepCopy(),
	}
	if err := c.Create(ctx, job); err != nil {
		return nil, fmt.Errorf("create the preflight Job from %s: %w", cj.Name, err)
	}
	return job, nil
}

// waitJob waits until the Job has succeeded or failed (backoffLimit 0: one pod).
func waitJob(ctx context.Context, c client.Client, job *batchv1.Job, timeout, poll time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		var j batchv1.Job
		if err := c.Get(ctx, client.ObjectKeyFromObject(job), &j); err != nil {
			return fmt.Errorf("read the preflight Job %s: %w", job.Name, err)
		}
		if j.Status.Succeeded > 0 || j.Status.Failed > 0 {
			return nil
		}
		for _, cond := range j.Status.Conditions {
			if (cond.Type == batchv1.JobComplete || cond.Type == batchv1.JobFailed) && cond.Status == corev1.ConditionTrue {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("the preflight Job %s did not finish in %s: kubectl -n %s describe job %s", job.Name, timeout, job.Namespace, job.Name)
		case <-time.After(poll):
		}
	}
}

// jobPod is the Job's pod (the newest, should there be more).
func jobPod(ctx context.Context, c client.Client, job *batchv1.Job) (string, error) {
	var pods corev1.PodList
	if err := c.List(ctx, &pods, client.InNamespace(job.Namespace), client.MatchingLabels{"batch.kubernetes.io/job-name": job.Name}); err != nil {
		return "", err
	}
	if len(pods.Items) == 0 {
		return "", fmt.Errorf("the preflight Job %s has no pod: kubectl -n %s describe job %s", job.Name, job.Namespace, job.Name)
	}
	slices.SortFunc(pods.Items, func(a, b corev1.Pod) int { return b.CreationTimestamp.Compare(a.CreationTimestamp.Time) })
	return pods.Items[0].Name, nil
}

// statusOf is a response's status code, or 0 on no answer.
func statusOf(ctx context.Context, hc *http.Client, method, url string) (int, []byte) {
	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return 0, nil
	}
	resp, err := hc.Do(req)
	if err != nil {
		return 0, nil
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	return resp.StatusCode, body
}
