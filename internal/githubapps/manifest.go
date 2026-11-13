// SPDX-License-Identifier: Apache-2.0

package githubapps

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// HomepageURL is the homepage an App created from a manifest points at.
const HomepageURL = "https://github.com/Smana/maku"

// maxNameLen is GitHub's limit on an App's name.
const maxNameLen = 34

// Kind is which of the two Apps a manifest creates (D12).
type Kind string

const (
	AgentsApp  Kind = "agents"  // mints the runs' and the publisher's tokens
	FactoryApp Kind = "factory" // issues, labels, statuses and merges, with the merger folded in
)

// AppManifest is the JSON GitHub's App manifest flow takes. The fields are in alphabetical order, so
// that its encoding matches a map's: `maku demo app` built its manifest from one.
type AppManifest struct {
	CallbackURLs          []string       `json:"callback_urls,omitempty"`
	DefaultEvents         []string       `json:"default_events"`
	DefaultPermissions    Permissions    `json:"default_permissions"`
	HookAttributes        HookAttributes `json:"hook_attributes"`
	Name                  string         `json:"name"`
	Public                bool           `json:"public"`
	RedirectURL           string         `json:"redirect_url"` // set by the Flow: its loopback callback
	RequestOAuthOnInstall *bool          `json:"request_oauth_on_install,omitempty"`
	URL                   string         `json:"url"`
}

// HookAttributes is the App's webhook; the factory polls, so it is never active (step 6 R2).
type HookAttributes struct {
	Active bool   `json:"active"`
	URL    string `json:"url"`
}

// ManifestOptions are the answers a manifest is built from.
type ManifestOptions struct {
	Homepage string        // the App's homepage; empty is HomepageURL
	WebURL   string        // the factory App: the web hostname, whose /link/github/callback is the OAuth callback (R5)
	Merger   MergerOptions // the factory App: what the folded merger asks for; Folded is always set
}

// Manifest is the manifest that creates the kind's App, named name: its required set, no webhook, not
// public. The factory App folds the merger in (no third App, D12) and registers the account link's
// OAuth callback (R5).
func Manifest(kind Kind, name string, o ManifestOptions) (AppManifest, error) {
	if name == "" || len(name) > maxNameLen {
		return AppManifest{}, fmt.Errorf("an App name has 1 to %d characters, got %d", maxNameLen, len(name))
	}
	home := o.Homepage
	if home == "" {
		home = HomepageURL
	}
	if u, err := url.Parse(home); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return AppManifest{}, errors.New("the App's homepage is not an http(s) URL")
	}
	no := false
	m := AppManifest{
		DefaultEvents:         []string{},
		HookAttributes:        HookAttributes{URL: home},
		Name:                  name,
		RequestOAuthOnInstall: &no,
		URL:                   home,
	}
	switch kind {
	case AgentsApp:
		m.DefaultPermissions = Agents()
	case FactoryApp:
		cb, err := linkCallback(o.WebURL)
		if err != nil {
			return AppManifest{}, err
		}
		merger := o.Merger
		merger.Folded = true
		m.DefaultPermissions = Factory(merger)
		m.CallbackURLs = []string{cb}
	default:
		return AppManifest{}, fmt.Errorf("unknown App kind %q", kind)
	}
	return m, nil
}

// linkCallback is web's /link/github/callback. The web hostname is https: its cookies are Secure.
func linkCallback(web string) (string, error) {
	u, err := url.Parse(web)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("the factory App needs the web URL, an https URL with no query, for its OAuth callback")
	}
	return strings.TrimRight(u.String(), "/") + "/link/github/callback", nil
}
