// SPDX-License-Identifier: Apache-2.0

package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/Smana/maku/internal/hardening"
)

// defaultDrillMaxAge is how recent a passing drill must be for readiness (R6): one weekly cadence.
const defaultDrillMaxAge = 168 * time.Hour

// modeField is a mode as the file writes it. YAML 1.1 reads an unquoted off as the boolean false,
// so false reads as off; any other non-string is refused.
type modeField hardening.Mode

func (m *modeField) UnmarshalJSON(b []byte) error {
	if string(b) == "false" {
		*m = modeField(hardening.ModeOff)
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("hardening.mode, layers.classifier and a repository's mode are off, audit or enforce, not %s", b)
	}
	*m = modeField(s)
	return nil
}

// Hardening is the untrusted-input hardening track's central configuration (hardening design,
// Configuration). A repository may only make it stricter. Stage H0 ships the contracts only, so
// every mode but off is refused: nothing reads this block yet but the readiness port.
type Hardening struct {
	Mode           hardening.Mode                 `json:"-"`
	MinAuthorTrust string                         `json:"-"`
	BlockedUsers   []string                       `json:"-"`
	LabelAllowlist []string                       `json:"-"`
	Layers         HardeningLayers                `json:"-"`
	Drills         HardeningDrills                `json:"-"`
	Repositories   map[string]HardeningRepository `json:"-"`
}

// HardeningLayers sets the layers that may differ from the mode: T5 alone, since T1 to T4 run at
// the mode and cannot be set below it.
type HardeningLayers struct {
	Classifier hardening.Mode
}

// HardeningDrills bounds how recent a passing drill must be for readiness (R6).
type HardeningDrills struct {
	MaxAge Duration
}

// HardeningRepository is one repository's stricter setting.
type HardeningRepository struct {
	Mode hardening.Mode
}

// hardeningFile is the block as written: decoded strictly, every key once.
type hardeningFile struct {
	Mode           *modeField `json:"mode,omitempty"`
	MinAuthorTrust string     `json:"minAuthorTrust,omitempty"`
	BlockedUsers   []string   `json:"blockedUsers,omitempty"`
	LabelAllowlist []string   `json:"labelAllowlist,omitempty"`
	Layers         struct {
		Classifier *modeField `json:"classifier,omitempty"`
	} `json:"layers,omitzero"`
	Drills struct {
		MaxAge *Duration `json:"maxAge,omitempty"`
	} `json:"drills,omitzero"`
	Repositories map[string]struct {
		Mode *modeField `json:"mode,omitempty"`
	} `json:"repositories,omitempty"`
}

// UnmarshalJSON decodes the block strictly and applies its defaults: off, approved, the classifier
// in audit and a week's drill age.
func (h *Hardening) UnmarshalJSON(b []byte) error {
	var f hardeningFile
	if err := strictJSON(b, &f); err != nil {
		return fmt.Errorf("hardening: %w", err)
	}
	*h = Hardening{Mode: hardening.ModeOff, MinAuthorTrust: f.MinAuthorTrust, BlockedUsers: f.BlockedUsers,
		LabelAllowlist: f.LabelAllowlist}
	h.ApplyDefaults()
	if f.Mode != nil {
		h.Mode = hardening.Mode(*f.Mode)
	}
	if f.Layers.Classifier != nil {
		h.Layers.Classifier = hardening.Mode(*f.Layers.Classifier)
	}
	if f.Drills.MaxAge != nil {
		h.Drills.MaxAge = *f.Drills.MaxAge
	}
	for name, r := range f.Repositories {
		if h.Repositories == nil {
			h.Repositories = map[string]HardeningRepository{}
		}
		m := h.Mode
		if r.Mode != nil {
			m = hardening.Mode(*r.Mode)
		}
		h.Repositories[name] = HardeningRepository{Mode: m}
	}
	return nil
}

// ApplyDefaults fills what an omitted block or key leaves out.
func (h *Hardening) ApplyDefaults() {
	if h.Mode == "" {
		h.Mode = hardening.ModeOff
	}
	if h.MinAuthorTrust == "" {
		h.MinAuthorTrust = hardening.Approved.String()
	}
	if h.Layers.Classifier == "" {
		h.Layers.Classifier = hardening.ModeAudit
	}
	if h.Drills.MaxAge.Duration == 0 {
		h.Drills.MaxAge.Duration = defaultDrillMaxAge
	}
}

// ModeFor is repository's effective mode: its own when listed, else the central one.
func (h Hardening) ModeFor(repository string) hardening.Mode {
	if r, ok := h.Repositories[repository]; ok && r.Mode.AtLeast(h.Mode) {
		return r.Mode
	}
	return h.Mode
}

// MinTrust is MinAuthorTrust parsed; Validate refuses a file where it does not parse.
func (h Hardening) MinTrust() hardening.Level {
	l, err := hardening.ParseLevel(h.MinAuthorTrust)
	if err != nil {
		return hardening.Merged // unreachable after Validate; the strictest reading otherwise
	}
	return l
}

func (c *Config) validateHardening(bad func(string, ...any)) {
	h := c.Hardening
	if _, err := hardening.ParseMode(string(h.Mode)); err != nil {
		bad("hardening.mode: %v", err)
	} else if h.Mode != hardening.ModeOff {
		bad("hardening.mode %q: this release is stage H0, contracts only; audit arrives with stage H1", h.Mode)
	}
	if _, err := hardening.ParseMode(string(h.Layers.Classifier)); err != nil {
		bad("hardening.layers.classifier: %v", err)
	}
	if l, err := hardening.ParseLevel(h.MinAuthorTrust); err != nil || l == hardening.Blocked {
		bad("hardening.minAuthorTrust %q is none of none, unapproved, approved, merged", h.MinAuthorTrust)
	}
	if h.Drills.MaxAge.Duration <= 0 {
		bad("hardening.drills.maxAge must be positive")
	}
	repos := c.Repos()
	reserved := map[string]bool{}
	maintainers := []string{}
	for _, r := range repos {
		reserved[r.TriggerLabel] = true
		for cl := range r.Classes {
			reserved[cl] = true
		}
		maintainers = append(maintainers, r.Maintainers...)
	}
	for _, l := range h.LabelAllowlist {
		if strings.TrimSpace(l) == "" || strings.HasPrefix(l, "factory/") || reserved[l] {
			bad("hardening.labelAllowlist: %q is empty, a factory/ label, a trigger label or a class: an agent must not choose its own class", l)
		}
	}
	for _, u := range h.BlockedUsers {
		if strings.TrimSpace(u) == "" || slices.ContainsFunc(maintainers, func(m string) bool { return strings.EqualFold(m, u) }) {
			bad("hardening.blockedUsers: %q is empty or a maintainer", u)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(h.Repositories)) {
		r := h.Repositories[name]
		switch {
		case !slices.ContainsFunc(repos, func(x Repository) bool { return x.Name == name }):
			bad("hardening.repositories: %q is not a configured repository", name)
		case func() bool { _, err := hardening.ParseMode(string(r.Mode)); return err != nil }():
			bad("hardening.repositories.%s.mode %q is none of off, audit, enforce", name, r.Mode)
		case r.Mode != hardening.ModeOff:
			bad("hardening.repositories.%s.mode %q: this release is stage H0, contracts only", name, r.Mode)
		}
	}
	h.validateStricter(bad)
}

// validateStricter refuses a repository mode below the central one: a repository may only make
// the configuration stricter.
func (h Hardening) validateStricter(bad func(string, ...any)) {
	for _, name := range slices.Sorted(maps.Keys(h.Repositories)) {
		if !h.Repositories[name].Mode.AtLeast(h.Mode) {
			bad("hardening.repositories.%s.mode %q is below the central %q: a repository may only be stricter", name, h.Repositories[name].Mode, h.Mode)
		}
	}
}

// strictJSON decodes b into v, refusing unknown keys.
func strictJSON(b []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// Hash is the sha256 of the effective hardening configuration: the key a drill report proves
// (R6), so any change to the block needs a new passing drill.
func (h Hardening) Hash() string {
	b, _ := json.Marshal(struct { // a struct of strings, slices and maps: Marshal cannot fail
		Mode, MinAuthorTrust, Classifier, MaxAge string
		BlockedUsers, LabelAllowlist             []string
		Repositories                             map[string]hardening.Mode
	}{string(h.Mode), h.MinAuthorTrust, string(h.Layers.Classifier), h.Drills.MaxAge.String(),
		h.BlockedUsers, h.LabelAllowlist, h.repositoryModes()})
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func (h Hardening) repositoryModes() map[string]hardening.Mode {
	m := map[string]hardening.Mode{}
	for name, r := range h.Repositories {
		m[name] = r.Mode
	}
	return m
}

// HardeningSettings are the facts readiness reads from this configuration for repository. The read
// filter is off until forge_read is wired (stage H4).
func (c *Config) HardeningSettings(repository, makuVersion string) hardening.Settings {
	h := c.Hardening
	mode := h.ModeFor(repository)
	return hardening.Settings{Mode: mode, Layers: hardening.LayersFor(mode, h.Layers.Classifier),
		Demo: c.Demo, DrillMaxAge: h.Drills.MaxAge.Duration, ConfigHash: h.Hash(), Version: makuVersion}
}
