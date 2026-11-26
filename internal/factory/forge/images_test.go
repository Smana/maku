// SPDX-License-Identifier: Apache-2.0

package forge

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestImageRefs(t *testing.T) {
	html := `<p>see</p>
<p><a href="https://private-user-images.githubusercontent.com/1/a.png?jwt=x"><img src="https://private-user-images.githubusercontent.com/1/a.png?jwt=x" alt="the bug" style="max-width:100%"></a></p>
<img src="https://evil.example/x.png" alt="external">
<img src="data:image/png;base64,AAAA" alt="inline">
<img src="https://github.com/user-attachments/assets/0b5f8e2c-1111-2222-3333-444455556666" alt="two">
<img src="https://github.com/Smana/maku/raw/main/x.png" alt="a repo file">
<img src="https://user-images.githubusercontent.com/5/b.jpg" alt="legacy">
<img src="https://private-user-images.githubusercontent.com.evil.example/z.png" alt="look-alike">
<img src="http://user-images.githubusercontent.com/5/c.jpg" alt="plain http">
<img src="https://user-images.githubusercontent.com/5/d.jpg" alt="fifth">`
	got := imageRefs(html, 4)
	want := []imageRef{
		{url: "https://private-user-images.githubusercontent.com/1/a.png?jwt=x", alt: "the bug"},
		{url: "https://github.com/user-attachments/assets/0b5f8e2c-1111-2222-3333-444455556666", alt: "two"},
		{url: "https://user-images.githubusercontent.com/5/b.jpg", alt: "legacy"},
		{url: "https://user-images.githubusercontent.com/5/d.jpg", alt: "fifth"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%d: got %+v, want %+v", i, got[i], want[i])
		}
	}
	if n := len(imageRefs(html, 2)); n != 2 {
		t.Errorf("max 2: %d", n)
	}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestFetchImageFollowsGitHubHostsOnly(t *testing.T) {
	hc := imageClient(&http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		h := http.Header{}
		switch r.URL.Host {
		case "github.com":
			if strings.Contains(r.URL.Path, "evil") {
				h.Set("Location", "https://evil.example/x.png")
			} else {
				h.Set("Location", "https://private-user-images.githubusercontent.com/1/a.png?jwt=y")
			}
			return &http.Response{StatusCode: http.StatusFound, Header: h, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
		case "private-user-images.githubusercontent.com":
			if r.Header.Get("Authorization") != "" {
				t.Error("an image host never gets a token")
			}
			return &http.Response{StatusCode: http.StatusOK, Header: h, Body: io.NopCloser(bytes.NewReader(bytes.Repeat([]byte{1}, 100))), Request: r}, nil
		}
		t.Errorf("dialled %s", r.URL)
		return &http.Response{StatusCode: http.StatusOK, Header: h, Body: io.NopCloser(strings.NewReader("x")), Request: r}, nil
	})})
	b, err := fetchImage(t.Context(), hc, "https://github.com/user-attachments/assets/ok", 1<<10)
	if err != nil || len(b) != 100 {
		t.Fatalf("%d bytes, %v", len(b), err)
	}
	if _, err := fetchImage(t.Context(), hc, "https://github.com/user-attachments/assets/evil", 1<<10); err == nil {
		t.Fatal("a redirect off GitHub's image hosts is refused")
	}
	if _, err := fetchImage(t.Context(), hc, "https://github.com/user-attachments/assets/ok", 50); err == nil {
		t.Fatal("an image over the limit is refused")
	}
}
