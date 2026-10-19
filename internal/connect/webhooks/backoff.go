// SPDX-License-Identifier: AGPL-3.0-only

package webhooks

import (
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Schedule is the retry plan: the delay before attempt n (1-based) and the
// total budget after which a delivery is exhausted.
type Schedule struct {
	Steps  []time.Duration // delay before attempts 2..len+1; the last step repeats
	Budget time.Duration   // measured from the first attempt
	Jitter float64         // fraction of the delay, e.g. 0.1 for ±10 %
}

// DefaultSchedule: immediate, 5 s, 5 min, 30 min, 2 h, 5 h, then 10 h steps,
// within 72 hours (12 attempts in total).
var DefaultSchedule = Schedule{
	Steps:  []time.Duration{5 * time.Second, 5 * time.Minute, 30 * time.Minute, 2 * time.Hour, 5 * time.Hour, 10 * time.Hour},
	Budget: 72 * time.Hour,
	Jitter: 0.1,
}

// Delay returns the nominal delay before attempt n (n ≥ 2); attempt 1 is immediate.
func (s Schedule) Delay(n int) time.Duration {
	if n <= 1 || len(s.Steps) == 0 {
		return 0
	}
	i := n - 2
	if i >= len(s.Steps) {
		i = len(s.Steps) - 1
	}
	return s.Steps[i]
}

// Next decides when the attempt after attempt n runs: retryAfter (from a
// Retry-After header) overrides the schedule; jitter is applied to
// scheduled delays; ok is false when the next attempt would fall outside
// the budget measured from first.
func (s Schedule) Next(n int, first, now time.Time, retryAfter time.Duration, rnd *rand.Rand) (time.Time, bool) {
	delay := s.Delay(n + 1)
	if retryAfter > 0 {
		delay = retryAfter
	} else if s.Jitter > 0 && delay > 0 && rnd != nil {
		f := 1 + s.Jitter*(2*rnd.Float64()-1)
		delay = time.Duration(float64(delay) * f)
	}
	next := now.Add(delay)
	if s.Budget > 0 && next.Sub(first) > s.Budget {
		return time.Time{}, false
	}
	return next, true
}

// Attempts counts how many attempts the schedule allows within its budget
// when every attempt fails instantly (informational).
func (s Schedule) Attempts() int {
	n, elapsed := 1, time.Duration(0)
	for {
		d := s.Delay(n + 1)
		if elapsed+d > s.Budget {
			return n
		}
		elapsed += d
		n++
		if n > 10000 {
			return n
		}
	}
}

// retryAfter parses a Retry-After header (seconds or an HTTP date).
func retryAfter(h http.Header, now time.Time) time.Duration {
	v := strings.TrimSpace(h.Get("Retry-After"))
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs < 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := t.Sub(now); d > 0 {
			return d
		}
	}
	return 0
}
