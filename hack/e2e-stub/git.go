// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const (
	maxGitResponse = 64 << 20
	maxGitError    = 4 << 10
	gitTimeout     = 30 * time.Second
)

var errGitOutputTooLarge = errors.New("git http-backend output exceeds the bound")

// boundedBuffer retains at most limit bytes and fails rather than allowing an unbounded backend reply.
type boundedBuffer struct {
	b     bytes.Buffer
	limit int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if b.b.Len()+len(p) > b.limit {
		return 0, errGitOutputTooLarge
	}
	return b.b.Write(p)
}

// gitHTTP serves the test repository through git's own http-backend, with no CGI package or shell.
func gitHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), gitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/usr/bin/git", "http-backend") //nolint:gosec // fixed binary and arguments, no shell or user-controlled executable
	cmd.Env = append(os.Environ(),
		"GIT_PROJECT_ROOT="+gitProjectRoot,
		"GIT_HTTP_EXPORT_ALL=1",
		"GATEWAY_INTERFACE=CGI/1.1",
		"SERVER_PROTOCOL=HTTP/1.1",
		"SERVER_SOFTWARE=e2e-stub",
		"PATH_INFO="+r.URL.Path,
		"SCRIPT_NAME=/owner/repo.git",
		"REQUEST_METHOD="+r.Method,
		"REQUEST_URI="+r.URL.RequestURI(),
		"QUERY_STRING="+r.URL.RawQuery,
		"CONTENT_TYPE="+r.Header.Get("Content-Type"),
		"CONTENT_LENGTH="+strconv.FormatInt(r.ContentLength, 10),
		"REMOTE_ADDR="+r.RemoteAddr,
	)
	cmd.Stdin = r.Body
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		http.Error(w, "git http-backend stdout", http.StatusInternalServerError)
		return
	}
	stderr := &boundedBuffer{limit: maxGitError}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		http.Error(w, "git http-backend start", http.StatusInternalServerError)
		return
	}
	out := &boundedBuffer{limit: maxGitResponse}
	_, copyErr := io.Copy(out, stdout)
	waitErr := cmd.Wait()
	if copyErr != nil || waitErr != nil {
		http.Error(w, "git http-backend failed: "+stderr.b.String(), http.StatusInternalServerError)
		return
	}
	header, body, ok := strings.Cut(out.b.String(), "\r\n\r\n")
	if !ok {
		header, body, ok = strings.Cut(out.b.String(), "\n\n")
	}
	if !ok {
		http.Error(w, "git http-backend returned no CGI header", http.StatusInternalServerError)
		return
	}
	status := http.StatusOK
	for _, line := range strings.Split(header, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Status: ") {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				if n, err := strconv.Atoi(parts[1]); err == nil {
					status = n
				}
			}
		}
		if strings.HasPrefix(strings.ToLower(line), "content-type:") {
			if _, value, ok := strings.Cut(line, ":"); ok {
				w.Header().Set("Content-Type", strings.TrimSpace(value))
			}
		}
	}
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}
