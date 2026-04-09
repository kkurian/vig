package session

import (
	"bufio"
	"encoding/json"
	"os"
	"time"
)

// ParseFile reads a Claude Code JSONL file and returns one Message per
// assistant line that reports a non-zero output_tokens usage. All other
// lines (user messages, tool results, metadata, zero-token assistants)
// are discarded because the anomaly detector only cares about the tokens
// that actually came out of the model.
//
// Messages are returned in file order, which Claude Code writes
// chronologically. currentVelocity relies on that ordering for its
// backward scan + early break.
func ParseFile(path string) ([]Message, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var msgs []Message
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024)
	for sc.Scan() {
		if m, ok := parseLine(sc.Bytes()); ok {
			msgs = append(msgs, m)
		}
	}
	return msgs, sc.Err()
}

type jsonlLine struct {
	Type      string          `json:"type"`
	Timestamp string          `json:"timestamp"`
	Message   json.RawMessage `json:"message"`
}

type jsonlMessage struct {
	Usage *jsonlMessageUsage `json:"usage"`
}

type jsonlMessageUsage struct {
	OutputTokens int64 `json:"output_tokens"`
}

func parseLine(data []byte) (Message, bool) {
	var line jsonlLine
	if json.Unmarshal(data, &line) != nil {
		return Message{}, false
	}
	if line.Type != "assistant" || len(line.Message) == 0 {
		return Message{}, false
	}
	var jmsg jsonlMessage
	if json.Unmarshal(line.Message, &jmsg) != nil || jmsg.Usage == nil || jmsg.Usage.OutputTokens == 0 {
		return Message{}, false
	}
	ts, _ := time.Parse(time.RFC3339Nano, line.Timestamp)
	return Message{Timestamp: ts, OutputTokens: jmsg.Usage.OutputTokens}, true
}
