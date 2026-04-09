package daemon

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"text/template"
)

// Legacy label used by the pre-0.2 LaunchAgent. Kept only so Install and
// Uninstall can clean it up on upgrade.
const legacyLaunchLabel = "com.kkurian.vig"

const infoPlistTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>CFBundleIdentifier</key>
    <string>com.kkurian.vig</string>
    <key>CFBundleName</key>
    <string>vig</string>
    <key>CFBundleDisplayName</key>
    <string>vig</string>
    <key>CFBundleExecutable</key>
    <string>vig</string>
    <key>CFBundleVersion</key>
    <string>{{.Version}}</string>
    <key>CFBundleShortVersionString</key>
    <string>{{.Version}}</string>
    <key>CFBundlePackageType</key>
    <string>APPL</string>
    <key>CFBundleInfoDictionaryVersion</key>
    <string>6.0</string>
    <key>LSUIElement</key>
    <true/>
    <key>LSMinimumSystemVersion</key>
    <string>10.15</string>
</dict>
</plist>
`

// Install creates ~/Applications/vig.app (wrapping the currently-running
// binary), registers the bundle as a macOS Login Item via System Events,
// and launches it immediately. Idempotent: re-running Install cleans up
// any prior LaunchAgent, any prior Login Item matching "vig", and kills
// any running daemon before installing the fresh copy.
//
// The Login Item registration will trigger a one-time macOS Automation
// permission prompt ("vig wants to control System Events"). Click OK.
func Install(version string) error {
	srcBinary, err := resolveSelf()
	if err != nil {
		return err
	}

	bundlePath, err := bundlePath()
	if err != nil {
		return err
	}
	logFile, err := logPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(logFile), 0o755); err != nil {
		return fmt.Errorf("create Logs dir: %w", err)
	}

	// --- Legacy + prior-install cleanup ---
	cleanupLegacyLaunchAgent()
	if err := removeVigLoginItems(); err != nil {
		return fmt.Errorf("remove existing Login Item: %w", err)
	}
	killVigDaemon()

	// Remove any existing app bundle so we start fresh. `cp -p` over an
	// existing binary can leave stale metadata, and truncating + writing
	// is simpler than managing upgrades in place.
	if err := os.RemoveAll(bundlePath); err != nil {
		return fmt.Errorf("remove existing %s: %w", bundlePath, err)
	}

	// --- Build the new bundle ---
	macOSDir := filepath.Join(bundlePath, "Contents", "MacOS")
	if err := os.MkdirAll(macOSDir, 0o755); err != nil {
		return fmt.Errorf("create bundle MacOS dir: %w", err)
	}

	dstBinary := filepath.Join(macOSDir, "vig")
	if err := copyFile(srcBinary, dstBinary, 0o755); err != nil {
		return fmt.Errorf("copy binary into bundle: %w", err)
	}

	infoPlistPath := filepath.Join(bundlePath, "Contents", "Info.plist")
	if err := writeInfoPlist(infoPlistPath, version); err != nil {
		return fmt.Errorf("write Info.plist: %w", err)
	}

	// --- Register as a Login Item ---
	if err := addVigLoginItem(bundlePath); err != nil {
		return fmt.Errorf("register Login Item: %w", err)
	}

	// --- Launch right now so it's running before the next login ---
	if out, err := exec.Command("open", bundlePath).CombinedOutput(); err != nil {
		return fmt.Errorf("launch vig.app: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Uninstall removes everything Install creates, plus any legacy state
// from the 0.1.x LaunchAgent era. Safe to run regardless of prior state.
func Uninstall() error {
	cleanupLegacyLaunchAgent()
	if err := removeVigLoginItems(); err != nil {
		return fmt.Errorf("remove Login Item: %w", err)
	}
	killVigDaemon()

	bp, err := bundlePath()
	if err != nil {
		return err
	}
	if err := os.RemoveAll(bp); err != nil {
		return fmt.Errorf("remove %s: %w", bp, err)
	}
	return nil
}

// resolveSelf returns the absolute, symlink-free path to the running
// vig binary. Used as the source for the copy into the .app bundle.
func resolveSelf() (string, error) {
	bin, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve executable: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(bin)
	if err != nil {
		return "", fmt.Errorf("resolve executable symlink: %w", err)
	}
	return resolved, nil
}

func bundlePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Applications", "vig.app"), nil
}

// LogPath is exported so main.go can redirect the daemon's log output
// to the same file regardless of how it was launched (Login Item,
// foreground, etc.).
func LogPath() string {
	p, _ := logPath()
	return p
}

func logPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "Logs", "vig.log"), nil
}

// copyFile copies src to dst, creating dst with the given mode. Used
// only for the binary copy into the bundle.
func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func writeInfoPlist(path, version string) error {
	if version == "" {
		version = "0"
	}
	tmpl, err := template.New("infoplist").Parse(infoPlistTemplate)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, struct{ Version string }{Version: version}); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

// --- Legacy LaunchAgent cleanup ---

func cleanupLegacyLaunchAgent() {
	// bootout any running legacy job, ignoring errors: a missing job
	// is not a failure.
	target := fmt.Sprintf("gui/%d/%s", os.Getuid(), legacyLaunchLabel)
	_ = exec.Command("launchctl", "bootout", target).Run()

	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	legacyPlist := filepath.Join(home, "Library", "LaunchAgents", legacyLaunchLabel+".plist")
	_ = os.Remove(legacyPlist)
}

// --- Login Item management via System Events ---

// addVigLoginItem runs the osascript equivalent of
//
//	tell application "System Events"
//	    make new login item at end of login items
//	        with properties {path:"<bundlePath>", hidden:true}
//	end tell
//
// The first call triggers macOS's Automation permission prompt for
// "vig wants to control System Events". The user must click OK.
func addVigLoginItem(bundlePath string) error {
	script := fmt.Sprintf(
		`tell application "System Events" to make new login item at end of login items with properties {path:%s, hidden:true}`,
		appleScriptString(bundlePath),
	)
	if out, err := exec.Command("osascript", "-e", script).CombinedOutput(); err != nil {
		return fmt.Errorf("osascript add login item: %w: %s",
			err, strings.TrimSpace(string(out)))
	}
	return nil
}

// removeVigLoginItems deletes every vig-related login item. Matches:
//
//   - Anything named "vig" (catches bare-binary entries like
//     /usr/local/bin/vig that users may have added manually during
//     the 0.1.x LaunchAgent era).
//   - Anything whose path contains "vig.app" (catches the .app
//     bundle this installer creates).
//
// Deliberately narrow — we do not match "*vig*" because that would
// also hit unrelated apps whose names happen to contain the substring.
// Uses the `delete (every login item whose ...)` idiom instead of
// manual iteration; AppleScript object references can go stale between
// a collect loop and a later delete loop.
func removeVigLoginItems() error {
	script := `tell application "System Events"
    try
        delete (every login item whose name is "vig" or path contains "vig.app")
    end try
end tell`
	if out, err := exec.Command("osascript", "-e", script).CombinedOutput(); err != nil {
		return fmt.Errorf("osascript remove login items: %w: %s",
			err, strings.TrimSpace(string(out)))
	}
	return nil
}

// appleScriptString quotes s for inclusion inside an AppleScript string
// literal. AppleScript uses the same escapes as C for backslashes and
// double quotes.
func appleScriptString(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}

// killVigDaemon locates any running vig daemon (including legacy
// LaunchAgent instances at /usr/local/bin/vig and new Login Item
// instances inside vig.app/Contents/MacOS/vig) and SIGTERMs them.
// It explicitly avoids killing its own PID so that Install can call
// this without self-terminating mid-install.
func killVigDaemon() {
	self := os.Getpid()
	out, err := exec.Command("pgrep", "-f", "vig").Output()
	if err != nil {
		return
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSpace(line))
		if err != nil || pid == self {
			continue
		}
		// Confirm it's actually a vig process (and not this vig install
		// command or some unrelated match). Check the command line.
		cmd, err := exec.Command("ps", "-o", "command=", "-p", strconv.Itoa(pid)).Output()
		if err != nil {
			continue
		}
		cmdStr := strings.TrimSpace(string(cmd))
		// Match only the daemon forms: installed app-bundle binary, or
		// /usr/local/bin/vig running with no args (foreground daemon).
		isDaemon := strings.Contains(cmdStr, "vig.app/Contents/MacOS/vig") ||
			cmdStr == "/usr/local/bin/vig"
		if !isDaemon {
			continue
		}
		proc, err := os.FindProcess(pid)
		if err != nil {
			continue
		}
		_ = proc.Signal(os.Interrupt)
	}
}
