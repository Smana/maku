// SPDX-License-Identifier: Apache-2.0

// Command maku-e2e-client scripts the MCP calls used by the spec-intake k3d scenario.
package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Smana/maku/internal/factory/sanitize"
	"github.com/Smana/maku/internal/intake"
)

const maxStateFile = 4 << 20

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, _ io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: client plan|revise|accept-rest|answer|status|classify [flags]")
	}
	var err error
	switch args[0] {
	case "plan":
		err = runPlan(ctx, args[1:], stdout, stderr, false)
	case "revise":
		err = runPlan(ctx, args[1:], stdout, stderr, true)
	case "accept-rest":
		err = runAcceptRest(ctx, args[1:], stdout, stderr)
	case "answer":
		err = runAnswer(ctx, args[1:], stdout, stderr)
	case "status":
		err = runStatus(ctx, args[1:], stdout, stderr)
	case "classify":
		err = runClassify(ctx, args[1:], stdout, stderr)
	default:
		err = fmt.Errorf("unknown subcommand %q", args[0])
	}
	return err
}

func readLimitedFile(path string) ([]byte, error) {
	file, err := os.Open(path) //nolint:gosec // scenario-selected state file
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	raw, err := io.ReadAll(io.LimitReader(file, maxStateFile+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxStateFile {
		return nil, fmt.Errorf("%s is too large", path)
	}
	return raw, nil
}

func readToken() (string, error) {
	if token := strings.TrimSpace(os.Getenv("E2E_TOKEN")); token != "" {
		return token, nil
	}
	if path := os.Getenv("E2E_TOKEN_FILE"); path != "" {
		raw, err := readLimitedFile(path)
		if err != nil {
			return "", fmt.Errorf("read E2E_TOKEN_FILE: %w", err)
		}
		return strings.TrimSpace(string(raw)), nil
	}
	return "", nil
}

func newScenarioClient(stdio bool, stderr io.Writer) (*Client, error) {
	client := &Client{Stderr: stderr, Timeout: 90 * time.Second}
	if stdio || os.Getenv("E2E_MCP_STDIO") == "1" {
		command := os.Getenv("E2E_MAKU")
		if command == "" {
			command = "maku"
		}
		client.Command = []string{command, "mcp"}
		return client, nil
	}
	client.BrokerURL = strings.TrimRight(os.Getenv("E2E_BROKER_URL"), "/")
	if client.BrokerURL == "" {
		return nil, errors.New("E2E_BROKER_URL is required for broker HTTP MCP")
	}
	token, err := readToken()
	if err != nil {
		return nil, err
	}
	client.Token = token
	return client, nil
}

type planResult struct {
	Plan        intake.Plan      `json:"plan"`
	Hash        string           `json:"hash"`
	Question    *intake.Question `json:"question"`
	CanStart    bool             `json:"canStart"`
	Degraded    bool             `json:"degraded"`
	CanStartWhy string           `json:"canStartWhy"`
}

func decodePlan(raw json.RawMessage) (planResult, error) {
	var result planResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return result, fmt.Errorf("decode plan structuredContent: %w", err)
	}
	if result.Plan.APIVersion != intake.APIVersion || result.Hash == "" {
		return result, errors.New("plan result lacks plan/v2 or its hash")
	}
	return result, nil
}

func readPlan(path string) (intake.Plan, error) {
	var plan intake.Plan
	raw, err := readLimitedFile(path)
	if err != nil {
		return plan, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&plan); err != nil {
		return plan, fmt.Errorf("decode %s: %w", path, err)
	}
	return plan, nil
}

func writePlan(path string, plan intake.Plan) ([]byte, error) {
	raw, err := json.Marshal(plan)
	if err != nil {
		return nil, err
	}
	if path != "" {
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			return nil, err
		}
	}
	return raw, nil
}

func printPlan(stdout io.Writer, result planResult, out string) error {
	raw, err := writePlan(out, result.Plan)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stdout, "hash=%s\n", oneLine(result.Hash))
	if result.Question != nil {
		_, _ = fmt.Fprintf(stdout, "question=%s\nrecommended=%s\n", oneLine(result.Question.Text), oneLine(result.Question.Recommended.Answer))
	} else {
		_, _ = fmt.Fprint(stdout, "question=\nrecommended=\n")
	}
	_, _ = fmt.Fprintf(stdout, "gaps=%d\ncanStart=%t\ndegraded=%t\nplan=%s\n", len(result.Plan.Gaps), result.CanStart, result.Degraded,
		base64.StdEncoding.EncodeToString(raw))
	if out != "" {
		_, _ = fmt.Fprintf(stdout, "plan_file=%s\n", oneLine(out))
	}
	return nil
}

func runPlan(ctx context.Context, args []string, stdout, stderr io.Writer, revise bool) error {
	name := "plan"
	if revise {
		name = "revise"
	}
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(stderr)
	text := flags.String("text", "", "original intake request")
	repo := flags.String("repo", "", "owner/repository")
	out := flags.String("out", "", "write the returned plan to this file")
	planPath := flags.String("plan", "", "base plan JSON")
	candidatePath := flags.String("candidate", "", "candidate plan JSON")
	hash := flags.String("hash", "", "base plan hash")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *text == "" || *repo == "" {
		return fmt.Errorf("%s requires --text and --repo and takes no positional arguments", name)
	}
	toolArgs := map[string]any{"text": *text, "repository": *repo}
	if revise {
		if *planPath == "" || *candidatePath == "" || *hash == "" {
			return errors.New("revise requires --plan, --candidate and --hash")
		}
		base, err := readPlan(*planPath)
		if err != nil {
			return err
		}
		candidate, err := readPlan(*candidatePath)
		if err != nil {
			return err
		}
		toolArgs["plan"], toolArgs["hash"], toolArgs["candidate"] = base, *hash, candidate
	} else if *planPath != "" || *candidatePath != "" || *hash != "" {
		return errors.New("plan does not take revision flags")
	}
	client, err := newScenarioClient(false, stderr)
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()
	raw, err := client.Call(ctx, "intake_plan", toolArgs)
	if err != nil {
		return err
	}
	result, err := decodePlan(raw)
	if err != nil {
		return err
	}
	return printPlan(stdout, result, *out)
}

// acceptRestCandidate applies only the recommendations already present in base. The broker still
// revalidates the candidate and owns the returned hash; this helper never computes a hash itself.
func acceptRestCandidate(base intake.Plan) (intake.Plan, error) {
	raw, err := json.Marshal(base)
	if err != nil {
		return intake.Plan{}, err
	}
	var candidate intake.Plan
	if err := json.Unmarshal(raw, &candidate); err != nil {
		return intake.Plan{}, err
	}
	gaps := make(map[string]intake.Gap, len(base.Gaps))
	for _, gap := range base.Gaps {
		gaps[gap.ID] = gap
	}
	lastNumber := 0
	for _, issue := range base.Issues {
		records, err := sanitize.DecisionRecords(issue.Body)
		if err != nil {
			return intake.Plan{}, err
		}
		if len(records) > 0 {
			lastNumber = max(lastNumber, records[len(records)-1].Number)
		}
	}
	for i := range candidate.Issues {
		markers, err := sanitize.ClarificationMarkers(candidate.Issues[i].Body)
		if err != nil {
			return intake.Plan{}, err
		}
		type assumption struct {
			marker sanitize.ClarificationMarker
			answer string
			record string
		}
		added := make([]assumption, 0, len(markers))
		for _, marker := range markers {
			gap, ok := gaps[marker.ID]
			if !ok {
				return intake.Plan{}, fmt.Errorf("marker %s has no ranked recommendation", marker.ID)
			}
			answer, why := gap.Recommended.Answer, gap.Recommended.Why
			if strings.TrimSpace(answer) == "" || strings.ContainsAny(answer+why, "\r\n") {
				return intake.Plan{}, fmt.Errorf("recommendation %s cannot become a one-line Assumption", marker.ID)
			}
			if lastNumber == math.MaxInt {
				return intake.Plan{}, errors.New("no Decision number remains")
			}
			lastNumber++
			record := fmt.Sprintf("A%d [%s]: assumed %s", lastNumber, marker.ID, answer)
			if strings.TrimSpace(why) != "" {
				record += ", because " + why
			}
			added = append(added, assumption{marker: marker, answer: answer, record: record})
		}
		body := candidate.Issues[i].Body
		for j := len(added) - 1; j >= 0; j-- {
			body = body[:added[j].marker.Start] + added[j].answer + body[added[j].marker.End:]
		}
		for _, assumption := range added {
			body = appendAssumption(body, assumption.record)
		}
		candidate.Issues[i].Body = body
	}
	return candidate, nil
}

func appendAssumption(body, record string) string {
	hasDecisions := false
	for line := range strings.SplitSeq(body, "\n") {
		if strings.TrimSpace(line) == "## Decisions" {
			hasDecisions = true
			break
		}
	}
	if !hasDecisions {
		switch {
		case strings.HasSuffix(body, "\n\n"):
			body += "## Decisions\n"
		case strings.HasSuffix(body, "\n"):
			body += "\n## Decisions\n"
		default:
			body += "\n\n## Decisions\n"
		}
	} else if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	return body + record
}

func runAcceptRest(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("accept-rest", flag.ContinueOnError)
	flags.SetOutput(stderr)
	planPath := flags.String("plan", "", "base plan JSON")
	hash := flags.String("hash", "", "base plan hash")
	out := flags.String("out", "", "write the checked plan to this file")
	file := flags.Bool("file", false, "file the checked first issue through local issue_file")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *planPath == "" || *hash == "" {
		return errors.New("accept-rest requires --plan and --hash")
	}
	base, err := readPlan(*planPath)
	if err != nil {
		return err
	}
	candidate, err := acceptRestCandidate(base)
	if err != nil {
		return err
	}
	toolArgs := map[string]any{"plan": base, "hash": *hash}
	if !reflect.DeepEqual(base, candidate) {
		toolArgs["candidate"] = candidate
	}
	client, err := newScenarioClient(*file, stderr)
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()
	raw, err := client.Call(ctx, "plan_check", toolArgs)
	if err != nil {
		return err
	}
	result, err := decodePlan(raw)
	if err != nil {
		return err
	}
	if err := printPlan(stdout, result, *out); err != nil {
		return err
	}
	if !*file {
		return nil
	}
	if len(result.Plan.Issues) == 0 {
		return errors.New("checked plan has no issue to file")
	}
	issue := result.Plan.Issues[0]
	raw, err = client.Call(ctx, "issue_file", map[string]any{
		"repo": result.Plan.Repository, "title": issue.Title, "body": issue.Body,
		"plan": result.Plan, "hash": result.Hash,
	})
	if err != nil {
		return err
	}
	return printStructured(stdout, raw)
}

func runAnswer(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("answer", flag.ContinueOnError)
	flags.SetOutput(stderr)
	repo := flags.String("repo", "", "owner/repository")
	issue := flags.Int("issue", 0, "issue number")
	room := flags.String("room", "", "room id")
	answer := flags.String("answer", "", "one-line answer")
	conflictBody := flags.String("conflict-body-file", "", "replace the body after the form arrives")
	conflictLabel := flags.String("conflict-label", "", "add a label after the form arrives")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *repo == "" || *issue < 1 || *answer == "" {
		return errors.New("answer requires --repo, a positive --issue and --answer")
	}
	client, err := newScenarioClient(true, stderr)
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()
	client.Answer = *answer
	client.BeforeAnswer = func(ctx context.Context) error {
		if *conflictBody != "" {
			body, err := readLimitedFile(*conflictBody)
			if err != nil {
				return err
			}
			if err := runGH(ctx, bytes.NewReader(body), io.Discard, io.Discard,
				"issue", "edit", strconv.Itoa(*issue), "--repo="+*repo, "--body-file=-"); err != nil {
				return err
			}
		}
		if *conflictLabel != "" {
			if err := runGH(ctx, strings.NewReader(""), io.Discard, io.Discard,
				"issue", "edit", strconv.Itoa(*issue), "--repo="+*repo, "--add-label="+*conflictLabel); err != nil {
				return err
			}
		}
		return nil
	}
	raw, err := client.Call(ctx, "need_answer", map[string]any{"repo": *repo, "issue": *issue, "room": *room})
	if err != nil {
		return err
	}
	return printStructured(stdout, raw)
}

func runStatus(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("status", flag.ContinueOnError)
	flags.SetOutput(stderr)
	room := flags.String("room", "", "room id")
	repo := flags.String("repo", "", "owner/repository")
	issue := flags.Int("issue", 0, "issue number")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("status takes no positional arguments")
	}
	if *repo != "" || *issue != 0 {
		if *repo == "" || *issue < 1 || *room != "" {
			return errors.New("status issue mode requires --repo and --issue without --room")
		}
		var out bytes.Buffer
		if err := runGH(ctx, strings.NewReader(""), &out, stderr,
			"issue", "view", strconv.Itoa(*issue), "--repo="+*repo, "--json", "body,labels,updatedAt,comments"); err != nil {
			return err
		}
		return printStructured(stdout, json.RawMessage(out.Bytes()))
	}
	client, err := newScenarioClient(false, stderr)
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()
	tool, toolArgs := "rooms_list", map[string]any{}
	if *room != "" {
		tool, toolArgs = "room_brief", map[string]any{"room": *room}
	}
	raw, err := client.Call(ctx, tool, toolArgs)
	if err != nil {
		return err
	}
	return printStructured(stdout, raw)
}

func runGH(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, args ...string) error {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gh", args...) //nolint:gosec // fixed executable and argument forms, never a shell
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("gh %s: %w", strings.Join(args, " "), err)
	}
	return nil
}

func oneLine(value string) string {
	return strings.NewReplacer("\r", `\r`, "\n", `\n`).Replace(value)
}

func printStructured(stdout io.Writer, raw json.RawMessage) error {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return fmt.Errorf("decode structured output: %w", err)
	}
	lines := make(map[string]string)
	flatten("", value, lines)
	keys := make([]string, 0, len(lines))
	for key := range lines {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		_, _ = fmt.Fprintf(stdout, "%s=%s\n", key, lines[key])
	}
	return nil
}

func flatten(prefix string, value any, out map[string]string) {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			next := key
			if prefix != "" {
				next = prefix + "." + key
			}
			flatten(next, child, out)
		}
	case []any:
		for i, child := range typed {
			flatten(prefix+"."+strconv.Itoa(i), child, out)
		}
	case string:
		out[prefix] = oneLine(typed)
	default:
		raw, _ := json.Marshal(typed)
		out[prefix] = string(raw)
	}
}
