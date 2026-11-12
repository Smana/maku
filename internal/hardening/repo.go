// SPDX-License-Identifier: Apache-2.0

package hardening

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"sigs.k8s.io/yaml"
)

// RepositoryPolicy is one repository's protected paths and trust floor: the platform's, extended
// by the hardening block of its fence file (UH10, HR6).
type RepositoryPolicy struct {
	Paths    ProtectedPaths
	MinTrust Level
}

// ParseRepositoryBlock reads the value of a fence file's hardening key, strictly: protectedPaths
// extends the floor, and minAuthorTrust may raise central, never lower it. An empty block is the
// floor and central. A block that does not parse answers the floor and Merged with the error, so a
// typo never loosens a repository.
func ParseRepositoryBlock(raw []byte, central Level) (RepositoryPolicy, error) {
	j, err := yaml.YAMLToJSON(raw)
	if err != nil {
		return closed(errors.New("not YAML"))
	}
	var f struct {
		ProtectedPaths []blockPath `json:"protectedPaths"`
		MinAuthorTrust *Level      `json:"minAuthorTrust"`
	}
	if t := bytes.TrimSpace(j); len(t) > 0 && !bytes.Equal(t, []byte("null")) {
		dec := json.NewDecoder(bytes.NewReader(j))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&f); err != nil {
			return closed(fmt.Errorf("not {protectedPaths, minAuthorTrust}: %w", err))
		}
	}
	floor := central
	if l := f.MinAuthorTrust; l != nil {
		switch {
		case *l == Blocked:
			return closed(errors.New("minAuthorTrust is none of none, unapproved, approved, merged"))
		case *l < central:
			return closed(fmt.Errorf("minAuthorTrust %s is below the central %s: a repository may only raise it", *l, central))
		}
		floor = *l
	}
	if len(f.ProtectedPaths) > MaxExtraPaths {
		return closed(fmt.Errorf("protectedPaths: at most %d entries", MaxExtraPaths))
	}
	rules := make([]PathRule, 0, len(f.ProtectedPaths))
	for _, e := range f.ProtectedPaths {
		if e.On != "" && e.YAMLOn != "" {
			return closed(errors.New("protectedPaths: on is written twice"))
		}
		rules = append(rules, PathRule{Pattern: e.Path, On: e.On + e.YAMLOn})
	}
	p, err := NewProtectedPaths(rules)
	if err != nil {
		return closed(err)
	}
	return RepositoryPolicy{Paths: p, MinTrust: floor}, nil
}

// blockPath is one protectedPaths entry as YAML hands it over. YAML 1.1 reads an unquoted on key
// as the boolean true, so the design's `on: blocked` arrives as {"true": "blocked"}.
type blockPath struct {
	Path   string     `json:"path"`
	On     PathAction `json:"on"`
	YAMLOn PathAction `json:"true"`
}

// closed is the policy a block that does not read answers: the floor and Merged.
func closed(err error) (RepositoryPolicy, error) {
	p, _ := NewProtectedPaths(nil) // the floor compiles: TestFloor
	return RepositoryPolicy{Paths: p, MinTrust: Merged}, fmt.Errorf("hardening block: %w", err)
}

// RepositoryPolicies caches the hardening block of each owned repository's fence file, replaced at
// each successful read (the fence's OnFile). It is safe for concurrent use; the zero value is empty.
type RepositoryPolicies struct {
	mu     sync.RWMutex
	blocks map[string]block
}

type block struct {
	raw json.RawMessage
	err error // the fence file itself did not read
	// reported: ReportOnce has answered true since this read.
	reported bool
}

// Observe takes repository's fence file as the fence read it: its signature is fence.Fence.OnFile.
// A file without a hardening key clears the repository's extension.
func (c *RepositoryPolicies) Observe(_ context.Context, repository string, body []byte) {
	var b block
	var f struct {
		Hardening json.RawMessage `json:"hardening"`
	}
	if j, err := yaml.YAMLToJSON(body); err != nil {
		b.err = errors.New("the fence file is not YAML")
	} else if err := json.Unmarshal(j, &f); err != nil {
		b.err = errors.New("the fence file is not a map")
	} else {
		b.raw = f.Hardening
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.blocks == nil {
		c.blocks = map[string]block{}
	}
	c.blocks[strings.ToLower(repository)] = b
}

// For is repository's policy under the central trust floor, which the caller reads from the live
// configuration. A repository never observed is the floor and central; a block that does not parse
// is the floor and Merged, with the error (ParseRepositoryBlock).
func (c *RepositoryPolicies) For(repository string, central Level) (RepositoryPolicy, error) {
	c.mu.RLock()
	b := c.blocks[strings.ToLower(repository)]
	c.mu.RUnlock()
	if b.err != nil {
		return closed(b.err)
	}
	return ParseRepositoryBlock(b.raw, central)
}

// ReportOnce reports true the first time it is asked about repository since the fence last handed
// over its file, false after: a caller logs a block that does not read once per fence read, not on
// every check that reads it. A repository never observed has nothing to report.
func (c *RepositoryPolicies) ReportOnce(repository string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	k := strings.ToLower(repository)
	b, ok := c.blocks[k]
	if !ok || b.reported {
		return false
	}
	b.reported = true
	c.blocks[k] = b
	return true
}
