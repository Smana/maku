// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/hardening"
	"github.com/Smana/maku/internal/policy"
	"github.com/Smana/maku/internal/runwatch"
)

type memBundles struct {
	got  map[string][]byte
	room string
	err  error
}

func (m *memBundles) PutBundle(_ context.Context, room, run, digest string, b []byte) error {
	if m.err != nil {
		return m.err
	}
	if m.got == nil {
		m.got = map[string][]byte{}
	}
	m.got[run+"/"+digest] = b
	m.room = room
	return nil
}

func outputTools(log Log, b BundleStore) []Tool {
	return OutputTools(log, fakeRedactor{}, time.Now, OutputConfig{LabelAllowlist: []string{"documentation"}, Bundles: b})
}

func lastOutput(t *testing.T, log *memLog) (envelope.Draft, envelope.MessagePayload) {
	t.Helper()
	d := log.drafts[len(log.drafts)-1]
	var p envelope.MessagePayload
	if err := json.Unmarshal(d.Payload, &p); err != nil || p.Output == nil {
		t.Fatalf("%s: %v", d.Payload, err)
	}
	return d, p
}

// UH3, UH4: one output_* tool per type, for the implementer and the tester only, each a chat-level
// append: the run requests, the factory decides.
func TestOutputToolsVocabulary(t *testing.T) {
	ts := outputTools(&memLog{}, nil)
	var names []string
	for _, tl := range ts {
		names = append(names, tl.Name)
		// HR22: output_push alone is the harness's, as room_judge is.
		if !slices.Equal(tl.Roles, []string{"implementer", "tester"}) || tl.Action != policy.Chat || tl.HarnessOnly != (tl.Name == "output_push") {
			t.Errorf("%s: roles %v action %s", tl.Name, tl.Roles, tl.Action)
		}
		var schema map[string]any
		if json.Unmarshal(tl.InputSchema, &schema) != nil || schema["additionalProperties"] != false {
			t.Errorf("%s: schema %s", tl.Name, tl.InputSchema)
		}
	}
	if !slices.Equal(names, []string{"output_push", "output_pr", "output_comment", "output_labels", "output_issue"}) {
		t.Fatalf("%v", names)
	}
}

// A request is recorded, never applied: output_requested with an id, the digest of what is stored
// and, for every type but push, the redacted payload itself.
func TestOutputRequestRecorded(t *testing.T) {
	log := &memLog{}
	out, err := tool(outputTools(log, nil), "output_comment").Call(t.Context(), impl,
		json.RawMessage(`{"target":"pr","body":"Done; token SECRET removed."}`))
	if err != nil {
		t.Fatal(err)
	}
	d, p := lastOutput(t, log)
	r := p.Output
	if d.Type != envelope.Message || p.Kind != envelope.KindOutputRequested || r.Type != "comment" ||
		r.Validate(envelope.KindOutputRequested) != nil || !slices.Equal(d.Redactions, []string{"github-pat"}) {
		t.Fatalf("%+v %+v", d, r)
	}
	if strings.Contains(string(r.Payload), "SECRET") || r.Digest != hardening.Digest(r.Payload) {
		t.Fatalf("stored %s digest %s", r.Payload, r.Digest)
	}
	res := out.(map[string]any)
	if res["outputId"] != r.ID || res["status"] != "requested" || res["seq"] != int64(1) {
		t.Fatalf("result %+v", res)
	}
}

// T1's schema half answers at once with the bounded reason; nothing is recorded.
func TestOutputToolRefusesSchema(t *testing.T) {
	for name, tc := range map[string]struct{ tool, args string }{
		"a label off the list":   {"output_labels", `{"add":["factory/live"]}`},
		"a pr body too short":    {"output_pr", `{"title":"t","body":"short"}`},
		"another comment target": {"output_comment", `{"target":"o/r#1","body":"x"}`},
		"an issue off template":  {"output_issue", `{"title":"x","body":"y"}`},
		"an unknown field":       {"output_pr", `{"title":"t","body":"` + strings.Repeat("b", 20) + `","base":"main"}`},
	} {
		t.Run(name, func(t *testing.T) {
			log := &memLog{}
			_, err := tool(outputTools(log, nil), tc.tool).Call(t.Context(), impl, json.RawMessage(tc.args))
			var ae argError
			if !errors.As(err, &ae) || !strings.Contains(err.Error(), string(hardening.RefuseSchema)) || len(log.drafts) != 0 {
				t.Fatalf("%v, %d drafts", err, len(log.drafts))
			}
		})
	}
}

// A request whose stored record would pass the room log's payload cap is refused, never truncated.
func TestOutputToolRefusesOversize(t *testing.T) {
	log := &memLog{}
	body := strings.Repeat("<", 20000) // within 65,000 characters, six bytes each as stored
	_, err := tool(outputTools(log, nil), "output_pr").Call(t.Context(), impl, json.RawMessage(`{"title":"t","body":"`+body+`"}`))
	if err == nil || !strings.Contains(err.Error(), "65536") || len(log.drafts) != 0 {
		t.Fatalf("%v", err)
	}
}

// A push hands the bundle to the store under its digest; the record holds the digest and size.
func TestOutputPush(t *testing.T) {
	bundle := []byte("# v2 git bundle\n")
	args := json.RawMessage(`{"bundle":"` + base64.StdEncoding.EncodeToString(bundle) + `"}`)
	log, store := &memLog{}, &memBundles{}
	if _, err := tool(outputTools(log, store), "output_push").Call(t.Context(), impl, args); err != nil {
		t.Fatal(err)
	}
	_, p := lastOutput(t, log)
	if p.Output.Payload != nil || p.Output.Bytes != len(bundle) || p.Output.Digest != hardening.Digest(bundle) ||
		string(store.got[impl.Run.ID+"/"+p.Output.Digest]) != string(bundle) {
		t.Fatalf("%+v %v", p.Output, store.got)
	}
	// No store: push outputs are not served, and nothing is recorded.
	log = &memLog{}
	if _, err := tool(outputTools(log, nil), "output_push").Call(t.Context(), impl, args); err == nil || len(log.drafts) != 0 {
		t.Fatalf("%v", err)
	}
	// A store failure records nothing either.
	if _, err := tool(outputTools(log, &memBundles{err: errors.New("disk")}), "output_push").Call(t.Context(), impl, args); err == nil || len(log.drafts) != 0 {
		t.Fatalf("%v", err)
	}
}

// Stage H0: the room tools the server lists are unchanged; the hardening tools are built apart.
func TestRoomToolsUnchanged(t *testing.T) {
	for _, tl := range RoomTools(&memLog{}, fakeRedactor{}, time.Now) {
		if strings.HasPrefix(tl.Name, "output_") || strings.HasPrefix(tl.Name, "forge_") {
			t.Fatalf("%s is served in H0", tl.Name)
		}
	}
}

// gatedServer is the broker's :8090 as app builds it: the room tools and the output tools, with
// one implementer run annotated with ann (nil: no annotation).
func gatedServer(t *testing.T, ann map[string]any) (*Server, *memLog) {
	t.Helper()
	w := runwatch.New(runwatch.DefaultKind)
	w.Upsert(t.Context(), claim("7f3cq2xz", "implementer", "3kq7x2ma", "Running", ann))
	log := &memLog{}
	s, _ := testServer(w, newClock())
	s.Tools = append(RoomTools(log, fakeRedactor{}, time.Now), OutputTools(log, fakeRedactor{}, time.Now, OutputConfig{})...)
	return s, log
}

func listed(t *testing.T, s *Server) []string {
	t.Helper()
	var res struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	b, _ := json.Marshal(rpc(t, s, "k", sub, "tools/list", nil).out["result"])
	if err := json.Unmarshal(b, &res); err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, tl := range res.Tools {
		out = append(out, tl.Name)
	}
	return out
}

// HR14: a run lists and may call exactly the output tools its claim names; a run with none lists
// exactly the room tools it lists today. output_push is never served without a bundle store.
func TestToolGate(t *testing.T) {
	plain, _ := gatedServer(t, nil)
	want := listed(t, plain)
	for _, n := range want {
		if strings.HasPrefix(n, "output_") {
			t.Fatalf("an unannotated run lists %s", n)
		}
	}
	today, _ := testServer(watcher(t, "implementer"), newClock())
	today.Tools = RoomTools(&memLog{}, fakeRedactor{}, time.Now)
	if got := listed(t, today); !slices.Equal(got, want) {
		t.Fatalf("an unannotated run's list changed: %v, today %v", want, got)
	}

	s, log := gatedServer(t, map[string]any{runwatch.SafeOutputsAnnotation: "comment,pr,push",
		runwatch.LabelAllowlistAnnotation: `["documentation"]`})
	got := listed(t, s)
	for _, n := range []string{"output_pr", "output_comment"} {
		if !slices.Contains(got, n) {
			t.Errorf("%s not listed: %v", n, got)
		}
	}
	for _, n := range []string{"output_labels", "output_issue", "output_push"} {
		if slices.Contains(got, n) {
			t.Errorf("%s listed: %v", n, got)
		}
		if _, text := call(t, s, sub, n); !strings.HasPrefix(text, "not_permitted") {
			t.Errorf("%s callable: %q", n, text)
		}
	}
	if len(log.drafts) != 0 {
		t.Fatalf("an ungated call recorded %d drafts", len(log.drafts))
	}
	res, _ := rpc(t, s, "k", sub, "tools/call", map[string]any{"name": "output_comment",
		"arguments": map[string]any{"target": "pr", "body": "Done."}}).out["result"].(map[string]any)
	if res == nil || res["isError"] == true || len(log.drafts) != 1 {
		t.Fatalf("output_comment: %+v, %d drafts", res, len(log.drafts))
	}
}

// The allowlist a gated run's output_labels checks is the one on its claim, not the broker's.
func TestOutputLabelsReadTheRunsAllowlist(t *testing.T) {
	labels := tool(OutputTools(&memLog{}, fakeRedactor{}, time.Now, OutputConfig{LabelAllowlist: []string{"broker-side"}}), "output_labels")
	run := impl
	run.Run.SafeOutputs, run.Run.LabelAllowlist = []string{"labels"}, []string{"documentation"}
	if _, err := labels.Call(t.Context(), run, json.RawMessage(`{"add":["documentation"]}`)); err != nil {
		t.Fatalf("the run's label: %v", err)
	}
	if _, err := labels.Call(t.Context(), run, json.RawMessage(`{"add":["broker-side"]}`)); err == nil {
		t.Fatal("the broker's list served a gated run")
	}
}

type fakeForge struct {
	res         hardening.ForgeReadResult
	err         error
	run, target string
}

func (f *fakeForge) ForgeRead(_ context.Context, run, target string) (hardening.ForgeReadResult, error) {
	f.run, f.target = run, target
	return f.res, f.err
}

// HR35: forge_read forwards the verified caller's run and the target to the factory, which reads
// and filters; the broker returns its answer as is and never reads a run from the arguments.
func TestForgeReadToolForwards(t *testing.T) {
	want := hardening.ForgeReadResult{Items: []hardening.ForgeItem{{Kind: "comment", Author: "smana", Trust: hardening.Approved, Body: "fix it"}},
		Withheld: "[1 item withheld: author trust]", InputTrust: hardening.InputTrust{Min: hardening.Approved, Withheld: map[hardening.SourceKind]int{hardening.SourceComment: 1}}}
	f := &fakeForge{res: want}
	tl := ForgeReadTool(f, nil, time.Now)
	if tl.Name != "forge_read" || tl.Action != policy.Read || !slices.Equal(tl.Roles, allRoles) {
		t.Fatalf("%+v", tl)
	}
	out, err := tl.Call(t.Context(), impl, json.RawMessage(`{"target":"issue"}`))
	if err != nil {
		t.Fatal(err)
	}
	if f.run != impl.Run.ID || f.target != "issue" {
		t.Fatalf("forwarded %q %q", f.run, f.target)
	}
	if got, _ := json.Marshal(out); string(got) != string(envelope.Must(want)) {
		t.Fatalf("%s", got)
	}
	for _, args := range []string{`{}`, `{"target":"o/r#3"}`, `{"target":"issue","run":"other"}`, `{"target":"issue","repo":"x/y"}`} {
		if _, err := tl.Call(t.Context(), impl, json.RawMessage(args)); err == nil {
			t.Errorf("%s accepted", args)
		}
	}
	if _, err := ForgeReadTool(&fakeForge{err: ErrReadFilterOff}, nil, time.Now).Call(t.Context(), impl, json.RawMessage(`{"target":"pr"}`)); !errors.Is(err, ErrReadFilterOff) {
		t.Fatalf("the factory's refusal: %v", err)
	}
}

// HR35, HR36: forge_read is listed only for a run annotated forge-api: deny, and each call appends
// one input_read the broker stamps, counts and levels only, never the text.
func TestForgeReadServed(t *testing.T) {
	tl := ForgeReadTool(&fakeForge{}, nil, time.Now)
	if tl.servedTo(impl.Run, false) {
		t.Fatal("served to a run whose sandbox may still reach api.github.com")
	}
	denied := impl
	denied.Run.ForgeAPIDenied = true
	if !tl.servedTo(denied.Run, false) {
		t.Fatal("not served to a forge-api: deny run")
	}
	log := &memLog{}
	f := &fakeForge{res: hardening.ForgeReadResult{Items: []hardening.ForgeItem{{Kind: "comment", Trust: hardening.Approved, Body: "visible text"}},
		InputTrust: hardening.InputTrust{Min: hardening.Approved, Withheld: map[hardening.SourceKind]int{hardening.SourceComment: 2},
			Sources: []hardening.Source{{Kind: hardening.SourceComment, Level: hardening.Approved}}}}}
	if _, err := ForgeReadTool(f, log, time.Now).Call(t.Context(), denied, json.RawMessage(`{"target":"pr"}`)); err != nil {
		t.Fatal(err)
	}
	if len(log.drafts) != 1 {
		t.Fatalf("%d records", len(log.drafts))
	}
	d := log.drafts[0]
	if d.Actor.ID != envelope.BrokerActor || d.Origin != envelope.OriginBroker || d.RunID != denied.Run.ID || bytes.Contains(d.Payload, []byte("visible text")) {
		t.Fatalf("%+v %s", d, d.Payload)
	}
	got, ok := envelope.ReadInput(envelope.Event{Type: d.Type, RunID: d.RunID, Origin: d.Origin, Actor: d.Actor, Payload: d.Payload}, denied.Run.ID)
	if !ok || got.Target != "pr" || got.Min != "approved" || got.Withheld["comment"] != 2 || len(got.Sources) != 1 {
		t.Fatalf("%+v %v", got, ok)
	}
}

// memAnswers serves OutputEvents from fixed events, filtered as the store filters them.
type memAnswers struct {
	evs  []envelope.Event
	err  error
	asks []string
}

func (m *memAnswers) OutputEvents(_ context.Context, room, run, id string) ([]envelope.Event, error) {
	m.asks = append(m.asks, room+"/"+run+"/"+id)
	var out []envelope.Event
	for _, e := range m.evs {
		var p envelope.MessagePayload
		if e.RoomID == room && e.RunID == run && json.Unmarshal(e.Payload, &p) == nil && p.Output != nil && p.Output.ID == id {
			out = append(out, e)
		}
	}
	return out, m.err
}

// harnessServer is the broker's :8090 with a bundle store and the answers reader, for one
// implementer run of Smana/demo annotated with outputs, on a broker whose config lets push serve
// Smana/demo.
func harnessServer(t *testing.T, outputs string, answers OutputAnswers) (*Server, *memLog, *memBundles) {
	return harnessServerFor(t, outputs, answers, []string{"Smana/demo"})
}

// harnessServerFor is harnessServer with the repositories the broker's config serves push to.
func harnessServerFor(t *testing.T, outputs string, answers OutputAnswers, push []string) (*Server, *memLog, *memBundles) {
	t.Helper()
	w := runwatch.New(runwatch.DefaultKind)
	ann := map[string]any{}
	if outputs != "" {
		ann[runwatch.SafeOutputsAnnotation] = outputs
	}
	c := claim("7f3cq2xz", "implementer", "3kq7x2ma", "Running", ann)
	c.Object["spec"].(map[string]any)["repository"] = "Smana/demo"
	w.Upsert(t.Context(), c)
	log, bundles := &memLog{}, &memBundles{}
	s, _ := testServer(w, newClock())
	s.Tools = append(RoomTools(log, fakeRedactor{}, time.Now),
		OutputTools(log, fakeRedactor{}, time.Now, OutputConfig{Bundles: bundles, Answers: answers, PushRepositories: push})...)
	return s, log, bundles
}

// harnessRPC posts method as the run's harness, with HarnessHeader when harness is set.
func harnessRPC(t *testing.T, s *Server, harness bool, body []byte) response {
	t.Helper()
	r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/mcp", bytes.NewReader(body))
	r.Header.Set(KeyHeader, "k")
	r.Header.Set(IdentityHeader, sub)
	if harness {
		r.Header.Set(HarnessHeader, "1")
	}
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, r)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return response{status: rec.Code, body: rec.Body.Bytes(), out: out}
}

func toolsCall(name string, args any) []byte {
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": name, "arguments": args}})
	return b
}

func listBody() []byte { return []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`) }

// HR22: output_push is the harness's alone, served only to a run whose claim names push and only
// with a bundle store; the stored record holds the bundle's digest and size, never its bytes.
func TestOutputPushHarnessOnly(t *testing.T) {
	bundle := []byte("# v2 git bundle\nharness\n")
	args := map[string]string{"bundle": base64.StdEncoding.EncodeToString(bundle)}

	s, log, _ := harnessServer(t, "pr,push", nil)
	if l := harnessRPC(t, s, false, listBody()); strings.Contains(string(l.body), "output_push") {
		t.Fatalf("the model lists output_push: %s", l.body)
	}
	if l := harnessRPC(t, s, true, listBody()); !strings.Contains(string(l.body), "output_push") {
		t.Fatalf("the harness does not list output_push: %s", l.body)
	}
	res, _ := harnessRPC(t, s, false, toolsCall("output_push", args)).out["result"].(map[string]any)
	if res["isError"] != true || len(log.drafts) != 0 {
		t.Fatalf("the model pushed: %v", res)
	}
	s, log, bundles := harnessServer(t, "pr,push", nil) // a fresh limiter
	res, _ = harnessRPC(t, s, true, toolsCall("output_push", args)).out["result"].(map[string]any)
	if res == nil || res["isError"] == true {
		t.Fatalf("the harness was refused: %v", res)
	}
	_, p := lastOutput(t, log)
	if p.Output.Digest != hardening.Digest(bundle) || p.Output.Bytes != len(bundle) || p.Output.Payload != nil ||
		string(bundles.got["7f3cq2xz/"+p.Output.Digest]) != string(bundle) || bundles.room != "3kq7x2ma" {
		t.Fatalf("%+v %v", p.Output, bundles.got)
	}

	// A run whose claim does not name push lists and calls nothing, even as the harness.
	s, log, _ = harnessServer(t, "pr", nil)
	if l := harnessRPC(t, s, true, listBody()); strings.Contains(string(l.body), "output_push") {
		t.Fatalf("listed without push: %s", l.body)
	}
	if res, _ := harnessRPC(t, s, true, toolsCall("output_push", args)).out["result"].(map[string]any); res["isError"] != true || len(log.drafts) != 0 {
		t.Fatalf("called without push: %v", res)
	}
	// The annotation alone serves nothing: the broker's config must name the run's repository too.
	for name, push := range map[string][]string{"no repository": nil, "another repository": {"Smana/other"}} {
		t.Run(name, func(t *testing.T) {
			s, log, _ := harnessServerFor(t, "pr,push", nil, push)
			if l := harnessRPC(t, s, true, listBody()); strings.Contains(string(l.body), "output_push") {
				t.Fatalf("listed without the config: %s", l.body)
			}
			if res, _ := harnessRPC(t, s, true, toolsCall("output_push", args)).out["result"].(map[string]any); res["isError"] != true || len(log.drafts) != 0 {
				t.Fatalf("called without the config: %v", res)
			}
		})
	}
}

// HR22: the harness may send a request up to maxHarnessBody, for output_push alone; every other
// request keeps the 128 KiB cap.
func TestHarnessBodyCap(t *testing.T) {
	bundle := bytes.Repeat([]byte{0x5a}, 8<<20-64<<10)
	push := toolsCall("output_push", map[string]string{"bundle": base64.StdEncoding.EncodeToString(bundle)})
	if len(push) < 10<<20 {
		t.Fatalf("the fixture is %d bytes", len(push))
	}
	message := toolsCall("room_message", map[string]string{"text": strings.Repeat("a", 130<<10)})

	s, log, _ := harnessServer(t, "push", nil)
	if res, _ := harnessRPC(t, s, true, push).out["result"].(map[string]any); res == nil || res["isError"] == true || len(log.drafts) != 1 {
		t.Fatalf("the harness's push: %v, %d drafts", res, len(log.drafts))
	}
	for name, tc := range map[string]struct {
		harness bool
		body    []byte
	}{
		"a harness room_message over 128 KiB": {true, message},
		"a model request over 128 KiB":        {false, message},
		"a model output_push over 128 KiB":    {false, push},
		"a harness request over 12 MiB":       {true, append(bytes.Repeat([]byte(" "), maxHarnessBody), push...)},
	} {
		t.Run(name, func(t *testing.T) {
			s, log, _ := harnessServer(t, "push", nil)
			r := harnessRPC(t, s, tc.harness, tc.body)
			if r.status != http.StatusRequestEntityTooLarge || !strings.Contains(string(r.body), "request too large") || len(log.drafts) != 0 {
				t.Fatalf("%d %.200s, %d drafts", r.status, r.body, len(log.drafts))
			}
		})
	}
}

// output_status answers the caller's own output from the room log, trusting the stamp: requested,
// waiting with its approval, applied with its object, refused with its reason; anything else is
// unknown. It is served to every run with safe outputs, the model included.
func TestOutputStatus(t *testing.T) {
	const run, room, id = "7f3cq2xz", "3kq7x2ma", "0123456789abcdef"
	d := strings.Repeat("a", 64)
	agent := envelope.Actor{Kind: envelope.ActorAgent, ID: "agent:" + run}
	factory := envelope.Actor{Kind: envelope.ActorSystem, ID: envelope.FactoryActor}
	ev := func(runID string, a envelope.Actor, k envelope.MessageKind, r envelope.OutputRecord) envelope.Event {
		return envelope.Event{RoomID: room, RunID: runID, Type: envelope.Message, Actor: a, Origin: envelope.OriginClient,
			Payload: envelope.Must(envelope.MessagePayload{Kind: k, Delivery: envelope.DeliveryNone, Output: &r})}
	}
	req := ev(run, agent, envelope.KindOutputRequested, envelope.OutputRecord{ID: id, Type: "push", Digest: d})
	waiting := ev(run, factory, envelope.KindOutputWaiting, envelope.OutputRecord{ID: id, Type: "push", Digest: d,
		Reason: envelope.WaitingReason, ApprovalID: "01JC0000000000000000000000"})
	applied := ev(run, factory, envelope.KindOutputApplied, envelope.OutputRecord{ID: id, Type: "push", Digest: d,
		Object: "https://github.com/o/r/commit/abc"})
	refused := ev(run, factory, envelope.KindOutputRefused, envelope.OutputRecord{ID: id, Type: "push", Digest: d, Reason: "approval_denied"})
	forged := ev(run, agent, envelope.KindOutputApplied, envelope.OutputRecord{ID: id, Type: "push", Digest: d,
		Object: "https://github.com/o/r/commit/evil"})
	for _, tc := range []struct {
		name string
		evs  []envelope.Event
		want map[string]any
	}{
		{"requested", []envelope.Event{req}, map[string]any{"outputId": id, "status": "requested"}},
		{"waiting", []envelope.Event{req, waiting}, map[string]any{"outputId": id, "status": "waiting", "approvalId": "01JC0000000000000000000000"}},
		{"applied after waiting", []envelope.Event{req, waiting, applied}, map[string]any{"outputId": id, "status": "applied",
			"object": "https://github.com/o/r/commit/abc"}},
		{"refused", []envelope.Event{req, waiting, refused}, map[string]any{"outputId": id, "status": "refused", "reason": "approval_denied"}},
		{"an answer the run forged", []envelope.Event{req, forged}, map[string]any{"outputId": id, "status": "requested"}},
		{"another run's output", []envelope.Event{ev("aaaaaaaa", envelope.Actor{Kind: envelope.ActorAgent, ID: "agent:aaaaaaaa"},
			envelope.KindOutputRequested, envelope.OutputRecord{ID: id, Type: "push", Digest: d})}, map[string]any{"outputId": id, "status": "unknown"}},
		{"nothing", nil, map[string]any{"outputId": id, "status": "unknown"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			answers := &memAnswers{evs: tc.evs}
			got, err := tool(OutputTools(&memLog{}, fakeRedactor{}, time.Now, OutputConfig{Answers: answers}), "output_status").
				Call(t.Context(), impl, json.RawMessage(`{"outputId":"`+id+`"}`))
			if err != nil {
				t.Fatal(err)
			}
			if b, w := envelope.Must(got), envelope.Must(tc.want); string(b) != string(w) {
				t.Fatalf("%s, want %s", b, w)
			}
			if !slices.Equal(answers.asks, []string{room + "/" + run + "/" + id}) {
				t.Fatalf("read %v: the caller's own run only", answers.asks)
			}
		})
	}
	st := tool(OutputTools(&memLog{}, fakeRedactor{}, time.Now, OutputConfig{Answers: &memAnswers{}}), "output_status")
	if st.HarnessOnly || st.Action != policy.Read {
		t.Fatalf("%+v", st)
	}
	for _, args := range []string{`{}`, `{"outputId":"x"}`, `{"outputId":"0123456789abcdef","run":"aaaaaaaa"}`} {
		if _, err := st.Call(t.Context(), impl, json.RawMessage(args)); err == nil {
			t.Errorf("%s accepted", args)
		}
	}
	if _, err := tool(OutputTools(&memLog{}, fakeRedactor{}, time.Now, OutputConfig{Answers: &memAnswers{err: errors.New("db")}}), "output_status").
		Call(t.Context(), impl, json.RawMessage(`{"outputId":"0123456789abcdef"}`)); err == nil {
		t.Fatal("a store error passed")
	}

	// Served to the model of every run with safe outputs, and to none without.
	s, _, _ := harnessServer(t, "comment", &memAnswers{})
	if l := harnessRPC(t, s, false, listBody()); !strings.Contains(string(l.body), "output_status") {
		t.Fatalf("not listed to the model: %s", l.body)
	}
	s, _, _ = harnessServer(t, "", &memAnswers{})
	if l := harnessRPC(t, s, true, listBody()); strings.Contains(string(l.body), "output_status") {
		t.Fatalf("listed to a run with no safe outputs: %s", l.body)
	}
}
