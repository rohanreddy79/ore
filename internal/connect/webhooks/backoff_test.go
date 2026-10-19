// SPDX-License-Identifier: AGPL-3.0-only

package webhooks

import (
	"math/rand"
	"net/http"
	"testing"
	"time"
)

func TestDefaultScheduleSteps(t *testing.T) {
	s := DefaultSchedule
	want := []time.Duration{0, 5 * time.Second, 5 * time.Minute, 30 * time.Minute, 2 * time.Hour, 5 * time.Hour, 10 * time.Hour, 10 * time.Hour, 10 * time.Hour}
	for i, w := range want {
		if got := s.Delay(i + 1); got != w {
			t.Fatalf("delay before attempt %d: %s, want %s", i+1, got, w)
		}
	}
	if n := s.Attempts(); n != 12 {
		t.Fatalf("attempts within 72h: %d, want 12", n)
	}
	// the cumulative plan: 7h35m05s after six attempts, then 10 h steps to 67h35m05s; the next would pass 72 h
	first := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now, n := first, 1
	for {
		next, ok := s.Next(n, first, now, 0, nil)
		if !ok {
			break
		}
		now, n = next, n+1
	}
	if n != 12 || now.Sub(first) != 67*time.Hour+35*time.Minute+5*time.Second {
		t.Fatalf("attempts %d last at +%s", n, now.Sub(first))
	}
}

func TestScheduleJitterAndRetryAfter(t *testing.T) {
	s := DefaultSchedule
	first := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rnd := rand.New(rand.NewSource(1))
	for i := 0; i < 200; i++ {
		next, ok := s.Next(2, first, first, 0, rnd) // nominal 5 min before attempt 3
		if !ok {
			t.Fatal("not ok")
		}
		d := next.Sub(first)
		if d < 270*time.Second || d > 330*time.Second {
			t.Fatalf("jitter out of ±10%%: %s", d)
		}
	}
	// Retry-After overrides the schedule exactly, no jitter
	next, ok := s.Next(2, first, first, 42*time.Second, rnd)
	if !ok || next.Sub(first) != 42*time.Second {
		t.Fatalf("retry-after: %v %s", ok, next.Sub(first))
	}
	// a Retry-After beyond the budget exhausts the delivery
	if _, ok := s.Next(2, first, first.Add(71*time.Hour), 2*time.Hour, rnd); ok {
		t.Fatal("retry-after past the budget accepted")
	}
	h := http.Header{}
	h.Set("Retry-After", "120")
	if d := retryAfter(h, first); d != 2*time.Minute {
		t.Fatalf("seconds: %s", d)
	}
	h.Set("Retry-After", first.Add(90*time.Second).UTC().Format(http.TimeFormat))
	if d := retryAfter(h, first); d != 90*time.Second {
		t.Fatalf("http date: %s", d)
	}
	h.Set("Retry-After", "garbage")
	if d := retryAfter(h, first); d != 0 {
		t.Fatalf("garbage: %s", d)
	}
	if d := retryAfter(http.Header{}, first); d != 0 {
		t.Fatalf("absent: %s", d)
	}
}
