// SPDX-License-Identifier: Apache-2.0

package hardening

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"sigs.k8s.io/yaml"
)

// Readiness reports whether every run in repository reads through author trust and writes only
// through enforced safe outputs, right now. It is computed on every call: no caching, so a
// regression refuses the next admission. An error means a fact could not be read and is a refusal.
type Readiness interface {
	Hardened(ctx context.Context, repository string) (Report, error)
}

// Report is Readiness's answer.
type Report struct {
	Hardened bool
	Missing  []Missing // empty when Hardened; bounded vocabulary, never free text
	Drill    string    // the passing drill report it relied on, for the audit log
}

// Missing names a readiness fact that does not hold.
type Missing string

// The facts, R1 to R7 (execution prerequisites table).
const (
	MissingEnforce     Missing = "mode_not_enforce"      // central or repository mode below enforce
	MissingWriteToken  Missing = "sandbox_write_token"   // a sandbox role's policy grants a write
	MissingPublisher   Missing = "publisher_policy"      // no publisher policy on the default branch
	MissingLayer       Missing = "layer_not_enforced"    // T1–T4 not all enforce
	MissingReadFilter  Missing = "read_filter_off"       // forge_read or the egress change not active
	MissingDrill       Missing = "drill_stale_or_failed" // no passing drill for this config hash within maxAge
	MissingDemoProfile Missing = "demo_profile"          // the laptop profile is never hardened
)

// Settings are the facts readiness reads from the factory's configuration for one repository.
type Settings struct {
	Mode        Mode // the repository's effective mode (R1)
	Layers      Layers
	ReadFilter  bool // forge_read active and no api.github.com egress in the rendered run profile (R5)
	Demo        bool
	DrillMaxAge time.Duration
	ConfigHash  string // the hardening configuration's hash a drill proved
	Version     string // the Maku version a drill proved
	// TokenSource is where the runs' sandbox tokens come from (R2, plan PR8); empty reads as the XR.
	TokenSource TokenSource
	// PublisherMinter is the publisher's token minted by the factory (hardening.publisher.minter),
	// read from Grants for R3; otherwise R3 reads the octo-sts publisher policy.
	PublisherMinter bool
}

// TokenSource is where a run's sandbox token comes from: a closed set.
type TokenSource string

const (
	// TokenSourceXR is cloud-native-ref's XR: octo-sts, under each role's policy file.
	TokenSourceXR TokenSource = "xr"
	// TokenSourceMinter is the factory's per-run minter (runTokens.perRun): the role table.
	TokenSourceMinter TokenSource = "minter"
	// TokenSourceShared is any other: a shared token that grants writes (step 2's writer, a PAT).
	TokenSourceShared TokenSource = "shared"
)

// SandboxRoles are the run roles whose octo-sts policies must grant no write (R2).
func SandboxRoles() []string { return []string{"implementer", "tester", "reviewer", "triager"} }

// RolePolicyPath is a sandbox role's octo-sts policy on the repository's default branch.
func RolePolicyPath(role string) string { return ".github/chainguard/agent-" + role + ".sts.yaml" }

// PublisherPolicyPath is the factory's publisher policy (UH5, O5). It sits outside agent-* so no
// glob over the sandbox roles' policies reads it as one of them.
const PublisherPolicyPath = ".github/chainguard/publisher.sts.yaml"

// PolicyFiles reads a file from a repository's default branch, through the GitHub API.
type PolicyFiles interface {
	DefaultBranchFile(ctx context.Context, repository, path string) ([]byte, bool, error)
}

// DrillKey is what a passing drill proved: a Maku version under one hardening configuration.
type DrillKey struct {
	Version    string
	ConfigHash string
}

// DrillRecord is a scored drill report as the store keeps it.
type DrillRecord struct {
	ID string
	At time.Time
}

// Drills is the factory's store of drill reports.
type Drills interface {
	LastPass(ctx context.Context, key DrillKey) (DrillRecord, bool, error)
}

// Evaluator is the production Readiness. A nil port is a fact that cannot hold, never an error:
// stage H0 runs with neither a policy reader nor a drill store, so it answers false.
type Evaluator struct {
	Settings func(repository string) Settings
	Policies PolicyFiles
	// Grants is each sandbox role's and the publisher's permissions for a repository under the
	// minter: the role table for the repository's enforced outputs (HR42, option A).
	Grants func(repository string) (roles map[string]map[string]string, publisher map[string]string)
	Drills Drills
	Now    func() time.Time
}

// Hardened evaluates R1 to R7 for repository, reading every fact afresh.
func (e Evaluator) Hardened(ctx context.Context, repository string) (Report, error) {
	if e.Settings == nil {
		return Report{}, errors.New("hardening: readiness has no settings")
	}
	s := e.Settings(repository)
	var r Report
	miss := func(ok bool, m Missing) {
		if !ok {
			r.Missing = append(r.Missing, m)
		}
	}
	miss(s.Mode == ModeEnforce, MissingEnforce)
	writes, publisher, err := e.policies(ctx, s, repository)
	if err != nil {
		return Report{}, err
	}
	miss(!writes, MissingWriteToken)
	miss(publisher, MissingPublisher)
	l := s.Layers
	miss(l.Schema == ModeEnforce && l.Sanitise == ModeEnforce && l.Secrets == ModeEnforce && l.Paths == ModeEnforce, MissingLayer)
	miss(s.ReadFilter, MissingReadFilter)
	drill, err := e.drill(ctx, s)
	if err != nil {
		return Report{}, err
	}
	miss(drill != "", MissingDrill)
	miss(!s.Demo, MissingDemoProfile)
	if r.Hardened = len(r.Missing) == 0; r.Hardened {
		r.Drill = drill
	}
	return r, nil
}

// policies is R2 and R3 from the token source runs actually get (plan PR8, HR42): whether any
// sandbox role's token may write, and whether the publisher's may. A port that is missing is a fact
// that fails.
func (e Evaluator) policies(ctx context.Context, s Settings, repository string) (writes, publisher bool, err error) {
	var roles map[string]map[string]string
	var pub map[string]string
	if e.Grants != nil && (s.TokenSource == TokenSourceMinter || s.PublisherMinter) {
		roles, pub = e.Grants(repository)
	}
	switch s.TokenSource {
	case TokenSourceMinter:
		writes = e.Grants == nil
		for _, role := range SandboxRoles() {
			writes = writes || GrantsWrite(roles[role])
		}
	case TokenSourceShared:
		writes = true // the shared writer's token grants contents and pull_requests write
	default:
		if writes, err = e.rolePolicies(ctx, repository); err != nil {
			return false, false, err
		}
	}
	if s.PublisherMinter {
		return writes, e.Grants != nil && GrantsWrite(pub), nil
	}
	if e.Policies == nil {
		return writes, false, nil
	}
	b, ok, err := e.Policies.DefaultBranchFile(ctx, repository, PublisherPolicyPath)
	if err != nil {
		return false, false, fmt.Errorf("hardening: read publisher policy: %w", err)
	}
	if ok {
		w, err := PolicyGrantsWrite(b)
		publisher = err == nil && w
	}
	return writes, publisher, nil
}

// rolePolicies is R2 on the XR: whether any sandbox role's octo-sts policy grants a write. With no
// reader, it fails.
func (e Evaluator) rolePolicies(ctx context.Context, repository string) (writes bool, err error) {
	if e.Policies == nil {
		return true, nil
	}
	for _, role := range SandboxRoles() {
		b, ok, err := e.Policies.DefaultBranchFile(ctx, repository, RolePolicyPath(role))
		if err != nil {
			return false, fmt.Errorf("hardening: read %s policy: %w", role, err)
		}
		if !ok {
			continue // no policy, no token: the role cannot write
		}
		if w, err := PolicyGrantsWrite(b); err != nil || w {
			writes = true // a policy that does not read is one that may write
		}
	}
	return writes, nil
}

// drill is R6: the id of a passing drill for this version and configuration within maxAge.
func (e Evaluator) drill(ctx context.Context, s Settings) (string, error) {
	if e.Drills == nil || e.Now == nil || s.DrillMaxAge <= 0 {
		return "", nil
	}
	d, ok, err := e.Drills.LastPass(ctx, DrillKey{Version: s.Version, ConfigHash: s.ConfigHash})
	if err != nil {
		return "", fmt.Errorf("hardening: read drills: %w", err)
	}
	age := e.Now().Sub(d.At)
	if !ok || age < 0 || age > s.DrillMaxAge {
		return "", nil
	}
	return d.ID, nil
}

// PolicyGrantsWrite reports whether an octo-sts trust policy grants any write or admin permission.
func PolicyGrantsWrite(policy []byte) (bool, error) {
	perms, err := PolicyPermissions(policy)
	if err != nil {
		return false, err
	}
	return GrantsWrite(perms), nil
}

// GrantsWrite reports whether a permissions map grants any write or admin permission.
func GrantsWrite(perms map[string]string) bool {
	for _, v := range perms {
		if v := strings.ToLower(v); v == "write" || v == "admin" {
			return true
		}
	}
	return false
}

// PolicyPermissions is an octo-sts trust policy's permissions map, each permission and access
// lower-cased. A policy that does not parse is an error.
func PolicyPermissions(policy []byte) (map[string]string, error) {
	var p struct {
		Permissions map[string]string `json:"permissions"`
	}
	if err := yaml.Unmarshal(policy, &p); err != nil {
		return nil, fmt.Errorf("octo-sts policy: %w", err)
	}
	out := make(map[string]string, len(p.Permissions))
	for k, v := range p.Permissions {
		out[strings.ToLower(k)] = strings.ToLower(v)
	}
	return out, nil
}
