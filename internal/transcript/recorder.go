package transcript

// Recorder is responsible for monitoring all the events
// generated during the agent's operation and persistently recording them.
type Recorder interface {
	// Append persists one event and returns the sequenced copy that was written.
	// Callers that keep a projection must fold that copy, not the argument.
	Append(Event) (Event, error)
	ReadAll() ([]Event, error)
	Subscribe() <-chan Event
}
