// SPDX-License-Identifier: Apache-2.0

// Package maku is the human CLI's client (SP2 §8, phase 6): it lists rooms,
// follows one, posts or queues a message, and forks. It never steers,
// interrupts, moves the driver token or decides: a local agent can drive a
// terminal (ruling P18), and the broker refuses those acts from this client's
// tokens anyway.
package maku

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// The human listener's modes, as the broker reports one at /.well-known/roomctl: the same two values
// its config takes. A broker without the field is OIDC, so an older release reads as before.
const (
	HumanModeOIDC  = "oidc"
	HumanModeLocal = "local"
)

// Config is what `maku configure` saves; the room list's CLI setup view
// (GET /api/roomctl) shows every value.
type Config struct {
	URL       string `json:"url"`       // the rooms host, https://rooms.<private domain>
	Issuer    string `json:"issuer"`    // the OIDC issuer (ZITADEL under the zitadel preset)
	ClientID  string `json:"clientID"`  // the maku native app
	ProjectID string `json:"projectID"` // zitadel: a token's aud must hold it, and ZITADEL adds it only when asked
	// FactoryLogin is the factory App's GitHub login. need_answer trusts public bounce comments from
	// this login alone; empty disables that fallback while leaving authorized room_brief questions available.
	FactoryLogin string `json:"factoryLogin,omitempty"`
	// HumanMode is the broker's human listener mode as discovery reported it: "local" means no IdP and
	// no token, and the three values above are empty. Empty is OIDC, an older broker's answer.
	HumanMode string `json:"humanMode,omitempty"`
	// Preset, TokenUse, Scopes and Audience are the broker's human block as /.well-known/roomctl
	// reports it (v4 D8). An empty preset is zitadel: an older broker's answer, and today's config.
	Preset   string   `json:"preset,omitempty"`
	TokenUse string   `json:"tokenUse,omitempty"`
	Scopes   []string `json:"scopes,omitempty"`
	Audience string   `json:"audience,omitempty"`
	// DeviceAuthURL and TokenURL are the issuer's endpoints, read from its discovery document by
	// configure. Empty, zitadel uses its fixed paths and generic discovers them at login.
	DeviceAuthURL string `json:"deviceAuthorizationEndpoint,omitempty"`
	TokenURL      string `json:"tokenEndpoint,omitempty"`
}

// The broker's presets and token uses, as /.well-known/roomctl reports them (v4 D8).
const (
	PresetZITADEL  = "zitadel"
	PresetGeneric  = "generic"
	TokenUseID     = "id"
	TokenUseAccess = "access"
)

// EffectivePreset is the configured preset; an empty one is zitadel.
func (c Config) EffectivePreset() string {
	if c.Preset == "" {
		return PresetZITADEL
	}
	return c.Preset
}

// EffectiveTokenUse is the configured token use, else the preset's: zitadel's access, generic's id.
func (c Config) EffectiveTokenUse() string {
	switch {
	case c.TokenUse != "":
		return c.TokenUse
	case c.EffectivePreset() == PresetGeneric:
		return TokenUseID
	}
	return TokenUseAccess
}

// Validate refuses a plain-http URL, which would carry the bearer token in clear, and a missing id.
// A local broker holds no token, so its config needs no ids — but it must still be this machine: a
// plain-http listener is the port-forward, never a broker on another host.
func (c Config) Validate() error {
	u, err := url.Parse(c.URL)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return fmt.Errorf("url %q is not an absolute URL", c.URL)
	}
	switch c.HumanMode {
	case HumanModeLocal:
		if u.Scheme == "http" && !LoopbackURL(u) {
			return fmt.Errorf("url %q: a plain-http broker is only the loopback listener of a local install (http://localhost:<port>)", c.URL)
		}
		return nil
	case "", HumanModeOIDC:
		for name, raw := range map[string]string{"url": c.URL, "issuer": c.Issuer} {
			if u, err := url.Parse(raw); err != nil || u.Scheme != "https" || u.Host == "" {
				return fmt.Errorf("%s %q is not an https URL", name, raw)
			}
		}
		switch c.EffectivePreset() {
		case PresetZITADEL:
			if c.ClientID == "" || c.ProjectID == "" {
				return errors.New("the client id and the project id are both required")
			}
		case PresetGeneric:
			if c.ClientID == "" {
				return errors.New("the client id is required")
			}
		default:
			return fmt.Errorf("preset %q is not zitadel or generic: upgrade maku", c.Preset)
		}
		if u := c.EffectiveTokenUse(); u != TokenUseID && u != TokenUseAccess {
			return fmt.Errorf("tokenUse %q is not id or access: upgrade maku", c.TokenUse)
		}
		return nil
	default:
		return fmt.Errorf("humanMode %q is not oidc or local: run maku configure again", c.HumanMode)
	}
}

// LoopbackURL reports whether u names this machine: localhost, or an address the standard library
// calls loopback. A local install is reached only that way, by a port-forward.
func LoopbackURL(u *url.URL) bool {
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Dir is where maku keeps its config and token: <user config dir>/maku.
func Dir() (string, error) {
	d, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "maku"), nil
}

// SaveJSON writes v owner-only, through a new file renamed into place: the token
// file holds a refresh token, and WriteFile would keep an existing file's mode.
func SaveJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(path, b)
}

// writeFile writes b owner-only (0600), through a new file renamed into place.
func writeFile(path string, b []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".maku-*") // 0600
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }() // a no-op once renamed
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// LoadJSON reads what SaveJSON wrote.
func LoadJSON(path string, v any) error {
	b, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}
