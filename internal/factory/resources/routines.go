// SPDX-License-Identifier: Apache-2.0

package resources

import (
	"cmp"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
)

// RoutineOutcome is what Assemble made of one routine binding of a served Repository.
type RoutineOutcome struct {
	Repository, Name, Mode string
	Paused                 bool
	// RoutineGeneration is the generation of the Routine the served resolved routine came from:
	// the current one when Accepted, the last resolved one's when only Served; 0 when not served.
	RoutineGeneration int64
	Accepted          bool   // the binding resolved against the Routine served now
	Reason, Message   string // why not, when !Accepted: config.Reason* or v1alpha1.ReasonMissingRef
	Served            bool   // a resolved routine is served for it, this one's or the last resolved
}

type servedRoutine struct {
	Spec       config.RoutineSpec
	Generation int64
	Catalog    bool
}

// routineConfigContext builds the subset of config.Config needed by ValidateRoutineSpec and
// ResolveRoutine from the merged catalogue map. Invalid catalogue keys are left zero here so
// parseMap's culprit search blames the Factory cleanly.
func routineConfigContext(m map[string]any, ctx config.PolicyContext) *config.Config {
	cfg := &config.Config{HarnessProfiles: ctx.HarnessProfiles}
	if v, ok := m["caps"]; ok && v != nil {
		_ = decodeStrict(v, &cfg.Caps)
	}
	if v, ok := m["defaults"]; ok && v != nil {
		_ = decodeStrict(v, &cfg.Defaults)
	}
	if v, ok := m["policyManagement"]; ok && v != nil {
		_ = decodeStrict(v, &cfg.PolicyManagement)
	}
	return cfg
}

// judgeRoutines validates each standalone Routine object alone: one that fails is refused Invalid,
// and the caller's lastGood puts its last accepted generation back for consumers. Unbound catalog
// routines are never auto-bound to repositories (UP2).
func judgeRoutines(routines []Object, cfg *config.Config, res *resolution) {
	res.routines = map[string]servedRoutine{}
	res.refusedRoutines = map[string]bool{}
	for _, rt := range routines {
		var spec config.RoutineSpec
		var problems []string
		if err := decodeStrict(rt.Spec, &spec); err != nil {
			problems = []string{err.Error()}
		} else {
			problems = config.ValidateRoutineSpec(rt.Name, spec, cfg)
		}
		if len(problems) > 0 {
			res.refusedRoutines[rt.Name] = true
			res.refused = append(res.refused, Refusal{
				Kind:    KindRoutine,
				Name:    rt.Name,
				Reason:  v1alpha1.ReasonInvalid,
				Message: strings.Join(problems, "\n"),
			})
			continue
		}
		res.routines[rt.Name] = servedRoutine{Spec: spec, Generation: rt.Generation, Catalog: rt.Catalog}
		res.accepted = append(res.accepted, rt)
	}
}

// resolveRoutines resolves one Repository's spec.routines[] (and opt-in UIBindings when
// opts.UIWrite is true) in isolation so an invalid binding never causes parseMap to refuse the
// whole Repository. It returns the valid/restored bindings to pass to parseMap and ok=false only
// when spec.routines fails to decode as a slice (left raw for parseMap to refuse the Repository).
func resolveRoutines(r Object, cfgCtx *config.Config, classes map[string]config.Class, bound []string,
	opts Options, res *resolution) ([]config.RoutineBinding, bool) {
	repoName := str(r.Spec, "repository")
	var uiBindings []config.RoutineBinding
	if opts.UIWrite && len(opts.UIBindings) > 0 {
		uiBindings = slices.Clone(opts.UIBindings[strings.ToLower(repoName)])
		slices.SortFunc(uiBindings, func(a, b config.RoutineBinding) int {
			return cmp.Compare(a.Name, b.Name)
		})
	}
	raw, hasRoutines := r.Spec["routines"]
	var bindings []config.RoutineBinding
	if hasRoutines && raw != nil {
		if err := decodeStrict(raw, &bindings); err != nil {
			return nil, false
		}
	}
	if len(bindings) == 0 && len(uiBindings) == 0 {
		return nil, true
	}

	var checks config.Checks
	if rawChecks, ok := r.Spec["checks"]; ok && rawChecks != nil {
		_ = decodeStrict(rawChecks, &checks)
	}
	repoCtx := &config.Repository{
		Name:           repoName,
		ClassAuthority: str(r.Spec, "classAuthority"),
		Checks:         checks,
		Classes:        classes,
		Bound:          bound,
	}

	schedNames := map[string]bool{}
	if rawScheds, ok := r.Spec["schedules"].([]any); ok {
		for _, item := range rawScheds {
			if m, ok := item.(map[string]any); ok {
				if n := str(m, "name"); n != "" {
					schedNames[n] = true
				}
			}
		}
	}

	type taggedBinding struct {
		b      config.RoutineBinding
		fromUI bool
	}
	var all []taggedBinding
	for _, b := range bindings {
		all = append(all, taggedBinding{b: b})
	}
	for _, b := range uiBindings {
		all = append(all, taggedBinding{b: b, fromUI: true})
	}

	seen := map[string]bool{}
	resolvedMap := map[string]config.ResolvedRoutine{}
	uiNames := map[string]bool{}
	var kept []config.RoutineBinding

	for _, item := range all {
		b := item.b
		k := BindingKey{Repository: r.Name, Name: b.Name}
		o := RoutineOutcome{Repository: r.Name, Name: b.Name, Mode: b.Mode, Paused: b.Paused}

		switch {
		case !config.ValidRoutineName(b.Name):
			o.Reason = config.ReasonInvalid
			o.Message = fmt.Sprintf("routine name %q must match ^[a-z0-9]([-a-z0-9]{0,38}[a-z0-9])?$", b.Name)
		case seen[b.Name]:
			o.Reason = config.ReasonDefinedTwice
			if item.fromUI {
				o.Message = fmt.Sprintf("routine %q is defined in both Repository.spec.routines and UI bindings (%s)", b.Name, config.ReasonDefinedTwice)
			} else {
				o.Message = fmt.Sprintf("routine %q is defined twice (%s)", b.Name, config.ReasonDefinedTwice)
			}
		case schedNames[b.Name]:
			o.Reason = config.ReasonDefinedTwice
			o.Message = fmt.Sprintf("routine %q is also a schedule (%s)", b.Name, config.ReasonDefinedTwice)
		case b.Mode == config.RoutineModeDisabled:
			seen[b.Name] = true
			if msg := config.ValidateDisabledRoutineBinding(b, checks, cfgCtx); msg != "" {
				o.Reason = config.ReasonInvalid
				o.Message = msg
			} else {
				o.Accepted = true
				kept = append(kept, b)
				if item.fromUI {
					uiNames[b.Name] = true
				}
			}
		default:
			seen[b.Name] = true
			rt, ok := res.routines[b.Name]
			if !ok {
				o.Reason = v1alpha1.ReasonMissingRef
				if res.refusedRoutines[b.Name] {
					o.Message = fmt.Sprintf("Routine %s is refused", b.Name)
				} else {
					o.Message = fmt.Sprintf("no Routine %s", b.Name)
				}
				break
			}
			rr, reason, err := config.ResolveRoutine(b.Name, rt.Spec, b, repoCtx, cfgCtx)
			if err == nil {
				rr.RoutineGeneration = rt.Generation
				o.Accepted, o.Served, o.RoutineGeneration = true, true, rt.Generation
				if res.resolvedRoutines == nil {
					res.resolvedRoutines = map[BindingKey]config.ResolvedRoutine{}
				}
				res.resolvedRoutines[k] = rr
				resolvedMap[b.Name] = rr
				kept = append(kept, b)
				if item.fromUI {
					uiNames[b.Name] = true
				}
				break
			}
			o.Reason, o.Message = reason, err.Error()
			// Restore last resolved routine only for mode=pr (never widen disabled/propose/draft
			// or recover across MissingRef/DefinedTwice).
			if b.Mode == config.RoutineModePR {
				if last, ok := opts.LastResolvedRoutines[k]; ok {
					if why := recheckRestoredRoutine(last, repoCtx, cfgCtx); why != "" {
						o.Message += "; its last resolved routine no longer holds: " + why
					} else {
						last.Paused = b.Paused
						resolvedMap[b.Name] = last
						o.Served, o.RoutineGeneration = true, last.RoutineGeneration
						kept = append(kept, config.RoutineBinding{
							Name:     last.Name,
							Mode:     last.Mode,
							Paused:   b.Paused,
							Cron:     last.Cron,
							Workflow: last.Workflow,
							Egress:   slices.Clone(last.Egress),
						})
						if item.fromUI {
							uiNames[b.Name] = true
						}
					}
				}
			}
		}

		res.routineBindings = append(res.routineBindings, o)
	}

	if len(resolvedMap) > 0 {
		if res.repoRoutines == nil {
			res.repoRoutines = map[string]map[string]config.ResolvedRoutine{}
		}
		res.repoRoutines[strings.ToLower(repoName)] = resolvedMap
	}
	if len(uiNames) > 0 {
		if res.repoUIRoutines == nil {
			res.repoUIRoutines = map[string]map[string]bool{}
		}
		res.repoUIRoutines[strings.ToLower(repoName)] = uiNames
	}
	return kept, true
}

func recheckRestoredRoutine(last config.ResolvedRoutine, rc *config.Repository, cfg *config.Config) string {
	if last.Workflow != "" && cfg != nil && len(cfg.HarnessProfiles) > 0 && !cfg.IsWorkflow(last.Workflow) {
		return fmt.Sprintf("workflow %q is no longer configured", last.Workflow)
	}
	for _, p := range last.Egress {
		if !slices.Contains(rc.Checks.Egress, p) {
			return fmt.Sprintf("egress %q is no longer in repository checks.egress", p)
		}
	}
	if last.Probe.Kind == config.ProbeDeadLinks && last.OracleClass != "" && (cfg == nil || !cfg.RepositoryAuthoritative(rc)) {
		cl, ok := rc.Class(last.OracleClass)
		if !ok {
			if !slices.Contains(rc.Bound, last.OracleClass) {
				return fmt.Sprintf("oracleClass %q is no longer declared", last.OracleClass)
			}
		} else if cl.Oracle == nil || cl.Oracle.Probe != config.ProbeDeadLinks {
			return fmt.Sprintf("oracleClass %q no longer declares oracle.probe %s", last.OracleClass, config.ProbeDeadLinks)
		}
	}
	return ""
}

func toRoutineAnySlice(bindings []config.RoutineBinding) ([]any, error) {
	b, err := json.Marshal(bindings)
	if err != nil {
		return nil, err
	}
	var out []any
	return out, json.Unmarshal(b, &out)
}
