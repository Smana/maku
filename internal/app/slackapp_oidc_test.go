// SPDX-License-Identifier: Apache-2.0

package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Smana/maku/internal/slackapp"
)

// tokenIssuer serves a discovery document and a client-credentials token endpoint, recording each
// token request's path and form.
func tokenIssuer(t *testing.T) (*httptest.Server, *[]string) {
	t.Helper()
	var mu sync.Mutex
	var got []string
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]string{"issuer": srv.URL, "jwks_uri": srv.URL + "/keys", "token_endpoint": srv.URL + "/v1/token"})
		default:
			_ = r.ParseForm()
			mu.Lock()
			got = append(got, r.URL.Path+" "+r.Form.Encode())
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "jwt", "token_type": "Bearer", "expires_in": 300})
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &got
}

func secretFiles(t *testing.T, vals map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, v := range vals {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(v+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// v4 D8: today's config mints as today, at ZITADEL's fixed path with the project scope; the generic
// preset posts to the discovered token_endpoint with its audience and scopes.
func TestSlackTokenRequest(t *testing.T) {
	srv, got := tokenIssuer(t)
	dir := secretFiles(t, map[string]string{"cid": "slack", "csec": "s3cret", "pid": "29184"})
	zitadel := slackapp.Config{IssuerURL: srv.URL, ClientIDFile: dir + "/cid", ClientSecretFile: dir + "/csec", ProjectIDFile: dir + "/pid"}
	generic := slackapp.Config{IssuerURL: srv.URL, ClientIDFile: dir + "/cid", ClientSecretFile: dir + "/csec",
		Preset: "generic", Audience: "maku-api", Scopes: []string{}}
	for name, c := range map[string]struct {
		cfg  slackapp.Config
		want []string
		not  []string
	}{
		"zitadel": {zitadel, []string{"/oauth/v2/token ", "urn%3Azitadel%3Aiam%3Aorg%3Aproject%3Aid%3A29184%3Aaud"}, []string{"audience="}},
		"generic": {generic, []string{"/v1/token ", "audience=maku-api", "grant_type=client_credentials"}, []string{"zitadel", "scope="}},
		"generic, a set tokenURL": {func() slackapp.Config { g := generic; g.TokenURL = srv.URL + "/custom"; return g }(),
			[]string{"/custom "}, nil},
	} {
		t.Run(name, func(t *testing.T) {
			*got = nil
			if _, err := slackToken(t.Context(), srv.Client(), c.cfg); err != nil {
				t.Fatal(err)
			}
			req := strings.Join(*got, "\n")
			for _, w := range c.want {
				if !strings.Contains(req, w) {
					t.Errorf("no %q in %s", w, req)
				}
			}
			for _, n := range c.not {
				if strings.Contains(req, n) {
					t.Errorf("%q in %s", n, req)
				}
			}
		})
	}
}

// The preset's keys load strictly: generic takes no projectIDFile, naming audience, and zitadel
// takes no audience.
func TestSlackPresetConfig(t *testing.T) {
	for name, c := range map[string]struct {
		extra, drop, want string
	}{
		"generic with projectIDFile": {"preset: generic\naudience: maku-api\n", "", "projectIDFile: the zitadel preset's; set audience"},
		"zitadel with an audience":   {"audience: maku-api\n", "", "audience: the zitadel preset's audience is projectIDFile"},
		"an unknown preset":          {"preset: okta\n", "", `preset "okta" is not zitadel or generic`},
		"an http tokenURL":           {"preset: generic\ntokenURL: http://idp.example/token\n", "projectIDFile", "tokenURL"},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := slackFiles(t, slackChannels+c.extra)
			if c.drop != "" {
				raw, _ := os.ReadFile(cfg)
				var kept []string
				for _, l := range strings.Split(string(raw), "\n") {
					if !strings.HasPrefix(l, c.drop+":") {
						kept = append(kept, l)
					}
				}
				_ = os.WriteFile(cfg, []byte(strings.Join(kept, "\n")), 0o600)
			}
			_, err := slackapp.LoadConfig(cfg)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("got %v, want %q", err, c.want)
			}
		})
	}
	// generic without projectIDFile starts: the file is the zitadel preset's alone.
	cfg := slackFiles(t, slackChannels+"preset: generic\naudience: maku-api\n")
	raw, _ := os.ReadFile(cfg)
	_ = os.WriteFile(cfg, []byte(strings.Replace(string(raw), "projectIDFile: "+filepath.Dir(cfg)+"/pid\n", "", 1)), 0o600)
	if _, err := slackapp.LoadConfig(cfg); err != nil {
		t.Fatalf("generic without projectIDFile: %v", err)
	}
	if err := slackRequired(mustLoad(t, cfg)); err != nil {
		t.Fatalf("generic needs no projectIDFile: %v", err)
	}
}

func mustLoad(t *testing.T, path string) slackapp.Config {
	t.Helper()
	c, err := slackapp.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	return c
}
