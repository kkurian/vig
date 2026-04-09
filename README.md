# vig

> keep an eye on claude

Background anomaly detector for [Claude Code](https://docs.anthropic.com/en/docs/claude-code) sessions. Pops a persistent macOS alert when one of your sessions has been burning tokens sustained above your historical 95th percentile for 5+ minutes.

- Zero network calls. No LLM usage. Pure local Go + macOS `osascript`.
- Reads `~/.claude/projects/*.jsonl` every 30 seconds (gated on `pgrep claude`, so idle when Claude isn't running).
- Learns your normal output velocity from the last 30 days of sessions and refreshes the baseline every 6 hours.
- Runs as a LaunchAgent at login. Invisible until something is wrong.

## Install

```bash
curl -fsSL https://raw.githubusercontent.com/kkurian/vig/main/install.sh | bash
vig install
```

The first line downloads the latest release binary for your architecture (darwin/amd64 or darwin/arm64), verifies its SHA-256 against the checksum file in the release, and installs it to `/usr/local/bin/vig`. Set `VIG_INSTALL_DIR` to change the target; set `VIG_VERSION` to pin a specific tag.

The second line writes `~/Library/LaunchAgents/com.kkurian.vig.plist` pointing at the installed binary and boots it via `launchctl`. Re-run `vig install` after upgrading to re-point the LaunchAgent at the new binary.

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

`~/Library/Logs/vig.log`. Events only — start, baseline recompute, anomaly, shutdown. No per-scan spam.

## Manual run

```bash
vig            # foreground, Ctrl-C to stop
```

Useful for debugging or running without the LaunchAgent. Stdout goes to your terminal.

## How detection works

The detector reads assistant-message timestamps and `usage.output_tokens` from each active JSONL and computes output-token velocity over a rolling 2-minute window. That velocity is compared against the 95th percentile of your last 30 days of per-message velocities. If it exceeds P95 for 5+ consecutive minutes, you get one alert per session per anomaly (it won't re-alert until the session cools down and heats up again).

Cold start (fewer than ~10 sessions of history) falls back to a fixed threshold of 5000 tokens/min until enough data accumulates.

---

[MIT](LICENSE) © 2026 Kerry Ivan Kurian
