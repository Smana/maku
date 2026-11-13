// SPDX-License-Identifier: Apache-2.0

// Package statuswriter computes what each factory resource's status should say, from what the
// factory knows. It is the contract of the status the three kinds carry:
//
//   - Desired is pure: no I/O, no clock, no cluster. The caller passes the objects, the inputs
//     and now. The writer, not Desired, preserves a condition's stored lastTransitionTime when its
//     status did not change (apimeta.SetStatusCondition semantics).
//   - Every status carries one observedGeneration, at its level and on each condition: the
//     generation the Source observed when it has an outcome for the object, the object's own
//     otherwise. Conditions: Accepted (all kinds), Connected, RepositoryFileValid and InSync
//     (Repository; the per-repository MergePolicy keeps the last two until the next minor),
//     BindingsValid (a Repository with bindings), Ready (Factory).
//   - A refused object's other conditions are still computed when its repository is served, from
//     the last good version the factory serves.
//   - A nil Inputs.Snapshot means policyManagement.read is off: RepositoryFileValid and InSync are
//     then Unknown with reason PolicyReadOff.
//   - A class's last decision is best-effort and in memory: empty after a restart or a leader
//     change until the next decision.
//   - No counts and no spend.
package statuswriter

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"time"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/configsource"
	"github.com/Smana/maku/internal/factory/decision"
	"github.com/Smana/maku/internal/factory/mergepolicy"
	"github.com/Smana/maku/internal/factory/resources"
)

// Condition types besides Accepted, and the freshness a sync must have for the Factory to be Ready.
const (
	ConditionConnected           = "Connected"
	ConditionRepositoryFileValid = "RepositoryFileValid"
	ConditionInSync              = "InSync"
	ConditionReady               = "Ready"

	// ReasonPending is Accepted's reason before the factory looked at the object.
	ReasonPending = "Pending"

	syncFreshness      = 15 * time.Minute
	maxDivergences     = 5
	maxLastDecisionLen = 63
	maxBindingMessage  = 1024
	maxConsumers       = 64 // MergePolicyStatus.consumers MaxItems (B10)
)

// Inputs are what the factory knows besides the objects themselves.
type Inputs struct {
	Outcomes []configsource.Outcome
	// Synced is the last Sync: a stale or failing source makes the Factory not Ready (SyncFailing).
	Synced configsource.Synced
	// WireErr is the repository's last wiring error, nil when wired.
	WireErr func(repository string) error
	// Config is the published config.
	Config *config.Config
	// Connected is the repository's GitHub connection: True healthy, False unhealthy with a
	// message, Unknown when no call has been made yet (NotChecked).
	Connected func(repository string) (metav1.ConditionStatus, string)
	// Snapshot is the repository's file as last read; nil when policyManagement.read is off.
	Snapshot func(repository string) (mergepolicy.Snapshot, bool)
	// Decisions is the latest decision per class of the repository.
	Decisions func(repository string) map[string]decision.Last
	Stopped   bool
	// GitHubUp is true when no served repository's checked connection is unhealthy.
	GitHubUp bool
}

// Status is the desired status of every object passed to Desired, by object name.
type Status struct {
	Factories     map[string]v1alpha1.FactoryStatus
	Repositories  map[string]v1alpha1.RepositoryStatus
	MergePolicies map[string]v1alpha1.MergePolicyStatus
}

// Desired returns the status each object should have, conditions stamped with now.
func Desired(in Inputs, factories []v1alpha1.Factory, repos []v1alpha1.Repository, policies []v1alpha1.MergePolicy, now metav1.Time) Status {
	d := desirer{in: in, now: now, served: map[string]config.Repository{}}
	if in.Config != nil {
		for _, r := range in.Config.Repos() {
			d.served[strings.ToLower(r.Name)] = r
		}
	}
	out := Status{
		Factories:     map[string]v1alpha1.FactoryStatus{},
		Repositories:  map[string]v1alpha1.RepositoryStatus{},
		MergePolicies: map[string]v1alpha1.MergePolicyStatus{},
	}
	for i := range factories {
		out.Factories[factories[i].Name] = d.factory(&factories[i])
	}
	for i := range repos {
		out.Repositories[repos[i].Name] = d.repository(&repos[i], policies)
	}
	for i := range policies {
		out.MergePolicies[policies[i].Name] = d.mergePolicy(&policies[i])
	}
	return out
}

type desirer struct {
	in     Inputs
	now    metav1.Time
	served map[string]config.Repository // lowercased owner/name
}

// observed is the generation the Source observed when it has an outcome, the object's otherwise:
// the status never claims an observation the Source did not make.
func observed(o *configsource.Outcome, objectGen int64) int64 {
	if o != nil {
		return o.ObservedGeneration
	}
	return objectGen
}

func (d *desirer) set(cs *[]metav1.Condition, gen int64, typ string, status metav1.ConditionStatus, reason, message string) {
	apimeta.SetStatusCondition(cs, metav1.Condition{Type: typ, Status: status, Reason: reason, Message: message,
		ObservedGeneration: gen, LastTransitionTime: d.now})
}

func (d *desirer) outcome(kind resources.Kind, name string) *configsource.Outcome {
	for i := range d.in.Outcomes {
		if o := &d.in.Outcomes[i]; o.Kind == kind && o.Name == name {
			return o
		}
	}
	return nil
}

func (d *desirer) accepted(cs *[]metav1.Condition, gen int64, o *configsource.Outcome) {
	switch {
	case o == nil:
		d.set(cs, gen, v1alpha1.ConditionAccepted, metav1.ConditionUnknown, ReasonPending, "the factory has not looked at this object yet")
	case o.Accepted:
		d.set(cs, gen, v1alpha1.ConditionAccepted, metav1.ConditionTrue, "Served", "")
	case o.ServedGeneration > 0:
		d.set(cs, gen, v1alpha1.ConditionAccepted, metav1.ConditionFalse, o.Reason, fmt.Sprintf("%s; serving generation %d", o.Message, o.ServedGeneration))
	default:
		d.set(cs, gen, v1alpha1.ConditionAccepted, metav1.ConditionFalse, o.Reason, o.Message)
	}
}

func (d *desirer) factory(f *v1alpha1.Factory) v1alpha1.FactoryStatus {
	o := d.outcome(resources.KindFactory, f.Name)
	g := observed(o, f.Generation)
	st := v1alpha1.FactoryStatus{ObservedGeneration: g, Stopped: d.in.Stopped}
	if d.in.Config != nil {
		st.Repositories = len(d.in.Config.Repos())
	}
	d.accepted(&st.Conditions, g, o)
	ready := func(status metav1.ConditionStatus, reason, msg string) {
		d.set(&st.Conditions, g, ConditionReady, status, reason, msg)
	}
	sync := d.in.Synced
	switch {
	case o == nil || (!o.Accepted && o.ServedGeneration == 0):
		ready(metav1.ConditionFalse, "NotAccepted", "the Factory is not accepted")
	case !d.in.GitHubUp:
		ready(metav1.ConditionFalse, "GitHubDown", "a served repository's GitHub connection is unhealthy")
	case sync.Err != nil:
		ready(metav1.ConditionFalse, "SyncFailing", sync.Err.Error())
	case sync.At.IsZero():
		ready(metav1.ConditionFalse, "SyncFailing", "no sync yet")
	case d.now.Sub(sync.At) > syncFreshness:
		ready(metav1.ConditionFalse, "SyncFailing", "no sync since "+sync.At.UTC().Format(time.RFC3339))
	case !o.Accepted:
		// The last accepted generation keeps serving (D5): the factory is up, and Accepted says why
		// the edit is not (#448).
		ready(metav1.ConditionTrue, "ServingLastGood", fmt.Sprintf("serving generation %d; generation %d refused", o.ServedGeneration, o.ObservedGeneration))
	default:
		ready(metav1.ConditionTrue, "Ready", "")
	}
	return st
}

func (d *desirer) repository(r *v1alpha1.Repository, policies []v1alpha1.MergePolicy) v1alpha1.RepositoryStatus {
	o := d.outcome(resources.KindRepository, r.Name)
	g := observed(o, r.Generation)
	st := v1alpha1.RepositoryStatus{ObservedGeneration: g}
	d.accepted(&st.Conditions, g, o)
	name := r.Spec.Repository
	_, served := d.served[strings.ToLower(name)]
	switch {
	case served && d.in.Connected == nil:
		d.set(&st.Conditions, g, ConditionConnected, metav1.ConditionUnknown, "NotChecked", "")
	case served:
		switch status, msg := d.in.Connected(name); status {
		case metav1.ConditionTrue:
			d.set(&st.Conditions, g, ConditionConnected, metav1.ConditionTrue, "Healthy", "")
		case metav1.ConditionFalse:
			d.set(&st.Conditions, g, ConditionConnected, metav1.ConditionFalse, "Unhealthy", msg)
		default:
			d.set(&st.Conditions, g, ConditionConnected, metav1.ConditionUnknown, "NotChecked", "")
		}
	default:
		var err error
		if d.in.WireErr != nil {
			err = d.in.WireErr(name)
		}
		if err != nil {
			d.set(&st.Conditions, g, ConditionConnected, metav1.ConditionFalse, "WiringFailed", err.Error())
		} else {
			d.set(&st.Conditions, g, ConditionConnected, metav1.ConditionFalse, "NotServed", "")
		}
	}
	if rc, ok := d.served[strings.ToLower(name)]; ok {
		d.servedRepository(&st, r, &rc, o, g)
	} else {
		d.fileConditions(&st.Conditions, g, "", false)
	}
	for _, p := range sortedByName(policies) {
		// Several MergePolicies naming the Repository are all refused: it names none of them.
		if o := d.outcome(resources.KindMergePolicy, p.Name); o != nil && !o.Accepted && o.Reason == v1alpha1.ReasonDuplicatePolicy {
			continue
		}
		if p.Spec.RepositoryRef == r.Name {
			st.MergePolicy = p.Name
			break
		}
	}
	return st
}

// servedRepository fills what a served repository's status says in both shapes (B10, plan PR14): its
// classes, counts, authority, file conditions and resolved merge; and, for a bound one, each
// binding's outcome and BindingsValid.
func (d *desirer) servedRepository(st *v1alpha1.RepositoryStatus, r *v1alpha1.Repository, rc *config.Repository, o *configsource.Outcome, g int64) {
	st.Authority = config.ClassAuthorityCentral
	if d.in.Config.RepositoryAuthoritative(rc) {
		st.Authority = config.ClassAuthorityRepository
	}
	m := *rc.Merge.DeepCopy()
	st.Merge, st.MergeDefaulted = &m, slices.Clone(rc.MergeDefaulted)
	var last map[string]decision.Last
	if d.in.Decisions != nil {
		last = d.in.Decisions(rc.Name)
	}
	stamp := func(b *v1alpha1.BindingStatus) {
		if l, ok := last[b.Name]; ok {
			b.LastDecision = truncate(l.Verdict+":"+l.Reason, maxLastDecisionLen)
			at := metav1.NewTime(l.At.Truncate(time.Second)) // the API server stores seconds
			b.LastDecisionAt = &at
		}
	}
	if r.Spec.Merge != nil || r.Spec.MergePolicies != nil {
		var refused []string
		if o != nil {
			for _, b := range o.Bindings {
				bs := v1alpha1.BindingStatus{Name: b.Name, Mode: b.Mode, Accepted: b.Accepted, Reason: b.Reason, Message: truncate(b.Message, maxBindingMessage)}
				if b.Served {
					bs.PolicyGeneration = b.PolicyGeneration
				}
				if !b.Accepted {
					refused = append(refused, fmt.Sprintf("%s (%s)", b.Name, b.Reason))
				}
				stamp(&bs)
				st.Classes = append(st.Classes, bs)
			}
		}
		if len(refused) > 0 {
			d.set(&st.Conditions, g, v1alpha1.ConditionBindingsValid, metav1.ConditionFalse, "BindingsRefused", truncate("refused: "+strings.Join(refused, ", "), maxBindingMessage))
		} else {
			d.set(&st.Conditions, g, v1alpha1.ConditionBindingsValid, metav1.ConditionTrue, "BindingsValid", "")
		}
	} else {
		gen := d.in.Config.Generations.Repos[strings.ToLower(rc.Name)].MergePolicy
		for name, c := range rc.Classes {
			bs := v1alpha1.BindingStatus{Name: name, Mode: config.ModeOff, PolicyGeneration: gen, Accepted: true}
			switch {
			case c.Live:
				bs.Mode = config.ModeLive
			case c.Shadow:
				bs.Mode = config.ModeShadow
			}
			stamp(&bs)
			st.Classes = append(st.Classes, bs)
		}
		slices.SortFunc(st.Classes, func(a, b v1alpha1.BindingStatus) int { return cmp.Compare(a.Name, b.Name) })
	}
	for _, c := range rc.Classes { // what is served, whatever the bindings say
		if c.Live {
			st.LiveCount++
		}
	}
	st.ClassCount = len(st.Classes)
	d.fileConditions(&st.Conditions, g, rc.Name, true)
}

func sortedByName(ps []v1alpha1.MergePolicy) []v1alpha1.MergePolicy {
	out := slices.Clone(ps)
	slices.SortFunc(out, func(a, b v1alpha1.MergePolicy) int { return cmp.Compare(a.Name, b.Name) })
	return out
}

// mergePolicy computes a MergePolicy's status. A policy refused DuplicatePolicy is not the one the
// factory serves for its repository: it reports its own spec's classes, each mode computed from
// that spec, and no authority and no last decision, which are the served policy's. Its file
// conditions are still those of the repository's file.
func (d *desirer) mergePolicy(p *v1alpha1.MergePolicy) v1alpha1.MergePolicyStatus {
	o := d.outcome(resources.KindMergePolicy, p.Name)
	g := observed(o, p.Generation)
	st := v1alpha1.MergePolicyStatus{ObservedGeneration: g}
	if p.Spec.RepositoryRef == "" {
		return d.classPolicy(o, g, st)
	}
	d.accepted(&st.Conditions, g, o)
	repository := ""
	if o != nil {
		repository = o.Repository
	}
	rc, served := d.served[strings.ToLower(repository)]
	if repository == "" || !served {
		d.fileConditions(&st.Conditions, g, "", false)
		return st
	}
	classes := rc.Classes
	var last map[string]decision.Last
	if o.Reason == v1alpha1.ReasonDuplicatePolicy && !o.Accepted {
		classes = p.Spec.Classes
	} else {
		st.Authority = config.ClassAuthorityCentral
		if d.in.Config.RepositoryAuthoritative(&rc) {
			st.Authority = config.ClassAuthorityRepository
		}
		if d.in.Decisions != nil {
			last = d.in.Decisions(repository)
		}
	}
	for name, c := range classes {
		cs := v1alpha1.ClassStatus{Name: name, Mode: "unarmed"}
		switch {
		case c.Live:
			cs.Mode = "live"
			st.LiveCount++
		case c.Shadow:
			cs.Mode = "shadow"
		}
		if l, ok := last[name]; ok {
			cs.LastDecision = truncate(l.Verdict+":"+l.Reason, maxLastDecisionLen)
			at := metav1.NewTime(l.At.Truncate(time.Second)) // the API server stores seconds
			cs.LastDecisionAt = &at
		}
		st.Classes = append(st.Classes, cs)
	}
	slices.SortFunc(st.Classes, func(a, b v1alpha1.ClassStatus) int { return cmp.Compare(a.Name, b.Name) })
	st.ClassCount = len(st.Classes)
	d.fileConditions(&st.Conditions, g, repository, true)
	return st
}

// classPolicy computes a class-shape MergePolicy's status (B10): Accepted, and its consumers. A
// refused generation whose last good still serves says so; the consumers show which generation
// each one serves. It has no repository, so no authority, classes or file conditions.
func (d *desirer) classPolicy(o *configsource.Outcome, g int64, st v1alpha1.MergePolicyStatus) v1alpha1.MergePolicyStatus {
	if o != nil && !o.Accepted && o.ServedGeneration > 0 {
		d.set(&st.Conditions, g, v1alpha1.ConditionAccepted, metav1.ConditionFalse, o.Reason,
			fmt.Sprintf("serving generation %d; generation %d refused: %s", o.ServedGeneration, o.ObservedGeneration, o.Message))
	} else {
		d.accepted(&st.Conditions, g, o)
	}
	if o == nil {
		return st
	}
	for _, b := range o.Consumers {
		cs := v1alpha1.ConsumerStatus{Repository: b.Repository, Mode: b.Mode, Accepted: b.Accepted, Reason: b.Reason}
		if b.Served {
			cs.ServedGeneration = b.PolicyGeneration
		}
		if cs.Accepted && cs.ServedGeneration == g {
			st.CurrentCount++
		}
		if b.Mode == config.ModeLive && b.Served {
			st.LiveCount++
		}
		st.Consumers = append(st.Consumers, cs)
	}
	slices.SortFunc(st.Consumers, func(a, b v1alpha1.ConsumerStatus) int { return cmp.Compare(a.Repository, b.Repository) })
	// The counts are over every consumer; the list is cut to the schema's bound, or the status
	// update would be refused and the status freeze.
	st.ConsumerCount = len(st.Consumers)
	st.Consumers = st.Consumers[:min(len(st.Consumers), maxConsumers)]
	return st
}

func (d *desirer) fileConditions(cs *[]metav1.Condition, gen int64, repository string, served bool) {
	both := func(status metav1.ConditionStatus, reason string) {
		d.set(cs, gen, ConditionRepositoryFileValid, status, reason, "")
		d.set(cs, gen, ConditionInSync, status, reason, "")
	}
	if d.in.Snapshot == nil {
		both(metav1.ConditionUnknown, "PolicyReadOff")
		return
	}
	if !served {
		both(metav1.ConditionUnknown, "NotServed")
		return
	}
	snap, ok := d.in.Snapshot(repository)
	if !ok {
		both(metav1.ConditionUnknown, "NotRead")
		return
	}
	switch {
	case snap.Err != nil:
		d.set(cs, gen, ConditionRepositoryFileValid, metav1.ConditionFalse, "Invalid", snap.Err.Error())
	case !snap.HasClasses:
		d.set(cs, gen, ConditionRepositoryFileValid, metav1.ConditionFalse, "Invalid", "the file declares no classes")
	default:
		d.set(cs, gen, ConditionRepositoryFileValid, metav1.ConditionTrue, "Valid", "")
	}
	switch {
	case snap.Err != nil || !snap.HasClasses:
		d.set(cs, gen, ConditionInSync, metav1.ConditionFalse, "FileInvalid", "the repository's file is not valid")
	case len(snap.Divergences) > 0:
		shown := snap.Divergences[:min(len(snap.Divergences), maxDivergences)]
		msg := strings.Join(shown, "; ")
		if more := len(snap.Divergences) - len(shown); more > 0 {
			msg += fmt.Sprintf("; and %d more", more)
		}
		d.set(cs, gen, ConditionInSync, metav1.ConditionFalse, "Diverges", msg)
	default:
		d.set(cs, gen, ConditionInSync, metav1.ConditionTrue, "InSync", "")
	}
}

func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}
