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
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/kkurian/vig/internal/config"
	"github.com/kkurian/vig/internal/daemon"
)

// version is injected at release build time via
// -ldflags "-X main.version=v0.2.0". Defaults to "dev" for local builds.
var version = "dev"

func main() {
	log.SetFlags(log.LstdFlags | log.LUTC)

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
    vig              Run the daemon in the foreground.
    vig install      Install as a Login Item (auto-starts at login).
    vig uninstall    Remove the Login Item.
    vig --version    Show version.
    vig --help       Show this help.

Config (optional): ~/.config/vig/config.json
Logs:              ~/Library/Logs/vig.log`)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "vig:", err)
	os.Exit(1)
}
