// Package session discovers and parses Claude Code session JSONL files.
// It exposes only what the anomaly daemon needs: stateless scans, no
// caches, no mutexes.
package session

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	claudeProjectsDir = ".claude/projects"
	recentWindow      = 5 * time.Minute
)

// ProjectsDir returns ~/.claude/projects, the directory Claude Code writes
// its JSONL session logs into.
func ProjectsDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, claudeProjectsDir)
}

// ScanActive returns sessions whose JSONL files were modified within the
// recent window, but only if at least one claude process is currently
// running. Gating on the process list is the main reason this is cheap:
// when Claude Code is not running we do zero filesystem work beyond the
// pgrep call.
func ScanActive() []*Session {
	if !hasClaudeProcesses() {
		return nil
	}
	now := time.Now()
	return scanDir(func(info os.FileInfo) bool {
		return now.Sub(info.ModTime()) <= recentWindow
	})
}

// ScanAll returns every JSONL found under ProjectsDir, regardless of
// mtime. Used only by the periodic baseline recompute.
func ScanAll() []*Session {
	return scanDir(func(os.FileInfo) bool { return true })
}

func scanDir(keep func(os.FileInfo) bool) []*Session {
	entries, err := os.ReadDir(ProjectsDir())
	if err != nil {
		return nil
	}
	var out []*Session
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		files, _ := filepath.Glob(filepath.Join(ProjectsDir(), e.Name(), "*.jsonl"))
		for _, f := range files {
			info, err := os.Stat(f)
			if err != nil || !keep(info) {
				continue
			}
			if s := buildSession(f); s != nil {
				out = append(out, s)
			}
		}
	}
	return out
}

func buildSession(jsonlPath string) *Session {
	msgs, err := ParseFile(jsonlPath)
	if err != nil || len(msgs) == 0 {
		return nil
	}
	return &Session{
		ID:        strings.TrimSuffix(filepath.Base(jsonlPath), ".jsonl"),
		StartTime: msgs[0].Timestamp,
		Messages:  msgs,
	}
}

func hasClaudeProcesses() bool {
	out, err := exec.Command("pgrep", "-f", "claude").Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) != ""
}
