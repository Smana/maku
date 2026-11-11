// SPDX-License-Identifier: Apache-2.0

// Command fakegh implements only the gh issue forms used by the spec-intake k3d scenario.
package main

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Smana/maku/internal/httpx"
)

const (
	maxBody         = 1 << 20
	maxResponse     = 1 << 20
	maxCommentPages = 10000
)

var repositoryPattern = regexp.MustCompile(`^[A-Za-z0-9-]+/[A-Za-z0-9._-]+$`)

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdin, os.Stdout, os.Stderr, os.Getenv); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

type stubAPI struct {
	base   string
	client *http.Client
}

func stubBaseURL(getenv func(string) string) (string, error) {
	raw := strings.TrimSpace(getenv("E2E_STUB_URL"))
	if raw == "" {
		return "", errors.New("E2E_STUB_URL is required; fakegh has no GitHub fallback")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("invalid E2E_STUB_URL %q", raw)
	}
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if host == "github.com" || strings.HasSuffix(host, ".github.com") || host == "githubusercontent.com" || strings.HasSuffix(host, ".githubusercontent.com") {
		return "", errors.New("fakegh refuses a GitHub endpoint")
	}
	loopback := host == "localhost" || host == "127.0.0.1" || host == "::1"
	if parsed.Scheme != "https" && (parsed.Scheme != "http" || !loopback) {
		return "", errors.New("E2E_STUB_URL must use HTTPS (HTTP is allowed only on loopback)")
	}
	return strings.TrimRight(raw, "/"), nil
}

func newStubAPI(getenv func(string) string) (*stubAPI, error) {
	base, err := stubBaseURL(getenv)
	if err != nil {
		return nil, err
	}
	var roots *x509.CertPool
	if path := getenv("E2E_STUB_CA_FILE"); path != "" {
		raw, err := os.ReadFile(path) //nolint:gosec // scenario-selected throwaway CA
		if err != nil {
			return nil, fmt.Errorf("read E2E_STUB_CA_FILE: %w", err)
		}
		if len(raw) > maxResponse {
			return nil, errors.New("E2E_STUB_CA_FILE is too large")
		}
		roots = x509.NewCertPool()
		if !roots.AppendCertsFromPEM(raw) {
			return nil, errors.New("E2E_STUB_CA_FILE contains no certificate")
		}
	}
	return &stubAPI{base: base, client: httpx.New(30*time.Second, roots)}, nil
}

type stubResponse struct {
	body json.RawMessage
}

func (s *stubAPI) do(ctx context.Context, method, path string, input any, want int) (stubResponse, error) {
	var body io.Reader
	if input != nil {
		raw, err := json.Marshal(input)
		if err != nil {
			return stubResponse{}, err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, s.base+path, body) //nolint:gosec // base is the validated, explicit stub URL, never a gh/GitHub fallback
	if err != nil {
		return stubResponse{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req) //nolint:gosec // req targets only the validated stub; httpx also disables proxies and metadata egress
	if err != nil {
		return stubResponse{}, fmt.Errorf("stub %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := httpx.ReadBody(resp.Body, maxResponse)
	if err != nil {
		return stubResponse{}, err
	}
	if resp.StatusCode != want {
		return stubResponse{}, fmt.Errorf("stub %s %s: HTTP %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return stubResponse{body: raw}, nil
}

func readBody(stdin io.Reader) (string, error) {
	raw, err := io.ReadAll(io.LimitReader(stdin, maxBody+1))
	if err != nil {
		return "", err
	}
	if len(raw) > maxBody {
		return "", errors.New("issue body is too large")
	}
	return string(raw), nil
}

func repoAndNumber(repoFlag, number string) (string, int, error) {
	if !repositoryPattern.MatchString(repoFlag) {
		return "", 0, errors.New("unsupported repository")
	}
	n, err := strconv.Atoi(number)
	if err != nil || n < 1 {
		return "", 0, errors.New("issue number must be positive")
	}
	return repoFlag, n, nil
}

func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	_ = stderr // command diagnostics are returned to main, which owns stderr
	if len(args) < 2 || args[0] != "issue" {
		return errors.New("unsupported gh form; only issue create/view/edit are implemented")
	}
	switch args[1] {
	case "create":
		if len(args) != 5 || !strings.HasPrefix(args[2], "--repo=") || !strings.HasPrefix(args[3], "--title=") || args[4] != "--body-file=-" {
			return errors.New("unsupported gh issue create form")
		}
		repo := strings.TrimPrefix(args[2], "--repo=")
		if !repositoryPattern.MatchString(repo) || strings.TrimPrefix(args[3], "--title=") == "" {
			return errors.New("unsupported repository or empty title")
		}
		api, err := newStubAPI(getenv)
		if err != nil {
			return err
		}
		body, err := readBody(stdin)
		if err != nil {
			return err
		}
		response, err := api.do(ctx, http.MethodPost, "/repos/"+repo+"/issues", map[string]any{
			"title": strings.TrimPrefix(args[3], "--title="), "body": body, "author": "dev",
		}, http.StatusCreated)
		if err != nil {
			return err
		}
		var issue struct {
			Number int `json:"number"`
		}
		if err := json.Unmarshal(response.body, &issue); err != nil || issue.Number < 1 {
			return fmt.Errorf("decode created issue: %w", err)
		}
		_, err = fmt.Fprintf(stdout, "https://github.com/%s/issues/%d\n", repo, issue.Number)
		return err
	case "view":
		if len(args) != 6 || !strings.HasPrefix(args[3], "--repo=") || args[4] != "--json" || args[5] != "body,labels,updatedAt,comments" {
			return errors.New("unsupported gh issue view form")
		}
		repo, number, err := repoAndNumber(strings.TrimPrefix(args[3], "--repo="), args[2])
		if err != nil {
			return err
		}
		api, err := newStubAPI(getenv)
		if err != nil {
			return err
		}
		issue, err := api.view(ctx, repo, number)
		if err != nil {
			return err
		}
		encoder := json.NewEncoder(stdout)
		encoder.SetEscapeHTML(false)
		return encoder.Encode(issue)
	case "edit":
		if len(args) != 5 || !strings.HasPrefix(args[3], "--repo=") {
			return errors.New("unsupported gh issue edit form")
		}
		repo, number, err := repoAndNumber(strings.TrimPrefix(args[3], "--repo="), args[2])
		if err != nil {
			return err
		}
		api, err := newStubAPI(getenv)
		if err != nil {
			return err
		}
		switch {
		case args[4] == "--body-file=-":
			body, err := readBody(stdin)
			if err != nil {
				return err
			}
			_, err = api.do(ctx, http.MethodPatch, fmt.Sprintf("/repos/%s/issues/%d", repo, number), map[string]string{"body": body}, http.StatusOK)
			return err
		case strings.HasPrefix(args[4], "--add-label=") && len(strings.TrimPrefix(args[4], "--add-label=")) > 0:
			label := strings.TrimPrefix(args[4], "--add-label=")
			_, err = api.do(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/issues/%d/labels", repo, number), map[string]any{
				"labels": []string{label}, "actor": "dev",
			}, http.StatusOK)
			return err
		default:
			return errors.New("unsupported gh issue edit form")
		}
	default:
		return errors.New("unsupported gh issue command")
	}
}

type gitHubLabel struct {
	Name string `json:"name"`
}

type gitHubComment struct {
	ID     int64 `json:"id"`
	Author struct {
		Login string `json:"login"`
	} `json:"author"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"createdAt"`
}

type gitHubIssue struct {
	Body      string          `json:"body"`
	Labels    []gitHubLabel   `json:"labels"`
	UpdatedAt time.Time       `json:"updatedAt"`
	Comments  []gitHubComment `json:"comments"`
}

func (s *stubAPI) view(ctx context.Context, repo string, number int) (gitHubIssue, error) {
	response, err := s.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/issues/%d", repo, number), nil, http.StatusOK)
	if err != nil {
		return gitHubIssue{}, err
	}
	var issue gitHubIssue
	var raw struct {
		Body      string        `json:"body"`
		Labels    []gitHubLabel `json:"labels"`
		UpdatedAt time.Time     `json:"updated_at"`
	}
	if err := json.Unmarshal(response.body, &raw); err != nil {
		return issue, err
	}
	issue.Body, issue.Labels, issue.UpdatedAt = raw.Body, raw.Labels, raw.UpdatedAt
	issue.Comments = make([]gitHubComment, 0)
	for page := 1; page <= maxCommentPages; page++ {
		path := fmt.Sprintf("/repos/%s/issues/%d/comments?per_page=100&page=%d", repo, number, page)
		comments, err := s.do(ctx, http.MethodGet, path, nil, http.StatusOK)
		if err != nil {
			return issue, err
		}
		var batch []struct {
			ID     int64 `json:"id"`
			Author struct {
				Login string `json:"login"`
			} `json:"user"`
			Body      string    `json:"body"`
			CreatedAt time.Time `json:"created_at"`
		}
		if err := json.Unmarshal(comments.body, &batch); err != nil {
			return issue, err
		}
		for _, comment := range batch {
			out := gitHubComment{ID: comment.ID, Body: comment.Body, CreatedAt: comment.CreatedAt}
			out.Author.Login = comment.Author.Login
			issue.Comments = append(issue.Comments, out)
		}
		if len(batch) < 100 {
			return issue, nil
		}
	}
	return issue, errors.New("too many issue comment pages")
}
