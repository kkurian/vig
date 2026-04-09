# vig

> keep an eye on claude

Background anomaly detector for [Claude Code](https://docs.anthropic.com/en/docs/claude-code) sessions. Pops a persistent macOS alert when one of your sessions has been burning tokens sustained above your historical 95th percentile for 5+ minutes.

- Zero network calls. No LLM usage. Pure local Go + macOS `osascript`.
- Reads `~/.claude/projects/*.jsonl` every 30 seconds (gated on `pgrep claude`, so idle when Claude isn't running).
- Learns your normal output velocity from the last 30 days of sessions and refreshes the baseline every 6 hours.
- Installs as a Login Item (visible in System Settings) and auto-starts at every login. Invisible until something is wrong.

## Install

```bash
curl -fsSL https://raw.githubusercontent.com/kkurian/vig/main/install.sh | bash
vig install
```

The first line downloads the latest release binary for your architecture (darwin/amd64 or darwin/arm64), verifies its SHA-256 against the checksum file in the release, and installs it into a user-owned directory (prefers an existing in-PATH choice like `~/bin`, `~/.local/bin`, or `$GOPATH/bin`; falls back to `~/.local/bin`). No `sudo` required. Set `VIG_INSTALL_DIR` to override the location; set `VIG_VERSION` to pin a specific tag.

> **Why not `/usr/local/bin`?** macOS 15 Sequoia's `syspolicyd` can cache path-keyed Gatekeeper decisions that an in-place `cp` upgrade won't invalidate, causing a stale binary to SIGKILL on exec with no diagnostic (exit 137). Installing to a user-owned directory avoids the cache entirely.

The second line wraps the binary in `~/Applications/vig.app`, registers the app bundle as a macOS Login Item so it auto-starts at every login, and launches it immediately. You will see it listed in **System Settings → General → Login Items → Open at Login**.

**One-time permission prompt:** the first `vig install` triggers a macOS dialog ("vig wants to control System Events"). Click OK — this is what lets vig add itself to the Login Items list. Subsequent `vig install` runs (e.g. after upgrading) don't re-prompt.

Re-running `vig install` is safe and idempotent: it kills the running copy, removes any prior `vig.app` and any prior Login Item, copies the fresh binary in, and relaunches. Do this after every upgrade.

You will get a modal alert the next time a session goes sustained-hot. Dismiss it with a click.

### Alternative: build from source

```bash
go install github.com/kkurian/vig@latest
vig install
```

## Uninstall

```bash
vig uninstall
```

## Configuration (optional)

`~/.config/vig/config.json` — missing file means defaults:

```json
{
  "scan_interval_secs": 30,
  "baseline_refresh_hours": 6,
  "notify_on_anomaly": true
}
```

## Logs

`~/Library/Logs/vig.log`. Events only — start, baseline recompute, anomaly, shutdown. No per-scan spam. Written whether vig is running as a Login Item or in the foreground.

## Manual run

```bash
vig            # foreground, Ctrl-C to stop
```

Useful for debugging or running without installing as a Login Item. When stderr is a TTY the logger tees both to `~/Library/Logs/vig.log` and to your terminal, so you can also `tail -f ~/Library/Logs/vig.log` from another window.

## How detection works

The detector reads assistant-message timestamps and `usage.output_tokens` from each active JSONL and computes output-token velocity over a rolling 2-minute window. That velocity is compared against the 95th percentile of your last 30 days of per-message velocities. If it exceeds P95 for 5+ consecutive minutes, you get one alert per session per anomaly (it won't re-alert until the session cools down and heats up again).

Cold start (fewer than ~10 sessions of history) falls back to a fixed threshold of 5000 tokens/min until enough data accumulates.

---

[MIT](LICENSE) © 2026 Kerry Ivan Kurian
