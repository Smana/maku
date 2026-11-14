// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const factoryAuthor = "maku-demo[bot]"

// issueSnapshotQuery is the serialization of the Issue query struct in internal/factory/forge/github.go.
const issueSnapshotQuery = `query($name:String!$number:Int!$owner:String!){repository(owner: $owner, name: $name){issue(number: $number){number,url,title,body,state,author{__typename,login},authorAssociation,lastEditedAt,labels(first: 50){nodes{name}},timelineItems(itemTypes: [RENAMED_TITLE_EVENT], last: 1){nodes{... on RenamedTitleEvent{createdAt}}}}}}`

type restIssue struct {
	ID        int64     `json:"id"`
	Number    int       `json:"number"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	State     string    `json:"state"`
	HTMLURL   string    `json:"html_url"`
	UpdatedAt time.Time `json:"updated_at"`
	User      struct {
		Login string `json:"login"`
	} `json:"user"`
	Labels []struct {
		Name string `json:"name"`
	} `json:"labels"`
}

type restComment struct {
	ID        int64     `json:"id"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	User      struct {
		Login string `json:"login"`
	} `json:"user"`
}

func createIssue(t *testing.T, ts *httptest.Server, body string) int {
	t.Helper()
	status, raw := request(t, ts, http.MethodPost, "/repos/owner/repo/issues",
		[]byte(`{"title":"docs: verification","body":`+strconvQuote(body)+`,"labels":["needs/spec"],"author":"dev"}`))
	if status != http.StatusCreated {
		t.Fatalf("create issue status = %d, body %s", status, raw)
	}
	issue := decodeResponse[restIssue](t, raw)
	if issue.Number < 1 || issue.ID == 0 || issue.Body != body || issue.User.Login != "dev" || issue.State != "open" {
		t.Fatalf("created issue = %+v", issue)
	}
	return issue.Number
}

func strconvQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func graphQLIssue(t *testing.T, ts *httptest.Server, number int) (int, []byte) {
	t.Helper()
	body := []byte(fmt.Sprintf(`{"query":%s,"variables":{"owner":"owner","name":"repo","number":%d}}`,
		strconvQuote(issueSnapshotQuery), number))
	return request(t, ts, http.MethodPost, "/graphql", body)
}

func TestAddLabelsAcceptsGitHubClientArray(t *testing.T) {
	_, ts := newStubServer(t)
	number := createIssue(t, ts, "body")
	status, raw := request(t, ts, http.MethodPost, fmt.Sprintf("/repos/owner/repo/issues/%d/labels", number),
		[]byte(`["factory/needs-author"]`))
	if status != http.StatusOK || !strings.Contains(string(raw), `"name":"factory/needs-author"`) {
		t.Fatalf("go-github add labels status = %d, body = %s", status, raw)
	}
}

func TestIssueEventsAllowForgePagination(t *testing.T) {
	_, ts := newStubServer(t)
	number := createIssue(t, ts, "body")
	status, raw := request(t, ts, http.MethodPost, fmt.Sprintf("/repos/owner/repo/issues/%d/labels", number),
		[]byte(`{"labels":["factory/ready"],"actor":"dev"}`))
	if status != http.StatusOK {
		t.Fatalf("add label status = %d, body = %s", status, raw)
	}
	status, raw = request(t, ts, http.MethodGet, fmt.Sprintf("/repos/owner/repo/issues/%d/events?per_page=100&page=1", number), nil)
	if status != http.StatusOK {
		t.Fatalf("forge label events pagination status = %d, body = %s", status, raw)
	}
	var events []struct {
		Event string `json:"event"`
		Label struct {
			Name string `json:"name"`
		} `json:"label"`
	}
	if err := json.Unmarshal(raw, &events); err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[1].Event != "labeled" || events[1].Label.Name != "factory/ready" {
		t.Fatalf("label events = %+v", events)
	}
}

func TestFenceFileNamesItsFactory(t *testing.T) {
	for env, want := range map[string]string{"": "demo", "agent-factory": "agent-factory"} {
		t.Run("factory "+want, func(t *testing.T) {
			t.Setenv("E2E_STUB_FENCE_FACTORY", env)
			fenceFileNames(t, want)
		})
	}
}

func fenceFileNames(t *testing.T, factory string) {
	t.Helper()
	_, ts := newStubServer(t)
	status, raw := request(t, ts, http.MethodGet, "/repos/owner/repo/contents/.github/agent-factory.yaml", nil)
	if status != http.StatusOK {
		t.Fatalf("fence file status = %d, body = %s", status, raw)
	}
	var file struct {
		Type    string `json:"type"`
		SHA     string `json:"sha"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	decoded, err := base64.StdEncoding.DecodeString(file.Content)
	if err != nil {
		t.Fatal(err)
	}
	if file.Type != "file" || file.SHA != fullCommit || string(decoded) != "factory: "+factory+"\n" {
		t.Fatalf("fence file = %s (%s)", raw, decoded)
	}
}

func seedGitRepository(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	bare := filepath.Join(root, "owner", "repo.git")
	if err := os.MkdirAll(bare, 0o755); err != nil {
		t.Fatal(err)
	}
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v: %s", args, err, out)
		}
	}
	run(root, "git", "init", "--bare", bare)
	work := filepath.Join(root, "work")
	if err := os.MkdirAll(filepath.Join(work, ".github"), 0o755); err != nil {
		t.Fatal(err)
	}
	run(work, "git", "init", "-q")
	if err := os.WriteFile(filepath.Join(work, ".github", "agent-factory.yaml"), []byte("factory: demo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("# e2e-stub repository\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run(work, "git", "add", ".")
	run(work, "git", "-c", "user.name=e2e", "-c", "user.email=e2e@example.invalid", "commit", "-q", "-m", "init")
	run(work, "git", "push", "-q", bare, "HEAD:main")
	run(root, "git", "--git-dir="+bare, "symbolic-ref", "HEAD", "refs/heads/main")
	return root
}

func TestGitRepositoryServesMain(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root := seedGitRepository(t)
	oldRoot := gitProjectRoot
	gitProjectRoot = root
	t.Cleanup(func() { gitProjectRoot = oldRoot })
	_, ts := newStubServer(t)
	cmd := exec.CommandContext(t.Context(), "git", "ls-remote", ts.URL+"/owner/repo.git")
	cmd.Env = append(cmd.Env, "GIT_SSL_NO_VERIFY=true", "GIT_PROJECT_ROOT="+root)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git ls-remote: %v: %s", err, out)
	}
	if !strings.Contains(string(out), "refs/heads/main") {
		t.Fatalf("ls-remote did not serve main: %s", out)
	}
}

func TestPullRequestLookupIsEmpty(t *testing.T) {
	_, ts := newStubServer(t)
	for _, task := range []string{"qf2iszow", "26chvm5q"} {
		t.Run(task, func(t *testing.T) {
			status, raw := request(t, ts, http.MethodGet, fmt.Sprintf("/repos/owner/repo/pulls?head=owner%%3Aagent%%2F%s&per_page=10&state=all", task), nil)
			if status != http.StatusOK || strings.TrimSpace(string(raw)) != "[]" {
				t.Fatalf("empty pull request lookup status = %d, body = %s", status, raw)
			}
		})
	}
}

func TestAgentPullsQueryIsEmpty(t *testing.T) {
	_, ts := newStubServer(t)
	query := `query($name:String!$owner:String!){repository(owner: $owner, name: $name){pullRequests(states: [OPEN], first: 100, orderBy: {field: CREATED_AT, direction: DESC}){nodes{number,headRefName,headRepository{owner{login},name},labels(first: 30){nodes{name}}}}}}`
	body := []byte(fmt.Sprintf(`{"query":%s,"variables":{"owner":"owner","name":"repo"}}`, strconvQuote(query)))
	status, raw := request(t, ts, http.MethodPost, "/graphql", body)
	if status != http.StatusOK {
		normalized := normalizeGraphQL(query)
		open := strings.Index(normalized, "{")
		t.Fatalf("agent pulls query status = %d, body = %s\nactual:   %q\nexpected: %q", status, raw, normalized, normalized[:open]+"{"+normalizeGraphQL(agentPullsSelection))
	}
	if !strings.Contains(string(raw), `"nodes":[]`) {
		t.Fatalf("agent pulls response = %s", raw)
	}
}

func TestIssueStoreRealForgeQuery(t *testing.T) {
	_, ts := newStubServer(t)
	number := createIssue(t, ts, "body")
	status, raw := graphQLIssue(t, ts, number)
	if status != http.StatusOK {
		t.Fatalf("real forge query status = %d, body %s", status, raw)
	}
	var response struct {
		Data struct {
			Repository struct {
				Issue struct {
					Number int `json:"number"`
				} `json:"issue"`
			} `json:"repository"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatal(err)
	}
	if response.Data.Repository.Issue.Number != number {
		t.Fatalf("real forge query response = %s", raw)
	}
}

func TestIssueStoreLabelsBodyAndEvents(t *testing.T) {
	_, ts := newStubServer(t)
	number := createIssue(t, ts, "old body")

	status, raw := request(t, ts, http.MethodPost, fmt.Sprintf("/repos/owner/repo/issues/%d/labels", number),
		[]byte(`{"labels":["factory/ready"],"actor":"dev"}`))
	if status != http.StatusOK {
		t.Fatalf("add label status = %d, body %s", status, raw)
	}
	// Adding an existing label is idempotent and does not invent a new generation.
	status, _ = request(t, ts, http.MethodPost, fmt.Sprintf("/repos/owner/repo/issues/%d/labels", number),
		[]byte(`{"labels":["factory/ready"],"actor":"dev"}`))
	if status != http.StatusOK {
		t.Fatalf("repeat label status = %d", status)
	}

	status, raw = request(t, ts, http.MethodGet, fmt.Sprintf("/repos/owner/repo/issues/%d/events?per_page=100", number), nil)
	if status != http.StatusOK {
		t.Fatalf("events status = %d", status)
	}
	var events []struct {
		Event     string    `json:"event"`
		CreatedAt time.Time `json:"created_at"`
		Actor     struct {
			Login string `json:"login"`
		} `json:"actor"`
		Label struct {
			Name string `json:"name"`
		} `json:"label"`
	}
	if err := json.Unmarshal(raw, &events); err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Label.Name != "needs/spec" || events[1].Label.Name != "factory/ready" {
		t.Fatalf("label events = %+v", events)
	}
	for _, event := range events {
		if event.Event != "labeled" || event.Actor.Login != "dev" || event.CreatedAt.IsZero() {
			t.Errorf("label event = %+v", event)
		}
	}

	status, raw = request(t, ts, http.MethodPatch, fmt.Sprintf("/repos/owner/repo/issues/%d", number), []byte(`{"body":"new body"}`))
	if status != http.StatusOK {
		t.Fatalf("body edit status = %d, body %s", status, raw)
	}
	edited := decodeResponse[restIssue](t, raw)
	if edited.Body != "new body" {
		t.Fatalf("edited issue = %+v", edited)
	}

	status, raw = graphQLIssue(t, ts, number)
	if status != http.StatusOK {
		t.Fatalf("GraphQL status = %d, body %s", status, raw)
	}
	var snapshot struct {
		Data struct {
			Repository struct {
				Issue struct {
					Number       int        `json:"number"`
					Title        string     `json:"title"`
					Body         string     `json:"body"`
					State        string     `json:"state"`
					LastEditedAt *time.Time `json:"lastEditedAt"`
					Author       struct {
						Typename string `json:"__typename"`
						Login    string `json:"login"`
					} `json:"author"`
					Labels struct {
						Nodes []struct {
							Name string `json:"name"`
						} `json:"nodes"`
					} `json:"labels"`
					Timeline struct {
						Nodes []json.RawMessage `json:"nodes"`
					} `json:"timelineItems"`
				} `json:"issue"`
			} `json:"repository"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		t.Fatal(err)
	}
	issue := snapshot.Data.Repository.Issue
	if issue.Number != number || issue.Body != "new body" || issue.State != "OPEN" || issue.LastEditedAt == nil ||
		issue.Author.Typename != "User" || issue.Author.Login != "dev" || len(issue.Labels.Nodes) != 2 || len(issue.Timeline.Nodes) != 0 {
		t.Fatalf("GraphQL issue = %+v", issue)
	}

	status, raw = request(t, ts, http.MethodGet, "/repos/owner/repo/issues?state=open&labels=factory%2Fready&per_page=100", nil)
	if status != http.StatusOK {
		t.Fatalf("labelled issues status = %d", status)
	}
	listed := decodeResponse[[]restIssue](t, raw)
	if len(listed) != 1 || listed[0].Number != number {
		t.Fatalf("labelled issues = %+v", listed)
	}

	status, _ = request(t, ts, http.MethodDelete, fmt.Sprintf("/repos/owner/repo/issues/%d/labels/factory%%2Fready", number), nil)
	if status != http.StatusOK {
		t.Fatalf("remove label status = %d", status)
	}
	_, raw = graphQLIssue(t, ts, number)
	if bytes.Contains(raw, []byte(`"name":"factory/ready"`)) {
		t.Fatalf("removed label remains in snapshot: %s", raw)
	}
}

func TestIssueStoreGraphQLFailsClosed(t *testing.T) {
	_, ts := newStubServer(t)
	number := createIssue(t, ts, "body")
	queries := []string{
		`query { viewer { login } }`,
		strings.Replace(issueSnapshotQuery, "repository(", "viewer { login } repository(", 1),
		strings.Replace(issueSnapshotQuery, "number,url,title,body,state", "numberurltitlebodystate", 1),
	}
	for _, query := range queries {
		body := []byte(fmt.Sprintf(`{"query":%s,"variables":{"owner":"owner","name":"repo","number":%d}}`, strconvQuote(query), number))
		status, raw := request(t, ts, http.MethodPost, "/graphql", body)
		if status != http.StatusBadRequest || !strings.Contains(string(raw), "unsupported GraphQL query") {
			t.Errorf("unsupported query status = %d, body %s", status, raw)
		}
	}
}

func listComments(t *testing.T, ts *httptest.Server, number, page int) ([]restComment, string) {
	t.Helper()
	path := fmt.Sprintf("/repos/owner/repo/issues/%d/comments?per_page=100&page=%d", number, page)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, ts.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list comments status = %d, body %s", resp.StatusCode, raw)
	}
	return decodeResponse[[]restComment](t, raw), resp.Header.Get("Link")
}

func TestIssueStoreCommentIdentityRecentVsAll(t *testing.T) {
	_, ts := newStubServer(t)
	number := createIssue(t, ts, "body")
	for i := 1; i <= 130; i++ {
		status, raw := request(t, ts, http.MethodPost, fmt.Sprintf("/repos/owner/repo/issues/%d/comments", number),
			[]byte(fmt.Sprintf(`{"body":"comment %d"}`, i)))
		if status != http.StatusCreated {
			t.Fatalf("create comment %d status = %d, body %s", i, status, raw)
		}
		comment := decodeResponse[restComment](t, raw)
		if comment.ID != int64(i) || comment.User.Login != factoryAuthor || comment.CreatedAt.IsZero() {
			t.Fatalf("comment %d = %+v", i, comment)
		}
	}

	status, raw := request(t, ts, http.MethodPatch, "/repos/owner/repo/issues/comments/1", []byte(`{"body":"edited"}`))
	if status != http.StatusOK {
		t.Fatalf("edit comment status = %d, body %s", status, raw)
	}
	edited := decodeResponse[restComment](t, raw)
	if edited.ID != 1 || edited.Body != "edited" || !edited.UpdatedAt.After(edited.CreatedAt) {
		t.Fatalf("edited comment = %+v", edited)
	}

	first, link := listComments(t, ts, number, 1)
	last, _ := listComments(t, ts, number, 2)
	if len(first) != 100 || len(last) != 30 || first[0].ID != 1 || first[0].Body != "edited" || last[29].ID != 130 {
		t.Fatalf("pages = %d/%d, first %+v, last %+v", len(first), len(last), first[0], last[29])
	}
	if !strings.Contains(link, `rel="last"`) || !strings.Contains(link, "page=2") {
		t.Fatalf("first-page Link = %q", link)
	}
	all := append([]restComment{}, first...)
	all = append(all, last...)
	recent := all[len(all)-50:]
	if len(all) != 130 || len(recent) != 50 || recent[0].ID != 81 || recent[49].ID != 130 {
		t.Fatalf("all/recent = %d/%d, recent bounds %d/%d", len(all), len(recent), recent[0].ID, recent[49].ID)
	}
}

func publicBounceAuthor(comment restComment) bool { return comment.User.Login == factoryAuthor }

func TestPublicBounceAuthor(t *testing.T) {
	_, ts := newStubServer(t)
	number := createIssue(t, ts, "body")
	marker := `<!-- agent-factory scope=3buqdlot event=needs-author-1 -->`
	for _, body := range []string{
		`{"body":"` + marker + ` trusted"}`,
		`{"body":"` + marker + ` forged","author":"attacker"}`,
	} {
		status, raw := request(t, ts, http.MethodPost, fmt.Sprintf("/repos/owner/repo/issues/%d/comments", number), []byte(body))
		if status != http.StatusCreated {
			t.Fatalf("create bounce status = %d, body %s", status, raw)
		}
	}
	comments, _ := listComments(t, ts, number, 1)
	if len(comments) != 2 || !publicBounceAuthor(comments[0]) || publicBounceAuthor(comments[1]) {
		t.Fatalf("bounce authors = %+v", comments)
	}
	newest := comments[len(comments)-1]
	if publicBounceAuthor(newest) {
		t.Fatal("a forged newer bounce was accepted as the factory's")
	}
}

func TestIssueStoreConcurrentComments(t *testing.T) {
	_, ts := newStubServer(t)
	number := createIssue(t, ts, "body")
	const total = 60
	ids := make(chan int64, total)
	errs := make(chan error, total)
	var wg sync.WaitGroup
	for i := range total {
		wg.Add(1)
		go func() {
			defer wg.Done()
			body := []byte(fmt.Sprintf(`{"body":"concurrent %d"}`, i))
			req, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
				fmt.Sprintf("%s/repos/owner/repo/issues/%d/comments", ts.URL, number), bytes.NewReader(body))
			if err != nil {
				errs <- err
				return
			}
			req.Header.Set("Content-Type", "application/json")
			resp, err := ts.Client().Do(req)
			if err != nil {
				errs <- err
				return
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != http.StatusCreated {
				errs <- fmt.Errorf("status %d", resp.StatusCode)
				return
			}
			var comment restComment
			if err := json.NewDecoder(resp.Body).Decode(&comment); err != nil {
				errs <- err
				return
			}
			ids <- comment.ID
		}()
	}
	wg.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	seen := map[int64]bool{}
	for id := range ids {
		if seen[id] {
			t.Fatalf("duplicate comment id %d", id)
		}
		seen[id] = true
	}
	if len(seen) != total {
		t.Fatalf("created %d comments, want %d", len(seen), total)
	}
	comments, link := listComments(t, ts, number, 1)
	if len(comments) != total || link != "" {
		t.Fatalf("stored comments = %d, Link %q", len(comments), link)
	}
}

func TestIssueStoreRejectsUnsupportedRESTReads(t *testing.T) {
	_, ts := newStubServer(t)
	status, _ := request(t, ts, http.MethodGet, "/repos/owner/repo/issues?state=open&labels=factory%2Fready&page=2", nil)
	if status != http.StatusBadRequest {
		t.Fatalf("unsupported issue-list pagination status = %d, want 400", status)
	}
	values, _ := url.ParseQuery("state=open&labels=factory%2Fready")
	if values.Get("labels") != "factory/ready" {
		t.Fatal("test fixture does not preserve the label")
	}
}
