package session

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// FullSession is a richer view of a Claude Code session, populated only
// when an anomaly report needs to render human-readable context. The
// daemon's hot-path scan never loads this — it's reserved for the
// report package and the `vig report` CLI, which care about messages,
// tool calls, and metadata that the velocity detector does not.
type FullSession struct {
	ID          string
	ProjectName string
	ProjectDir  string
	Model       string
	GitBranch   string
	CWD         string
	FilePath    string
	StartTime   time.Time
	LastActive  time.Time

	Messages []FullMessage
}

// FullMessage carries the payload fields the report displays: role,
// timestamp, text content, tool calls, and both input/output token
// counts. The scanner's Message struct strips all of this in the
// interest of keeping the hot path lean.
type FullMessage struct {
	Type         string // "user" or "assistant"
	Timestamp    time.Time
	Model        string
	InputTokens  int64
	OutputTokens int64
	TextContent  string
	ToolCalls    []string // tool names only — we do not keep inputs to limit report size
}

// LoadFull parses a JSONL session file and returns a FullSession with
// the full message stream. Returns an error if the file can't be read
// or parsed.
func LoadFull(jsonlPath string) (*FullSession, error) {
	f, err := os.Open(jsonlPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	s := &FullSession{
		ID:         strings.TrimSuffix(filepath.Base(jsonlPath), ".jsonl"),
		ProjectDir: filepath.Base(filepath.Dir(jsonlPath)),
		FilePath:   jsonlPath,
	}
	s.ProjectName = projectNameFromDir(s.ProjectDir)

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024)

	for sc.Scan() {
		var line fullLine
		if json.Unmarshal(sc.Bytes(), &line) != nil {
			continue
		}

		if s.CWD == "" && line.CWD != "" {
			s.CWD = line.CWD
		}
		if s.GitBranch == "" && line.GitBranch != "" {
			s.GitBranch = line.GitBranch
		}

		if line.Type != "user" && line.Type != "assistant" {
			continue
		}

		ts, _ := time.Parse(time.RFC3339Nano, line.Timestamp)
		if s.StartTime.IsZero() && !ts.IsZero() {
			s.StartTime = ts
		}
		if !ts.IsZero() && ts.After(s.LastActive) {
			s.LastActive = ts
		}

		fm := FullMessage{
			Type:      line.Type,
			Timestamp: ts,
		}

		if len(line.Message) > 0 {
			var jmsg fullJSONLMessage
			if json.Unmarshal(line.Message, &jmsg) == nil {
				fm.Model = jmsg.Model
				if s.Model == "" && jmsg.Model != "" {
					s.Model = jmsg.Model
				}
				if jmsg.Usage != nil {
					fm.InputTokens = jmsg.Usage.InputTokens
					fm.OutputTokens = jmsg.Usage.OutputTokens
				}
				fm.TextContent, fm.ToolCalls = extractContent(jmsg.Content)
			}
		}

		s.Messages = append(s.Messages, fm)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return s, nil
}

// extractContent pulls human-readable text and tool call names out of
// a message's "content" field, which can be a plain string or an
// array of block objects depending on the message type.
func extractContent(raw json.RawMessage) (string, []string) {
	if len(raw) == 0 {
		return "", nil
	}
	// Case 1: content is a bare string (user messages).
	var str string
	if json.Unmarshal(raw, &str) == nil {
		return str, nil
	}
	// Case 2: content is an array of blocks (assistant messages).
	var blocks []fullContentBlock
	if json.Unmarshal(raw, &blocks) != nil {
		return "", nil
	}
	var texts []string
	var tools []string
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if b.Text != "" {
				texts = append(texts, b.Text)
			}
		case "tool_use":
			if b.Name != "" {
				tools = append(tools, b.Name)
			}
		}
	}
	return strings.Join(texts, "\n"), tools
}

// projectNameFromDir turns Claude Code's dash-escaped directory name
// (e.g. "-Users-kerrykurian-Desktop-repos-vig") into a short,
// human-readable project name by taking the last non-empty segment.
// Worktree-suffixed directories have their suffix stripped first.
func projectNameFromDir(dirName string) string {
	name := dirName
	if idx := strings.Index(dirName, "--claude-worktrees-"); idx >= 0 {
		name = dirName[:idx]
	}
	parts := strings.Split(name, "-")
	for i := len(parts) - 1; i >= 0; i-- {
		if parts[i] != "" {
			return parts[i]
		}
	}
	return dirName
}

// --- JSON line shape ---

type fullLine struct {
	Type      string          `json:"type"`
	Timestamp string          `json:"timestamp"`
	CWD       string          `json:"cwd"`
	GitBranch string          `json:"gitBranch"`
	Message   json.RawMessage `json:"message"`
}

type fullJSONLMessage struct {
	Role    string             `json:"role"`
	Model   string             `json:"model"`
	Content json.RawMessage    `json:"content"`
	Usage   *fullJSONLUsage    `json:"usage"`
}

type fullJSONLUsage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
}

type fullContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
	Name string `json:"name"`
}
