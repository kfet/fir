// Ported from: packages/ai/src/utils/event-stream.ts
// Upstream hash: 1caadb2e
package ai

import "sync"

// AssistantMessageEventStream is a channel-based event stream for LLM responses.
// Consumers read events from the Events channel. Producers call Push() and End().
// The Result() method blocks until the stream completes and returns the final message.
type AssistantMessageEventStream struct {
	// Events is the channel consumers read from.
	Events chan AssistantMessageEvent

	mu          sync.Mutex
	done        bool
	result      *AssistantMessage
	resultCh    chan struct{} // closed when result is available
	resultReady bool
}

// NewAssistantMessageEventStream creates a new event stream.
// The Events channel is buffered to avoid blocking producers.
func NewAssistantMessageEventStream() *AssistantMessageEventStream {
	return &AssistantMessageEventStream{
		Events:   make(chan AssistantMessageEvent, 64),
		resultCh: make(chan struct{}),
	}
}

// Push sends an event to the stream. If the event is a "done" or "error" event,
// the final result is captured. It's safe to call from any goroutine.
//
// If the event carries a Partial message, it is snapshotted so that consumers
// can safely read it without racing against subsequent producer mutations.
func (s *AssistantMessageEventStream) Push(event AssistantMessageEvent) {
	s.mu.Lock()
	if s.done {
		s.mu.Unlock()
		return
	}

	if event.IsDone() {
		s.result = event.FinalMessage()
		if !s.resultReady {
			s.resultReady = true
			close(s.resultCh)
		}
	}
	s.mu.Unlock()

	// Snapshot the partial message so the consumer gets an immutable copy.
	// The producer (provider goroutine) will continue mutating the original
	// for subsequent deltas; without this copy the consumer would race.
	if event.Partial != nil {
		event.Partial = event.Partial.SnapshotContent()
	}

	s.Events <- event
}

// End closes the event stream. After End(), no more events will be delivered.
// If a result was not already set via a done/error event, the provided fallback is used.
func (s *AssistantMessageEventStream) End(fallback *AssistantMessage) {
	s.mu.Lock()
	if s.done {
		s.mu.Unlock()
		return
	}
	s.done = true
	if !s.resultReady {
		s.result = fallback
		s.resultReady = true
		close(s.resultCh)
	}
	s.mu.Unlock()

	close(s.Events)
}

// Result blocks until the stream produces a final result (done or error event)
// and returns the final AssistantMessage.
func (s *AssistantMessageEventStream) Result() *AssistantMessage {
	<-s.resultCh
	return s.result
}

// Collect reads all events from the stream and returns them as a slice,
// along with the final result. This is useful for testing.
func (s *AssistantMessageEventStream) Collect() ([]AssistantMessageEvent, *AssistantMessage) {
	var events []AssistantMessageEvent
	for event := range s.Events {
		events = append(events, event)
	}
	return events, s.Result()
}
