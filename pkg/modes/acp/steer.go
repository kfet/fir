package acp

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"

	acpsdk "github.com/coder/acp-go-sdk"

	"github.com/kfet/fir/pkg/session"
)

// steerSession serves _fir/session/steer: it queues the prompt content as a
// steering message on the session's running turn (the same queue as the
// interactive steer path), so the model sees it at the next tool boundary
// without the turn being cancelled. With no running turn it returns
// NoActiveTurnError and queues nothing. Mode "record" instead appends a
// side_query custom entry (never sent to the LLM), turn or no turn.
func (pa *firAgent) steerSession(_ context.Context, p SteerSessionRequest) (SteerSessionResponse, error) {
	pa.mu.Lock()
	entry, ok := pa.sessions[string(p.SessionId)]
	pa.mu.Unlock()
	if !ok {
		return SteerSessionResponse{}, newSessionNotFound(string(p.SessionId))
	}
	switch p.Mode {
	case "", SteerModeSteer:
	case SteerModeRecord:
		return pa.recordSideQuery(entry, p)
	default:
		return SteerSessionResponse{}, acpsdk.NewInvalidParams(map[string]any{"error": "unknown mode " + strconv.Quote(p.Mode)})
	}
	text, images := ExtractPromptContent(p.Prompt)
	if text == "" && len(images) == 0 {
		return SteerSessionResponse{}, acpsdk.NewInvalidParams(map[string]any{"error": "prompt is empty"})
	}
	entry.touch(pa.now())
	if err := entry.session.Steer(text, images); err != nil {
		if errors.Is(err, session.ErrNotStreaming) {
			return SteerSessionResponse{}, &acpsdk.RequestError{
				Code:    NoActiveTurnError,
				Message: "No active turn",
				Data:    map[string]any{"sessionId": string(p.SessionId)},
			}
		}
		return SteerSessionResponse{}, err
	}
	return SteerSessionResponse{}, nil
}

// recordSideQuery appends a side_query custom entry to the session
// transcript. Custom entries are not part of the LLM context, so the running
// turn (if any) is unaffected.
func (pa *firAgent) recordSideQuery(entry *firSession, p SteerSessionRequest) (SteerSessionResponse, error) {
	if p.Question == "" || p.Answer == "" {
		return SteerSessionResponse{}, acpsdk.NewInvalidParams(map[string]any{"error": "record mode needs question and answer"})
	}
	data, _ := json.Marshal(SideQueryData{Question: p.Question, Answer: p.Answer, ForkSessionId: p.ForkSessionId}) // plain strings: cannot fail
	entry.touch(pa.now())
	return SteerSessionResponse{EntryId: entry.session.SessionStore.AppendCustomEntry(SideQueryCustomType, data)}, nil
}
