package notify

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

// Send surfaces a notification that persists until the user dismisses it.
//
// On macOS we use `display alert` (a modal dialog) rather than `display
// notification` (a toast) because toasts auto-dismiss after a few seconds
// and anomaly events need to survive the glance — a user monitoring vig in a
// tmux pane will often miss a banner but cannot miss a modal.
//
// The osascript call blocks until the user clicks OK, so Send runs it in a
// goroutine to keep the Bubble Tea update loop responsive. Errors are
// swallowed because there is no sensible recovery path from inside a
// detector callback.
func Send(title, message string) error {
	if runtime.GOOS != "darwin" {
		return nil
	}
	go func() {
		script := fmt.Sprintf(
			`display alert %s message %s as critical`,
			quote(title), quote(message),
		)
		_ = exec.Command("osascript", "-e", script).Run()
	}()
	return nil
}

// quote wraps s in AppleScript double quotes, escaping embedded quotes and
// backslashes. AppleScript's string-literal rules are the same as C's for
// these two characters.
func quote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}
