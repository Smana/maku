// SPDX-License-Identifier: Apache-2.0

package store

import (
	"errors"
	"strconv"
	"strings"
	"testing"
)

func sample(reply string) IntentSample {
	return IntentSample{Channel: "C01ABC", Reply: reply, Intent: "stop", Confidence: 0.82}
}

func TestIntentSampleInsert(t *testing.T) {
	ctx := t.Context()
	s, _, _, super := open(t)
	id, err := s.InsertIntentSample(ctx, sample("please stop"), 30)
	if err != nil || id < 1 {
		t.Fatalf("insert = %d, %v", id, err)
	}
	got, err := s.IntentSamplesSince(ctx, 1)
	if err != nil || len(got) != 1 {
		t.Fatalf("samples = %+v, %v", got, err)
	}
	x := got[0]
	if x.ID != id || x.Channel != "C01ABC" || x.Reply != "please stop" || x.Intent != "stop" ||
		x.Outcome != SampleShadowed || x.CreatedAt.IsZero() || x.Confidence < 0.81 || x.Confidence > 0.83 {
		t.Fatalf("sample = %+v", x)
	}
	days, err := strconv.ParseFloat(query(t, super, `SELECT (extract(epoch FROM expires_at - created_at) / 86400)::text FROM intent_samples`), 64)
	if err != nil {
		t.Fatal(err)
	}
	if days < 29.99 || days > 30.01 {
		t.Fatalf("expires %v days after creation, want 30", days)
	}
	for name, in := range map[string]IntentSample{
		"a channel that is not Slack's": {Channel: "chan", Reply: "x", Intent: "stop", Confidence: 0.5},
		"an intent outside the nine":    {Channel: "C01ABC", Reply: "x", Intent: "merge", Confidence: 0.5},
		"a confidence above one":        {Channel: "C01ABC", Reply: "x", Intent: "stop", Confidence: 1.5},
		"an empty reply":                {Channel: "C01ABC", Reply: "", Intent: "stop", Confidence: 0.5},
		"a reply over the bound":        {Channel: "C01ABC", Reply: strings.Repeat("a", 8001), Intent: "stop", Confidence: 0.5},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := s.InsertIntentSample(ctx, in, 30); !errors.Is(err, ErrBadIntentSample) {
				t.Fatalf("err = %v, want ErrBadIntentSample", err)
			}
		})
	}
	if _, err := s.InsertIntentSample(ctx, sample("x"), 0); !errors.Is(err, ErrBadIntentSample) {
		t.Fatalf("zero retention: %v", err)
	}
}

func TestIntentSampleOutcome(t *testing.T) {
	ctx := t.Context()
	s, _, _, _ := open(t)
	id, err := s.InsertIntentSample(ctx, sample("stop it"), 30)
	if err != nil {
		t.Fatal(err)
	}
	if moved, err := s.SetIntentSampleOutcome(ctx, id, SampleCancelled); err != nil || !moved {
		t.Fatalf("first move = %v, %v", moved, err)
	}
	if moved, err := s.SetIntentSampleOutcome(ctx, id, SampleConfirmed); err != nil || moved {
		t.Fatalf("a sample moves once: second move = %v, %v", moved, err)
	}
	if moved, err := s.SetIntentSampleOutcome(ctx, id+100, SampleExpired); err != nil || moved {
		t.Fatalf("unknown id = %v, %v", moved, err)
	}
	if _, err := s.SetIntentSampleOutcome(ctx, id, "shadowed"); !errors.Is(err, ErrBadIntentSample) {
		t.Fatalf("back to shadowed: %v, want ErrBadIntentSample", err)
	}
	got, _ := s.IntentSamplesSince(ctx, 1)
	if len(got) != 1 || got[0].Outcome != SampleCancelled {
		t.Fatalf("samples = %+v, want the cancelled one", got)
	}
}

func TestIntentSamplesRetention(t *testing.T) {
	ctx := t.Context()
	s, _, retention, super := open(t)
	for _, r := range []string{"old a", "old b", "fresh"} {
		if _, err := s.InsertIntentSample(ctx, sample(r), 30); err != nil {
			t.Fatal(err)
		}
	}
	forge(t, super, `UPDATE intent_samples SET created_at = now() - interval '40 days', expires_at = now() - interval '10 days'
		WHERE reply LIKE 'old %'`)
	if _, err := s.PurgeIntentSamples(ctx); err == nil {
		t.Fatal("the broker's role must not be able to purge samples")
	}
	if got, _ := s.IntentSamplesSince(ctx, 60); len(got) != 3 {
		t.Fatalf("the broker role purged: %d samples left, want 3", len(got))
	}
	r, err := Open(ctx, retention)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if n, err := r.PurgeIntentSamples(ctx); err != nil || n != 2 {
		t.Fatalf("purged %d (%v), want the two expired", n, err)
	}
	got, _ := s.IntentSamplesSince(ctx, 60)
	if len(got) != 1 || got[0].Reply != "fresh" {
		t.Fatalf("samples = %+v, want the fresh one", got)
	}
}

func TestIntentSamplesGrants(t *testing.T) {
	ctx := t.Context()
	s, broker, retention, _ := open(t)
	id, err := s.InsertIntentSample(ctx, sample("stop"), 30)
	if err != nil {
		t.Fatal(err)
	}
	for name, sql := range map[string]string{
		"an UPDATE":         `UPDATE intent_samples SET reply = 'rewritten'`,
		"an outcome UPDATE": `UPDATE intent_samples SET outcome = 'confirmed'`,
		"a DELETE":          `DELETE FROM intent_samples`,
		"a TRUNCATE":        `TRUNCATE intent_samples`,
	} {
		t.Run("the broker role cannot run "+name, func(t *testing.T) {
			if err := run(ctx, broker, sql); err == nil {
				t.Fatal("the broker role holds a write beyond SELECT, INSERT and the outcome function")
			}
		})
	}
	t.Run("the retention role reads nothing but the expiry", func(t *testing.T) {
		if err := run(ctx, retention, `SELECT reply FROM intent_samples`); err == nil {
			t.Fatal("the retention role read a reply")
		}
		if err := run(ctx, retention, `INSERT INTO intent_samples (channel, reply, intent, confidence, expires_at)
			VALUES ('C01ABC', 'x', 'stop', 0.5, now() + interval '1 day')`); err == nil {
			t.Fatal("the retention role inserted")
		}
	})
	t.Run("the outcome function is the broker's", func(t *testing.T) {
		if moved, err := s.SetIntentSampleOutcome(ctx, id, SampleExpired); err != nil || !moved {
			t.Fatalf("broker call = %v, %v", moved, err)
		}
		if err := run(ctx, retention, `SELECT intent_sample_outcome(1, 'expired')`); err == nil {
			t.Fatal("the retention role may call the outcome function")
		}
	})
}
