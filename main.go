// vig is a headless background anomaly detector for Claude Code
// sessions. It scans ~/.claude/projects/ for active JSONL session logs,
// runs a P95-over-2-minute-window velocity detector against them, and
// raises a persistent macOS alert when a session sustains above the
// 95th percentile for 5+ consecutive minutes.
//
// No TUI, no remote API, no network calls. Pure local Go + osascript.
package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/kkurian/vig/internal/config"
	"github.com/kkurian/vig/internal/daemon"
	"github.com/kkurian/vig/internal/report"
	"github.com/kkurian/vig/internal/session"
)

// version is injected at release build time via
// -ldflags "-X main.version=v0.3.0". Defaults to "dev" for local builds.
var version = "dev"

func main() {
	log.SetFlags(log.LstdFlags | log.LUTC)
	daemon.Version = version

	if len(os.Args) >= 2 {
		switch os.Args[1] {
		case "install":
			if err := daemon.Install(version); err != nil {
				fatal(err)
			}
			fmt.Printf("vig %s installed. Logs: %s\n", version, daemon.LogPath())
			fmt.Println("Visible in System Settings → General → Login Items → Open at Login.")
			return
		case "uninstall":
			if err := daemon.Uninstall(); err != nil {
				fatal(err)
			}
			fmt.Println("vig uninstalled.")
			return
		case "report":
			cmdReport(os.Args[2:])
			return
		case "reports":
			cmdReports(os.Args[2:])
			return
		case "-v", "--version", "version":
			fmt.Println("vig", version)
			return
		case "-h", "--help", "help":
			printUsage()
			return
		default:
			fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n\n", os.Args[1])
			printUsage()
			os.Exit(2)
		}
	}

	// No subcommand → run the daemon in the foreground.
	setupLogging()

	cfg := config.Load()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	go func() {
		sig := <-sigCh
		log.Printf("received %s, shutting down", sig)
		cancel()
	}()

	log.Printf("vig %s starting: scan=%s baseline_refresh=%s",
		version, cfg.ScanInterval(), cfg.BaselineRefresh())

	if err := daemon.Run(ctx, cfg); err != nil && err != context.Canceled {
		fatal(err)
	}
}

// cmdReport implements `vig report <session-prefix>`: locate a JSONL
// file in ~/.claude/projects whose basename starts with the given
// prefix, build a fresh anomaly-style report against it, and open
// the report in the user's default HTML handler.
//
// This is primarily a diagnostic — it doesn't check the session for
// actual anomaly behavior, it just generates the same report the
// daemon would. Useful for eyeballing a session after the fact.
func cmdReport(args []string) {
	if len(args) != 1 || args[0] == "" {
		fmt.Fprintln(os.Stderr, "usage: vig report <session-id-prefix>")
		os.Exit(2)
	}
	prefix := args[0]

	path, err := findSessionJSONL(prefix)
	if err != nil {
		fatal(err)
	}

	msgs, err := session.ParseFile(path)
	if err != nil {
		fatal(fmt.Errorf("parse %s: %w", path, err))
	}
	if len(msgs) == 0 {
		fatal(fmt.Errorf("no assistant messages in %s", path))
	}
	s := &session.Session{
		ID:        trimExt(filepath.Base(path)),
		StartTime: msgs[0].Timestamp,
		Messages:  msgs,
		FilePath:  path,
	}
	// Velocity/baseline aren't meaningful for a manual report since
	// the session isn't currently exceeding anything — use the final
	// rolling window as the "velocity" reading and zero as baseline.
	v := finalWindowVelocity(msgs)
	reportPath, err := report.Build(report.Params{
		Session:     s,
		Velocity:    v,
		BaselineP95: 0,
		AlertTime:   time.Now(),
		Version:     version,
	})
	if err != nil {
		fatal(err)
	}
	fmt.Println(reportPath)
	if err := exec.Command("open", reportPath).Start(); err != nil {
		fatal(err)
	}
}

// cmdReports implements `vig reports` (list) and `vig reports N`
// (open the Nth most recent report).
func cmdReports(args []string) {
	entries, err := report.List()
	if err != nil {
		fatal(err)
	}
	if len(args) == 0 {
		if len(entries) == 0 {
			dir, _ := report.ReportsDir()
			fmt.Printf("No reports yet. (Reports land in %s when the daemon fires an alert.)\n", dir)
			return
		}
		for i, e := range entries {
			fmt.Printf("%3d  %s  %s\n",
				i+1,
				e.AlertTime.Format("2006-01-02 15:04:05"),
				e.SessionPfx,
			)
		}
		fmt.Println()
		fmt.Println("Open one with:  vig reports <N>")
		return
	}

	// vig reports <N>
	n, err := strconv.Atoi(args[0])
	if err != nil || n < 1 || n > len(entries) {
		fmt.Fprintf(os.Stderr, "invalid report index: %s (have %d reports)\n", args[0], len(entries))
		os.Exit(2)
	}
	target := entries[n-1].Path
	fmt.Println(target)
	if err := exec.Command("open", target).Start(); err != nil {
		fatal(err)
	}
}

// findSessionJSONL walks ~/.claude/projects looking for a JSONL file
// whose basename (without extension) begins with prefix. Returns an
// error if zero or more than one file matches.
func findSessionJSONL(prefix string) (string, error) {
	root := session.ProjectsDir()
	projDirs, err := os.ReadDir(root)
	if err != nil {
		return "", fmt.Errorf("read projects dir: %w", err)
	}
	var matches []string
	for _, pd := range projDirs {
		if !pd.IsDir() {
			continue
		}
		sessFiles, _ := filepath.Glob(filepath.Join(root, pd.Name(), "*.jsonl"))
		for _, f := range sessFiles {
			base := trimExt(filepath.Base(f))
			if len(base) >= len(prefix) && base[:len(prefix)] == prefix {
				matches = append(matches, f)
			}
		}
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("no session found with prefix %q", prefix)
	case 1:
		return matches[0], nil
	default:
		lines := "multiple sessions match:\n"
		for _, m := range matches {
			lines += "  " + m + "\n"
		}
		return "", fmt.Errorf("%s", lines)
	}
}

func trimExt(name string) string {
	ext := filepath.Ext(name)
	return name[:len(name)-len(ext)]
}

// finalWindowVelocity replays the last rolling-2-minute window over
// a session's messages and returns the resulting tok/min figure.
// Used by `vig report` when there's no live detector reading to
// surface.
func finalWindowVelocity(msgs []session.Message) float64 {
	if len(msgs) == 0 {
		return 0
	}
	last := msgs[len(msgs)-1].Timestamp
	cutoff := last.Add(-2 * time.Minute)
	var total int64
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Timestamp.Before(cutoff) {
			break
		}
		total += msgs[i].OutputTokens
	}
	return float64(total) / 2.0
}

// setupLogging points the global logger at ~/Library/Logs/vig.log so
// the daemon always writes to a known file regardless of how it was
// launched (Login Item, LaunchAgent, foreground shell). When stderr is
// a TTY — i.e. the user ran `./vig` directly — we also tee to stderr so
// live debugging is still possible.
//
// Any failure to open the log file is non-fatal: the default stderr
// sink is left in place and the daemon keeps running.
func setupLogging() {
	logPath := daemon.LogPath()
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(logPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return
	}

	if isCharDevice(os.Stderr) {
		log.SetOutput(io.MultiWriter(os.Stderr, f))
	} else {
		log.SetOutput(f)
	}
}

func isCharDevice(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

func printUsage() {
	fmt.Println(`vig — background anomaly detector for Claude Code sessions

Usage:
    vig                      Run the daemon in the foreground.
    vig install              Install as a Login Item (auto-starts at login).
    vig uninstall            Remove the Login Item.
    vig report <session>     Build an HTML report for a session prefix and open it.
    vig reports              List past anomaly reports.
    vig reports <N>          Open the Nth most-recent past report.
    vig --version            Show version.
    vig --help               Show this help.

Config (optional): ~/.config/vig/config.json
Logs:              ~/Library/Logs/vig.log
Reports:           ~/Library/Logs/vig-reports/`)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "vig:", err)
	os.Exit(1)
}
