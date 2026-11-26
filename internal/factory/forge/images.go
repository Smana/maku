// SPDX-License-Identifier: Apache-2.0

package forge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/net/html"
)

// IssueImage is one image of an issue's body, fetched from GitHub's own image hosts (v5 Q16 B3).
type IssueImage struct {
	// N is the image's place among the body's GitHub-hosted images, from 1: a skipped image leaves
	// its number unused, so the others keep theirs.
	N     int
	Alt   string
	Bytes []byte
}

// imageRef is an <img> of an issue's rendered body that GitHub hosts.
type imageRef struct{ url, alt string }

// MaxIssueImageBytes bounds one fetched issue image.
const MaxIssueImageBytes = 5 << 20

// imageHost reports whether u is an image GitHub hosts for an issue: its attachment hosts, or an
// attachment's address on github.com. https only; nothing else is ever fetched (no SSRF through an
// issue's markdown).
func imageHost(u *url.URL) bool {
	if u.Scheme != "https" || u.User != nil || u.Port() != "" {
		return false
	}
	switch u.Hostname() {
	case "private-user-images.githubusercontent.com", "user-images.githubusercontent.com":
		return true
	case "github.com":
		return strings.HasPrefix(u.EscapedPath(), "/user-attachments/assets/")
	}
	return false
}

// imageRefs are the first limit <img> of GitHub's rendered body html that GitHub hosts, in order.
func imageRefs(body string, limit int) []imageRef {
	var out []imageRef
	z := html.NewTokenizer(strings.NewReader(body))
	for len(out) < limit {
		switch z.Next() {
		case html.ErrorToken:
			return out
		case html.StartTagToken, html.SelfClosingTagToken:
			t := z.Token()
			if t.Data != "img" {
				continue
			}
			var ref imageRef
			for _, a := range t.Attr {
				switch a.Key {
				case "src":
					ref.url = a.Val
				case "alt":
					ref.alt = a.Val
				}
			}
			if u, err := url.Parse(ref.url); err == nil && imageHost(u) {
				out = append(out, ref)
			}
		}
	}
	return out
}

var errImageHost = errors.New("forge: an issue image redirected off GitHub's image hosts")

// imageClient is base following redirects among GitHub's image hosts only, at most three.
func imageClient(base *http.Client) *http.Client {
	c := *base
	c.Jar = nil
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 3 || !imageHost(req.URL) {
			return errImageHost
		}
		return nil
	}
	return &c
}

// fetchImage GETs one issue image through hc, refusing anything over limit bytes.
func fetchImage(ctx context.Context, hc *http.Client, raw string, limit int) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, fmt.Errorf("forge: issue image: %w", err)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("forge: fetch an issue image: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("forge: fetch an issue image: %s", resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, int64(limit)+1))
	if err != nil {
		return nil, fmt.Errorf("forge: read an issue image: %w", err)
	}
	if len(b) > limit {
		return nil, fmt.Errorf("forge: an issue image is over %d bytes", limit)
	}
	return b, nil
}

// IssueImages is issue number's snapshot sha256 (SnapshotSHA256, to compare with the task's) and
// its first limit images that GitHub hosts, fetched now: the rendered body's image addresses are
// signed and short-lived, so they are read and fetched together. An image that fails to fetch is
// skipped; the rest are returned.
func (g *GitHub) IssueImages(ctx context.Context, number, limit int) (string, []IssueImage, error) {
	var q struct {
		Repository struct {
			Issue struct {
				Title    string
				Body     string
				BodyHTML string `graphql:"bodyHTML"`
			} `graphql:"issue(number: $number)"`
		} `graphql:"repository(owner: $owner, name: $name)"`
	}
	vars, err := g.vars(number)
	if err != nil {
		return "", nil, err
	}
	err = g.v4.Query(ctx, &q, vars)
	g.mark(err)
	if err != nil {
		return "", nil, wrap("read an issue's images", err)
	}
	i := q.Repository.Issue
	sum := SnapshotSHA256(Issue{Title: i.Title, Body: i.Body})
	hc := imageClient(g.plain)
	var out []IssueImage
	for k, ref := range imageRefs(i.BodyHTML, limit) {
		b, err := fetchImage(ctx, hc, ref.url, MaxIssueImageBytes)
		if err != nil {
			continue
		}
		out = append(out, IssueImage{N: k + 1, Alt: ref.alt, Bytes: b})
	}
	return sum, out, nil
}
