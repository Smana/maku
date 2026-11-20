// SPDX-License-Identifier: Apache-2.0

package humanapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Smana/maku/internal/authn"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/runrequest"
	"github.com/Smana/maku/internal/wire"
)

type cancelCall struct {
	task, token string
	discard     bool
}

// fakeCancels answers each cancel with result, or err, and records it.
type fakeCancels struct {
	calls  []cancelCall
	result string
	err    error
}

func (f *fakeCancels) CancelTask(_ context.Context, task string, discard bool, token string) (string, error) {
	f.calls = append(f.calls, cancelCall{task, token, discard})
	return f.result, f.err
}

func cancelAct(a *Actor, who string, action Action) wire.ServerFrame {
	_, _, room := fixture("system:factory")
	raw, _ := json.Marshal(action)
	p := authn.Principal{Kind: envelope.ActorHuman, ID: who, Groups: []string{"agents-member"}, AccessToken: "tok-" + who}
	return a.Handle(context.Background(), p, true, "s1", room, wire.ClientFrame{Type: "act", ClientSeq: 1, Action: raw})
}

// #644: cancel is the owner's act (as close is), forwarded with the owner's own token; the
// factory's refusal is the ack's.
func TestCancelActIsTheOwnersAndForwarded(t *testing.T) {
	a, _, _ := fixture("system:factory")
	fc := &fakeCancels{result: "discarded"}
	a.Cancels = fc
	if f := cancelAct(a, "human:col", Action{Kind: "cancel"}); f.Rejected != "not_permitted" || len(fc.calls) != 0 {
		t.Fatalf("a collaborator cancels nothing: %+v %v", f, fc.calls)
	}
	if f := cancelAct(a, "human:own", Action{Kind: "cancel", Discard: true}); f.Rejected != "" {
		t.Fatalf("%+v", f)
	}
	if c := fc.calls; len(c) != 1 || c[0] != (cancelCall{"3kq7x2ma", "tok-human:own", true}) {
		t.Fatalf("calls %+v", c)
	}
	fc.result = "not_maintainer"
	if f := cancelAct(a, "human:own", Action{Kind: "cancel"}); f.Rejected != "not_maintainer" {
		t.Fatalf("the factory's refusal: %+v", f)
	}
	fc.err = runrequest.ErrRefused{Status: http.StatusForbidden, Reason: "not_linked"}
	if f := cancelAct(a, "human:own", Action{Kind: "cancel"}); f.Rejected != "not_linked" {
		t.Fatalf("the factory's 403: %+v", f)
	}
	a.Cancels = nil
	if f := cancelAct(a, "human:own", Action{Kind: "cancel"}); f.Rejected != "factory_unavailable" {
		t.Fatalf("no factory: %+v", f)
	}
}
