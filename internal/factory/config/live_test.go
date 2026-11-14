// SPDX-License-Identifier: Apache-2.0

package config

import (
	"sync"
	"testing"
)

func TestLivePublishRunsPrepareThenSubscribers(t *testing.T) {
	a, b := &Config{Hash: "a"}, &Config{Hash: "b"}
	l := NewLive(a)
	var prepared []string
	l.Prepare(func(c *Config) { prepared = append(prepared, c.Hash) })
	var seen [][2]string
	l.Subscribe(func(prev, next *Config) { seen = append(seen, [2]string{prev.Hash, next.Hash}) })
	l.Publish(b)
	if l.Current() != b {
		t.Fatal("Current is the published config")
	}
	if len(prepared) != 2 || prepared[0] != "a" || prepared[1] != "b" {
		t.Fatalf("Prepare runs on the current config at registration, then on each publish: %v", prepared)
	}
	if len(seen) != 1 || seen[0] != [2]string{"a", "b"} {
		t.Fatalf("subscribers see prev and next: %v", seen)
	}
}

func TestLivePublishSkipsAnUnchangedConfig(t *testing.T) {
	l := NewLive(&Config{Hash: "a"})
	calls := 0
	l.Subscribe(func(_, _ *Config) { calls++ })
	l.Publish(&Config{Hash: "a"})
	if calls != 0 {
		t.Fatal("same hash and generations: no publish")
	}
	l.Publish(&Config{Hash: "a", Generations: Generations{Factory: 2}})
	if calls != 1 {
		t.Fatal("a new generation with the same content is still a publish (tasks record it)")
	}
}

func TestLiveIsRaceFree(_ *testing.T) {
	l := NewLive(&Config{Hash: "0"})
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(2)
		go func() { defer wg.Done(); l.Publish(&Config{Hash: string(rune('a' + i))}) }()
		go func() { defer wg.Done(); _ = l.Current().Hash }()
	}
	wg.Wait()
}

func TestConfigIsItsOwnSource(t *testing.T) {
	c := &Config{}
	var s Source = c
	if s.Current() != c {
		t.Fatal("a *Config is a fixed Source")
	}
}

func TestGenerationsFor(t *testing.T) {
	c := &Config{Generations: Generations{Factory: 3, Repos: map[string]RepoGenerations{"smana/maku": {Repository: 2}}}}
	if f, r := c.GenerationsFor("Smana/maku"); f != 3 || r != 2 {
		t.Fatalf("got %d %d", f, r)
	}
	if f, r := c.GenerationsFor("Smana/other"); f != 3 || r != 0 {
		t.Fatalf("a file repository has zero object generations: %d %d", f, r)
	}
}
