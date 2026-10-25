package session

import (
	"context"
	"sync"
	"time"

	"github.com/kfet/fir/pkg/ai/providers"
)

// Provider response headers, per agent session.
//
// The external agent module runs each turn on its own background context and
// its message types carry no header field, so fir cannot thread headers
// through the message itself. Instead the default stream function
// (defaultstream.go) opens a capture slot per stream call under the agent's
// session id (StreamOptions.SessionID) and the provider transport fills it.
// The extension bridge later matches a message to its call by time: every
// provider stamps AssistantMessage.Timestamp after its call started and before
// the next call can start, so the latest slot started at or before that
// timestamp is the message's. Matching by time, not "latest slot", keeps the
// pairing right even when event delivery lags behind the agent loop.
// Side queries and calls without a session id are not recorded.

const respHeaderSlots = 4 // per session: covers a few calls of event lag

type respHeaderSlot struct {
	startMs int64
	headers map[string]string
}

type respHeaderLog struct {
	mu    sync.Mutex
	slots []*respHeaderSlot // oldest first
}

var responseHeaderLogs sync.Map // agent session id -> *respHeaderLog

// withResponseHeaderCapture opens a capture slot for a new stream call and
// returns a ctx whose provider responses fill it (last response wins).
func withResponseHeaderCapture(ctx context.Context, sessionID string) context.Context {
	if sessionID == "" {
		return ctx
	}
	v, _ := responseHeaderLogs.LoadOrStore(sessionID, &respHeaderLog{})
	log := v.(*respHeaderLog)
	slot := &respHeaderSlot{startMs: time.Now().UnixMilli()}
	log.mu.Lock()
	log.slots = append(log.slots, slot)
	if len(log.slots) > respHeaderSlots {
		log.slots = log.slots[len(log.slots)-respHeaderSlots:]
	}
	log.mu.Unlock()
	return providers.WithResponseHeaderSink(ctx, func(h map[string]string) {
		log.mu.Lock()
		slot.headers = h
		log.mu.Unlock()
	})
}

// ResponseHeadersFor returns the redacted headers of the last provider HTTP
// response of the stream call that produced a message stamped msgTimestampMs
// (Unix ms; 0 means "the latest call"), or nil when that call got none
// (transport failure, non-HTTP transport, not recorded).
func (s *AgentSession) ResponseHeadersFor(msgTimestampMs int64) map[string]string {
	if s == nil || s.Agent == nil {
		return nil
	}
	v, ok := responseHeaderLogs.Load(s.Agent.GetSessionID())
	if !ok {
		return nil
	}
	log := v.(*respHeaderLog)
	log.mu.Lock()
	defer log.mu.Unlock()
	for i := len(log.slots) - 1; i >= 0; i-- {
		if msgTimestampMs == 0 || log.slots[i].startMs <= msgTimestampMs {
			return log.slots[i].headers
		}
	}
	return nil
}

func dropResponseHeaders(sessionID string) {
	responseHeaderLogs.Delete(sessionID)
}
