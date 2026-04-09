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
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/kkurian/vig/internal/config"
	"github.com/kkurian/vig/internal/daemon"
)

// version is injected at release build time via
// -ldflags "-X main.version=v0.1.0". Defaults to "dev" for local builds.
var version = "dev"

func main() {
	log.SetFlags(log.LstdFlags | log.LUTC)

	if len(os.Args) >= 2 {
		switch os.Args[1] {
		case "install":
			if err := daemon.Install(); err != nil {
				fatal(err)
			}
			fmt.Printf("vig installed as LaunchAgent. Logs: %s\n", daemon.LogPath())
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

func printUsage() {
	fmt.Println(`vig — background anomaly detector for Claude Code sessions

Usage:
    vig              Run the daemon in the foreground.
    vig install      Install as a LaunchAgent (auto-starts at login).
    vig uninstall    Remove the LaunchAgent.
    vig --version    Show version.
    vig --help       Show this help.

Config (optional): ~/.config/vig/config.json
Logs (when installed): ~/Library/Logs/vig.log`)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "vig:", err)
	os.Exit(1)
}
