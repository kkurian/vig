package anomaly

import (
	"math"
	"sort"
	"sync"
	"time"

	"github.com/kkurian/vig/internal/session"
)

const (
	velocityWindowMin  = 2
	baselineDays       = 30
	anomalyPercentile  = 95.0
	sustainedMin       = 5
	coldStartThreshold = 5000.0
	minSessions        = 10
	minCleanSamples    = 20
)

type Baseline struct {
	P95            float64
	UsingColdStart bool
}

// velocitySample ties a rolling-window velocity to the moment it was
// observed. The timestamp is needed by wasAnomalousHistoric, which
// replays the real-time sustained-exceedance check offline against
// historical sessions.
type velocitySample struct {
	ts       time.Time
	velocity float64
}

// ComputeBaseline derives the P95 velocity threshold from historical
// sessions, with contamination guarding.
//
// Anomaly drift problem: a naive P95 over all historical velocity
// samples is contaminated by any runaway session still within the
// 30-day window — the prior anomaly's own samples inflate the
// percentile and make the next anomaly of similar magnitude invisible
// to the real-time detector. Left unchecked, every unaddressed
// anomaly progressively desensitizes detection.
//
// Fix: two passes.
//
//  1. Compute a "raw" P95 across every eligible session's samples.
//     This threshold is somewhat inflated by contamination but still
//     far below the actual runaway-velocity regime.
//  2. Replay the real-time sustained-exceedance check against each
//     session using the raw P95 as its threshold. Any session that
//     would have been flagged is excluded wholesale. Recompute the
//     P95 only over survivors.
//
// One pass is sufficient for realistic data because P95 is already a
// robust statistic: even under contamination the raw threshold lands
// comfortably between normal and anomaly velocity regimes, so the
// replay catches the contaminating sessions. If the exclusion is
// somehow so aggressive that fewer than minCleanSamples survive, we
// fall back to the raw P95 rather than returning a wildly optimistic
// value.
func ComputeBaseline(sessions []*session.Session) Baseline {
	if sessions == nil {
		return Baseline{P95: coldStartThreshold, UsingColdStart: true}
	}
	cutoff := time.Now().AddDate(0, 0, -baselineDays)

	// Gather per-session samples once; we need both a flat pool for
	// percentile computation and per-session grouping for replay.
	var perSession [][]velocitySample
	var allVelocities []float64
	valid := 0
	for _, s := range sessions {
		if s.StartTime.Before(cutoff) {
			continue
		}
		valid++
		samples := sessionVelocitySamples(s)
		if len(samples) == 0 {
			continue
		}
		perSession = append(perSession, samples)
		for _, vs := range samples {
			allVelocities = append(allVelocities, vs.velocity)
		}
	}
	if valid < minSessions || len(allVelocities) < minCleanSamples {
		return Baseline{P95: coldStartThreshold, UsingColdStart: true}
	}

	// Pass 1: raw P95, contamination and all.
	p95Raw := pct(allVelocities, anomalyPercentile)

	// Pass 2: replay the detector against each session using the raw
	// P95 as threshold. Drop sessions that sustained above it.
	var cleanVelocities []float64
	for _, samples := range perSession {
		if wasAnomalousHistoric(samples, p95Raw) {
			continue
		}
		for _, vs := range samples {
			cleanVelocities = append(cleanVelocities, vs.velocity)
		}
	}

	// Defensive fallback — exclusion should never strip so much that
	// nothing survives, but if it does, prefer the (inflated) raw P95
	// to a wildly optimistic cleaned value.
	if len(cleanVelocities) < minCleanSamples {
		return Baseline{P95: p95Raw}
	}
	return Baseline{P95: pct(cleanVelocities, anomalyPercentile)}
}

// OnAlert is the callback signature the detector invokes the first
// time a session transitions into the "sustained above P95" state.
// It receives the session that triggered and the velocity reading
// at that moment. Callbacks should be fast and non-blocking; any
// heavy work (report generation, notification) should be dispatched
// to a goroutine inside the callback.
type OnAlert func(s *session.Session, velocity float64)

type Detector struct {
	mu       sync.Mutex
	baseline Baseline
	trackers map[string]*tracker
	onAlert  OnAlert
}

type tracker struct {
	exceedingSince time.Time
	exceeding      bool
	alerted        bool
}

func NewDetector(b Baseline, onAlert OnAlert) *Detector {
	return &Detector{baseline: b, trackers: make(map[string]*tracker), onAlert: onAlert}
}

func (d *Detector) UpdateBaseline(b Baseline) {
	d.mu.Lock()
	d.baseline = b
	d.mu.Unlock()
}

// SetOnAlert installs (or replaces) the callback fired when a
// session transitions to sustained-above-P95. Useful when the
// callback needs a stable reference to the detector it's bound to —
// the caller can create the detector first, then install a closure
// that captures it.
func (d *Detector) SetOnAlert(fn OnAlert) {
	d.mu.Lock()
	d.onAlert = fn
	d.mu.Unlock()
}

// P95Snapshot returns the current P95 baseline value. Safe to call
// from any goroutine. Reports use this to record the threshold the
// detector was using at the moment an alert fired.
func (d *Detector) P95Snapshot() float64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.baseline.P95
}

// GCTrackers drops tracker state for any session ID not present in seen.
// Call this after each scan so the trackers map does not grow unboundedly
// as sessions come and go across days of uptime.
func (d *Detector) GCTrackers(seen map[string]bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for id := range d.trackers {
		if !seen[id] {
			delete(d.trackers, id)
		}
	}
}

func (d *Detector) Check(s *session.Session) bool {
	d.mu.Lock()
	defer d.mu.Unlock()

	t, ok := d.trackers[s.ID]
	if !ok {
		t = &tracker{}
		d.trackers[s.ID] = t
	}

	v := currentVelocity(s)
	now := time.Now()

	if v > d.baseline.P95 {
		if !t.exceeding {
			t.exceeding = true
			t.exceedingSince = now
		}
		sustained := now.Sub(t.exceedingSince) >= time.Duration(sustainedMin)*time.Minute
		if sustained && !t.alerted {
			t.alerted = true
			if d.onAlert != nil {
				d.onAlert(s, v)
			}
		}
		return sustained
	}

	t.exceeding = false
	t.alerted = false
	return false
}

func currentVelocity(s *session.Session) float64 {
	now := time.Now()
	cutoff := now.Add(-time.Duration(velocityWindowMin) * time.Minute)
	var tokens int64
	for i := len(s.Messages) - 1; i >= 0; i-- {
		if s.Messages[i].Timestamp.Before(cutoff) {
			break
		}
		tokens += s.Messages[i].OutputTokens
	}
	elapsed := now.Sub(cutoff).Minutes()
	if elapsed <= 0 {
		return 0
	}
	return float64(tokens) / elapsed
}

// sessionVelocitySamples returns per-message rolling-window velocities
// paired with the timestamp at which each sample was observed. The
// timestamps are required by wasAnomalousHistoric; the velocities alone
// are enough for the percentile computation.
func sessionVelocitySamples(s *session.Session) []velocitySample {
	type tokenSample struct {
		ts     time.Time
		tokens int64
	}
	var tokens []tokenSample
	for _, m := range s.Messages {
		if m.OutputTokens > 0 && !m.Timestamp.IsZero() {
			tokens = append(tokens, tokenSample{m.Timestamp, m.OutputTokens})
		}
	}
	if len(tokens) < 2 {
		return nil
	}
	sort.Slice(tokens, func(i, j int) bool { return tokens[i].ts.Before(tokens[j].ts) })

	window := time.Duration(velocityWindowMin) * time.Minute
	out := make([]velocitySample, 0, len(tokens))
	for i := range tokens {
		start := tokens[i].ts.Add(-window)
		var total int64
		for j := i; j >= 0 && !tokens[j].ts.Before(start); j-- {
			total += tokens[j].tokens
		}
		out = append(out, velocitySample{
			ts:       tokens[i].ts,
			velocity: float64(total) / window.Minutes(),
		})
	}
	return out
}

// wasAnomalousHistoric is the offline replay of Detector.Check's
// sustained-exceedance logic. It walks a session's velocity samples
// in chronological order and returns true if any contiguous run of
// strictly-above-threshold samples spans at least sustainedMin
// minutes of wall time (measured between the first and last samples
// of the run, matching the real-time detector's semantics).
func wasAnomalousHistoric(samples []velocitySample, threshold float64) bool {
	if len(samples) == 0 {
		return false
	}
	var exceedingSince time.Time
	exceeding := false
	sustainedDur := time.Duration(sustainedMin) * time.Minute
	for _, vs := range samples {
		if vs.velocity > threshold {
			if !exceeding {
				exceeding = true
				exceedingSince = vs.ts
			}
			if vs.ts.Sub(exceedingSince) >= sustainedDur {
				return true
			}
		} else {
			exceeding = false
		}
	}
	return false
}

func pct(data []float64, p float64) float64 {
	if len(data) == 0 {
		return 0
	}
	s := make([]float64, len(data))
	copy(s, data)
	sort.Float64s(s)
	rank := p / 100 * float64(len(s)-1)
	lo := int(math.Floor(rank))
	hi := int(math.Ceil(rank))
	if lo == hi || hi >= len(s) {
		return s[lo]
	}
	frac := rank - float64(lo)
	return s[lo]*(1-frac) + s[hi]*frac
}
