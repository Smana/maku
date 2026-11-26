// SPDX-License-Identifier: Apache-2.0

package envelope

import (
	"net"
	"net/url"
)

// MaxPageURL bounds a screenshot's page URL, as the store's column does.
const MaxPageURL = 2048

// ScreenshotPayload is a screenshot event's payload (harness screenshots phase B): the meta of an
// image the broker stored for the room, by digest. It never carries the image: Bytes is its size.
// The broker writes it from what it decoded and stored, never from what the bridge sent, except n,
// the page URL (through PageURL) and vision.
type ScreenshotPayload struct {
	Run    string `json:"run"`
	N      int    `json:"n"`
	Digest string `json:"digest"`
	Mime   string `json:"mime"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	Bytes  int    `json:"bytes"`
	// Source is "browser" for a run's screenshot, "request" for an issue's image.
	Source string `json:"source"`
	// PageURL is the loopback page shown, "" when unknown or not loopback.
	PageURL string `json:"pageUrl,omitempty"`
	// Vision is whether the run's model could see the image (C15).
	Vision bool `json:"vision"`
}

// PageURL returns raw as http://<loopback host>[:port]/<path> when it is an http URL of a loopback
// host (127.0.0.0/8, ::1, localhost) of at most MaxPageURL bytes, and "" otherwise. Its user info,
// query and fragment are dropped: they can carry a token the page was opened with.
func PageURL(raw string) string {
	if len(raw) > MaxPageURL {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.Opaque != "" {
		return ""
	}
	host := u.Hostname()
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return ""
	}
	clean := url.URL{Scheme: "http", Host: u.Host, Path: u.Path, RawPath: u.RawPath}
	return clean.String()
}
