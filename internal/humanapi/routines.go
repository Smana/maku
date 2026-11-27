// SPDX-License-Identifier: Apache-2.0

package humanapi

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/Smana/maku/internal/authn"
	fconfig "github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/runrequest"
)

// RoutinesGateway forwards /api/routines/* requests to the factory's /v1/routines* endpoints with
// the caller's own bearer token; runrequest.Factory satisfies it.
type RoutinesGateway interface {
	ListRoutines(ctx context.Context, accessToken, repo string) (json.RawMessage, error)
	RoutineDigest(ctx context.Context, accessToken string, repos []string, window string) (json.RawMessage, error)
	GetRoutine(ctx context.Context, accessToken, owner, name, routine string) (json.RawMessage, error)
	RunRoutine(ctx context.Context, accessToken, owner, name, routine, idempotencyKey string, body json.RawMessage) (int, json.RawMessage, error)
	PauseRoutine(ctx context.Context, accessToken, owner, name, routine string, body json.RawMessage) (json.RawMessage, error)
	ResumeRoutine(ctx context.Context, accessToken, owner, name, routine string, body json.RawMessage) (json.RawMessage, error)
}

const maxRoutineBody = 8 << 10

func (s *Server) routineRoutes(mux *http.ServeMux, index http.Handler) {
	mux.Handle("GET /routines", withCSP(index))
	mux.Handle("GET /routines/{owner}/{name}/{routine}", withCSP(index))
	mux.HandleFunc("GET /api/routines", s.listRoutines)
	mux.HandleFunc("GET /api/routines/digest", s.routineDigest)
	mux.HandleFunc("GET /api/routines/{owner}/{name}/{routine}", s.getRoutine)
	mux.HandleFunc("POST /api/routines/{owner}/{name}/{routine}/runs", s.runRoutine)
	mux.HandleFunc("POST /api/routines/{owner}/{name}/{routine}/pause", s.pauseRoutine)
	mux.HandleFunc("POST /api/routines/{owner}/{name}/{routine}/resume", s.resumeRoutine)
}

func (s *Server) routineWriter(w http.ResponseWriter, r *http.Request) (authn.Principal, bool) {
	p, ok := s.policyCaller(w, r)
	if !ok {
		return p, false
	}
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		refuseJSON(w, http.StatusUnsupportedMediaType, "unsupported_media_type")
		return p, false
	}
	isWeb := s.WebClient != nil && p.ClientID != "" && p.ClientID == s.WebClient()
	o := r.Header.Get("Origin")
	if isWeb {
		if want := origin(s.PublicURL); want == "" || o == "" || origin(o) != want {
			refuseJSON(w, http.StatusForbidden, "cross_origin")
			return p, false
		}
	} else if o != "" {
		if want := origin(s.PublicURL); want == "" || origin(o) != want {
			refuseJSON(w, http.StatusForbidden, "cross_origin")
			return p, false
		}
	}
	return p, true
}

func validRoutineRepo(owner, name string) bool {
	if owner == "" || owner == "." || owner == ".." || name == "" || name == "." || name == ".." {
		return false
	}
	return repositoryRE.MatchString(owner + "/" + name)
}

func validRoutineRepoString(repo string) bool {
	owner, name, ok := strings.Cut(repo, "/")
	return ok && !strings.Contains(name, "/") && validRoutineRepo(owner, name)
}

func routinePathParams(w http.ResponseWriter, r *http.Request) (owner, name, routine string, ok bool) {
	owner = r.PathValue("owner")
	name = r.PathValue("name")
	if !validRoutineRepo(owner, name) {
		refuseJSON(w, http.StatusBadRequest, "bad_repository")
		return "", "", "", false
	}
	routine = r.PathValue("routine")
	if !fconfig.ValidRoutineName(routine) {
		refuseJSON(w, http.StatusBadRequest, "bad_routine")
		return "", "", "", false
	}
	return owner, name, routine, true
}

func decodeRoutineBody(w http.ResponseWriter, r *http.Request) (json.RawMessage, bool) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRoutineBody))
	if err != nil {
		refuseJSON(w, http.StatusBadRequest, "bad_json")
		return nil, false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	var obj map[string]json.RawMessage
	if err := dec.Decode(&obj); err != nil || obj == nil {
		refuseJSON(w, http.StatusBadRequest, "bad_json")
		return nil, false
	}
	if dec.More() {
		refuseJSON(w, http.StatusBadRequest, "bad_json")
		return nil, false
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		refuseJSON(w, http.StatusBadRequest, "bad_json")
		return nil, false
	}
	return bytes.TrimSpace(raw), true
}

func writeRoutineAnswer(w http.ResponseWriter, status int, raw json.RawMessage, err error) {
	var ref runrequest.ErrRefused
	switch {
	case err == nil:
		w.Header().Set("Cache-Control", "no-store")
		if status == http.StatusNoContent {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(cmp.Or(status, http.StatusOK))
		_ = json.NewEncoder(w).Encode(raw)
	case errors.As(err, &ref):
		refuseJSON(w, ref.Status, ref.Reason)
	case errors.Is(err, runrequest.ErrForbidden):
		refuseJSON(w, http.StatusForbidden, "human_only")
	default:
		refuseJSON(w, http.StatusServiceUnavailable, "routines_unavailable")
	}
}

func (s *Server) listRoutines(w http.ResponseWriter, r *http.Request) {
	p, ok := s.policyCaller(w, r)
	if !ok {
		return
	}
	if s.Routines == nil {
		refuseJSON(w, http.StatusNotFound, "routines_unavailable")
		return
	}
	repo := cmp.Or(r.URL.Query().Get("repo"), r.URL.Query().Get("repository"))
	if repo != "" && !validRoutineRepoString(repo) {
		refuseJSON(w, http.StatusBadRequest, "bad_repository")
		return
	}
	raw, err := s.Routines.ListRoutines(r.Context(), p.AccessToken, repo)
	writeRoutineAnswer(w, http.StatusOK, raw, err)
}

func validDigestWindow(window string) bool {
	if !strings.HasSuffix(window, "d") {
		return false
	}
	numStr := strings.TrimSuffix(window, "d")
	n, err := strconv.Atoi(numStr)
	return err == nil && n >= 1 && n <= 30 && strconv.Itoa(n)+"d" == window
}

func (s *Server) routineDigest(w http.ResponseWriter, r *http.Request) {
	p, ok := s.sponsoredPrincipal(r)
	if ok {
		if p.Internal || p.AccessToken == "" {
			refuseJSON(w, http.StatusForbidden, "human_only")
			return
		}
		if !s.readAllowedJSON(w, r, p) {
			return
		}
	} else {
		p, ok = s.policyCaller(w, r)
		if !ok {
			return
		}
	}
	if s.Routines == nil {
		refuseJSON(w, http.StatusNotFound, "routines_unavailable")
		return
	}
	window := strings.TrimSpace(r.URL.Query().Get("window"))
	if window != "" && !validDigestWindow(window) {
		refuseJSON(w, http.StatusBadRequest, "bad_window")
		return
	}
	q := r.URL.Query()
	var repos []string
	for _, key := range []string{"repository", "repo", "repositories"} {
		for _, raw := range q[key] {
			if strings.TrimSpace(raw) == "" {
				continue
			}
			for _, part := range strings.Split(raw, ",") {
				item := strings.TrimSpace(part)
				if !validRoutineRepoString(item) {
					refuseJSON(w, http.StatusBadRequest, "bad_repository")
					return
				}
				repos = append(repos, item)
			}
		}
	}
	if p.Sponsored {
		allowed := s.SponsoredRepositories[p.ID]
		var allowedList []string
		for repo, ok := range allowed {
			if ok {
				allowedList = append(allowedList, repo)
			}
		}
		slices.Sort(allowedList)
		if len(repos) > 0 {
			scoped := make([]string, 0, len(repos))
			for _, asked := range repos {
				matched := ""
				for _, allow := range allowedList {
					if strings.EqualFold(allow, asked) {
						matched = allow
						break
					}
				}
				if matched == "" {
					refuseJSON(w, http.StatusForbidden, "repository_not_allowed")
					return
				}
				scoped = append(scoped, matched)
			}
			repos = scoped
		} else {
			if len(allowedList) == 0 {
				raw, _ := json.Marshal(map[string]any{
					"window":       cmp.Or(window, "7d"),
					"repositories": []any{},
				})
				writeRoutineAnswer(w, http.StatusOK, raw, nil)
				return
			}
			repos = allowedList
		}
	}
	raw, err := s.Routines.RoutineDigest(r.Context(), p.AccessToken, repos, window)
	writeRoutineAnswer(w, http.StatusOK, raw, err)
}

func (s *Server) getRoutine(w http.ResponseWriter, r *http.Request) {
	p, ok := s.policyCaller(w, r)
	if !ok {
		return
	}
	if s.Routines == nil {
		refuseJSON(w, http.StatusNotFound, "routines_unavailable")
		return
	}
	owner, name, routine, ok := routinePathParams(w, r)
	if !ok {
		return
	}
	raw, err := s.Routines.GetRoutine(r.Context(), p.AccessToken, owner, name, routine)
	writeRoutineAnswer(w, http.StatusOK, raw, err)
}

func (s *Server) runRoutine(w http.ResponseWriter, r *http.Request) {
	p, ok := s.routineWriter(w, r)
	if !ok {
		return
	}
	if s.Routines == nil {
		refuseJSON(w, http.StatusNotFound, "routines_unavailable")
		return
	}
	owner, name, routine, ok := routinePathParams(w, r)
	if !ok {
		return
	}
	body, ok := decodeRoutineBody(w, r)
	if !ok {
		return
	}
	status, raw, err := s.Routines.RunRoutine(r.Context(), p.AccessToken, owner, name, routine, r.Header.Get("Idempotency-Key"), body)
	writeRoutineAnswer(w, status, raw, err)
}

func (s *Server) pauseRoutine(w http.ResponseWriter, r *http.Request) {
	p, ok := s.routineWriter(w, r)
	if !ok {
		return
	}
	if s.Routines == nil {
		refuseJSON(w, http.StatusNotFound, "routines_unavailable")
		return
	}
	owner, name, routine, ok := routinePathParams(w, r)
	if !ok {
		return
	}
	body, ok := decodeRoutineBody(w, r)
	if !ok {
		return
	}
	raw, err := s.Routines.PauseRoutine(r.Context(), p.AccessToken, owner, name, routine, body)
	writeRoutineAnswer(w, http.StatusOK, raw, err)
}

func (s *Server) resumeRoutine(w http.ResponseWriter, r *http.Request) {
	p, ok := s.routineWriter(w, r)
	if !ok {
		return
	}
	if s.Routines == nil {
		refuseJSON(w, http.StatusNotFound, "routines_unavailable")
		return
	}
	owner, name, routine, ok := routinePathParams(w, r)
	if !ok {
		return
	}
	body, ok := decodeRoutineBody(w, r)
	if !ok {
		return
	}
	raw, err := s.Routines.ResumeRoutine(r.Context(), p.AccessToken, owner, name, routine, body)
	writeRoutineAnswer(w, http.StatusOK, raw, err)
}
