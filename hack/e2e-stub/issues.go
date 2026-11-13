// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

const factoryLogin = "maku-demo[bot]"

// Issue is the stub's issue snapshot. Labels are copied at every store boundary.
type Issue struct {
	ID           int64
	Number       int
	Title        string
	Body         string
	State        string
	Author       string
	Labels       []string
	CreatedAt    time.Time
	UpdatedAt    time.Time
	LastEditedAt *time.Time
}

// Comment is one issue comment with a stable numeric identity.
type Comment struct {
	ID        int64
	Author    string
	Body      string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// LabelEvent records one label addition; removing a label never erases its history.
type LabelEvent struct {
	ID        int64
	Actor     string
	Label     string
	CreatedAt time.Time
}

type issueRecord struct {
	issue    Issue
	comments []Comment
	events   []LabelEvent
}

// IssueStore is the concurrency-safe in-memory GitHub issue state for the scenario.
type IssueStore struct {
	mu          sync.RWMutex
	issues      map[int]*issueRecord
	nextIssue   int
	nextComment int64
	nextEvent   int64
	ticks       int64
}

// NewIssueStore builds an empty store with deterministic identities and timestamps.
func NewIssueStore() *IssueStore {
	return &IssueStore{issues: map[int]*issueRecord{}, nextIssue: 1, nextComment: 1, nextEvent: 1}
}

func (s *IssueStore) nowLocked() time.Time {
	s.ticks++
	return time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC).Add(time.Duration(s.ticks) * time.Second)
}

func cloneIssue(issue Issue) Issue {
	issue.Labels = slices.Clone(issue.Labels)
	if issue.LastEditedAt != nil {
		edited := *issue.LastEditedAt
		issue.LastEditedAt = &edited
	}
	return issue
}

// CreateIssue opens an issue and records its initial labels as maintainer or App events.
func (s *IssueStore) CreateIssue(title, body, author string, labels []string) Issue {
	s.mu.Lock()
	defer s.mu.Unlock()
	if author == "" {
		author = factoryLogin
	}
	now := s.nowLocked()
	number := s.nextIssue
	s.nextIssue++
	issue := Issue{ID: int64(number), Number: number, Title: title, Body: body, State: "open", Author: author,
		CreatedAt: now, UpdatedAt: now}
	record := &issueRecord{issue: issue}
	for _, label := range labels {
		if label == "" || slices.Contains(record.issue.Labels, label) {
			continue
		}
		record.issue.Labels = append(record.issue.Labels, label)
		record.events = append(record.events, LabelEvent{ID: s.nextEvent, Actor: author, Label: label, CreatedAt: now})
		s.nextEvent++
	}
	s.issues[number] = record
	return cloneIssue(record.issue)
}

// Issue returns a copy of one issue snapshot.
func (s *IssueStore) Issue(number int) (Issue, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	record, ok := s.issues[number]
	if !ok {
		return Issue{}, false
	}
	return cloneIssue(record.issue), true
}

// SetBody replaces only an issue's body and advances its edit and update timestamps.
func (s *IssueStore) SetBody(number int, body string) (Issue, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.issues[number]
	if !ok {
		return Issue{}, false
	}
	now := s.nowLocked()
	record.issue.Body = body
	record.issue.UpdatedAt = now
	record.issue.LastEditedAt = &now
	return cloneIssue(record.issue), true
}

// Comments returns every comment oldest first, as GitHub's REST list does.
func (s *IssueStore) Comments(number int) []Comment {
	s.mu.RLock()
	defer s.mu.RUnlock()
	record := s.issues[number]
	if record == nil {
		return nil
	}
	return slices.Clone(record.comments)
}

// LabelEvents returns all label additions oldest first.
func (s *IssueStore) LabelEvents(number int) []LabelEvent {
	s.mu.RLock()
	defer s.mu.RUnlock()
	record := s.issues[number]
	if record == nil {
		return nil
	}
	return slices.Clone(record.events)
}

// ListLabeled returns open issues carrying label in issue-number order.
func (s *IssueStore) ListLabeled(label string) []Issue {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []Issue
	for number := 1; number < s.nextIssue; number++ {
		record := s.issues[number]
		if record.issue.State == "open" && slices.Contains(record.issue.Labels, label) {
			out = append(out, cloneIssue(record.issue))
		}
	}
	return out
}

// AddLabels adds each new label once and records its actor in the timeline.
func (s *IssueStore) AddLabels(number int, actor string, labels []string) (Issue, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.issues[number]
	if !ok {
		return Issue{}, false
	}
	if actor == "" {
		actor = factoryLogin
	}
	var added []string
	for _, label := range labels {
		if label != "" && !slices.Contains(record.issue.Labels, label) && !slices.Contains(added, label) {
			added = append(added, label)
		}
	}
	if len(added) > 0 {
		now := s.nowLocked()
		record.issue.UpdatedAt = now
		for _, label := range added {
			record.issue.Labels = append(record.issue.Labels, label)
			record.events = append(record.events, LabelEvent{ID: s.nextEvent, Actor: actor, Label: label, CreatedAt: now})
			s.nextEvent++
		}
	}
	return cloneIssue(record.issue), true
}

// RemoveLabel removes current state without changing the label-addition timeline.
func (s *IssueStore) RemoveLabel(number int, label string) (Issue, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.issues[number]
	if !ok {
		return Issue{}, false
	}
	if index := slices.Index(record.issue.Labels, label); index >= 0 {
		record.issue.Labels = slices.Delete(record.issue.Labels, index, index+1)
		record.issue.UpdatedAt = s.nowLocked()
	}
	return cloneIssue(record.issue), true
}

// AddComment appends a comment with a store-wide stable ID. An empty author is the factory App.
func (s *IssueStore) AddComment(number int, body, author string) (Comment, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.issues[number]
	if !ok {
		return Comment{}, false
	}
	if author == "" {
		author = factoryLogin
	}
	now := s.nowLocked()
	comment := Comment{ID: s.nextComment, Author: author, Body: body, CreatedAt: now, UpdatedAt: now}
	s.nextComment++
	record.comments = append(record.comments, comment)
	record.issue.UpdatedAt = now
	return comment, true
}

// EditComment replaces a comment body without changing its ID or creation time.
func (s *IssueStore) EditComment(id int64, body string) (Comment, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, record := range s.issues {
		for index := range record.comments {
			if record.comments[index].ID != id {
				continue
			}
			now := s.nowLocked()
			record.comments[index].Body = body
			record.comments[index].UpdatedAt = now
			record.issue.UpdatedAt = now
			return record.comments[index], true
		}
	}
	return Comment{}, false
}

func (s *Stub) registerIssueRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /repos/owner/repo/contents/.github/agent-factory.yaml", s.fenceFile)
	mux.HandleFunc("POST /repos/owner/repo/issues", s.createIssue)
	mux.HandleFunc("GET /repos/owner/repo/pulls", s.listPullRequests)
	mux.HandleFunc("GET /repos/owner/repo/issues", s.listIssues)
	mux.HandleFunc("GET /repos/owner/repo/issues/{number}", s.getIssue)
	mux.HandleFunc("PATCH /repos/owner/repo/issues/{number}", s.editIssue)
	mux.HandleFunc("POST /repos/owner/repo/issues/{number}/labels", s.addLabels)
	mux.HandleFunc("DELETE /repos/owner/repo/issues/{number}/labels/{label...}", s.removeLabel)
	mux.HandleFunc("GET /repos/owner/repo/issues/{number}/events", s.issueEvents)
	mux.HandleFunc("POST /repos/owner/repo/issues/{number}/comments", s.createComment)
	mux.HandleFunc("GET /repos/owner/repo/issues/{number}/comments", s.listComments)
	mux.HandleFunc("PATCH /repos/owner/repo/issues/comments/{id}", s.editComment)
	mux.HandleFunc("POST /graphql", s.graphQL)
}

func issueNumber(w http.ResponseWriter, r *http.Request) (int, bool) {
	number, err := strconv.Atoi(r.PathValue("number"))
	if err != nil || number < 1 {
		http.Error(w, "invalid issue number", http.StatusBadRequest)
		return 0, false
	}
	return number, true
}

// association is the author's relation to the repository, as GitHub's GraphQL reports it: the
// scenario repository is the developer's own, and a bot has none.
func association(author string) string {
	if strings.HasSuffix(author, "[bot]") {
		return "NONE"
	}
	return "OWNER"
}

func gitHubIssue(issue Issue) map[string]any {
	labels := make([]map[string]string, 0, len(issue.Labels))
	for _, label := range issue.Labels {
		labels = append(labels, map[string]string{"name": label})
	}
	return map[string]any{
		"id": issue.ID, "number": issue.Number, "title": issue.Title, "body": issue.Body, "state": issue.State,
		"html_url": fmt.Sprintf("https://example.invalid/owner/repo/issues/%d", issue.Number),
		"user":     map[string]any{"login": issue.Author}, "labels": labels,
		"created_at": issue.CreatedAt, "updated_at": issue.UpdatedAt,
	}
}

func gitHubComment(comment Comment) map[string]any {
	return map[string]any{
		"id": comment.ID, "body": comment.Body, "user": map[string]any{"login": comment.Author},
		"created_at": comment.CreatedAt, "updated_at": comment.UpdatedAt,
	}
}

func (s *Stub) fenceFile(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"type": "file", "name": "agent-factory.yaml", "path": ".github/agent-factory.yaml",
		"sha": fullCommit, "encoding": "base64", "content": base64.StdEncoding.EncodeToString([]byte("factory: demo\n")),
	})
}

func (s *Stub) createIssue(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Title  string   `json:"title"`
		Body   string   `json:"body"`
		Author string   `json:"author"`
		Labels []string `json:"labels"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.Title) == "" {
		http.Error(w, "issue title is required", http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusCreated, gitHubIssue(s.issues.CreateIssue(body.Title, body.Body, body.Author, body.Labels)))
}

func (s *Stub) listIssues(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	if len(query) > 3 || query.Get("page") != "" || (query.Get("per_page") != "" && query.Get("per_page") != "100") ||
		(query.Get("state") != "open" && query.Get("state") != "all") || query.Get("labels") == "" ||
		strings.Contains(query.Get("labels"), ",") {
		http.Error(w, "unsupported issue list query", http.StatusBadRequest)
		return
	}
	for key := range query {
		if key != "state" && key != "labels" && key != "per_page" {
			http.Error(w, "unsupported issue list query", http.StatusBadRequest)
			return
		}
	}
	issues := s.issues.ListLabeled(query.Get("labels"))
	out := make([]map[string]any, 0, len(issues))
	for _, issue := range issues {
		out = append(out, gitHubIssue(issue))
	}
	writeJSON(w, http.StatusOK, out)
}

var agentPRHeadRE = regexp.MustCompile(`^owner:agent/[a-z2-7]{8}$`)

func (s *Stub) listPullRequests(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	if !agentPRHeadRE.MatchString(query.Get("head")) || query.Get("state") != "all" || query.Get("per_page") != "10" {
		http.Error(w, "unsupported pull request query", http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, []any{})
}

func (s *Stub) getIssue(w http.ResponseWriter, r *http.Request) {
	number, ok := issueNumber(w, r)
	if !ok {
		return
	}
	issue, ok := s.issues.Issue(number)
	if !ok {
		http.Error(w, "issue not found", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, gitHubIssue(issue))
}

func (s *Stub) editIssue(w http.ResponseWriter, r *http.Request) {
	number, ok := issueNumber(w, r)
	if !ok {
		return
	}
	var body struct {
		Body string `json:"body"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	issue, ok := s.issues.SetBody(number, body.Body)
	if !ok {
		http.Error(w, "issue not found", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, gitHubIssue(issue))
}

type addLabelsRequest struct {
	Labels []string `json:"labels"`
	Actor  string   `json:"actor"`
}

// UnmarshalJSON accepts go-github's top-level label array and fakegh's actor-carrying object.
func (r *addLabelsRequest) UnmarshalJSON(raw []byte) error {
	var labels []string
	if err := json.Unmarshal(raw, &labels); err == nil {
		r.Labels, r.Actor = labels, ""
		return nil
	}
	type object addLabelsRequest
	var decoded object
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return err
	}
	*r = addLabelsRequest(decoded)
	return nil
}

func (s *Stub) addLabels(w http.ResponseWriter, r *http.Request) {
	number, ok := issueNumber(w, r)
	if !ok {
		return
	}
	var body addLabelsRequest
	if !decodeBody(w, r, &body) {
		return
	}
	issue, ok := s.issues.AddLabels(number, body.Actor, body.Labels)
	if !ok {
		http.Error(w, "issue not found", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, gitHubIssue(issue)["labels"])
}

func (s *Stub) removeLabel(w http.ResponseWriter, r *http.Request) {
	number, ok := issueNumber(w, r)
	if !ok {
		return
	}
	issue, ok := s.issues.RemoveLabel(number, r.PathValue("label"))
	if !ok {
		http.Error(w, "issue not found", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, gitHubIssue(issue)["labels"])
}

func (s *Stub) issueEvents(w http.ResponseWriter, r *http.Request) {
	number, ok := issueNumber(w, r)
	if !ok {
		return
	}
	if _, ok := s.issues.Issue(number); !ok {
		http.Error(w, "issue not found", http.StatusNotFound)
		return
	}
	query := r.URL.Query()
	if len(query) > 2 || (query.Get("per_page") != "" && query.Get("per_page") != "100") {
		http.Error(w, "unsupported issue events query", http.StatusBadRequest)
		return
	}
	for key := range query {
		if key != "page" && key != "per_page" {
			http.Error(w, "unsupported issue events query", http.StatusBadRequest)
			return
		}
	}
	if page, err := strconv.Atoi(query.Get("page")); query.Get("page") != "" && (err != nil || page < 1) {
		http.Error(w, "unsupported issue events page", http.StatusBadRequest)
		return
	}
	events := s.issues.LabelEvents(number)
	out := make([]map[string]any, 0, len(events))
	for _, event := range events {
		out = append(out, map[string]any{
			"id": event.ID, "event": "labeled", "actor": map[string]any{"login": event.Actor},
			"label": map[string]any{"name": event.Label}, "created_at": event.CreatedAt,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Stub) createComment(w http.ResponseWriter, r *http.Request) {
	number, ok := issueNumber(w, r)
	if !ok {
		return
	}
	var body struct {
		Body   string `json:"body"`
		Author string `json:"author"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	comment, ok := s.issues.AddComment(number, body.Body, body.Author)
	if !ok {
		http.Error(w, "issue not found", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusCreated, gitHubComment(comment))
}

func pageOptions(w http.ResponseWriter, r *http.Request) (int, int, bool) {
	page, perPage := 1, 30
	var err error
	query := r.URL.Query()
	for key := range query {
		if key != "page" && key != "per_page" {
			http.Error(w, "unsupported comment list query", http.StatusBadRequest)
			return 0, 0, false
		}
	}
	if query.Get("page") != "" {
		page, err = strconv.Atoi(query.Get("page"))
	}
	if err == nil && query.Get("per_page") != "" {
		perPage, err = strconv.Atoi(query.Get("per_page"))
	}
	if err != nil || page < 1 || perPage < 1 || perPage > 100 {
		http.Error(w, "invalid comment pagination", http.StatusBadRequest)
		return 0, 0, false
	}
	return page, perPage, true
}

func commentPageLink(r *http.Request, page, perPage int) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	query := url.Values{"page": {strconv.Itoa(page)}, "per_page": {strconv.Itoa(perPage)}}
	return scheme + "://" + r.Host + r.URL.Path + "?" + query.Encode()
}

func (s *Stub) listComments(w http.ResponseWriter, r *http.Request) {
	number, ok := issueNumber(w, r)
	if !ok {
		return
	}
	if _, ok := s.issues.Issue(number); !ok {
		http.Error(w, "issue not found", http.StatusNotFound)
		return
	}
	page, perPage, ok := pageOptions(w, r)
	if !ok {
		return
	}
	comments := s.issues.Comments(number)
	start := min((page-1)*perPage, len(comments))
	end := min(start+perPage, len(comments))
	out := make([]map[string]any, 0, end-start)
	for _, comment := range comments[start:end] {
		out = append(out, gitHubComment(comment))
	}
	lastPage := max(1, (len(comments)+perPage-1)/perPage)
	var links []string
	if page < lastPage {
		links = append(links, fmt.Sprintf(`<%s>; rel="next"`, commentPageLink(r, page+1, perPage)))
		links = append(links, fmt.Sprintf(`<%s>; rel="last"`, commentPageLink(r, lastPage, perPage)))
	}
	if page > 1 {
		links = append(links, fmt.Sprintf(`<%s>; rel="prev"`, commentPageLink(r, page-1, perPage)))
		links = append(links, fmt.Sprintf(`<%s>; rel="first"`, commentPageLink(r, 1, perPage)))
	}
	w.Header().Set("Link", strings.Join(links, ", "))
	writeJSON(w, http.StatusOK, out)
}

func (s *Stub) editComment(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		http.Error(w, "invalid comment id", http.StatusBadRequest)
		return
	}
	var body struct {
		Body string `json:"body"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	comment, ok := s.issues.EditComment(id, body.Body)
	if !ok {
		http.Error(w, "comment not found", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, gitHubComment(comment))
}

const issueSnapshotSelection = "repository(owner:$owner,name:$name){issue(number:$number){" +
	"number,url,title,body,state,author{__typename,login},authorAssociation,lastEditedAt,labels(first:50){nodes{name}}," +
	"timelineItems(itemTypes:[RENAMED_TITLE_EVENT],last:1){nodes{... on RenamedTitleEvent{createdAt}}}}}}"

var graphQLPunctuation = regexp.MustCompile(`\s*([{}():])\s*`)

func normalizeGraphQL(query string) string {
	normalized := strings.Join(strings.Fields(strings.ReplaceAll(query, ",", " ")), " ")
	normalized = graphQLPunctuation.ReplaceAllString(normalized, "$1")
	return strings.ReplaceAll(normalized, "... ", "...")
}

func scenarioQueryHeader(header string) bool {
	header = strings.TrimSpace(header)
	if !strings.HasPrefix(header, "query(") || !strings.HasSuffix(header, ")") {
		return false
	}
	declarations := strings.TrimSuffix(strings.TrimPrefix(header, "query("), ")")
	seen := map[string]bool{}
	for _, declaration := range strings.Split(declarations, "$") {
		declaration = strings.TrimSpace(declaration)
		if declaration == "" {
			continue
		}
		if (declaration != "owner:String!" && declaration != "name:String!" && declaration != "number:Int!") || seen[declaration] {
			return false
		}
		seen[declaration] = true
	}
	return len(seen) == 2 || len(seen) == 3
}

const agentPullsSelection = "repository(owner:$owner,name:$name){pullRequests(states:[OPEN],first:100,orderBy:{field:CREATED_AT,direction:DESC}){nodes{number,headRefName,headRepository{owner{login},name},labels(first:30){nodes{name}}}}}}"

func (s *Stub) graphQL(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Query     string `json:"query"`
		Variables struct {
			Owner  string `json:"owner"`
			Name   string `json:"name"`
			Number int    `json:"number"`
		} `json:"variables"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	// Canonicalize separators without joining adjacent field names into a different token.
	normalized := normalizeGraphQL(body.Query)
	selection := normalizeGraphQL(issueSnapshotSelection)
	open := strings.Index(normalized, "{")
	if open < 0 || !scenarioQueryHeader(normalized[:open]) {
		http.Error(w, "unsupported GraphQL query: only the scenario issue snapshot, labels and title timeline are served", http.StatusBadRequest)
		return
	}
	if normalized == normalized[:open]+"{"+normalizeGraphQL(agentPullsSelection) {
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequests": map[string]any{"nodes": []any{}}}}})
		return
	}
	if normalized != normalized[:open]+"{"+selection {
		http.Error(w, "unsupported GraphQL query: only the scenario issue snapshot, labels and title timeline are served", http.StatusBadRequest)
		return
	}
	if body.Variables.Owner != "owner" || body.Variables.Name != "repo" || body.Variables.Number < 1 {
		http.Error(w, "unsupported GraphQL repository or issue", http.StatusBadRequest)
		return
	}
	issue, ok := s.issues.Issue(body.Variables.Number)
	if !ok {
		http.Error(w, "issue not found", http.StatusNotFound)
		return
	}
	typename, login := "User", issue.Author
	if strings.HasSuffix(login, "[bot]") {
		typename, login = "Bot", strings.TrimSuffix(login, "[bot]")
	}
	labels := make([]map[string]string, 0, len(issue.Labels))
	for _, label := range issue.Labels {
		labels = append(labels, map[string]string{"name": label})
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"repository": map[string]any{"issue": map[string]any{
		"number": issue.Number, "url": fmt.Sprintf("https://example.invalid/owner/repo/issues/%d", issue.Number),
		"title": issue.Title, "body": issue.Body, "state": strings.ToUpper(issue.State),
		"author": map[string]any{"__typename": typename, "login": login}, "authorAssociation": association(issue.Author),
		"lastEditedAt": issue.LastEditedAt, "labels": map[string]any{"nodes": labels}, "timelineItems": map[string]any{"nodes": []any{}},
	}}}})
}
