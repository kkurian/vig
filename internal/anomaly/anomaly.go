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
)

type Baseline struct {
	P95            float64
	UsingColdStart bool
}

func ComputeBaseline(sessions []*session.Session) Baseline {
	if sessions == nil {
		return Baseline{P95: coldStartThreshold, UsingColdStart: true}
	}
	cutoff := time.Now().AddDate(0, 0, -baselineDays)
	var velocities []float64
	valid := 0
	for _, s := range sessions {
		if s.StartTime.Before(cutoff) {
			continue
		}
		valid++
		velocities = append(velocities, sessionVelocities(s)...)
	}
	if valid < minSessions || len(velocities) < 20 {
		return Baseline{P95: coldStartThreshold, UsingColdStart: true}
	}
	return Baseline{P95: pct(velocities, anomalyPercentile)}
}

type Detector struct {
	mu       sync.Mutex
	baseline Baseline
	trackers map[string]*tracker
	onAlert  func(string, float64)
}

type tracker struct {
	exceedingSince time.Time
	exceeding      bool
	alerted        bool
}

func NewDetector(b Baseline, onAlert func(string, float64)) *Detector {
	return &Detector{baseline: b, trackers: make(map[string]*tracker), onAlert: onAlert}
}

func (d *Detector) UpdateBaseline(b Baseline) {
	d.mu.Lock()
	d.baseline = b
	d.mu.Unlock()
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
				d.onAlert(s.ID, v)
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

func sessionVelocities(s *session.Session) []float64 {
	type sample struct {
		ts     time.Time
		tokens int64
	}
	var samples []sample
	for _, m := range s.Messages {
		if m.OutputTokens > 0 && !m.Timestamp.IsZero() {
			samples = append(samples, sample{m.Timestamp, m.OutputTokens})
		}
	}
	if len(samples) < 2 {
		return nil
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i].ts.Before(samples[j].ts) })

	window := time.Duration(velocityWindowMin) * time.Minute
	var out []float64
	for i := range samples {
		start := samples[i].ts.Add(-window)
		var total int64
		for j := i; j >= 0 && !samples[j].ts.Before(start); j-- {
			total += samples[j].tokens
		}
		out = append(out, float64(total)/window.Minutes())
	}
	return out
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
