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
	"github.com/kkurian/vig/internal/report"
	"github.com/kkurian/vig/internal/session"
)

// Version is set by main at startup so the daemon can stamp the
// currently-running binary's version into generated anomaly reports.
// Defaults to "dev" so tests and local builds don't render "".
var Version = "dev"

// Run is the daemon's main loop. It blocks until ctx is cancelled.
//
// The anomaly detector starts with a cold-start baseline so the first
// scan has something to compare against; the real baseline is computed
// asynchronously from 30 days of session history and then refreshed on
// cfg.BaselineRefresh() intervals.
func Run(ctx context.Context, cfg config.Config) error {
	// The detector is created with a nil callback first so the
	// callback closure can capture a stable reference to it. This
	// is the cleanest way to break the cycle between "onAlert
	// needs the detector to read the baseline" and "the detector
	// needs onAlert to fire on match."
	det := anomaly.NewDetector(
		anomaly.Baseline{P95: 5000, UsingColdStart: true},
		nil,
	)
	det.SetOnAlert(buildOnAlert(cfg, det))

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

// buildOnAlert returns the callback the detector invokes when a
// session transitions into the sustained-above-P95 state. It
// generates an HTML report for the anomaly, logs the path, and
// pops a modal macOS alert with two buttons: "Show Report" (opens
// the HTML) and "Dismiss".
//
// Report generation may take a few tens of ms (reading the JSONL,
// parsing, templating). We do it on the calling goroutine because
// the detector's callback runs on the ticker goroutine — not a
// perf-critical path — and it's simpler than dispatching. If the
// report fails for any reason we still fire an alert with no
// "Show Report" button so the user still hears about the anomaly.
func buildOnAlert(cfg config.Config, det *anomaly.Detector) anomaly.OnAlert {
	return func(s *session.Session, v float64) {
		title := "vig — Anomaly Detected"
		body := fmt.Sprintf(
			"Session %s sustained %s above the P95 baseline.",
			shortID(s.ID), fmtVelocity(v),
		)

		if !cfg.NotifyOnAnomaly {
			log.Printf("ANOMALY (suppressed) %s: %s", shortID(s.ID), fmtVelocity(v))
			return
		}

		reportPath, err := report.Build(report.Params{
			Session:     s,
			Velocity:    v,
			BaselineP95: det.P95Snapshot(),
			AlertTime:   time.Now(),
			Version:     Version,
		})
		if err != nil {
			log.Printf("ANOMALY report build failed: %v", err)
			log.Printf("ANOMALY %s", body)
			_ = notify.SendAnomalyAlert(title, body, "")
			return
		}

		log.Printf("ANOMALY %s (report: %s)", body, reportPath)
		_ = notify.SendAnomalyAlert(
			title,
			body+"\n\nClick \u201cShow Report\u201d for the full breakdown.",
			reportPath,
		)
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
