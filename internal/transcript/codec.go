package transcript

import (
	"encoding/json"
	"fmt"
)

// FormatVersion is the on-disk transcript format version.
const FormatVersion = 1

// envelope is the stable JSONL record shape. The event itself lives in Data so
// sequence numbers and format versioning stay independent of event fields.
type envelope struct {
	Version int             `json:"v"`
	Seq     int64           `json:"seq"`
	Type    string          `json:"type"`
	Data    json.RawMessage `json:"data"`
}

// Encode marshals one event into its JSONL record (without the trailing
// newline).
func Encode(ev Event) ([]byte, error) {
	if ev == nil {
		return nil, fmt.Errorf("transcript: cannot encode a nil event")
	}
	data, err := json.Marshal(ev)
	if err != nil {
		return nil, fmt.Errorf("transcript: encode %s: %w", ev.Type(), err)
	}
	record, err := json.Marshal(envelope{
		Version: FormatVersion,
		Seq:     ev.Seq(),
		Type:    ev.Type(),
		Data:    data,
	})
	if err != nil {
		return nil, fmt.Errorf("transcript: encode envelope for %s: %w", ev.Type(), err)
	}
	return record, nil
}

// Decode parses one JSONL record and reconstructs the event with its sequence
// number. Unknown versions and event types are errors.
func Decode(line []byte) (Event, error) {
	var env envelope
	if err := json.Unmarshal(line, &env); err != nil {
		return nil, fmt.Errorf("decode transcript envelope: %w", err)
	}
	if env.Version != FormatVersion {
		return nil, fmt.Errorf("unsupported transcript version %d (want %d)", env.Version, FormatVersion)
	}
	if env.Seq <= 0 {
		return nil, fmt.Errorf("invalid transcript sequence %d", env.Seq)
	}
	if len(env.Data) == 0 {
		return nil, fmt.Errorf("transcript event %q has no data", env.Type)
	}

	var (
		ev  Event
		err error
	)
	switch env.Type {
	case "user_message":
		var e UserMessage
		err = json.Unmarshal(env.Data, &e)
		ev = e
	case "assistant_output_msg":
		var e AssistantOutputMessage
		err = json.Unmarshal(env.Data, &e)
		ev = e
	case "assistant_reasoning_msg":
		var e AssistantReasoningMessage
		err = json.Unmarshal(env.Data, &e)
		ev = e
	case "assistant_reasoning_summary":
		var e AssistantReasoningSummary
		err = json.Unmarshal(env.Data, &e)
		ev = e
	case "tool_result":
		var e ToolResult
		err = json.Unmarshal(env.Data, &e)
		ev = e
	case "compaction_summary":
		var e CompactionSummary
		err = json.Unmarshal(env.Data, &e)
		ev = e
	case "turn_start":
		var e TurnStart
		err = json.Unmarshal(env.Data, &e)
		ev = e
	case "turn_end":
		var e TurnEnd
		err = json.Unmarshal(env.Data, &e)
		ev = e
	case "turn_failed":
		var e TurnFailed
		err = json.Unmarshal(env.Data, &e)
		ev = e
	case "step_start":
		var e StepStart
		err = json.Unmarshal(env.Data, &e)
		ev = e
	case "step_end":
		var e StepEnd
		err = json.Unmarshal(env.Data, &e)
		ev = e
	default:
		return nil, fmt.Errorf("unknown transcript event type %q", env.Type)
	}
	if err != nil {
		return nil, fmt.Errorf("decode transcript event %q: %w", env.Type, err)
	}
	return WithSeq(ev, env.Seq), nil
}

// WithSeq returns a copy of ev carrying seq. Events are value types and Event
// intentionally does not expose a setter, so this helper lives in the transcript
// package where it can stamp the embedded baseEvent directly.
func WithSeq(ev Event, seq int64) Event {
	switch e := ev.(type) {
	case UserMessage:
		e.seq = seq
		return e
	case AssistantOutputMessage:
		e.seq = seq
		return e
	case AssistantReasoningMessage:
		e.seq = seq
		return e
	case AssistantReasoningSummary:
		e.seq = seq
		return e
	case ToolResult:
		e.seq = seq
		return e
	case CompactionSummary:
		e.seq = seq
		return e
	case TurnStart:
		e.seq = seq
		return e
	case TurnEnd:
		e.seq = seq
		return e
	case TurnFailed:
		e.seq = seq
		return e
	case StepStart:
		e.seq = seq
		return e
	case StepEnd:
		e.seq = seq
		return e
	default:
		return ev
	}
}
