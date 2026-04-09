package session

import "time"

// Session is the minimal view of a Claude Code session needed to compute
// output-token velocity. Fields the daemon does not consume (cwd, branch,
// model, cache counters, subagent children, etc.) are deliberately absent
// from the hot-path struct — when a report needs them they are re-parsed
// on demand via LoadFull.
//
// FilePath is kept so an anomaly callback can hand a session off to the
// report package without re-discovering where the JSONL lives.
type Session struct {
	ID        string
	StartTime time.Time
	Messages  []Message
	FilePath  string
}

// Message is a single JSONL entry projected to the two fields the anomaly
// detector reads. The parser fills in additional fields internally but they
// are discarded when the Message is attached to a Session.
type Message struct {
	Timestamp    time.Time
	OutputTokens int64
}
