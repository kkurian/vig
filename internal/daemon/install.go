package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"
)

const launchLabel = "com.kkurian.vig"

const plistTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>{{.Label}}</string>
  <key>ProgramArguments</key>
  <array>
    <string>{{.Binary}}</string>
  </array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>ProcessType</key><string>Background</string>
  <key>StandardOutPath</key><string>{{.LogPath}}</string>
  <key>StandardErrorPath</key><string>{{.LogPath}}</string>
</dict>
</plist>
`

// Install writes ~/Library/LaunchAgents/com.kkurian.vig.plist pointing at
// the currently-running vig binary and boots it via launchctl. Idempotent:
// if the service is already loaded, it is unloaded first.
func Install() error {
	bin, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve executable: %w", err)
	}
	bin, err = filepath.EvalSymlinks(bin)
	if err != nil {
		return fmt.Errorf("resolve executable symlink: %w", err)
	}

	plistPath, err := plistPath()
	if err != nil {
		return err
	}
	logPath, err := logPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(plistPath), 0o755); err != nil {
		return fmt.Errorf("create LaunchAgents dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return fmt.Errorf("create Logs dir: %w", err)
	}

	tmpl, err := template.New("plist").Parse(plistTemplate)
	if err != nil {
		return fmt.Errorf("parse plist template: %w", err)
	}
	f, err := os.Create(plistPath)
	if err != nil {
		return fmt.Errorf("create plist: %w", err)
	}
	defer f.Close()
	if err := tmpl.Execute(f, struct{ Label, Binary, LogPath string }{
		Label:   launchLabel,
		Binary:  bin,
		LogPath: logPath,
	}); err != nil {
		return fmt.Errorf("write plist: %w", err)
	}

	// bootout ignores "not loaded" errors — this makes Install re-runnable
	// after a binary upgrade.
	target := fmt.Sprintf("gui/%d/%s", os.Getuid(), launchLabel)
	_ = exec.Command("launchctl", "bootout", target).Run()

	if out, err := exec.Command("launchctl", "bootstrap",
		fmt.Sprintf("gui/%d", os.Getuid()), plistPath).CombinedOutput(); err != nil {
		return fmt.Errorf("launchctl bootstrap: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Uninstall removes the LaunchAgent and the plist file. Safe to call even
// if vig was never installed.
func Uninstall() error {
	plistPath, err := plistPath()
	if err != nil {
		return err
	}
	target := fmt.Sprintf("gui/%d/%s", os.Getuid(), launchLabel)
	_ = exec.Command("launchctl", "bootout", target).Run()
	if err := os.Remove(plistPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove plist: %w", err)
	}
	return nil
}

// IsInstalled reports whether the LaunchAgent is currently loaded.
func IsInstalled() bool {
	target := fmt.Sprintf("gui/%d/%s", os.Getuid(), launchLabel)
	return exec.Command("launchctl", "print", target).Run() == nil
}

func plistPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", launchLabel+".plist"), nil
}

// LogPath is exported so `main.go` can point users at the log file.
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

