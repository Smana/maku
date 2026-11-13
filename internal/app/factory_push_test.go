// SPDX-License-Identifier: Apache-2.0

package app

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/applier"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/minter"
	"github.com/Smana/maku/internal/factory/rooms"
	"github.com/Smana/maku/internal/hardening"
	"github.com/Smana/maku/internal/runwatch"
)

// Task 39: a repository that enforces push gets an applier with a pusher under MAKU_GIT_CACHE,
// the broker's push routes, the task writer for push intents, the class caps and the approval TTL;
// without MAKU_GIT_CACHE the factory refuses to start, and without push none of it is needed.
// Coordination's prerequisites stay nil.
func TestFactoryPushWiring(t *testing.T) {
	log := slog.New(slog.DiscardHandler)
	token := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(token, []byte("jwt"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := enforcingConfig(map[string]config.HardeningRepository{
		"Smana/ai-native-ref": {Mode: hardening.ModeAudit, EnforceOutputs: []hardening.OutputType{hardening.PR, hardening.Push}}})
	cfg.Hardening.Publisher.OctoSTS.TokenFile = token
	cfg.Hardening.Applier.ApprovalTTL.Duration = 48 * time.Hour
	cfg.Repositories = []config.Repository{{Name: "Smana/ai-native-ref",
		Classes: map[string]config.Class{"docs": {MaxPatchFiles: 7, MaxModifiedLines: 70}}}}
	cache := t.TempDir()
	broker := &rooms.Client{}
	writer := fake.NewClientBuilder().Build()
	deps := func(env map[string]string) applierDeps {
		return applierDeps{Hardened: hardeningPorts(cfg, nil), Live: func() *config.Config { return cfg }, Log: log,
			PushRooms: broker, Tasks: writer, Getenv: func(k string) string { return env[k] }}
	}

	a, err := safeOutputApplier(cfg, deps(map[string]string{"MAKU_GIT_CACHE": cache}))
	if err != nil || a == nil {
		t.Fatalf("applier: %v", err)
	}
	if a.Pushers == nil || a.PushRooms != broker || a.Tasks != writer || a.ApprovalTTL != 48*time.Hour || a.Caps == nil {
		t.Fatalf("applier %+v", a)
	}
	p, err := a.Pushers(t.Context(), "Smana/ai-native-ref")
	if err != nil || p == nil {
		t.Fatalf("pusher: %v", err)
	}
	task := &v1alpha1.Task{ObjectMeta: metav1.ObjectMeta{Name: "t"},
		Spec: v1alpha1.TaskSpec{Repository: "Smana/ai-native-ref", PredictedClass: "docs"}}
	if f, l := a.Caps(task); f != 7 || l != 70 {
		t.Fatalf("caps %d %d", f, l)
	}
	// #513's review: an undeclared class takes the strictest declared caps, never none.
	cfg.Repositories[0].Classes["config"] = config.Class{MaxPatchFiles: 3}
	task.Spec.PredictedClass = "absent"
	if f, l := a.Caps(task); f != 3 || l != 70 {
		t.Fatalf("an undeclared class's caps %d %d", f, l)
	}
	task.Spec.PredictedClass = ""
	if f, l := a.Caps(task); f != 3 || l != 70 {
		t.Fatalf("no class's caps %d %d", f, l)
	}
	cfg.Repositories[0].Classes = map[string]config.Class{"docs": {}}
	task.Spec.PredictedClass = "absent"
	if f, l := a.Caps(task); f != -1 || l != -1 {
		t.Fatalf("no declared cap: %d %d, want -1 -1 (push_size)", f, l)
	}
	cfg.Repositories[0].Classes = map[string]config.Class{"docs": {MaxPatchFiles: 7, MaxModifiedLines: 70}}

	if _, err := safeOutputApplier(cfg, deps(nil)); err == nil || !strings.Contains(err.Error(), "MAKU_GIT_CACHE") {
		t.Fatalf("no git cache started: %v", err)
	}

	cfg.Hardening.Repositories["Smana/ai-native-ref"] = config.HardeningRepository{Mode: hardening.ModeAudit,
		EnforceOutputs: []hardening.OutputType{hardening.PR}}
	a, err = safeOutputApplier(cfg, deps(nil))
	if err != nil || a == nil || a.Pushers != nil {
		t.Fatalf("without push: %+v, %v", a, err)
	}
	if _, err := os.Stat(filepath.Join(cache, "smana")); !os.IsNotExist(err) {
		t.Fatalf("a mirror was created before any push: %v", err)
	}
	var _ applier.PushRooms = broker
}

// #509's note for H3: under runTokens.perRun, the D10 warning also names a repository that enforces
// push while the role table leaves the implementer or the tester contents: write.
func TestRoleTableWarningPush(t *testing.T) {
	const repo = "Smana/ai-native-ref"
	noPushWrite := minter.Table{}
	for k, v := range minter.TableHR12 {
		noPushWrite[k] = v
	}
	testerWrites := minter.Table{}
	for k, v := range minter.TableHR12 {
		testerWrites[k] = v
	}
	testerWrites[minter.Tester] = minter.Rule{Base: minter.Permissions{"contents": "write", "pull_requests": "read"}}
	for name, tc := range map[string]struct {
		enforced []hardening.OutputType
		table    minter.Table
		want     []string
	}{
		"HR12, push":                 {[]hardening.OutputType{hardening.Push}, noPushWrite, nil},
		"D10, push alone":            {[]hardening.OutputType{hardening.Push}, minter.TableD10, []string{"contents: write", "implementer", "tester"}},
		"D10, all five":              {hardening.Types(), minter.TableD10, []string{"pull_requests: write", "contents: write"}},
		"a tester keeping its write": {[]hardening.OutputType{hardening.Push}, testerWrites, []string{"contents: write", "tester"}},
		"D10, no push, no pr":        {[]hardening.OutputType{hardening.Issue}, minter.TableD10, nil},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := enforcingConfig(map[string]config.HardeningRepository{repo: {Mode: hardening.ModeAudit, EnforceOutputs: tc.enforced}})
			cfg.Repositories = []config.Repository{{Name: repo}, {Name: "Smana/cloud-native-ref"}}
			cfg.Runs = runwatch.Kind{Group: "maku.ogenki.io", Namespace: "agents"}
			cfg.RunTokens = config.RunTokens{Enabled: true, PerRun: true}
			w := roleTableWarning(cfg, tc.table)
			if (w != "") != (tc.want != nil) {
				t.Fatalf("warning %q, want %v", w, tc.want)
			}
			for _, s := range tc.want {
				if !strings.Contains(w, s) || !strings.Contains(w, repo) || strings.Contains(w, "cloud-native-ref") {
					t.Fatalf("warning %q lacks %q", w, s)
				}
			}
		})
	}
}
