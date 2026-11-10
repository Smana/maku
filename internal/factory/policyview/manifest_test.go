// SPDX-License-Identifier: Apache-2.0

package policyview

import (
	"context"
	"encoding/json"
	"maps"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"sigs.k8s.io/yaml"

	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/evidence"
	"github.com/Smana/maku/internal/factory/mergepolicy"
	"github.com/Smana/maku/internal/factory/resources"
)

// served is the resources package's factory file split in two: the file without its repository,
// and the config as served when that repository comes from a Repository and a MergePolicy object.
func served(t *testing.T) (base []byte, cfg *config.Config, rc *config.Repository, rep evidence.Report) {
	t.Helper()
	raw, err := os.ReadFile("../resources/testdata/file.yaml")
	if err != nil {
		t.Fatal(err)
	}
	base = regexp.MustCompile(`(?s)repositories:\n.*?\nfactoryLogin:`).ReplaceAll(raw, []byte("factoryLogin:"))
	cfg, err = config.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	cfg.HarnessProfiles = map[string]config.HarnessProfile{"plain": {}}
	cfg.Generations = config.Generations{Repos: map[string]config.RepoGenerations{"smana/cloud-native-ref": {
		Repository: 2, MergePolicy: 3, RepositoryName: "cnr", PolicyName: "cnr-policy"}}}
	rc = cfg.RepositoryFor("Smana/cloud-native-ref")
	rep = evidence.Report{Repository: rc.Name, DefaultBranch: "main", Factory: "aws-0",
		MergePolicy: "cnr-policy", ManifestPath: "mergepolicy-cnr-policy.yaml", Namespace: "factory", RepositoryRef: "cnr"}
	return base, cfg, rc, rep
}

const edited = `factory: aws-0
classes:
  docs-links:
    mode: live
    author: agents
    harness: plain
    conditions:
      all:
        - allowedFiles: ['^docs/.+\.md$']
  revert:
    mode: shadow
    author: agents
    harness: plain
`

// A preview of an edited class (docs-links promoted from shadow to live) for a repository served
// from a MergePolicy carries the MergePolicy manifest of the edited classes, in spec.classes' own
// shape: live: true and no shadow. The factory's decoder and its assembly accept it as is.
func TestPreviewCarriesTheEditedMergePolicyManifest(t *testing.T) {
	base, cfg, rc, rep := served(t)
	pv := Check(cfg, rc, rep, edited)
	if !pv.Valid || pv.Manifest == "" || pv.ManifestPath != "mergepolicy-cnr-policy.yaml" {
		t.Fatalf("%+v", pv)
	}
	objs, err := resources.Decode([]byte(pv.Manifest))
	if err != nil || len(objs) != 1 || objs[0].Kind != resources.KindMergePolicy || objs[0].Name != "cnr-policy" || objs[0].Namespace != "factory" {
		t.Fatalf("%v %+v\n%s", err, objs, pv.Manifest)
	}
	classes, _ := objs[0].Spec["classes"].(map[string]any)
	docs, _ := classes["docs-links"].(map[string]any)
	if docs["live"] != true || docs["shadow"] != nil || docs["mode"] != nil || docs["harness"] != nil {
		t.Fatalf("docs-links is not a config.Class: %v\n%s", docs, pv.Manifest)
	}
	if objs[0].Spec["repositoryRef"] != "cnr" || objs[0].Spec["merge"] == nil {
		t.Fatal(pv.Manifest)
	}
	repo := resources.Object{Kind: resources.KindRepository, Name: "cnr", Generation: 2, Spec: map[string]any{
		"factoryRef": "aws-0", "repository": "Smana/cloud-native-ref", "maintainers": []any{"Smana"}, "triggerLabel": "factory/ready"}}
	res, err := resources.Assemble(base, append([]resources.Object{repo}, objs...))
	if err != nil || len(res.Refused) != 0 {
		t.Fatalf("%v %+v\n%s", err, res.Refused, pv.Manifest)
	}
	got := res.Config.RepositoryFor("Smana/cloud-native-ref")
	if got == nil || !got.Classes["docs-links"].Live || got.Classes["docs-links"].Shadow || !got.Classes["revert"].Shadow {
		t.Fatalf("%+v", got)
	}
}

// A file the factory refuses has no manifest, and a repository the file serves gets none.
func TestPreviewManifestOnlyForAValidMergePolicyServedFile(t *testing.T) {
	_, cfg, rc, rep := served(t)
	if pv := Check(cfg, rc, rep, strings.Replace(edited, "harness: plain", "harness: frontier", 1)); pv.Valid || pv.Manifest != "" {
		t.Fatalf("%+v", pv)
	}
	file := rep
	file.MergePolicy, file.ManifestPath, file.Namespace, file.RepositoryRef = "", "", "", ""
	if pv := Check(cfg, rc, file, edited); !pv.Valid || pv.Manifest != "" || pv.ManifestPath != "" {
		t.Fatalf("%+v", pv)
	}
}

// A class spec.classes cannot hold (any, requiresApproval) leaves the preview valid, with no
// manifest and the reason why.
func TestPreviewWithoutAManifestShapeSaysWhy(t *testing.T) {
	_, cfg, rc, rep := served(t)
	file := strings.Replace(edited, "  revert:\n", "    requiresApproval:\n      - changedFiles: ['^docs/security/.+$']\n  revert:\n", 1)
	if _, err := mergepolicy.Parse([]byte(file)); err != nil {
		t.Fatal(err)
	}
	pv := Check(cfg, rc, rep, file)
	if !pv.Valid || pv.Manifest != "" || !strings.Contains(strings.Join(pv.Errors, "\n"), "requiresApproval") {
		t.Fatalf("%+v", pv)
	}
}

type quietSource struct{}

func (quietSource) MergedRecords(context.Context, string) ([]evidence.PR, error) { return nil, nil }
func (quietSource) RevertRecords(context.Context) ([]evidence.Revert, error)     { return nil, nil }
func (quietSource) AnyMerged(context.Context, string) (bool, error)              { return false, nil }
func (quietSource) FenceFile(context.Context) ([]byte, bool, error) {
	return []byte("factory: aws-0\n"), true, nil
}
func (quietSource) DefaultBranch(context.Context) (string, error) { return "main", nil }

// uiGolden is the settings page's fixture (web/test/settings.test.ts): the factory's own report
// of a repository served from a MergePolicy, the editor's text with docs-links promoted, and the
// factory's preview of that text. UPDATE_GOLDEN=1 rewrites it.
const uiGolden = "testdata/ui/merge-policy.json"

func TestUIMergePolicyGolden(t *testing.T) {
	_, cfg, _, _ := served(t)
	cfg.Generations.Repos["smana/cloud-native-ref"] = config.RepoGenerations{Repository: 2, MergePolicy: 3}
	ev := &evidence.Service{Cfg: config.NewLive(cfg), Namespace: "agent-system", Now: time.Now,
		Sources: func(string) (evidence.Source, bool) { return quietSource{}, true }}
	rep, err := ev.Report(t.Context(), "Smana/cloud-native-ref")
	if err != nil || rep.Served == nil || rep.Manifest == "" {
		t.Fatalf("%v %+v", err, rep)
	}
	promoted := *rep.Served
	promoted.Classes = maps.Clone(rep.Served.Classes)
	docs := promoted.Classes["docs-links"]
	docs.Mode = mergepolicy.ModeLive
	promoted.Classes["docs-links"] = docs
	edited, err := yaml.Marshal(promoted)
	if err != nil {
		t.Fatal(err)
	}
	pv := Check(cfg, cfg.RepositoryFor(rep.Repository), rep, string(edited))
	if !pv.Valid || pv.Manifest == "" {
		t.Fatalf("%+v", pv)
	}
	got, err := json.MarshalIndent(map[string]any{"report": rep, "edited": string(edited), "preview": pv}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	// The report's times are the test's own: the page reads none of them.
	got = regexp.MustCompile(`"readAt": "[^"]*"`).ReplaceAll(got, []byte(`"readAt": "2026-11-10T00:00:00Z"`))
	got = append(got, '\n')
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.WriteFile(uiGolden, got, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(uiGolden)
	if err != nil || string(want) != string(got) {
		t.Fatalf("%s is stale (%v): UPDATE_GOLDEN=1 go test -run TestUIMergePolicyGolden ./internal/factory/policyview/", uiGolden, err)
	}
}
