// SPDX-License-Identifier: Apache-2.0

// Package ledger is the factory's daily budget ledger (R50): one ConfigMap per UTC day in the
// factory's namespace, whose resourceVersion is the transaction, so two replicas cannot take one
// slot. Admission reserves a run's ceiling (Reserve) and drops it when the run ends (Release); the
// meter owns the spent and settled columns and stamps its heartbeat. The run-request API, the
// reconciler's Queued, intake's daily read and the meter all read and write the one encoding
// here. Every amount is micro-USD (TC6). It imports no other factory package.
package ledger

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Ledger layout (R50). One ConfigMap per UTC day, agent-factory-ledger-usd-<YYYYMMDD>, in the
// factory's namespace: the apiserver's resourceVersion is the transaction, so two replicas
// cannot take one slot. Every amount is micro-USD (truthful costs TC6). Exported so the meter
// reads and writes the same encoding: admission owns the reservations, the meter owns the spend.
const (
	Prefix = "agent-factory-ledger-usd-"
	// LegacyPrefix names the token ledgers the USD ones replaced (#155): nothing reads or
	// writes them; the meter's pruner deletes them once they are older than its retention.
	LegacyPrefix = "agent-factory-ledger-"
	// Spent is one JSON map, principal → micro-USD. It is a map rather than one key per
	// principal because principal ids carry ":", which no ConfigMap key may hold. The meter
	// owns this column; admission only reads it.
	Spent = "spent"
	// Reserved prefixes one key per admitted run — reserved.<runId>, whose C2 id is
	// key-safe — holding {principal, person, room, maxMicroUSD}. Release drops the entry when the
	// run ends or its claim goes (#55); the spend it made stays: deleting a run refunds nothing.
	Reserved = "reserved."
	// Settled prefixes one key per metered run, settled.<runId>: the micro-USD charged to
	// that run that day, written by the meter in the same write as spent. Per key, spent is the
	// sum of settled over the runs charged to it; summed over the retained days, it is how much
	// of a run any meter has charged, so a new leader charges neither twice nor less (TC6). One
	// exception: on the day a lifted run (R10) is first charged, its settled entry also holds the
	// lift, which spent never counted, so settled exceeds that run's share of spent by it.
	Settled = "settled."
	// DayLayout labels the ledger with the UTC day it counts.
	DayLayout = "20060102"
	// Attempts bounds the conflict-retry loop: steady contention of that depth is
	// failure, not patience.
	Attempts = 5
)

// Reservation is one ledger entry: what the run may spend, in micro-USD, on whose behalf and on
// which room. Person is the second key the same entry charges (v2 per-person caps D3): the person
// who caused the run, beside the fleet as Principal. One key per run is what makes both
// reservations one write: no run ever holds one without the other.
type Reservation struct {
	Principal   string `json:"principal"`
	Person      string `json:"person,omitempty"`
	Room        string `json:"room,omitempty"`
	MaxMicroUSD int64  `json:"maxMicroUSD"`
	// Repository and Role are what the run-request API admitted the run for: the run-token minter
	// mints an API run's token from these, never from its claim (v4 D10). A Task's runs leave them
	// empty: their Task binds them.
	Repository string `json:"repository,omitempty"`
	Role       string `json:"role,omitempty"`
}

// Reserve's refusals. Any other error is the ledger unread or unwritten.
var (
	ErrOverBudget = errors.New("ledger: over the daily budget")
	// ErrPersonOverBudget is the person's day, not the fleet's (D4): checked first, so a refusal
	// names whose cap it is.
	ErrPersonOverBudget = errors.New("ledger: over the person's daily budget")
	ErrRunBusy          = errors.New("ledger: the run id holds another reservation")
	ErrRoomBusy         = errors.New("ledger: a reservation holds the room")
	ErrBusy             = errors.New("ledger: busy")
)

// Reserve is R50's atomic admission, shared by the API and the factory's Queued (#78): read the
// UTC day of now's ledger, check spent + reserved + maxMicroUSD against limit and that no
// reservation holds mine.Room, then write reserved.<id> — the Update conflicts against a
// concurrent writer's, and the retry re-reads and re-checks, so exactly one admission per slot
// lands whatever the interleaving. Unless enforce (R3's shadow), an over day is reported as over,
// written, and never refused; room exclusion stays exact. An enforced cap also refuses while the
// meter is blind (#54): its spend column is then stale. Release drops the entry when the run ends.
//
// mine.Person, when set, is checked against personLimit in the same read and charged by the same
// write (D3). over names the key whose day the run does not fit, the person's first; "" fits.
func Reserve(ctx context.Context, c client.Client, ns string, now time.Time, id string, mine Reservation,
	limit, personLimit int64, enforce bool) (over string, err error) {
	if enforce {
		switch blind, err := meterBlind(ctx, c, ns, now); {
		case err != nil:
			return "", err
		case blind:
			return "", ErrMeterBlind
		}
	}
	name := Prefix + now.UTC().Format(DayLayout)
	key := types.NamespacedName{Namespace: ns, Name: name}
	for range Attempts {
		cm := &corev1.ConfigMap{}
		err := c.Get(ctx, key, cm)
		existed := true
		if apierrors.IsNotFound(err) {
			cm = &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}}
			existed = false
		} else if err != nil {
			return "", fmt.Errorf("ledger %s: %w", name, err)
		}
		spent, err := SpentColumn(cm)
		if err != nil {
			return "", err
		}
		reserved, err := ledgerReservations(cm)
		if err != nil {
			return "", err
		}
		// R48: the run id is the retry's identity. Our own equal entry means we already
		// landed this admission: pass without reserving twice. A different value under this
		// id is another run's slot, and overwriting it would unreserve theirs: refuse.
		if prev, ok := reserved[id]; ok {
			if prev == mine {
				return "", nil
			}
			return "", ErrRunBusy
		}
		over = ""
		switch {
		case mine.Person != "" && OverDaily(dayUsed(spent, reserved, mine.Person), mine.MaxMicroUSD, personLimit):
			over = mine.Person
			if enforce {
				return over, ErrPersonOverBudget
			}
		case OverDaily(dayUsed(spent, reserved, mine.Principal), mine.MaxMicroUSD, limit):
			over = mine.Principal
			if enforce {
				return over, ErrOverBudget
			}
		}
		if mine.Room != "" {
			for _, v := range reserved {
				if v.Room == mine.Room {
					return over, ErrRoomBusy
				}
			}
		}
		if cm.Data == nil {
			cm.Data = map[string]string{}
		}
		b, err := json.Marshal(mine)
		if err != nil {
			return "", fmt.Errorf("ledger %s: %w", name, err)
		}
		cm.Data[Reserved+id] = string(b)
		if existed {
			err = c.Update(ctx, cm)
		} else {
			err = c.Create(ctx, cm) // a concurrent Create conflicts; the loop re-reads
		}
		switch {
		case err == nil:
			return over, nil
		case apierrors.IsConflict(err) || apierrors.IsAlreadyExists(err):
			continue // re-read and re-check (R50)
		default:
			return "", fmt.Errorf("ledger %s: %w", name, err)
		}
	}
	// Conflicts at this depth mean steady contention, not a lost slot: fail open never.
	return "", ErrBusy
}

// OverDaily is the daily cap's one decision (R50), in micro-USD (TC7): a run that may spend
// maxMicroUSD does not fit beside what the principal's day already holds. The API's admission and
// the factory's Queued (N2) both decide on it.
func OverDaily(usedMicroUSD, maxMicroUSD, limit int64) bool { return usedMicroUSD+maxMicroUSD > limit }

// dayUsed is what principal holds of its day, in micro-USD: the meter's settled spend plus its
// admitted runs' reservations, as their principal or their person.
func dayUsed(spent map[string]int64, reserved map[string]Reservation, principal string) int64 {
	used := spent[principal]
	for _, v := range reserved {
		if v.Principal == principal || v.Person == principal {
			used += v.MaxMicroUSD
		}
	}
	return used
}

// DayUsed reads principal's use of the UTC day of now from that day's ledger, in micro-USD, as
// admission does: a missing ledger is a day with nothing used. c must read ConfigMaps live: the
// factory's cached watch sees the stop object only.
func DayUsed(ctx context.Context, c client.Reader, ns, principal string, now time.Time) (int64, error) {
	cm := &corev1.ConfigMap{}
	err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: Prefix + now.UTC().Format(DayLayout)}, cm)
	switch {
	case apierrors.IsNotFound(err):
		return 0, nil
	case err != nil:
		return 0, fmt.Errorf("ledger: %w", err)
	}
	spent, err := SpentColumn(cm)
	if err != nil {
		return 0, err
	}
	reserved, err := ledgerReservations(cm)
	if err != nil {
		return 0, err
	}
	return dayUsed(spent, reserved, principal), nil
}

// SpentColumn reads a ledger's spent map, nil when the column is empty. Exported with
// the layout constants above so admission and the meter parse and write one encoding.
func SpentColumn(cm *corev1.ConfigMap) (map[string]int64, error) {
	raw, ok := cm.Data[Spent]
	if !ok || raw == "" {
		return nil, nil
	}
	var m map[string]int64
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil, fmt.Errorf("ledger %s: %w", cm.Name, err)
	}
	return m, nil
}

// SetSpentColumn writes the spent map as the ledger's one JSON column.
func SetSpentColumn(cm *corev1.ConfigMap, spent map[string]int64) error {
	b, err := json.Marshal(spent)
	if err != nil {
		return fmt.Errorf("ledger %s spent: %w", cm.Name, err)
	}
	if cm.Data == nil {
		cm.Data = map[string]string{}
	}
	cm.Data[Spent] = string(b)
	return nil
}

func ledgerReservations(cm *corev1.ConfigMap) (map[string]Reservation, error) {
	m := map[string]Reservation{}
	for k, raw := range cm.Data {
		id, ok := strings.CutPrefix(k, Reserved)
		if !ok {
			continue
		}
		var r Reservation
		if err := json.Unmarshal([]byte(raw), &r); err != nil {
			return nil, fmt.Errorf("ledger %s reservation %s: %w", cm.Name, id, err)
		}
		m[id] = r
	}
	return m, nil
}
