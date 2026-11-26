// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"

	factoryv1 "github.com/Smana/maku/api/factory/v1alpha1"
)

func TestPolicyPauseAndResumeWithoutGitOps(t *testing.T) {
	var out strings.Builder
	m := Maku{Dir: t.TempDir(), Out: &out, Err: &out}
	if err := m.run(t.Context(), []string{"policy", "pause", "--repository", "Smana/a", "--reason", "Incident freeze"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `paused: {reason: "Incident freeze"}`) {
		t.Fatalf("%s", out.String())
	}
	out.Reset()
	if err := m.run(t.Context(), []string{"policy", "resume", "--repository", "Smana/a"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "paused: null") {
		t.Fatalf("%s", out.String())
	}
}

func TestOpenPausePR(t *testing.T) {
	repository := factoryv1.Repository{Spec: factoryv1.RepositorySpec{Repository: "Smana/a"}}
	raw, _ := yaml.Marshal(repository)
	var calls [][]string
	m := Maku{
		GH: func(_ context.Context, path string) (int, []byte, error) {
			if strings.Contains(path, "/branches/") {
				return 200, []byte(`{"commit":{"sha":"base123"}}`), nil
			}
			return 200, []byte(`{"sha":"file123","content":"` + base64.StdEncoding.EncodeToString(raw) + `"}`), nil // the manifest on the gitops branch
		},
		Capture: func(_ context.Context, argv []string) (string, error) {
			calls = append(calls, argv)
			if argv[0] == "gh" && argv[1] == "pr" {
				return "https://github.com/Smana/gitops/pull/7\n", nil
			}
			return "", nil
		},
	}
	factory := factoryv1.Factory{Spec: factoryv1.FactorySpec{GitOps: &factoryv1.GitOps{Repository: "Smana/gitops", Path: "repository-a.yaml"}}}
	url, err := m.openPausePR(t.Context(), factory, "Smana/a", "pause", "Incident freeze")
	if err != nil || url != "https://github.com/Smana/gitops/pull/7" {
		t.Fatalf("%q %v", url, err)
	}
	if len(calls) != 3 {
		t.Fatalf("%d calls: %q", len(calls), calls)
	}
	var content string
	for _, arg := range calls[1] {
		if strings.HasPrefix(arg, "content=") {
			content = strings.TrimPrefix(arg, "content=")
		}
	}
	updated, err := base64.StdEncoding.DecodeString(content)
	if err != nil {
		t.Fatal(err)
	}
	var got factoryv1.Repository
	if err := yaml.Unmarshal(updated, &got); err != nil {
		t.Fatal(err)
	}
	if got.Spec.Merge.Paused == nil || got.Spec.Merge.Paused.Reason != "Incident freeze" {
		t.Fatalf("%+v", got.Spec.Merge.Paused)
	}
	if calls[2][0] != "gh" || calls[2][1] != "pr" || calls[2][2] != "create" {
		t.Fatalf("%q", calls[2])
	}
	raw = updated // the pause merged: the resume reads a paused manifest
	url, err = m.openPausePR(t.Context(), factory, "Smana/a", "resume", "")
	if err != nil || url == "" {
		t.Fatalf("%q %v", url, err)
	}
	var resumed factoryv1.Repository
	for _, arg := range calls[4] {
		if strings.HasPrefix(arg, "content=") {
			content = strings.TrimPrefix(arg, "content=")
		}
	}
	updated, _ = base64.StdEncoding.DecodeString(content)
	if err := yaml.Unmarshal(updated, &resumed); err != nil {
		t.Fatal(err)
	}
	if resumed.Spec.Merge != nil && resumed.Spec.Merge.Paused != nil {
		t.Fatalf("%+v", resumed.Spec.Merge.Paused)
	}
}

// A resume of a repository that is not paused, or a pause that changes nothing, is refused before
// anything is written: the gcp-0 drill's resume opened an empty pull request (#549).
func TestPausePRRefusesANoOpBeforeWriting(t *testing.T) {
	notPaused := "apiVersion: maku.ogenki.io/v1alpha1\nkind: Repository\nspec:\n  repository: Smana/a\n  merge:\n    maxPerDay: 2\n"
	paused := "apiVersion: maku.ogenki.io/v1alpha1\nkind: Repository\nspec:\n  repository: Smana/a\n  merge:\n    paused: {reason: \"Incident freeze\"}\n"
	for _, tc := range []struct {
		name, manifest, verb, reason, want string
	}{
		{"resume when not paused", notPaused, "resume", "", "is not paused"},
		{"resume with no merge block", "kind: Repository\nspec:\n  repository: Smana/a\n", "resume", "", "is not paused"},
		{"pause when paused for the same reason", paused, "pause", "Incident freeze", "already paused"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls [][]string
			m := Maku{
				GH: func(_ context.Context, path string) (int, []byte, error) {
					if strings.Contains(path, "/branches/") {
						return 200, []byte(`{"commit":{"sha":"base123"}}`), nil
					}
					return 200, []byte(`{"sha":"file123","content":"` + base64.StdEncoding.EncodeToString([]byte(tc.manifest)) + `"}`), nil
				},
				Capture: func(_ context.Context, argv []string) (string, error) {
					calls = append(calls, argv)
					return "https://github.com/Smana/gitops/pull/9\n", nil
				},
			}
			factory := factoryv1.Factory{Spec: factoryv1.FactorySpec{GitOps: &factoryv1.GitOps{Repository: "Smana/gitops", Path: "repository-a.yaml"}}}
			url, err := m.openPausePR(t.Context(), factory, "Smana/a", tc.verb, tc.reason)
			if err == nil || !strings.Contains(err.Error(), tc.want) || url != "" || len(calls) != 0 {
				t.Fatalf("url=%q err=%v calls=%q", url, err, calls)
			}
		})
	}
	// A resume of a paused repository still opens its pull request.
	var calls [][]string
	m := Maku{
		GH: func(_ context.Context, path string) (int, []byte, error) {
			if strings.Contains(path, "/branches/") {
				return 200, []byte(`{"commit":{"sha":"base123"}}`), nil
			}
			return 200, []byte(`{"sha":"file123","content":"` + base64.StdEncoding.EncodeToString([]byte(paused)) + `"}`), nil
		},
		Capture: func(_ context.Context, argv []string) (string, error) {
			calls = append(calls, argv)
			return "https://github.com/Smana/gitops/pull/9\n", nil
		},
	}
	factory := factoryv1.Factory{Spec: factoryv1.FactorySpec{GitOps: &factoryv1.GitOps{Repository: "Smana/gitops", Path: "repository-a.yaml"}}}
	if url, err := m.openPausePR(t.Context(), factory, "Smana/a", "resume", ""); err != nil || url == "" || len(calls) != 3 {
		t.Fatalf("resume of a paused repository: %q %v %d calls", url, err, len(calls))
	}
}
