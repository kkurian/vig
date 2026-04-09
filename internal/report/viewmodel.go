package report

import (
	"fmt"
	"html/template"
	"math"
	"time"

	"github.com/kkurian/vig/internal/session"
)

// viewModel is the flat, template-ready shape of a rendered report.
// The template is pure text substitution with a single `range` — all
// formatting decisions (durations, token counts, truncation) live
// here so the template file stays dumb.
type viewModel struct {
	// Header
	ProjectName    string
	SessionID      string
	SessionIDShort string
	AlertTime      string
	Version        string
	GeneratedAt    string

	// Trigger
	VelocityFmt  string
	BaselineFmt  string
	Ratio        string
	SustainedMin int

	// Chart (pre-rendered SVG)
	Chart template.HTML

	// Session
	Model       string
	GitBranch   string
	CWD         string
	StartTime   string
	Duration    string
	TotalOutput string
	FilePath    string

	// Recent activity
	RecentCount    int
	TotalMessages  int
	RecentMessages []viewMessage
}

type viewMessage struct {
	TimeFmt      string
	Role         string
	RoleClass    string
	Tools        string
	OutputTokens string
	Text         string
}

// recentMessageLimit caps how many messages the report renders. The
// tail end of a session is what a human reviewing an anomaly cares
// about; older history makes reports huge without adding signal.
const recentMessageLimit = 40

// textTruncate is the per-message text length cap. Claude messages
// can be thousands of characters; keeping them short in the report
// is the difference between a scannable page and a wall of text.
const textTruncate = 600

// sustainedMinutes matches the anomaly.sustainedMin constant. We
// don't import the anomaly package to avoid a cycle (anomaly could
// in principle want to import report in the future).
const sustainedMinutes = 5

func buildViewModel(p Params, full *session.FullSession) viewModel {
	v := viewModel{
		ProjectName:    full.ProjectName,
		SessionID:      full.ID,
		SessionIDShort: shortID(full.ID),
		AlertTime:      p.AlertTime.Local().Format("2006-01-02 15:04:05 MST"),
		Version:        p.Version,
		GeneratedAt:    time.Now().Local().Format("2006-01-02 15:04:05 MST"),

		VelocityFmt:  fmtVelocity(p.Velocity),
		BaselineFmt:  fmtVelocity(p.BaselineP95),
		Ratio:        fmtRatio(p.Velocity, p.BaselineP95),
		SustainedMin: sustainedMinutes,

		Model:     full.Model,
		GitBranch: full.GitBranch,
		CWD:       full.CWD,
		StartTime: full.StartTime.Local().Format("2006-01-02 15:04:05 MST"),
		Duration:  fmtDuration(full.LastActive.Sub(full.StartTime)),
		FilePath:  full.FilePath,
	}

	// Chart: compute per-message velocities from the rich session so
	// the report's sparkline matches what the detector saw.
	samples := computeChartSamples(full)
	v.Chart = template.HTML(renderSVGChart(samples, p.BaselineP95, 880, 180))

	var totalOut int64
	for _, m := range full.Messages {
		totalOut += m.OutputTokens
	}
	v.TotalOutput = fmtCompact(totalOut)
	v.TotalMessages = len(full.Messages)

	// Tail of the message list, oldest-first within the shown slice
	// so a reader can scan top-to-bottom and see progression.
	start := len(full.Messages) - recentMessageLimit
	if start < 0 {
		start = 0
	}
	recent := full.Messages[start:]
	v.RecentCount = len(recent)
	for _, m := range recent {
		v.RecentMessages = append(v.RecentMessages, toViewMessage(m))
	}
	return v
}

func toViewMessage(m session.FullMessage) viewMessage {
	role := "USER"
	cls := "user"
	if m.Type == "assistant" {
		role = "ASST"
		cls = "asst"
	}
	var tools string
	if len(m.ToolCalls) > 0 {
		tools = joinStrings(m.ToolCalls, ", ")
	}
	var outFmt string
	if m.OutputTokens > 0 {
		outFmt = fmt.Sprintf("%s out", fmtCompact(m.OutputTokens))
	}
	return viewMessage{
		TimeFmt:      m.Timestamp.Local().Format("15:04:05"),
		Role:         role,
		RoleClass:    cls,
		Tools:        tools,
		OutputTokens: outFmt,
		Text:         truncateText(m.TextContent, textTruncate),
	}
}

// --- chart ---

type chartSample struct {
	minutesFromStart float64
	velocity         float64
}

// computeChartSamples recomputes the rolling 2-minute velocity per
// assistant message with non-zero output_tokens. It's a near-copy of
// anomaly.sessionVelocitySamples but lives here to avoid coupling the
// report to internal details of the anomaly package. The list of
// token samples is already sorted chronologically by the parser.
func computeChartSamples(full *session.FullSession) []chartSample {
	type tokenPoint struct {
		ts     time.Time
		tokens int64
	}
	var tokens []tokenPoint
	for _, m := range full.Messages {
		if m.Type != "assistant" || m.OutputTokens <= 0 || m.Timestamp.IsZero() {
			continue
		}
		tokens = append(tokens, tokenPoint{m.Timestamp, m.OutputTokens})
	}
	if len(tokens) < 2 {
		return nil
	}

	window := 2 * time.Minute
	var out []chartSample
	startTime := tokens[0].ts
	for i := range tokens {
		cutoff := tokens[i].ts.Add(-window)
		var total int64
		for j := i; j >= 0 && !tokens[j].ts.Before(cutoff); j-- {
			total += tokens[j].tokens
		}
		out = append(out, chartSample{
			minutesFromStart: tokens[i].ts.Sub(startTime).Minutes(),
			velocity:         float64(total) / window.Minutes(),
		})
	}
	return out
}

// renderSVGChart produces a self-contained SVG element drawing the
// velocity series, the baseline, and a shaded region where the series
// exceeds the baseline. No external fonts, no JavaScript.
func renderSVGChart(samples []chartSample, baseline float64, width, height int) string {
	if len(samples) == 0 {
		return `<svg viewBox="0 0 1 1" xmlns="http://www.w3.org/2000/svg"></svg>`
	}

	padL, padR, padT, padB := 50, 12, 12, 22
	plotW := width - padL - padR
	plotH := height - padT - padB

	// Scale.
	maxX := samples[len(samples)-1].minutesFromStart
	if maxX <= 0 {
		maxX = 1
	}
	maxY := baseline
	for _, s := range samples {
		if s.velocity > maxY {
			maxY = s.velocity
		}
	}
	if maxY <= 0 {
		maxY = 1
	}
	// Add 10% headroom so the peak isn't pinned to the top edge.
	maxY *= 1.1

	xFor := func(minutes float64) float64 {
		return float64(padL) + (minutes/maxX)*float64(plotW)
	}
	yFor := func(v float64) float64 {
		return float64(padT+plotH) - (v/maxY)*float64(plotH)
	}

	var series string
	for i, s := range samples {
		x, y := xFor(s.minutesFromStart), yFor(s.velocity)
		if i == 0 {
			series += fmt.Sprintf("M %.1f,%.1f ", x, y)
		} else {
			series += fmt.Sprintf("L %.1f,%.1f ", x, y)
		}
	}

	// Shaded region: for every run where velocity > baseline, shade
	// under the curve down to the baseline line. Kept simple: per
	// segment, a filled polygon bounded by the curve above and the
	// baseline below.
	var shades string
	inRun := false
	var runStart int
	closeRun := func(end int) {
		if end-runStart < 2 {
			return
		}
		var pts string
		// Top of polygon: curve.
		for i := runStart; i <= end; i++ {
			pts += fmt.Sprintf("%.1f,%.1f ",
				xFor(samples[i].minutesFromStart),
				yFor(samples[i].velocity),
			)
		}
		// Bottom of polygon: baseline, right-to-left.
		for i := end; i >= runStart; i-- {
			pts += fmt.Sprintf("%.1f,%.1f ",
				xFor(samples[i].minutesFromStart),
				yFor(baseline),
			)
		}
		shades += fmt.Sprintf(`<polygon points="%s" fill="var(--chart-shade, rgba(255,95,95,0.15))" />`, pts)
	}
	for i, s := range samples {
		if s.velocity > baseline {
			if !inRun {
				inRun = true
				runStart = i
			}
		} else {
			if inRun {
				closeRun(i - 1)
				inRun = false
			}
		}
	}
	if inRun {
		closeRun(len(samples) - 1)
	}

	// Y-axis labels: baseline and max.
	baselineY := yFor(baseline)
	maxYLabel := fmtVelocity(maxY / 1.1)
	baselineLabel := fmtVelocity(baseline)

	svg := fmt.Sprintf(`<svg viewBox="0 0 %d %d" xmlns="http://www.w3.org/2000/svg" role="img" aria-label="velocity chart">
  <rect x="0" y="0" width="%d" height="%d" fill="none" />
  %s
  <line x1="%d" y1="%.1f" x2="%d" y2="%.1f" stroke="var(--chart-base,#626262)" stroke-width="1" stroke-dasharray="4,3" />
  <path d="%s" fill="none" stroke="var(--chart-line,#00d7ff)" stroke-width="1.6" />
  <text x="%d" y="%d" fill="var(--dim,#9097a8)" font-size="10" text-anchor="end">%s</text>
  <text x="%d" y="%.1f" fill="var(--dim,#9097a8)" font-size="10" text-anchor="end" dominant-baseline="middle">%s</text>
  <text x="%d" y="%d" fill="var(--dim,#9097a8)" font-size="10">0 min</text>
  <text x="%d" y="%d" fill="var(--dim,#9097a8)" font-size="10" text-anchor="end">%.0f min</text>
</svg>`,
		width, height,
		width, height,
		shades,
		padL, baselineY, width-padR, baselineY,
		series,
		padL-4, padT+10, maxYLabel,
		padL-4, baselineY, baselineLabel,
		padL, height-6,
		width-padR, height-6, maxX,
	)
	return svg
}

// --- helpers ---

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func fmtVelocity(v float64) string {
	if v >= 1000 {
		return fmt.Sprintf("%.1fK tok/min", v/1000)
	}
	return fmt.Sprintf("%.0f tok/min", v)
}

func fmtRatio(numerator, denominator float64) string {
	if denominator <= 0 {
		return "∞"
	}
	r := numerator / denominator
	if r >= 10 {
		return fmt.Sprintf("%.0f", math.Round(r))
	}
	return fmt.Sprintf("%.1f", r)
}

func fmtDuration(d time.Duration) string {
	if d <= 0 {
		return "0m"
	}
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	return fmt.Sprintf("%dh %dm", h, m)
}

func fmtCompact(n int64) string {
	if n >= 1_000_000 {
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	}
	if n >= 1_000 {
		return fmt.Sprintf("%.1fK", float64(n)/1_000)
	}
	return fmt.Sprintf("%d", n)
}

func truncateText(s string, n int) string {
	s = stripWhitespace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// stripWhitespace collapses runs of whitespace (including newlines)
// into single spaces so report messages don't carry giant vertical
// gaps from Claude's output formatting.
func stripWhitespace(s string) string {
	var b []byte
	prevSpace := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\n' || c == '\r' || c == '\t' || c == ' ' {
			if !prevSpace {
				b = append(b, ' ')
				prevSpace = true
			}
			continue
		}
		b = append(b, c)
		prevSpace = false
	}
	// trim leading/trailing space
	start, end := 0, len(b)
	for start < end && b[start] == ' ' {
		start++
	}
	for end > start && b[end-1] == ' ' {
		end--
	}
	return string(b[start:end])
}

func joinStrings(ss []string, sep string) string {
	if len(ss) == 0 {
		return ""
	}
	out := ss[0]
	for i := 1; i < len(ss); i++ {
		out += sep + ss[i]
	}
	return out
}
