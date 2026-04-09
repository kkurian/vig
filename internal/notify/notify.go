// Package notify is the macOS alert layer. It owns the osascript
// incantations needed to surface vig events as user-visible dialogs.
package notify

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

// SendAnomalyAlert pops a modal macOS alert with two buttons —
// "Show Report" (default, activated by Enter) and "Dismiss" (cancel,
// activated by Escape). If the user clicks "Show Report", vig opens
// reportPath in the user's default HTML handler. If the user clicks
// "Dismiss" (or presses Escape) nothing else happens.
//
// The alert is modal and will sit on screen until the user makes a
// choice — which is the point, because anomaly events are easy to
// miss and a banner that auto-dismisses is worse than useless. We
// run the osascript in a goroutine so the detector's callback path
// doesn't block waiting for the user.
//
// Errors from osascript and `open` are swallowed: there is no
// sensible recovery from inside a detector callback.
func SendAnomalyAlert(title, message, reportPath string) error {
	if runtime.GOOS != "darwin" {
		return nil
	}
	go func() {
		// AppleScript returns a record; `button returned of ...`
		// pulls the clicked label out of it. We echo that label to
		// stdout and then decide in Go what to do.
		script := fmt.Sprintf(
			`set r to (display alert %s message %s as critical `+
				`buttons {"Dismiss", "Show Report"} `+
				`default button "Show Report" `+
				`cancel button "Dismiss")`+"\n"+
				`return (button returned of r)`,
			quote(title), quote(message),
		)
		out, err := exec.Command("osascript", "-e", script).Output()
		if err != nil {
			return
		}
		if strings.TrimSpace(string(out)) == "Show Report" && reportPath != "" {
			// `open` forks LaunchServices and returns immediately.
			_ = exec.Command("open", reportPath).Start()
		}
	}()
	return nil
}

// quote wraps s in AppleScript double quotes, escaping embedded quotes
// and backslashes. AppleScript's string-literal rules match C's for
// these two characters.
func quote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}
