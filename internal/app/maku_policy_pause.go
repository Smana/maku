// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"sigs.k8s.io/yaml"

	factoryv1 "github.com/Smana/maku/api/factory/v1alpha1"
)

func (r Maku) policyPause(ctx context.Context, verb string, args []string) error {
	fs := flag.NewFlagSet("policy "+verb, flag.ContinueOnError)
	configPath := fs.String("config", "", "the Factory manifest, for its spec.gitops")
	repository := fs.String("repository", "", "owner/name whose policy changes")
	reason := fs.String("reason", "", "the pause reason")
	output := fs.String("output", "", "json")
	pos, err := flags(fs, args)
	if err != nil {
		return err
	}
	if *repository == "" || len(pos) != 0 {
		return errors.New("policy pause/resume needs --repository owner/name")
	}
	if verb == "pause" && strings.TrimSpace(*reason) == "" {
		return errors.New("policy pause needs --reason")
	}
	if *configPath == "" {
		return printPauseEdit(r.Out, verb, *reason, *output, "")
	}
	var factory factoryv1.Factory
	if err := readYAMLFile(*configPath, &factory); err != nil {
		return err
	}
	if factory.Spec.GitOps == nil {
		return printPauseEdit(r.Out, verb, *reason, *output, "")
	}
	url, err := r.openPausePR(ctx, factory, *repository, verb, *reason)
	if err != nil {
		return err
	}
	if *output == "json" {
		return writePolicyJSON(r.Out, map[string]any{"repository": *repository, "paused": verb == "pause", "reason": *reason, "pullRequest": url})
	}
	_, err = fmt.Fprintln(r.Out, url)
	return err
}

func printPauseEdit(out interface{ Write([]byte) (int, error) }, verb, reason, output, url string) error {
	if output == "json" {
		return writePolicyJSON(out, map[string]any{"paused": verb == "pause", "reason": reason, "pullRequest": url})
	}
	if verb == "resume" {
		_, err := fmt.Fprintln(out, "merge:\n  paused: null")
		return err
	}
	_, err := fmt.Fprintf(out, "merge:\n  paused: {reason: %q}\n", reason)
	return err
}

func (r Maku) openPausePR(ctx context.Context, factory factoryv1.Factory, repository, verb, reason string) (string, error) {
	gitops := factory.Spec.GitOps
	branch := gitops.Branch
	if branch == "" {
		branch = "main"
	}
	var base struct {
		Commit struct {
			SHA string `json:"sha"`
		} `json:"commit"`
	}
	if err := r.ghJSON(ctx, "repos/"+gitops.Repository+"/branches/"+branch, &base); err != nil {
		return "", err
	}
	var file struct {
		SHA     string `json:"sha"`
		Content string `json:"content"`
	}
	if err := r.ghJSON(ctx, "repos/"+gitops.Repository+"/contents/"+gitops.Path+"?ref="+branch, &file); err != nil {
		return "", err
	}
	raw, err := base64.StdEncoding.DecodeString(file.Content)
	if err != nil {
		return "", fmt.Errorf("gitops manifest: %w", err)
	}
	var repositoryObject factoryv1.Repository
	if err := yaml.Unmarshal(raw, &repositoryObject); err != nil {
		return "", fmt.Errorf("gitops manifest: %w", err)
	}
	if !strings.EqualFold(repositoryObject.Spec.Repository, repository) {
		return "", fmt.Errorf("gitops manifest is for %s, not %s", repositoryObject.Spec.Repository, repository)
	}
	// Refuse before anything is written: a resume of a repository that is not paused once opened a
	// pull request of zero files (gcp-0 live drill, #549), and a second pause would do the same.
	if paused := repositoryObject.Spec.Merge != nil && repositoryObject.Spec.Merge.Paused != nil; verb == "resume" && !paused {
		return "", fmt.Errorf("policy resume: %s is not paused (its manifest has no spec.merge.paused); nothing to resume", repository)
	} else if verb == "pause" && paused && repositoryObject.Spec.Merge.Paused.Reason == reason {
		return "", fmt.Errorf("policy pause: %s is already paused with that reason; nothing to change", repository)
	}
	updated, err := setMergePaused(raw, verb == "pause", reason)
	if err != nil {
		return "", err
	}
	if string(updated) == string(raw) {
		return "", fmt.Errorf("policy %s: the edit changes nothing in %s; no pull request opened", verb, gitops.Path)
	}
	changeBranch := "factory/" + verb + "-" + strings.ReplaceAll(repository, "/", "-")
	if _, err := r.capture(ctx, []string{"gh", "api", "--method", "POST", "repos/" + gitops.Repository + "/git/refs",
		"-f", "ref=refs/heads/" + changeBranch, "-f", "sha=" + base.Commit.SHA}); err != nil {
		return "", err
	}
	message := "chore(policy): " + verb + " " + repository
	if _, err := r.capture(ctx, []string{"gh", "api", "--method", "PUT", "repos/" + gitops.Repository + "/contents/" + gitops.Path,
		"-f", "message=" + message, "-f", "content=" + base64.StdEncoding.EncodeToString(updated),
		"-f", "sha=" + file.SHA, "-f", "branch=" + changeBranch}); err != nil {
		return "", err
	}
	title := "Policy " + verb + ": " + repository
	body := message + ".\n\nOnly spec.merge.paused changes. A maintainer reviews and merges this pull request."
	url, err := r.capture(ctx, []string{"gh", "pr", "create", "--repo", gitops.Repository, "--base", branch,
		"--head", changeBranch, "--title", title, "--body", body})
	return strings.TrimSpace(url), err
}

func readYAMLFile(path string, out any) error {
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return err
	}
	return yaml.Unmarshal(raw, out)
}

func (r Maku) capture(ctx context.Context, argv []string) (string, error) {
	if r.Capture != nil {
		return r.Capture(ctx, argv)
	}
	out, err := exec.CommandContext(ctx, argv[0], argv[1:]...).Output() //nolint:gosec // gh's fixed policy-change arguments
	if err != nil {
		return "", fmt.Errorf("%s: %w", strings.Join(argv, " "), err)
	}
	return string(out), nil
}
