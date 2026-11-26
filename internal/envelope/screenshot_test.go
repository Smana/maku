// SPDX-License-Identifier: Apache-2.0

package envelope

import (
	"encoding/json"
	"strings"
	"testing"
)

// A screenshot event is one of the C4 types, and its payload names the image, never holds it.
func TestScreenshotPayload(t *testing.T) {
	if !Screenshot.Valid() {
		t.Fatal("screenshot is not a valid event type")
	}
	b := Must(ScreenshotPayload{Run: "7f3cq2xz", N: 1, Digest: strings.Repeat("a", 64), Mime: "image/png",
		Width: 1280, Height: 800, Bytes: 123, Source: "browser", PageURL: "http://127.0.0.1:8080/", Vision: true})
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(b, &keys); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"run", "n", "digest", "mime", "width", "height", "bytes", "source", "pageUrl", "vision"} {
		if _, ok := keys[k]; !ok {
			t.Errorf("key %q missing from %s", k, b)
		}
	}
}

// Only a loopback http page is kept, and only its origin and path.
func TestPageURL(t *testing.T) {
	for in, want := range map[string]string{
		"http://127.0.0.1:8080/":                 "http://127.0.0.1:8080/",
		"http://localhost:3000/a/b?token=x#frag": "http://localhost:3000/a/b",
		"http://[::1]:9000/x":                    "http://[::1]:9000/x",
		"http://127.0.0.2/":                      "http://127.0.0.2/",
		"http://user:pass@127.0.0.1/":            "http://127.0.0.1/",
		"https://127.0.0.1/":                     "",
		"http://example.com/":                    "",
		"http://10.0.0.1/":                       "",
		"file:///etc/passwd":                     "",
		"data:text/html,hi":                      "",
		"javascript:alert(1)":                    "",
		"":                                       "",
		"http://127.0.0.1/" + strings.Repeat("a", MaxPageURL): "",
	} {
		if got := PageURL(in); got != want {
			t.Errorf("PageURL(%q) = %q, want %q", in, got, want)
		}
	}
}
