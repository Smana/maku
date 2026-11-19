// SPDX-License-Identifier: Apache-2.0

package maku

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// repositoryRE is the broker's handoff schema pattern for a repository.
var repositoryRE = regexp.MustCompile(`^[A-Za-z0-9-]+/[A-Za-z0-9._-]+$`)

// maxHandoffContext is the broker's maxLength for handoff's context.
const maxHandoffContext = 4096

// withWorkspaceRepository is the handoff call m with the repository of the client's workspace
// filled in (#639), as if the agent had passed it: only for a new request that names neither a
// repository nor candidates, and only when one of the client's roots is a git checkout whose
// origin reads owner/name. Anything else is raw as it came.
func (r *Relay) withWorkspaceRepository(ctx context.Context, m message, raw []byte) []byte {
	var p map[string]json.RawMessage
	if json.Unmarshal(m.Params, &p) != nil {
		return raw
	}
	var args map[string]any
	if json.Unmarshal(p["arguments"], &args) != nil || args == nil {
		return raw
	}
	if id, _ := args["id"].(string); id != "" {
		return raw
	}
	if repo, _ := args["repository"].(string); repo != "" {
		return raw
	}
	if c, _ := args["candidates"].([]any); len(c) > 0 {
		return raw
	}
	roots, err := r.rootPaths(ctx)
	if err != nil {
		return raw
	}
	repo := ""
	for _, root := range roots {
		if repo = originRepository(root); repo != "" {
			break
		}
	}
	if repo == "" {
		return raw
	}
	args["repository"] = repo
	line := "Repository: " + repo + " (from the workspace)"
	ctxText, _ := args["context"].(string)
	if ctxText != "" {
		line += "\n" + ctxText
	}
	if len(line) <= maxHandoffContext {
		args["context"] = line
	}
	b, err := json.Marshal(args)
	if err != nil {
		return raw
	}
	p["arguments"] = b
	params, _ := json.Marshal(p)
	out, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": m.ID, "method": m.Method, "params": json.RawMessage(params)})
	return out
}

// originRepository is owner/name of the origin remote of the git checkout at dir, read from its
// config file with no git and no network: "" when dir is not a checkout, has no origin, or its URL
// does not end in owner/name. A worktree's .git file points to its gitdir, whose commondir holds
// the config.
func originRepository(dir string) string {
	gitDir := filepath.Join(dir, ".git")
	fi, err := os.Stat(gitDir)
	if err != nil {
		return ""
	}
	if !fi.IsDir() {
		b, err := readGitFile(gitDir)
		if err != nil {
			return ""
		}
		target, ok := strings.CutPrefix(strings.TrimSpace(string(b)), "gitdir:")
		if !ok {
			return ""
		}
		if gitDir = strings.TrimSpace(target); !filepath.IsAbs(gitDir) {
			gitDir = filepath.Join(dir, gitDir)
		}
		if c, err := readGitFile(filepath.Join(gitDir, "commondir")); err == nil {
			if common := strings.TrimSpace(string(c)); filepath.IsAbs(common) {
				gitDir = common
			} else {
				gitDir = filepath.Join(gitDir, common)
			}
		}
	}
	cfg, err := readGitFile(filepath.Join(gitDir, "config"))
	if err != nil {
		return ""
	}
	return repositoryOf(originURL(cfg))
}

// readGitFile reads one of git's own small files of the developer's checkout.
func readGitFile(path string) ([]byte, error) {
	return os.ReadFile(path) //nolint:gosec // a git file of the client's own root, read and never written
}

// originURL is the url of [remote "origin"] in a git config file.
func originURL(cfg []byte) string {
	in := false
	sc := bufio.NewScanner(bytes.NewReader(cfg))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "[") {
			in = strings.EqualFold(strings.Join(strings.Fields(line), " "), `[remote "origin"]`)
			continue
		}
		if k, v, ok := strings.Cut(line, "="); in && ok && strings.EqualFold(strings.TrimSpace(k), "url") {
			return strings.Trim(strings.TrimSpace(v), `"`)
		}
	}
	return ""
}

// repositoryOf is owner/name from a remote URL: https://host/owner/name(.git), ssh://user@host/owner/name
// or user@host:owner/name; "" for a local path or anything else.
func repositoryOf(remote string) string {
	var path string
	switch {
	case strings.Contains(remote, "://"):
		_, rest, _ := strings.Cut(remote, "://")
		_, path, _ = strings.Cut(rest, "/")
	case strings.Contains(remote, ":") && !strings.HasPrefix(remote, "/"):
		_, path, _ = strings.Cut(remote, ":")
	default:
		return ""
	}
	path = strings.TrimSuffix(strings.Trim(path, "/"), ".git")
	parts := strings.Split(path, "/")
	if len(parts) < 2 {
		return ""
	}
	repo := parts[len(parts)-2] + "/" + parts[len(parts)-1]
	if !repositoryRE.MatchString(repo) {
		return ""
	}
	return repo
}
