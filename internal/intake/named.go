// SPDX-License-Identifier: Apache-2.0

package intake

import (
	"regexp"
	"slices"
	"strings"
)

// tokenRE is a run of what a repository name or a URL is made of. Everything else (spaces,
// quotes, brackets, `:`, `,`) separates tokens, so `runlore's` and `(runlore)` read as `runlore`.
var tokenRE = regexp.MustCompile(`[A-Za-z0-9][A-Za-z0-9._/-]*`)

// CheckNamed is the deterministic answer to "did the request name a repository?", asked before any
// model call so the model is never left to pick the only candidate and invent a reason (#469).
//
// A repository counts as named when the text holds, as a whole token and without regard to case:
//   - `owner/name`, when the owner is one the factory knows or the form is a github.com URL;
//   - a URL to github.com/owner/name;
//   - a bare name that is the name part of a repository in known (the allowlist, the asked set and
//     the candidates), so `ai` is not `ai-native-ref` and `runlore-kb` is not `runlore`.
//
// Any named repository outside candidates refuses the request with `repository_not_served_here`,
// whose sentence names it and the candidates: the requester typed the name and owns the candidate
// set, so neither is a secret from them. Otherwise the candidates narrow to the ones named, in their
// order, so a plan can only target a repository the text asked for; with nothing named they are
// returned unchanged.
func CheckNamed(text string, known, candidates []string) ([]string, *Refusal) {
	spelling := map[string]string{} // lower case -> the first spelling seen
	owners := map[string]bool{}
	byName := map[string][]string{} // bare name -> repositories (lower case)
	for _, list := range [][]string{known, candidates} {
		for _, r := range list {
			r = strings.TrimSpace(r)
			owner, name, ok := strings.Cut(r, "/")
			if !ok || owner == "" || name == "" {
				continue
			}
			key := strings.ToLower(r)
			if _, seen := spelling[key]; !seen {
				spelling[key] = r
				byName[strings.ToLower(name)] = append(byName[strings.ToLower(name)], key)
			}
			owners[strings.ToLower(owner)] = true
		}
	}

	var named []string // lower case, in order of appearance, de-duplicated
	add := func(key string, display string) {
		if !slices.Contains(named, key) {
			named = append(named, key)
			if _, seen := spelling[key]; !seen {
				spelling[key] = display
			}
		}
	}
	for _, tok := range tokenRE.FindAllString(text, -1) {
		tok = strings.TrimRight(tok, ".-/")
		low := strings.ToLower(tok)
		if strings.Contains(tok, "/") {
			parts := strings.Split(low, "/")
			orig := strings.Split(tok, "/")
			github := parts[0] == "github.com" || parts[0] == "www.github.com"
			if github {
				parts, orig = parts[1:], orig[1:]
			}
			if len(parts) < 2 || parts[0] == "" || parts[1] == "" || (!github && (len(parts) != 2 || !owners[parts[0]])) {
				continue
			}
			name := strings.TrimSuffix(parts[1], ".git")
			if name == "" {
				continue
			}
			add(parts[0]+"/"+name, orig[0]+"/"+strings.TrimSuffix(orig[1], ".git"))
			continue
		}
		for _, key := range byName[low] {
			add(key, spelling[key])
		}
	}
	if len(named) == 0 {
		return candidates, nil
	}

	for _, key := range named {
		if !isCandidate(candidates, key) {
			served := make([]string, 0, len(candidates))
			for _, c := range candidates {
				if s, ok := spelling[strings.ToLower(c)]; ok {
					c = s
				}
				served = append(served, c)
			}
			return nil, refuse(ReasonRepositoryNotServedHere,
				spelling[key]+" isn't served here (served: "+strings.Join(served, ", ")+")")
		}
	}
	narrowed := make([]string, 0, len(named))
	for _, c := range candidates {
		if slices.Contains(named, strings.ToLower(c)) {
			narrowed = append(narrowed, c)
		}
	}
	return narrowed, nil
}
