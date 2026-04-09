package session

import "time"

// Session is the minimal view of a Claude Code session needed to compute
// output-token velocity. Fields the daemon does not consume (cwd, branch,
// model, cache counters, subagent children, etc.) are deliberately absent.
type Session struct {
	ID        string
	StartTime time.Time
	Messages  []Message
}

// Message is a single JSONL entry projected to the two fields the anomaly
// detector reads. The parser fills in additional fields internally but they
// are discarded when the Message is attached to a Session.
type Message struct {
	Timestamp    time.Time
	OutputTokens int64
}
