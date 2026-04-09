package anomaly

import (
	"fmt"
	"testing"
	"time"

	"github.com/kkurian/vig/internal/session"
)

// newHotSession fabricates a session whose last message is "now" and carries
// enough output tokens to push currentVelocity well above any plausible
// baseline, so a single call to Detector.Check always creates a tracker.
func newHotSession(id string) *session.Session {
	now := time.Now()
	return &session.Session{
		ID:        id,
		StartTime: now.Add(-10 * time.Minute),
		Messages: []session.Message{
			{Timestamp: now.Add(-30 * time.Second), OutputTokens: 1_000_000},
		},
	}
}

func TestGCTrackersRemovesUnseen(t *testing.T) {
	d := NewDetector(Baseline{P95: 100}, nil)

	a := newHotSession("a")
	b := newHotSession("b")
	d.Check(a)
	d.Check(b)

	if got := len(d.trackers); got != 2 {
		t.Fatalf("want 2 trackers before GC, got %d", got)
	}

	// Only "a" is still active.
	d.GCTrackers(map[string]bool{"a": true})

	if _, ok := d.trackers["a"]; !ok {
		t.Errorf("tracker for 'a' was incorrectly removed")
	}
	if _, ok := d.trackers["b"]; ok {
		t.Errorf("tracker for 'b' should have been removed")
	}
	if got := len(d.trackers); got != 1 {
		t.Errorf("want 1 tracker after GC, got %d", got)
	}
}

func TestGCTrackersEmptySeenClearsAll(t *testing.T) {
	d := NewDetector(Baseline{P95: 100}, nil)
	d.Check(newHotSession("x"))
	d.Check(newHotSession("y"))

	d.GCTrackers(map[string]bool{})

	if got := len(d.trackers); got != 0 {
		t.Errorf("want 0 trackers, got %d", got)
	}
}

// makeNormalSession fabricates a historical session with steady,
// low-velocity output: 50 messages 10 seconds apart at 100 output
// tokens each. In steady state (from message 12 onward) the rolling
// 2-minute window contains 13 messages for 1300 tokens / 2 min =
// 650 tokens/min.
func makeNormalSession(id string, startOffset time.Duration) *session.Session {
	start := time.Now().Add(-startOffset)
	msgs := make([]session.Message, 50)
	for j := range msgs {
		msgs[j] = session.Message{
			Timestamp:    start.Add(time.Duration(j) * 10 * time.Second),
			OutputTokens: 100,
		}
	}
	return &session.Session{ID: id, StartTime: start, Messages: msgs}
}

// makeAnomalySession fabricates a clearly runaway historical session:
// 40 messages 10 seconds apart at 100,000 output tokens each. The
// steady-state velocity is ~650,000 tok/min — three orders of
// magnitude above the normal fixture — and it sustains for 6.5 real
// minutes, comfortably past the 5-minute sustained threshold.
func makeAnomalySession(id string, startOffset time.Duration) *session.Session {
	start := time.Now().Add(-startOffset)
	msgs := make([]session.Message, 40)
	for j := range msgs {
		msgs[j] = session.Message{
			Timestamp:    start.Add(time.Duration(j) * 10 * time.Second),
			OutputTokens: 100_000,
		}
	}
	return &session.Session{ID: id, StartTime: start, Messages: msgs}
}

func TestComputeBaselineExcludesAnomalousSessions(t *testing.T) {
	// 12 normals + 1 runaway. The raw P95 across the pooled samples
	// lands inside the anomaly's ramp-up range (~350,000 tok/min)
	// because the 40 anomaly samples make up ~6% of the total — enough
	// to contaminate the percentile. The second pass replays the
	// sustained-exceedance check against each session using that
	// contaminated threshold; because the anomaly's steady state
	// (650,000) is well above the contaminated P95, the replay still
	// flags it and drops the entire session from the clean pool.
	var sessions []*session.Session
	for i := 0; i < 12; i++ {
		sessions = append(sessions,
			makeNormalSession(fmt.Sprintf("normal-%d", i), time.Duration(i+1)*24*time.Hour))
	}
	sessions = append(sessions, makeAnomalySession("runaway", 12*time.Hour))

	b := ComputeBaseline(sessions)
	if b.UsingColdStart {
		t.Fatalf("unexpected cold-start result: P95=%.0f", b.P95)
	}
	// Steady-state normal velocity is 650 tok/min. The cleaned P95
	// should be within spitting distance of that — certainly nowhere
	// near the anomaly regime (hundreds of thousands).
	if b.P95 > 2000 {
		t.Errorf("baseline P95=%.0f too high — runaway session was not excluded", b.P95)
	}
	if b.P95 < 100 {
		t.Errorf("baseline P95=%.0f suspiciously low — exclusion may have been too aggressive", b.P95)
	}
}

func TestComputeBaselineUnchangedWithoutAnomaly(t *testing.T) {
	// Control: the same corpus without the runaway should produce
	// essentially the same baseline. This guards against the two-pass
	// machinery regressing the clean case.
	var withAnomaly []*session.Session
	var withoutAnomaly []*session.Session
	for i := 0; i < 12; i++ {
		s := makeNormalSession(fmt.Sprintf("normal-%d", i), time.Duration(i+1)*24*time.Hour)
		withAnomaly = append(withAnomaly, s)
		withoutAnomaly = append(withoutAnomaly, s)
	}
	withAnomaly = append(withAnomaly, makeAnomalySession("runaway", 12*time.Hour))

	a := ComputeBaseline(withAnomaly)
	c := ComputeBaseline(withoutAnomaly)
	if a.UsingColdStart || c.UsingColdStart {
		t.Fatalf("unexpected cold-start: with=%+v without=%+v", a, c)
	}
	// The two baselines should agree because the anomaly is excluded
	// in the contaminated case. Allow a small tolerance for percentile
	// interpolation differences caused by different total sample counts.
	diff := a.P95 - c.P95
	if diff < 0 {
		diff = -diff
	}
	if diff > 5 {
		t.Errorf("baseline drifted after exclusion: with=%.0f without=%.0f diff=%.0f",
			a.P95, c.P95, diff)
	}
}

func TestWasAnomalousHistoric(t *testing.T) {
	now := time.Now()
	mk := func(tsSec int, v float64) velocitySample {
		return velocitySample{ts: now.Add(time.Duration(tsSec) * time.Second), velocity: v}
	}

	cases := []struct {
		name      string
		samples   []velocitySample
		threshold float64
		want      bool
	}{
		{
			name:      "empty",
			samples:   nil,
			threshold: 100,
			want:      false,
		},
		{
			name: "always below",
			samples: []velocitySample{
				mk(0, 50), mk(60, 50), mk(120, 50), mk(180, 50),
				mk(240, 50), mk(300, 50), mk(360, 50),
			},
			threshold: 100,
			want:      false,
		},
		{
			name: "brief spike — under 5 minutes",
			samples: []velocitySample{
				mk(0, 50), mk(30, 200), mk(60, 200), mk(90, 200),
				mk(120, 50), mk(150, 50),
			},
			threshold: 100,
			want:      false,
		},
		{
			name: "sustained 5+ minutes",
			samples: []velocitySample{
				mk(0, 200), mk(60, 200), mk(120, 200),
				mk(180, 200), mk(240, 200), mk(300, 200), mk(360, 200),
			},
			threshold: 100,
			want:      true,
		},
		{
			name: "sustained then reset then sustained again",
			samples: []velocitySample{
				mk(0, 200), mk(60, 200),
				mk(120, 50), // reset
				mk(180, 200), mk(240, 200), mk(300, 200),
				mk(360, 200), mk(420, 200), mk(480, 200), // 5 min from 180
			},
			threshold: 100,
			want:      true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := wasAnomalousHistoric(c.samples, c.threshold)
			if got != c.want {
				t.Errorf("wasAnomalousHistoric: got %v, want %v", got, c.want)
			}
		})
	}
}
