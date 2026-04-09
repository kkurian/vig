package anomaly

import (
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
