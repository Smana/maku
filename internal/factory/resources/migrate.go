// SPDX-License-Identifier: Apache-2.0

package resources

import (
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"

	"github.com/robfig/cron/v3"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"

	"github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
)

// Migrate moves a config file's catalogue and repositories into manifests (D8): it returns the
// manifests, as multi-document YAML in namespace ns, and the file with those keys removed. The
// workflow settings release N+1 refuses are rewritten first (upgradeWorkflows).
// Keys move verbatim, so a defaulted field stays omitted. Each repository's classes and merge are
// split into class-shape MergePolicies and the Repository's bindings (split, B17). A file still in
// the single-repository shape, which the factory refuses, is read as its one-entry list first (R7).
func Migrate(file []byte, ns string) (manifests, residual []byte, err error) {
	if file, err = Upgrade(file); err != nil {
		return nil, nil, err
	}
	var base map[string]any
	if err := yaml.Unmarshal(file, &base); err != nil {
		return nil, nil, fmt.Errorf("config file: %w", err)
	}
	if base == nil {
		base = map[string]any{}
	}
	// Parse's refusals (an empty factory name, an entry without a name) are Migrate's: what it
	// would split must already be a valid file.
	if _, err := config.Parse(file); err != nil {
		return nil, nil, fmt.Errorf("migrate: %w", err)
	}
	factory := str(base, "factory")

	spec := map[string]any{}
	for _, k := range CatalogueKeys {
		if v, ok := base[k]; ok {
			spec[k] = v
			delete(base, k)
		}
	}
	if meter, ok := base["meter"].(map[string]any); ok {
		if v, ok := meter["prices"]; ok {
			spec["prices"] = v
			delete(meter, "prices")
			if len(meter) == 0 {
				delete(base, "meter")
			}
		}
	}
	if triage, ok := base["triage"].(map[string]any); ok {
		if v, ok := triage["controlPercent"]; ok {
			spec["triage"] = map[string]any{"controlPercent": v}
			delete(triage, "controlPercent")
			if len(triage) == 0 {
				delete(base, "triage")
			}
		}
	}

	// YAML 1.1 reads a bare off as false; the CRD wants the string.
	offFields(spec, func(_ string, row map[string]any, key string) {
		if b, ok := row[key].(bool); ok && !b {
			row[key] = "off"
		}
	})

	objs := []Object{{Kind: KindFactory, Name: factory, Namespace: ns, Spec: spec}}

	entries, _ := base["repositories"].([]any)
	delete(base, "repositories")
	var repoNames []string
	for _, e := range entries {
		if m, ok := e.(map[string]any); ok {
			repoNames = append(repoNames, str(m, "name"))
		}
	}
	for _, e := range entries {
		m, ok := e.(map[string]any)
		if !ok {
			return nil, nil, fmt.Errorf("migrate: repositories[] entry is not a map: %v", e)
		}
		repository := str(m, "name")
		name := ObjectName(repository, repoNames)
		rs := map[string]any{"factoryRef": factory, "repository": repository}
		for _, k := range []string{"maintainers", "triggerLabel", "checks", "schedules", "routines", "workflow", "classAuthority", "privateCode"} {
			if v, ok := m[k]; ok {
				rs[k] = v
			}
		}
		ps := map[string]any{"repositoryRef": name}
		for _, k := range []string{"classes", "merge"} {
			if v, ok := m[k]; ok {
				ps[k] = v
			}
		}
		objs = append(objs, Object{Kind: KindRepository, Name: name, Namespace: ns, Spec: rs},
			Object{Kind: KindMergePolicy, Name: name, Namespace: ns, Spec: ps})
	}
	if objs, err = split(objs, ns); err != nil {
		return nil, nil, fmt.Errorf("migrate: %w", err)
	}
	if manifests, err = Encode(objs); err != nil {
		return nil, nil, err
	}
	residual, err = yaml.Marshal(base)
	if err != nil {
		return nil, nil, err
	}
	return manifests, residual, nil
}

// LiftLegacy is file in the repositories shape: a file still in the single-repository shape (v2
// D1), which the factory refuses (R7), as the one-entry list it stood for, and any other file as is.
// maku config migrate reads the old file through it.
func LiftLegacy(file []byte) ([]byte, error) {
	var base map[string]any
	if err := yaml.Unmarshal(file, &base); err != nil {
		return nil, fmt.Errorf("config file: %w", err)
	}
	if _, legacy := base["repository"]; !legacy {
		return file, nil
	}
	if err := liftLegacy(base); err != nil {
		return nil, err
	}
	return yaml.Marshal(base)
}

// liftLegacy rewrites, in place, the single-repository shape (v2 D1) that the factory no longer
// reads (v4 step 8, R7) as the one-entry repositories it stood for: the per-repository keys move
// into the entry, controlIssue becomes control on that repository, and api.repositories goes, as
// the list derives it. It refuses an allowlist naming another repository, which the list cannot say.
func liftLegacy(base map[string]any) error {
	name, _ := base["repository"].(string)
	if _, ok := base["repositories"]; ok {
		return errors.New("migrate: repository is the single-repository shape: write repositories or repository, never both")
	}
	entry := map[string]any{"name": name}
	for _, k := range []string{"maintainers", "triggerLabel", "classes", "merge", "schedules"} {
		if v, ok := base[k]; ok {
			entry[k] = v
			delete(base, k)
		}
	}
	delete(base, "repository")
	base["repositories"] = []any{entry}
	if n, ok := base["controlIssue"]; ok {
		if _, set := base["control"]; set {
			return errors.New("migrate: controlIssue and control both set: keep one")
		}
		base["control"] = map[string]any{"repository": name, "issue": n}
		delete(base, "controlIssue")
	}
	if api, ok := base["api"].(map[string]any); ok {
		if list, ok := api["repositories"].([]any); ok {
			for _, r := range list {
				if s, _ := r.(string); !strings.EqualFold(s, name) {
					return fmt.Errorf("migrate: api.repositories names %v, which a repositories list cannot admit: serve it from its own Repository", r)
				}
			}
			delete(api, "repositories")
		}
	}
	return nil
}

// ObjectName is the name Migrate gives repository's Repository and MergePolicy objects: its name
// part, or the whole name when another of repositories shares that part.
func ObjectName(repository string, repositories []string) string {
	n := namePart(repository)
	for _, o := range repositories {
		if o != repository && namePart(o) == n {
			return dnsLabel(repository)
		}
	}
	return n
}

// RepositoryManifest is the YAML of a Repository named name in ns with spec as given (B2, B3).
func RepositoryManifest(name, ns string, spec map[string]any) ([]byte, error) {
	return yaml.Marshal(map[string]any{
		"apiVersion": "maku.ogenki.io/v1alpha1",
		"kind":       string(KindRepository),
		"metadata":   map[string]any{"name": name, "namespace": ns},
		"spec":       spec,
	})
}

// PolicyManifest is the YAML of a class-shape MergePolicy named name in ns (B1, B7): spec as JSON
// writes it, so an omitted field stays omitted.
func PolicyManifest(name, ns string, spec config.PolicySpec) ([]byte, error) {
	m, err := toMap(spec)
	if err != nil {
		return nil, err
	}
	return yaml.Marshal(map[string]any{
		"apiVersion": "maku.ogenki.io/v1alpha1",
		"kind":       string(KindMergePolicy),
		"metadata":   map[string]any{"name": name, "namespace": ns},
		"spec":       m,
	})
}

// namePart is the repository's name without its owner, as a DNS-1123 label.
func namePart(repository string) string {
	_, n, ok := strings.Cut(repository, "/")
	if !ok {
		n = repository
	}
	return dnsLabel(n)
}

// dnsLabel lowercases s, turns '/', '_' and '.' into '-' and trims '-' from both ends: a GitHub
// owner or repository name is then a DNS-1123 label.
func dnsLabel(s string) string {
	return strings.Trim(strings.NewReplacer("/", "-", "_", "-", ".", "-").Replace(strings.ToLower(s)), "-")
}

// offFields visits loops.*.scope and workflows.*.loop where set, in name order: the two
// fields where YAML 1.1 reads a bare off as the boolean false.
func offFields(spec map[string]any, visit func(path string, row map[string]any, key string)) {
	for _, f := range []struct{ table, key string }{{"loops", "scope"}, {"workflows", "loop"}} {
		rows, _ := spec[f.table].(map[string]any)
		for _, name := range slices.Sorted(maps.Keys(rows)) {
			if row, ok := rows[name].(map[string]any); ok {
				if _, set := row[f.key]; set {
					visit(fmt.Sprintf("spec.%s.%s.%s", f.table, name, f.key), row, f.key)
				}
			}
		}
	}
}

// Upgrade is file as this release reads it: unwrapped from a saved golden envelope if present,
// lifted from the single-repository shape (LiftLegacy), and with the workflow and merge-policy
// settings release N warned about rewritten (upgradeWorkflows, upgradeMergePolicies, WF9, WF13,
// MR9, MR13). A file that needs none of these is returned as is.
func Upgrade(file []byte) ([]byte, error) {
	var env map[string]any
	if yaml.Unmarshal(file, &env) == nil && env["factory"] == nil {
		if inner, ok := env["Config"].(map[string]any); ok {
			var err error
			if file, err = yaml.Marshal(inner); err != nil {
				return nil, err
			}
		}
	}
	file, err := LiftLegacy(file)
	if err != nil {
		return nil, err
	}
	var base map[string]any
	if yaml.Unmarshal(file, &base) != nil {
		return file, nil // a file that does not read is Parse's to refuse
	}
	wfChanged := upgradeWorkflows(base)
	mpChanged := upgradeMergePolicies(base)
	if !wfChanged && !mpChanged {
		return file, nil
	}
	return yaml.Marshal(base)
}

// isCatalogPolicy reports whether name is one of the shipped catalog policies (MR2, MR9).
func isCatalogPolicy(name string) bool {
	switch name {
	case "docs", "docs-links", "deps-patch", "revert":
		return true
	}
	return false
}

// upgradeMergePolicies rewrites in base every merge-policy setting release N deprecated and
// release N+1 refuses (MR9, MR13), moving workflow fields on classes and schedules to their new
// homes under workflows and Repository.workflow.
func upgradeMergePolicies(base map[string]any) bool {
	changed := false
	truthy := func(v any) bool { b, ok := v.(bool); return ok && b }

	if api, ok := base["api"].(map[string]any); ok {
		if _, hasRepos := base["repositories"]; hasRepos {
			if _, hasAPIRepos := api["repositories"]; hasAPIRepos {
				delete(api, "repositories")
				changed = true
			}
		}
	}

	if pm, ok := base["policyManagement"].(map[string]any); ok {
		stage := ""
		switch {
		case truthy(pm["promotion"]) || truthy(pm["approvalMemory"]) || truthy(pm["repositoryAuthority"]):
			stage = config.StageManage
		case truthy(pm["simulation"]) || truthy(pm["dualEvaluation"]):
			stage = config.StageSimulate
		case truthy(pm["read"]):
			stage = config.StageRead
		}
		hadSwitch := false
		for _, k := range []string{"read", "simulation", "dualEvaluation", "repositoryAuthority", "approvalMemory", "promotion"} {
			if _, ok := pm[k]; ok {
				delete(pm, k)
				hadSwitch = true
			}
		}
		if hadSwitch {
			if cur, _ := pm["stage"].(string); cur == "" && stage != "" {
				pm["stage"] = stage
			}
			if len(pm) == 0 {
				delete(base, "policyManagement")
			}
			changed = true
		}
	}

	if mg, ok := base["mergeGuardrails"].(map[string]any); ok {
		if _, ok := mg["minPromotionEvidence"]; ok {
			delete(mg, "minPromotionEvidence")
			if len(mg) == 0 {
				delete(base, "mergeGuardrails")
			}
			changed = true
		}
	}

	renamePerDay := func(m map[string]any) {
		if m == nil {
			return
		}
		if v, ok := m["autoMergesPerDay"]; ok {
			if _, hasMax := m["maxPerDay"]; !hasMax {
				m["maxPerDay"] = v
			}
			delete(m, "autoMergesPerDay")
			changed = true
		}
	}
	if md, ok := base["mergeDefaults"].(map[string]any); ok {
		renamePerDay(md)
	}

	wfMap, _ := base["workflows"].(map[string]any)
	ensureWorkflow := func(name string) map[string]any {
		if wfMap == nil {
			wfMap = map[string]any{}
			base["workflows"] = wfMap
		}
		w, ok := wfMap[name].(map[string]any)
		if !ok {
			w = map[string]any{}
			wfMap[name] = w
		}
		return w
	}
	isKnownWorkflow := func(name string) bool {
		switch name {
		case "", config.TemplateNone:
			return false
		case config.WorkflowQuick, config.WorkflowStandard, config.WorkflowCareful:
			return true
		}
		return wfMap != nil && wfMap[name] != nil
	}

	repos, _ := base["repositories"].([]any)
	for _, entry := range repos {
		m, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		merge, _ := m["merge"].(map[string]any)
		renamePerDay(merge)

		classHarness := map[string]string{}
		classHasProbe := map[string]bool{}
		defaultWF := ""
		anyTestFirst := false

		if classes, ok := m["classes"].(map[string]any); ok {
			if rev, ok := classes["review"].(map[string]any); ok {
				if h, _ := rev["harness"].(string); isKnownWorkflow(h) {
					defaultWF = h
				}
			}
			for _, cname := range slices.Sorted(maps.Keys(classes)) {
				cl, ok := classes[cname].(map[string]any)
				if !ok {
					continue
				}
				if oracle, ok := cl["oracle"].(map[string]any); ok {
					if p, _ := oracle["probe"].(string); p != "" {
						classHasProbe[cname] = true
					}
				}
				h, _ := cl["harness"].(string)
				if h != "" {
					classHarness[cname] = h
					if defaultWF == "" && isKnownWorkflow(h) {
						defaultWF = h
					}
				}
				if h != "" && h != config.TemplateNone {
					if esc, ok := cl["escalation"].(map[string]any); ok && len(esc) > 0 {
						w := ensureWorkflow(h)
						if w["escalation"] == nil {
							w["escalation"] = esc
						}
					}
					if truthy(cl["checks"]) {
						w := ensureWorkflow(h)
						w["checks"] = true
					}
				}
				if truthy(cl["testFirst"]) {
					anyTestFirst = true
				}
				for _, f := range []string{"harness", "harnessAllowed", "escalation", "testFirst", "checks"} {
					if _, ok := cl[f]; ok {
						delete(cl, f)
						changed = true
					}
				}
			}
			for _, cname := range slices.Sorted(maps.Keys(classes)) {
				if cname == "review" {
					delete(classes, cname)
					changed = true
					continue
				}
				cl, _ := classes[cname].(map[string]any)
				if !truthy(cl["live"]) && !truthy(cl["shadow"]) && !isCatalogPolicy(cname) {
					delete(classes, cname)
					changed = true
				}
			}
		}

		rw, _ := m["workflow"].(map[string]any)
		if defaultWF != "" && (rw == nil || str(rw, "default") == "") {
			if rw == nil {
				rw = map[string]any{}
			}
			rw["default"] = defaultWF
			m["workflow"] = rw
			changed = true
		}
		if anyTestFirst && str(merge, "contractCheck") != "" {
			if rw == nil {
				rw = map[string]any{}
			}
			if !truthy(rw["testFirst"]) {
				rw["testFirst"] = true
				m["workflow"] = rw
				changed = true
			}
		}

		if schedules, ok := m["schedules"].([]any); ok {
			for _, raw := range schedules {
				s, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				cname := str(s, "class")
				if cname != "" && !classHasProbe[cname] {
					delete(s, "class")
					if str(s, "workflow") == "" && isKnownWorkflow(classHarness[cname]) {
						s["workflow"] = classHarness[cname]
					}
					changed = true
				}
			}
		}

		if bindings, ok := m["mergePolicies"].([]any); ok {
			kept := make([]any, 0, len(bindings))
			for _, raw := range bindings {
				b, ok := raw.(map[string]any)
				if !ok {
					kept = append(kept, raw)
					continue
				}
				bname := str(b, "name")
				if bname == "review" {
					changed = true
					continue
				}
				if b["mode"] == "off" || b["mode"] == false {
					changed = true
					if isCatalogPolicy(bname) {
						b["mode"] = config.ModeDisabled
						kept = append(kept, b)
					}
					continue
				}
				kept = append(kept, b)
			}
			m["mergePolicies"] = kept
		}
	}

	if loops, ok := base["loops"].(map[string]any); ok {
		if docsRow, hasDocs := loops["docs"]; hasDocs {
			if _, hasCareful := loops[config.WorkflowCareful]; !hasCareful {
				loops[config.WorkflowCareful] = docsRow
			}
			delete(loops, "docs")
			changed = true
		}
		for _, lname := range slices.Sorted(maps.Keys(loops)) {
			if lname == "default" || lname == config.WorkflowQuick || lname == config.WorkflowStandard || lname == config.WorkflowCareful {
				continue
			}
			if wfMap != nil && wfMap[lname] != nil {
				continue
			}
			delete(loops, lname)
			changed = true
		}
	}

	return changed
}

// upgradeWorkflows rewrites in base the settings of the workflows design that release N warned
// about and release N+1 refuses (WF9, WF13), and reports whether it changed any: harnessProfiles is
// renamed workflows, a workflow's template trio becomes pair, and templates.trio,
// triage.classifierURL, defaults.template, defaults.tier and a workflow's testFirst (the
// Repository's workflow.testFirst now) are dropped.
func upgradeWorkflows(base map[string]any) bool {
	changed := false
	if hp, ok := base["harnessProfiles"]; ok {
		if _, both := base["workflows"]; !both {
			base["workflows"] = hp
			delete(base, "harnessProfiles")
			changed = true
		}
	}
	if wf, ok := base["workflows"].(map[string]any); ok {
		for _, v := range wf {
			w, _ := v.(map[string]any)
			if w["template"] == "trio" {
				w["template"], changed = "pair", true
			}
			if _, ok := w["testFirst"]; ok {
				delete(w, "testFirst")
				changed = true
			}
		}
	}
	for _, k := range [][2]string{{"templates", "trio"}, {"triage", "classifierURL"}, {"defaults", "template"}, {"defaults", "tier"}} {
		if block, ok := base[k[0]].(map[string]any); ok {
			if _, ok := block[k[1]]; ok {
				delete(block, k[1])
				changed = true
			}
		}
	}
	return changed
}

// MigrateSchedules converts each repository's deprecated spec.schedules[] into reusable Routine
// manifests and per-repository RoutineBindings (P1-R2). Schedules sharing a name across
// repositories produce one Routine when their meaning matches, and an error when they differ.
func MigrateSchedules(repos []config.Repository) ([]v1alpha1.Routine, map[string][]config.RoutineBinding, error) {
	type seenRoutine struct {
		spec config.RoutineSpec
		repo string
	}
	byName := map[string]seenRoutine{}
	bindings := map[string][]config.RoutineBinding{}

	for _, r := range repos {
		if len(r.Schedules) == 0 {
			continue
		}
		var repoBindings []config.RoutineBinding
		for _, s := range r.Schedules {
			if !config.ValidRoutineName(s.Name) {
				return nil, nil, fmt.Errorf("migrate schedules: %s: schedule name %q is not a valid Routine name", r.Name, s.Name)
			}
			if s.Cron == "" {
				return nil, nil, fmt.Errorf("migrate schedules: %s: schedule %q has empty cron", r.Name, s.Name)
			}
			if _, err := cron.ParseStandard(s.Cron); err != nil {
				return nil, nil, fmt.Errorf("migrate schedules: %s: schedule %q cron %q does not parse: %w", r.Name, s.Name, s.Cron, err)
			}
			if s.Text == "" {
				return nil, nil, fmt.Errorf("migrate schedules: %s: schedule %q has empty text", r.Name, s.Name)
			}

			spec := config.RoutineSpec{
				Workflow:      s.WorkflowOr(),
				DataClass:     s.DataClass,
				DefaultPrompt: s.Text,
				MaxOpenPRs:    1,
			}
			b := config.RoutineBinding{
				Name: s.Name,
				Mode: config.RoutineModePR,
				Cron: s.Cron,
			}

			switch s.Probe {
			case "", config.ProbeNone:
				spec.Probe = config.ProbeSpec{Kind: config.ProbeNone}
			case config.ProbeDeadLinks:
				if s.Class == "" {
					return nil, nil, fmt.Errorf("migrate schedules: %s: schedule %q with probe dead-links requires class", r.Name, s.Name)
				}
				spec.Probe = config.ProbeSpec{
					Kind:               config.ProbeDeadLinks,
					RequireConsecutive: 2,
					MaxFindings:        10,
				}
				spec.Requires = []string{"oracleClass"}
				b.OracleClass = s.Class
			case config.ProbeRenovateRed:
				spec.Probe = config.ProbeSpec{Kind: config.ProbeRenovateRed}
				if s.ProbeAuthor != "" && s.ProbeAuthor != "renovate[bot]" {
					b.ProbeAuthor = s.ProbeAuthor
				}
			default:
				return nil, nil, fmt.Errorf("migrate schedules: %s: schedule %q has unsupported probe %q", r.Name, s.Name, s.Probe)
			}

			if prev, ok := byName[s.Name]; ok {
				if !reflect.DeepEqual(prev.spec, spec) {
					return nil, nil, fmt.Errorf("migrate schedules: schedule %q differs between %s and %s", s.Name, prev.repo, r.Name)
				}
			} else {
				byName[s.Name] = seenRoutine{spec: spec, repo: r.Name}
			}
			repoBindings = append(repoBindings, b)
		}
		bindings[r.Name] = repoBindings
	}

	routines := make([]v1alpha1.Routine, 0, len(byName))
	for _, name := range slices.Sorted(maps.Keys(byName)) {
		routines = append(routines, v1alpha1.Routine{
			TypeMeta: metav1.TypeMeta{
				APIVersion: v1alpha1.GroupVersion.String(),
				Kind:       string(KindRoutine),
			},
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec:       v1alpha1.RoutineSpec{RoutineSpec: byName[name].spec},
		})
	}
	return routines, bindings, nil
}
