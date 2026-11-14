// SPDX-License-Identifier: Apache-2.0

package install

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
)

// Registry is where the images' digests are read when the answers give none: ghcr.io, anonymously.
type Registry struct {
	HC   *http.Client
	Base string // empty is https://ghcr.io
}

var digestRE = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// Digest is the manifest digest of ghcr.io/smana/<image>:<tag>, as <tag>@<digest>.
func (r Registry) Digest(ctx context.Context, image, tag string) (string, error) {
	base := r.Base
	if base == "" {
		base = "https://ghcr.io"
	}
	repo := "smana/" + image
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/token?scope=repository:"+repo+":pull", nil)
	if err != nil {
		return "", err
	}
	resp, err := r.HC.Do(req)
	if err != nil {
		return "", fmt.Errorf("%s: the registry's token: %w", image, err)
	}
	var tok struct {
		Token string `json:"token"`
	}
	err = json.NewDecoder(http.MaxBytesReader(nil, resp.Body, 1<<16)).Decode(&tok)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || err != nil || tok.Token == "" {
		return "", fmt.Errorf("%s: the registry's token: HTTP %d", image, resp.StatusCode)
	}
	req, err = http.NewRequestWithContext(ctx, http.MethodHead, base+"/v2/"+repo+"/manifests/"+tag, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+tok.Token)
	req.Header.Set("Accept", "application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.oci.image.manifest.v1+json")
	resp, err = r.HC.Do(req)
	if err != nil {
		return "", fmt.Errorf("%s:%s: %w", image, tag, err)
	}
	_ = resp.Body.Close()
	d := resp.Header.Get("Docker-Content-Digest")
	if resp.StatusCode != http.StatusOK || !digestRE.MatchString(d) {
		return "", fmt.Errorf("%s:%s: not found on the registry (HTTP %d): answer images.pins", image, tag, resp.StatusCode)
	}
	return tag + "@" + d, nil
}

// ResolvePins fills the digests the answers do not give, for the images this install runs.
func ResolvePins(ctx context.Context, a Answers, digest func(ctx context.Context, image, tag string) (string, error)) (map[string]string, error) {
	pins := Pins(a)
	for _, img := range Images {
		if pins[img] != "" || (img == "maku-slack" && !a.Bool("slack.enabled")) {
			continue
		}
		d, err := digest(ctx, img, a.String("version"))
		if err != nil {
			return nil, err
		}
		pins[img] = d
	}
	return pins, nil
}
