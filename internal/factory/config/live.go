// SPDX-License-Identifier: Apache-2.0

package config

import (
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
)

// Source is where a reader takes the config for one decision (factory resources D5).
type Source interface{ Current() *Config }

// Current makes a fixed *Config its own Source: tests and the CLI pass one.
func (c *Config) Current() *Config { return c }

// Generations are the object generations a published config was built from (D5: tasks record
// them). Zero means the file, not an object.
type Generations struct {
	Factory int64
	Repos   map[string]RepoGenerations // keyed by lowercased owner/name
}

// RepoGenerations are the generations of one repository's two objects: its Repository object
// and its MergePolicy object. Zero means the file served that part.
type RepoGenerations struct {
	Repository, MergePolicy int64
	// RepositoryName and PolicyName are the objects' names: a manifest for the repository must
	// carry them, or applying it creates a second MergePolicy.
	RepositoryName, PolicyName string
	// Classes are a bound repository's per-class generations (B12): each class's own MergePolicy.
	// Nil for a repository served from the per-repository shape.
	Classes map[string]ClassGeneration
}

// ClassGeneration is the generation of the MergePolicy one bound class was resolved from.
type ClassGeneration struct {
	Policy     int64
	PolicyName string
}

// ClassGenerations returns the generations class of repository was decided under (B12): its own
// policy's and the Repository's, bound true, for a bound class; the repository's MergePolicy's and
// the Repository's, bound false, for one served from the per-repository shape; zeros for the file.
func (c *Config) ClassGenerations(repository, class string) (policy, repo int64, bound bool) {
	g := c.Generations.Repos[strings.ToLower(repository)]
	if cg, ok := g.Classes[class]; ok {
		return cg.Policy, g.Repository, true
	}
	return g.MergePolicy, g.Repository, false
}

// GenerationsFor returns the generations repository was served under; zeros for a file entry.
// The repository name is matched case-insensitively, as Repos is keyed lowercased.
func (c *Config) GenerationsFor(repository string) (factory, repo, policy int64) {
	g := c.Generations.Repos[strings.ToLower(repository)]
	return c.Generations.Factory, g.Repository, g.MergePolicy
}

// Live is the factory's published config. Reads are load-free: Current never takes a lock, so a
// reader loads the config once per decision and never keeps it past that decision. Publishes
// are serialized, and each one runs its subscribers in registration order.
type Live struct {
	mu      sync.Mutex // serializes Prepare and Publish; Current never takes it
	p       atomic.Pointer[Config]
	prepare []func(*Config)
	subs    []func(prev, next *Config)
}

// NewLive returns a Live that serves c until the first publish. c must not be nil.
func NewLive(c *Config) *Live {
	l := &Live{}
	l.p.Store(c)
	return l
}

// Current returns the published config. It never blocks, even during a publish.
func (l *Live) Current() *Config { return l.p.Load() }

// Prepare registers f to run on every config before it is published, and runs it on the
// current one now: what Parse cannot set, such as SetClassSource (stage 4).
func (l *Live) Prepare(f func(*Config)) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.prepare = append(l.prepare, f)
	f(l.p.Load())
}

// Subscribe registers f to run after each publish, in registration order, with the previous
// and the new config. Subscribers re-derive what startup derived (D12).
func (l *Live) Subscribe(f func(prev, next *Config)) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.subs = append(l.subs, f)
}

// Publish runs the prepare hooks on next, stores it, then calls the subscribers. A next whose
// Hash and Generations equal the current one's is not published (no subscriber call).
// Subscribers run under the publish lock, so they see publishes in order; a subscriber must not
// call Publish or Subscribe, which would deadlock.
func (l *Live) Publish(next *Config) {
	l.mu.Lock()
	defer l.mu.Unlock()
	prev := l.p.Load()
	if prev.Hash == next.Hash && reflect.DeepEqual(prev.Generations, next.Generations) {
		return
	}
	for _, f := range l.prepare {
		f(next)
	}
	l.p.Store(next)
	for _, f := range l.subs {
		f(prev, next)
	}
}
