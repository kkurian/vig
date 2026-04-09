package report

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kkurian/vig/internal/session"
)

// writeFixtureJSONL writes a minimal JSONL file that session.LoadFull
// can parse. Each line is one assistant message with the requested
// timestamp and output_tokens, plus one trailing user message so the
// parser has a cwd/branch to pick up.
func writeFixtureJSONL(t *testing.T, dir string, filename string, msgs []struct {
	TS    time.Time
	OutTk int64
}) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, filename)
	var body strings.Builder
	for _, m := range msgs {
		// Assistant line with a text content block + usage.
		body.WriteString(`{"type":"assistant","timestamp":"` + m.TS.UTC().Format(time.RFC3339Nano) +
			`","cwd":"/tmp/cerebro","gitBranch":"main","message":{"model":"claude-opus-4-6",` +
			`"content":[{"type":"text","text":"hello world"}],` +
			`"usage":{"input_tokens":10,"output_tokens":`)
		body.WriteString(itoa(m.OutTk))
		body.WriteString(`}}}` + "\n")
	}
	if err := os.WriteFile(path, []byte(body.String()), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	if neg {
		return "-" + string(digits)
	}
	return string(digits)
}

func TestBuildProducesReport(t *testing.T) {
	tmp := t.TempDir()
	// Override HOME so ReportsDir() lands inside tmp.
	t.Setenv("HOME", tmp)

	// Fabricate a JSONL with 10 assistant messages 30s apart.
	now := time.Now()
	var msgs []struct {
		TS    time.Time
		OutTk int64
	}
	for i := 0; i < 10; i++ {
		msgs = append(msgs, struct {
			TS    time.Time
			OutTk int64
		}{
			TS:    now.Add(-time.Duration(10-i) * 30 * time.Second),
			OutTk: 2500,
		})
	}
	jsonlDir := filepath.Join(tmp, ".claude", "projects", "-Users-test-cerebro")
	jsonlPath := writeFixtureJSONL(t, jsonlDir, "abc12345-dead-beef-cafe-feedfacebabe.jsonl", msgs)

	s := &session.Session{
		ID:        "abc12345-dead-beef-cafe-feedfacebabe",
		StartTime: msgs[0].TS,
		Messages:  nil, // Build uses FilePath, not Messages, for the rich parse
		FilePath:  jsonlPath,
	}

	path, err := Build(Params{
		Session:     s,
		Velocity:    42_300,
		BaselineP95: 3_500,
		AlertTime:   now,
		Version:     "test",
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !strings.HasSuffix(path, ".html") {
		t.Errorf("expected .html path, got %q", path)
	}
	if !strings.Contains(path, "abc12345") {
		t.Errorf("expected session prefix in path, got %q", path)
	}

	html, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read rendered report: %v", err)
	}
	body := string(html)

	// Smoke checks — the template should contain the pieces the
	// report viewmodel populates.
	for _, want := range []string{
		"ANOMALY",
		"cerebro",                 // project name derived from the dashed dir
		"abc12345",                // short session id
		"42.3K tok/min",           // velocity formatting
		"3.5K tok/min",            // baseline formatting
		"12×",                     // ratio rounded (42300/3500 ≈ 12.09)
		"claude-opus-4-6",         // model pulled from the assistant line
		"main",                    // git branch
		"/tmp/cerebro",            // cwd
		`<svg`,                    // chart SVG
		"hello world",             // text content from the fixture
		"vig test",                // version footer
	} {
		if !strings.Contains(body, want) {
			t.Errorf("rendered report missing %q", want)
		}
	}
}

func TestListSortsByAlertTimeDesc(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	dir, _ := ReportsDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir reports: %v", err)
	}

	// Seed three reports with manual filenames in increasing time order.
	names := []string{
		"2026-04-07T10-00-00-aaa.html",
		"2026-04-09T15-30-00-bbb.html",
		"2026-04-08T12-15-00-ccc.html",
	}
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("<html></html>"), 0o644); err != nil {
			t.Fatalf("seed %s: %v", n, err)
		}
	}
	// Also drop an unrelated file and a non-html file that List should ignore.
	_ = os.WriteFile(filepath.Join(dir, "junk"), []byte("x"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "2026-04-99T00-00-00-ddd.html"), []byte("x"), 0o644) // unparsable date

	entries, err := List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d: %+v", len(entries), entries)
	}
	// Most recent first.
	if entries[0].SessionPfx != "bbb" || entries[1].SessionPfx != "ccc" || entries[2].SessionPfx != "aaa" {
		t.Errorf("wrong order: %v / %v / %v",
			entries[0].SessionPfx, entries[1].SessionPfx, entries[2].SessionPfx)
	}
}

func TestParseReportFilenameRoundTrip(t *testing.T) {
	now := time.Date(2026, 4, 9, 17, 45, 30, 0, time.Local)
	name := reportFilename(now, "abcd1234deadbeef")
	ts, sess, ok := parseReportFilename(name)
	if !ok {
		t.Fatalf("parse failed for %q", name)
	}
	if !ts.Equal(now) {
		t.Errorf("timestamp round-trip: got %v want %v", ts, now)
	}
	if sess != "abcd1234" {
		t.Errorf("session prefix: got %q want %q", sess, "abcd1234")
	}
}
