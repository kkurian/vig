// Package daemon implements the vig background loop: scan sessions,
// check the anomaly detector, fire persistent notifications, and prune
// per-session state so nothing grows unboundedly over days of uptime.
package daemon

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/kkurian/vig/internal/anomaly"
	"github.com/kkurian/vig/internal/config"
	"github.com/kkurian/vig/internal/notify"
	"github.com/kkurian/vig/internal/session"
)

// Run is the daemon's main loop. It blocks until ctx is cancelled.
//
// The anomaly detector starts with a cold-start baseline so the first
// scan has something to compare against; the real baseline is computed
// asynchronously from 30 days of session history and then refreshed on
// cfg.BaselineRefresh() intervals.
func Run(ctx context.Context, cfg config.Config) error {
	det := anomaly.NewDetector(
		anomaly.Baseline{P95: 5000, UsingColdStart: true},
		onAlert(cfg),
	)

	go refreshBaseline(ctx, det, cfg.BaselineRefresh())

	// Fire an immediate scan so we don't wait a full interval on startup.
	scanOnce(det)

	ticker := time.NewTicker(cfg.ScanInterval())
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			scanOnce(det)
		}
	}
}

// scanOnce does one pass: pull active sessions, run them through the
// detector, and prune trackers for any session that has disappeared
// since the previous scan.
func scanOnce(det *anomaly.Detector) {
	active := session.ScanActive()
	seen := make(map[string]bool, len(active))
	for _, s := range active {
		seen[s.ID] = true
		det.Check(s)
	}
	det.GCTrackers(seen)
}

func refreshBaseline(ctx context.Context, det *anomaly.Detector, every time.Duration) {
	compute := func() {
		b := anomaly.ComputeBaseline(session.ScanAll())
		det.UpdateBaseline(b)
		log.Printf("baseline: P95=%.0f cold_start=%v", b.P95, b.UsingColdStart)
	}
	compute()

	if every <= 0 {
		return
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			compute()
		}
	}
}

func onAlert(cfg config.Config) func(string, float64) {
	return func(sid string, v float64) {
		if !cfg.NotifyOnAnomaly {
			log.Printf("ANOMALY (suppressed) %s: %s", shortID(sid), fmtVelocity(v))
			return
		}
		msg := fmt.Sprintf("Session %s burning %s sustained above P95", shortID(sid), fmtVelocity(v))
		log.Printf("ANOMALY %s", msg)
		_ = notify.Send("vig — Anomaly Detected", msg)
	}
}

func shortID(sid string) string {
	if len(sid) > 8 {
		return sid[:8]
	}
	return sid
}

func fmtVelocity(v float64) string {
	if v >= 1000 {
		return fmt.Sprintf("%.1fK tok/min", v/1000)
	}
	return fmt.Sprintf("%.0f tok/min", v)
}
