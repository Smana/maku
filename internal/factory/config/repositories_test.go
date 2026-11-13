// SPDX-License-Identifier: Apache-2.0

package config

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// The single-repository shape (v2 D1) is removed (v4 step 8, R7): each of its top-level keys is
// refused by name, pointing at the Repository object that replaced it, never decoded as unknown.
func TestTheLegacyShapeIsRefused(t *testing.T) {
	for _, k := range []string{
		"repository: Smana/cloud-native-ref", "maintainers: [Smana]", "triggerLabel: factory/ready", "controlIssue: 7",
		"classes: {docs: {}}", "merge: {fixRuns: 1}", "schedules: []",
	} {
		key, _, _ := strings.Cut(k, ":")
		t.Run(key, func(t *testing.T) {
			for name, parse := range map[string]func([]byte) error{
				"Parse":      func(b []byte) error { _, err := Parse(b); return err },
				"CheckShape": CheckShape,
			} {
				err := parse([]byte(k + "\n" + good))
				if err == nil || !strings.Contains(err.Error(), key+" is the single-repository shape") ||
					!strings.Contains(err.Error(), "Repository object") || !strings.Contains(err.Error(), "maku config migrate") {
					t.Errorf("%s: err = %v, want the removal named with its replacement", name, err)
				}
			}
		})
	}
}

func entry(name, maintainer string) string {
	return fmt.Sprintf(`  - name: %s
    maintainers: [%s]
    triggerLabel: factory/ready
    classes: {docs-links: {}, revert: {}}
    merge: {requiredChecks: [ci], verifyChecks: [ci], leakScanCheck: ci, policyBotLogin: "p[bot]", mergerLogin: "m[bot]", verifyFor: 30m, revertWindow: 168h, breaker: {window: 10, maxReverts: 1}}
`, name, maintainer)
}

func twoRepos(t *testing.T) string {
	t.Helper()
	base := good
	return base + entry("Smana/ai-native-ref", "Other")
}

func TestTwoRepositories(t *testing.T) {
	c, err := Parse([]byte(twoRepos(t)))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(c.API.Repositories, []string{"Smana/cloud-native-ref", "Smana/ai-native-ref"}) {
		t.Fatalf("api.repositories is derived from the list: %v", c.API.Repositories)
	}
	a, b := c.RepositoryFor("smana/CLOUD-native-ref"), c.RepositoryFor("Smana/ai-native-ref")
	if a.Name != "Smana/cloud-native-ref" || b.Name != "Smana/ai-native-ref" {
		t.Fatalf("matched case-insensitively: %q %q", a.Name, b.Name)
	}
	if !a.IsMaintainer("Smana") || a.IsMaintainer("Other") || !b.IsMaintainer("other") || b.IsMaintainer("Smana") {
		t.Fatal("maintainers are per repository (v2 D5)")
	}
	if b.NeedsMerger() || !a.NeedsMerger() || !c.NeedsMerger() {
		t.Fatal("the merger is needed when any repository's class is live or shadow")
	}
	if u := c.RepositoryFor("Smana/unlisted"); u != nil {
		t.Fatalf("an unlisted repository among several is not served: %+v", u)
	}
}

// One entry governs a task that names no repository, the behaviour before the list; a task that
// names another repository is not served, so a repository removed by a publish is never decided
// under the remaining one's settings (factory resources D12).
func TestOneEntryGovernsOnlyTheUnnamed(t *testing.T) {
	c, err := Parse([]byte(good))
	if err != nil {
		t.Fatal(err)
	}
	if r := c.RepositoryFor(""); r == nil || r.Name != "Smana/cloud-native-ref" || !r.IsMaintainer("Smana") {
		t.Fatalf("%+v", r)
	}
	if r := c.RepositoryFor("Smana/elsewhere"); r != nil {
		t.Fatalf("an unlisted name is served by the only entry: %+v", r)
	}
	// nil answers no maintainer and no merger: a caller that only asks those stands down.
	var none *Repository
	if none.IsMaintainer("Smana") || none.NeedsMerger() || none.AnyLive() || none.AnyMonitored() || none.TestFirstFor("x") {
		t.Fatal("a repository not served has no maintainer and no class")
	}
	if on, _ := none.ChecksFor("x"); on {
		t.Fatal("a repository not served runs no checks")
	}
}

func TestBadRepositoriesFail(t *testing.T) {
	nine := good
	for i := range MaxRepositories {
		nine += entry(fmt.Sprintf("Smana/r%d", i), "Smana")
	}
	emptyList, _, _ := strings.Cut(good, "repositories:")
	emptyList += "repositories: []\n"
	sched := "    schedules: [{name: a, cron: \"0 6 * * 1\", class: review, text: fix}]\n"
	for name, c := range map[string]struct{ raw, err string }{
		"more than eight":  {nine, "repositories holds 9 entries, at most 8"},
		"a duplicate":      {twoRepos(t) + entry("smana/AI-native-ref", "Smana"), "repository smana/AI-native-ref is listed twice"},
		"an entry unnamed": {strings.Replace(twoRepos(t), "name: Smana/ai-native-ref", "name: ''", 1), `repository "" is not owner/name`},
		"no maintainers":   {strings.Replace(twoRepos(t), "maintainers: [Other]", "maintainers: []", 1), "Smana/ai-native-ref: maintainers is empty"},
		"no trigger label": {strings.Replace(twoRepos(t), "classes: {docs-links: {}, revert: {}}", "classes: {docs-links: {}, revert: {}}\n    triggerLabel: ''", 1), "Smana/ai-native-ref: triggerLabel is required"},
		"no revert class":  {strings.Replace(twoRepos(t), "classes: {docs-links: {}, revert: {}}", "classes: {docs-links: {}}", 1), "Smana/ai-native-ref: class revert is required"},
		"merge left empty": {twoRepos(t) + "  - {name: Smana/z, maintainers: [Smana], triggerLabel: x, classes: {docs-links: {}, revert: {}}}\n", "Smana/z: merge.requiredChecks is empty"},
		"an empty list":    {emptyList, "repositories is empty"},
		"api.repositories with the list": {strings.Replace(good, "  listen: \":8443\"", "  listen: \":8443\"\n  repositories: [Smana/cloud-native-ref]", 1),
			"api.repositories is derived from repositories"},
		"control on an unlisted repository": {good + "control: {repository: Smana/other, issue: 1}\n", `control.repository "Smana/other" is not a listed repository`},
		"a negative control issue":          {good + "control: {repository: Smana/cloud-native-ref, issue: -1}\n", "control.issue must be 0 or more"},
		// The schedule's task key carries its name only (taskid.ScheduleKey): two repositories
		// sharing a name would dedupe one of them away.
		"a schedule name in two repositories": {good + sched + entry("Smana/b", "Smana") + sched, `schedule name "a" is used twice`},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Parse([]byte(c.raw))
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("err = %v, want one naming %q", err, c.err)
			}
		})
	}
}

// A class that declares a monitor reads main's checks through the merger, so it needs the key as
// live and shadow do.
func TestAMonitoredClassNeedsTheMerger(t *testing.T) {
	r := &Repository{Classes: map[string]Class{"dead-links": {Monitor: &Monitor{}}}}
	if !r.NeedsMerger() {
		t.Fatal("monitor needs the merger")
	}
	if (&Repository{Classes: map[string]Class{"x": {}}}).NeedsMerger() {
		t.Fatal("an off class does not")
	}
}
