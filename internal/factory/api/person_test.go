// SPDX-License-Identifier: Apache-2.0

package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/Smana/maku/internal/authn"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/ledger"
	"github.com/Smana/maku/internal/factory/runs"
)

const alice = "github:alice"

// logins is D1's identity link as the test sees it: sub-bearing principal id → GitHub login.
type personLinks struct {
	by  map[string]string
	err error
}

func (l personLinks) Person(_ context.Context, p authn.Principal, _ string) (string, error) {
	if l.err != nil {
		return "", l.err
	}
	if login, ok := l.by[p.ID]; ok {
		return "github:" + login, nil
	}
	return "", nil
}

func personServer(t *testing.T, st *store, enabled, enforce bool, people PersonResolver, objs ...client.Object) *Server {
	t.Helper()
	c := fake.NewClientBuilder().WithScheme(scheme()).WithObjects(objs...).Build()
	s := mkServer(t, st, c, enforce, false, "7f3cq2xz", 5_000_000)
	s.Cfg.Current().Budgets.PerPerson = config.PerPerson{Enabled: enabled, DailyUSD: 5}
	s.People = people
	return s
}

// D1 + D4 on the API: alice's labels and her API runs are one key. Her day spent through labels
// refuses her API run with 429 person_budget_exhausted (counted); off, nothing changes; in shadow
// the run is admitted.
func TestTheAPIChargesThePersonOfTheToken(t *testing.T) {
	people := personLinks{by: map[string]string{"human:291": "alice"}}
	spent := func() *corev1.ConfigMap { return ledgerCM("20260927", map[string]int64{alice: 4_000_000}) }
	t.Run("off: the human key, as before", func(t *testing.T) {
		st := &store{}
		s := personServer(t, st, false, true, people, spent())
		if w := post(s.Handler(), "alice", base()); w.Code != http.StatusCreated {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
		if r := entry(t, s, "7f3cq2xz"); r != (ledger.Reservation{Principal: "human:291", MaxMicroUSD: 3_400_000, Repository: "Smana/cloud-native-ref", Role: "implementer"}) || st.created[0].ChargedTo != "" {
			t.Fatalf("%+v %+v", r, st.created[0])
		}
	})
	t.Run("on, enforced: refused at the door", func(t *testing.T) {
		st := &store{}
		s := personServer(t, st, true, true, people, spent())
		w := post(s.Handler(), "alice", base())
		if w.Code != http.StatusTooManyRequests || !strings.Contains(w.Body.String(), "person_budget_exhausted") || len(st.created) != 0 {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
	})
	t.Run("on, fits: one entry, the fleet and alice", func(t *testing.T) {
		st := &store{}
		s := personServer(t, st, true, true, people)
		if w := post(s.Handler(), "alice", base()); w.Code != http.StatusCreated {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
		if r := entry(t, s, "7f3cq2xz"); r != (ledger.Reservation{Principal: runs.PrincipalFactory, Person: alice, MaxMicroUSD: 3_400_000, Repository: "Smana/cloud-native-ref", Role: "implementer"}) ||
			st.created[0].ChargedTo != alice || st.created[0].Principal != "human:291" {
			t.Fatalf("the gateway principal stays the token's, the charge is alice's: %+v %+v", r, st.created[0])
		}
	})
	t.Run("on, shadow: admitted", func(t *testing.T) {
		st := &store{}
		s := personServer(t, st, true, false, people, spent())
		if w := post(s.Handler(), "alice", base()); w.Code != http.StatusCreated {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
	})
	t.Run("on, no link: the sub is the person", func(t *testing.T) {
		st := &store{}
		s := personServer(t, st, true, true, personLinks{})
		if w := post(s.Handler(), "alice", base()); w.Code != http.StatusCreated {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
		if r := entry(t, s, "7f3cq2xz"); r.Person != "human:291" || st.created[0].ChargedTo != "human:291" {
			t.Fatalf("%+v", r)
		}
	})
	t.Run("on, the link unreadable: 503, nothing reserved", func(t *testing.T) {
		st := &store{}
		s := personServer(t, st, true, true, personLinks{err: errors.New("zitadel down")})
		if w := post(s.Handler(), "alice", base()); w.Code != http.StatusServiceUnavailable ||
			!strings.Contains(w.Body.String(), "identity_unavailable") || len(st.created) != 0 {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
	})
}

func entry(t *testing.T, s *Server, id string) ledger.Reservation {
	t.Helper()
	var cm corev1.ConfigMap
	if err := s.Rooms.Get(t.Context(), types.NamespacedName{Namespace: "agent-system", Name: ledger.Prefix + "20260927"}, &cm); err != nil {
		t.Fatal(err)
	}
	var r ledger.Reservation
	if err := json.Unmarshal([]byte(cm.Data[ledger.Reserved+id]), &r); err != nil {
		t.Fatalf("%s: %v", id, err)
	}
	return r
}
