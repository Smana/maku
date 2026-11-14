// SPDX-License-Identifier: Apache-2.0

package main

import (
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// slackPosts are the chat.postMessage calls the stub received as Slack's Web API (maku-slack's
// apiBaseURL, plan v4 step 6 Task 16's follow-up), in order.
type slackPosts struct {
	mu    sync.Mutex
	posts []map[string]string
	// grants are the clients of the client-credentials grants with an audience (a sponsored
	// principal's sign-in, the Slack App's), in order.
	grants []string
}

// slackPostMessage answers as Slack's chat.postMessage: ok with a ts for a bearer, not_authed without.
func (s *Stub) slackPostMessage(w http.ResponseWriter, r *http.Request) {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	var body struct {
		Channel  string `json:"channel"`
		ThreadTS string `json:"thread_ts"`
		Text     string `json:"text"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	if !ok || token == "" {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "not_authed"})
		return
	}
	s.slack.mu.Lock()
	s.slack.posts = append(s.slack.posts, map[string]string{"channel": body.Channel, "threadTS": body.ThreadTS, "text": body.Text, "token": token})
	n := len(s.slack.posts)
	s.slack.mu.Unlock()
	ts := strconv.FormatInt(time.Now().Unix(), 10) + "." + strconv.Itoa(n)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "channel": body.Channel, "ts": ts})
}

func (s *Stub) slackReceived(w http.ResponseWriter, _ *http.Request) {
	s.slack.mu.Lock()
	defer s.slack.mu.Unlock()
	out := append([]map[string]string{}, s.slack.posts...)
	writeJSON(w, http.StatusOK, out)
}

// clientGrants is the client of each client-credentials grant the token endpoint answered.
func (s *Stub) clientGrants(w http.ResponseWriter, _ *http.Request) {
	s.slack.mu.Lock()
	defer s.slack.mu.Unlock()
	writeJSON(w, http.StatusOK, append([]string{}, s.slack.grants...))
}
