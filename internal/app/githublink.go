// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Smana/maku/internal/config"
	"github.com/Smana/maku/internal/ghidentity"
	"github.com/Smana/maku/internal/github"
	"github.com/Smana/maku/internal/httpx"
	"github.com/Smana/maku/internal/humanapi"
	"github.com/Smana/maku/internal/store"
)

// githubLinkTimeout bounds one call of the link's OAuth flow to GitHub.
const githubLinkTimeout = 15 * time.Second

// secretFile reads a mounted secret at use, trimmed: a rotated Secret needs no restart. A missing
// or empty file is an error that names the path, never the content.
func secretFile(path string) func() (string, error) {
	return func() (string, error) {
		raw, err := os.ReadFile(filepath.Clean(path))
		if err != nil {
			return "", fmt.Errorf("%s is unreadable", path)
		}
		v := strings.TrimSpace(string(raw))
		if v == "" {
			return "", fmt.Errorf("%s is empty", path)
		}
		return v, nil
	}
}

// githubLinker is human.githubLink's flow over the store: GitHub's site at webURL, its API where the
// factory App's is (ROOMS_GITHUB_API_URL, else api.github.com), and the client's two files. A link
// written or removed drops the resolver's cached answers for its sub.
func githubLinker(l *config.GitHubLinkConfig, st humanapi.GitHubLinkStore, id *ghidentity.Resolver) (*humanapi.GitHubLinker, error) {
	u, err := url.Parse(l.WebURL)
	if err != nil || u.Host == "" {
		return nil, errors.New("human.githubLink.webURL is not a URL")
	}
	gl := &humanapi.GitHubLinker{Store: st, WebOrigin: u.Scheme + "://" + u.Host,
		OAuth:    github.OAuth{WebURL: l.WebURL, APIURL: githubAPIURL(), HC: httpx.New(githubLinkTimeout, nil)},
		ClientID: secretFile(l.ClientIDFile), ClientSecret: secretFile(l.ClientSecretFile)}
	if id != nil {
		gl.Changed = id.Forget
	}
	return gl, nil
}

// runGitHubUnlink is `maku-broker github-unlink --sub <sub>`, the break-glass unlink, as rooms_broker
// (ROOMS_DATABASE_URL): the link goes and the act is kept, by actor break-glass.
func runGitHubUnlink(ctx context.Context, log *slog.Logger, args []string, getenv func(string) string) error {
	fs := flag.NewFlagSet("github-unlink", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	sub := fs.String("sub", "", "the account whose GitHub link goes")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("room-broker github-unlink: %w", err)
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("room-broker github-unlink: unexpected argument %q", fs.Arg(0))
	}
	if *sub == "" || len(*sub) > 128 {
		return errors.New("room-broker github-unlink: --sub is required, at most 128 bytes")
	}
	dsn := getenv("ROOMS_DATABASE_URL")
	if dsn == "" {
		return errors.New("room-broker github-unlink: ROOMS_DATABASE_URL is not set")
	}
	st, err := store.Open(ctx, dsn)
	if err != nil {
		return fmt.Errorf("room-broker github-unlink: %w", err)
	}
	defer st.Close()
	gone, err := st.UnlinkGitHub(ctx, *sub, "break-glass", true, time.Now())
	if err != nil {
		return fmt.Errorf("room-broker github-unlink: %w", err)
	}
	log.Info("github link", "sub", *sub, "unlinked", gone)
	return nil
}
