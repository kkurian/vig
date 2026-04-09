// Package report renders anomaly context into a standalone HTML file
// the user can open in a browser. Reports are stored in
// ~/Library/Logs/vig-reports/ and persist until manually pruned.
package report

import (
	_ "embed"
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/kkurian/vig/internal/session"
)

//go:embed template.html
var reportTemplateSrc string

// Params are the inputs Build needs to render a report. Velocity and
// BaselineP95 are the snapshot values at the moment the alert fired,
// not recomputed — we want the report to reflect what the detector
// saw, not what the detector would see a few seconds later.
type Params struct {
	Session     *session.Session
	Velocity    float64
	BaselineP95 float64
	AlertTime   time.Time
	Version     string
}

// Build generates an HTML report for a single anomaly and writes it
// to ReportsDir() with a sortable filename. Returns the absolute
// path of the written file.
//
// The report draws its "summary" fields from the hot-path Session
// (which is already in memory because the detector just fired on it)
// but loads the rich message stream separately via session.LoadFull
// so the report can show text content and tool calls without
// bloating the daemon's steady-state memory.
func Build(p Params) (string, error) {
	if p.Session == nil {
		return "", fmt.Errorf("report: nil session")
	}
	if p.AlertTime.IsZero() {
		p.AlertTime = time.Now()
	}

	full, err := session.LoadFull(p.Session.FilePath)
	if err != nil {
		return "", fmt.Errorf("report: load full session: %w", err)
	}

	data := buildViewModel(p, full)

	tmpl, err := template.New("report").Parse(reportTemplateSrc)
	if err != nil {
		return "", fmt.Errorf("report: parse template: %w", err)
	}

	dir, err := ReportsDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("report: create reports dir: %w", err)
	}

	filename := reportFilename(p.AlertTime, p.Session.ID)
	path := filepath.Join(dir, filename)

	f, err := os.Create(path)
	if err != nil {
		return "", fmt.Errorf("report: create file: %w", err)
	}
	defer f.Close()

	if err := tmpl.Execute(f, data); err != nil {
		return "", fmt.Errorf("report: render template: %w", err)
	}
	return path, nil
}

// ReportsDir returns the directory reports are written to
// (~/Library/Logs/vig-reports). Created lazily by Build.
func ReportsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "Logs", "vig-reports"), nil
}

// Entry is one row in the past-reports listing.
type Entry struct {
	Path       string
	Filename   string
	AlertTime  time.Time
	SessionPfx string
}

// List returns every .html file in the reports directory, sorted by
// AlertTime descending (most recent first). Non-matching filenames
// are ignored.
func List() ([]Entry, error) {
	dir, err := ReportsDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Entry
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".html") {
			continue
		}
		ts, sess, ok := parseReportFilename(e.Name())
		if !ok {
			continue
		}
		out = append(out, Entry{
			Path:       filepath.Join(dir, e.Name()),
			Filename:   e.Name(),
			AlertTime:  ts,
			SessionPfx: sess,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].AlertTime.After(out[j].AlertTime)
	})
	return out, nil
}

// reportFilename produces a sortable filename of the form
// "2026-04-09T17-45-30-abc12345.html". The timestamp is in local
// time and uses hyphens (not colons) so it survives being a file
// name on HFS+/APFS and copy-pasted into URL bars without escaping.
func reportFilename(ts time.Time, sessionID string) string {
	prefix := sessionID
	if len(prefix) > 8 {
		prefix = prefix[:8]
	}
	return fmt.Sprintf("%s-%s.html", ts.Local().Format("2006-01-02T15-04-05"), prefix)
}

// parseReportFilename is the inverse of reportFilename. Returns the
// parsed timestamp, the session prefix, and ok=true on success.
func parseReportFilename(name string) (time.Time, string, bool) {
	base := strings.TrimSuffix(name, ".html")
	// Split on the LAST hyphen: the prefix may be 8 chars, the
	// timestamp has hyphens of its own.
	idx := strings.LastIndex(base, "-")
	if idx < 0 {
		return time.Time{}, "", false
	}
	tsStr := base[:idx]
	sess := base[idx+1:]
	ts, err := time.ParseInLocation("2006-01-02T15-04-05", tsStr, time.Local)
	if err != nil {
		return time.Time{}, "", false
	}
	return ts, sess, true
}
