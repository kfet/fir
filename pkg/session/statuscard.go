package session

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/kfet/agent"
	"github.com/kfet/ai"
	"github.com/kfet/fir/pkg/session/store"
)

// Session status card.
//
// Every AgentSession mirrors its lifecycle state into the observable cards
// store as a single core-owned "session/status" card, so out-of-process
// observers (`fir observe`, `fir htop`, the observe_session tool, ssh-driven
// operators on another host) can read what the TUI shows without scraping a
// screen: is the agent idle, mid-turn, failed, or unable to run at all.
//
// The card is written from AgentSession's own event handler, which the agent
// loop invokes sequentially, so status transitions land on disk in order.
// (The observe extension's agent_start/agent_end handlers run on independent
// threads and can apply out of order — that is how a busy session used to be
// listed as "idle".)
//
// Detail is line-oriented "key: value" text — stable enough for scripts to
// parse, readable as-is in a card dump:
//
//	status: running
//	model: anthropic/claude-opus-5-5
//	tool: bash
//	runs: 3
//	error: …
//	notice: …

// Status values published in the "status:" line of the session/status card.
const (
	StatusIdle    = "idle"     // ready, waiting for input
	StatusRunning = "running"  // an agent run (turn) is in progress
	StatusError   = "error"    // the last run ended in an error; see "error:"
	StatusNoModel = "no-model" // no usable model — prompts will be refused
)

const (
	statusCardSource = "session"
	statusCardKey    = "status"
	// statusErrMax bounds the error/notice text kept in the card.
	statusErrMax = 2000
)

// sessionStatus is the mutable state behind the session/status card.
type sessionStatus struct {
	mu     sync.Mutex
	status string
	tool   string
	runs   int    // completed agent runs (agent_end count)
	err    string // error from the most recent run, cleared on the next run
	notice string // startup notice (e.g. model fallback); sticky
	// aborted is set by AgentSession.Abort and cleared at agent_start, so a
	// "context canceled" turn error is known to be the operator's abort
	// rather than a provider failure.
	aborted bool

	// Last card written, to skip identical rewrites (every assistant
	// message_end lands here). Keyed by store: /new and session switches
	// bind a fresh cards store that needs the card again.
	lastStore        *store.ObservableStore
	lastSlug, lastDt string
}

// Abort cancels the in-flight run (the ESC equivalent) and remembers that
// the cancellation was requested, so the status card reports the resulting
// "context canceled" turn as an abort, not an error.
func (s *AgentSession) Abort() {
	s.status.mu.Lock()
	s.status.aborted = true
	s.status.mu.Unlock()
	s.Agent.Abort()
}

// initStatus publishes the initial card for a freshly constructed session.
func (s *AgentSession) initStatus() {
	s.status.mu.Lock()
	defer s.status.mu.Unlock()
	if s.Model() == nil {
		s.status.status = StatusNoModel
	} else {
		s.status.status = StatusIdle
	}
	s.publishStatusLocked()
}

// SetStatusNotice attaches a sticky operator-facing notice (for example the
// model-fallback warning the CLI prints at startup) to the status card.
func (s *AgentSession) SetStatusNotice(notice string) {
	s.status.mu.Lock()
	defer s.status.mu.Unlock()
	s.status.notice = truncateStatusText(notice)
	s.publishStatusLocked()
}

// Status returns the current status value (one of the Status* constants).
func (s *AgentSession) Status() string {
	s.status.mu.Lock()
	defer s.status.mu.Unlock()
	return s.status.status
}

// statusPromptRefused records a prompt that never reached the agent loop
// (no model, etc.) so observers see why nothing happened.
func (s *AgentSession) statusPromptRefused(status string, err error) {
	s.status.mu.Lock()
	defer s.status.mu.Unlock()
	s.status.status = status
	s.status.err = truncateStatusText(err.Error())
	// Always stamp a fresh card, even if identical: `fir send --wait`
	// detects a refusal by the card's timestamp moving past its send.
	s.status.lastStore = nil
	s.publishStatusLocked()
}

// statusPromptAccepted marks the session busy as soon as a prompt is
// accepted, ahead of agent_start (see Prompt).
func (s *AgentSession) statusPromptAccepted() {
	s.status.mu.Lock()
	defer s.status.mu.Unlock()
	if s.status.status == StatusRunning {
		return
	}
	s.status.status = StatusRunning
	s.status.tool = ""
	s.status.err = ""
	s.publishStatusLocked()
}

// statusModelChanged refreshes the card after a model or session switch.
// Selecting a model clears a no-model state, and the startup notice (e.g.
// "Could not restore model X") no longer applies.
func (s *AgentSession) statusModelChanged() {
	s.status.mu.Lock()
	defer s.status.mu.Unlock()
	if s.status.status == StatusNoModel && s.Model() != nil {
		s.status.status = StatusIdle
		s.status.err = ""
	}
	if s.Model() != nil {
		s.status.notice = ""
	}
	s.publishStatusLocked()
}

// republishStatus rewrites the card, e.g. after the session file (and with
// it the cards store) was swapped by /new or a session switch.
func (s *AgentSession) republishStatus() {
	s.status.mu.Lock()
	defer s.status.mu.Unlock()
	s.publishStatusLocked()
}

// statusOnAgentEvent advances the status from an agent event. Called from
// handleAgentEvent, i.e. in agent-loop order.
func (s *AgentSession) statusOnAgentEvent(event agent.AgentEvent) {
	s.status.mu.Lock()
	defer s.status.mu.Unlock()
	switch event.Type {
	case agent.EventAgentStart:
		s.status.status = StatusRunning
		s.status.tool = ""
		s.status.err = ""
		s.status.aborted = false
	case agent.EventToolExecutionStart:
		s.status.tool = event.ToolName
	case agent.EventToolExecutionEnd:
		s.status.tool = ""
	case agent.EventMessageEnd:
		if event.Message == nil {
			return
		}
		am := event.Message.AsAssistant()
		if am == nil {
			return
		}
		switch {
		case am.StopReason == ai.StopReasonAborted ||
			(am.StopReason == ai.StopReasonError && s.status.aborted && isCancellation(am.ErrorMessage)):
			// An abort that lands mid-request surfaces as an error
			// carrying "context canceled" (often wrapped, e.g. "Post …:
			// context canceled"); it was the operator's choice, not a
			// failure.
			s.status.err = "aborted"
		case am.StopReason == ai.StopReasonError:
			msg := am.ErrorMessage
			if msg == "" {
				msg = "provider error"
			}
			s.status.err = truncateStatusText(msg)
		default:
			// A later successful response supersedes a transient error
			// the loop recovered from (auto-resume, stream retry).
			s.status.err = ""
		}
	case agent.EventAutoResume, agent.EventStreamRetry:
		// Still running; the transient error is surfaced until the retry
		// produces a response.
		if event.ErrorMessage != "" {
			s.status.err = truncateStatusText("retrying: " + event.ErrorMessage)
		}
	case agent.EventAgentEnd:
		s.status.runs++
		s.status.tool = ""
		switch {
		case s.status.err == "aborted":
			s.status.status = StatusIdle
			s.status.err = ""
		case s.status.err != "":
			s.status.status = StatusError
		default:
			s.status.status = StatusIdle
		}
	default:
		return
	}
	s.publishStatusLocked()
}

// publishStatusLocked writes the card. Caller holds s.status.mu, which also
// serialises writers so the on-disk card never goes backwards.
func (s *AgentSession) publishStatusLocked() {
	st := &s.status
	if st.status == "" {
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "status: %s\n", st.status)
	if m := s.Model(); m != nil {
		fmt.Fprintf(&b, "model: %s/%s\n", m.Provider, m.ID)
	}
	if st.tool != "" {
		fmt.Fprintf(&b, "tool: %s\n", st.tool)
	}
	fmt.Fprintf(&b, "runs: %d\n", st.runs)
	if st.err != "" {
		fmt.Fprintf(&b, "error: %s\n", oneLine(st.err))
	}
	if st.notice != "" {
		fmt.Fprintf(&b, "notice: %s\n", oneLine(st.notice))
	}
	slug := st.status
	if st.status == StatusRunning && st.tool != "" {
		slug = "running: " + st.tool
	}
	detail := strings.TrimRight(b.String(), "\n")
	obs := s.Observables()
	if obs == st.lastStore && slug == st.lastSlug && detail == st.lastDt {
		return
	}
	st.lastStore, st.lastSlug, st.lastDt = obs, slug, detail
	obs.Put(statusCardSource, statusCardKey, slug, detail, "")
}

// isCancellation reports whether a turn error is the run's context being
// cancelled. Only meaningful together with an abort request.
func isCancellation(msg string) bool {
	return strings.Contains(msg, context.Canceled.Error())
}

func truncateStatusText(s string) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) > statusErrMax {
		return string(r[:statusErrMax-1]) + "…"
	}
	return s
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
