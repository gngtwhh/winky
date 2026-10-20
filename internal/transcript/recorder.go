package transcript

// Recorder is responsible for monitoring all the events
// generated during the agent's operation and persistently recording them.
type Recorder interface {
	// Append persists one event. Implementations assign the event's sequence
	// number and expose it through ReadAll and Subscribe.
	Append(Event) error
	ReadAll() ([]Event, error)
	Subscribe() <-chan Event
}
