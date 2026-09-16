package acp

import (
	acpsdk "github.com/coder/acp-go-sdk"
)

// Legacy model-selection wire types.
//
// acp-go-sdk v0.13 removed SessionModelState, ModelInfo, ModelId and the
// SetSessionModel request/response pair: upstream replaced them with the
// generic SessionConfigOption mechanism (a Select option categorised as
// "model"), and dropped the "session/set_model" RPC from its constants.
//
// fir still speaks the OLD shape, deliberately:
//
//   - fir owns its own dispatch table (rawMethodHandler in conn.go) for
//     session/new, session/list, session/load and session/resume, so the
//     SDK's typed structs were never load-bearing here — only the JSON is.
//   - the "models" object and "session/set_model" are what fir's clients
//     read today. acp-kit still parses both generations (see its
//     client/models.go: modelsFromConfigOptions, then modelsFromLegacy),
//     so staying on the legacy shape keeps every current client working.
//
// These types reproduce the removed SDK structs byte-for-byte on the wire.
// Migrating to config options is a separate change with UI consequences
// (it is what drives the model dropdown in Zed), and is tracked apart from
// the SDK bump.

// ModelId identifies a model in the legacy "models" object. Mirrors the
// removed acpsdk.ModelId.
type ModelId string

// ModelInfo is one entry of the legacy "models.availableModels" list.
// Mirrors the removed acpsdk.ModelInfo.
type ModelInfo struct {
	ModelId ModelId `json:"modelId"`
	Name    string  `json:"name"`
}

// SessionModelState is the legacy "models" object returned by session/new,
// session/load and session/resume. Mirrors the removed
// acpsdk.SessionModelState.
type SessionModelState struct {
	AvailableModels []ModelInfo `json:"availableModels"`
	CurrentModelId  ModelId     `json:"currentModelId"`
}

// SetSessionModelRequest is the params object of the legacy
// "session/set_model" RPC. Mirrors the removed
// acpsdk.SetSessionModelRequest.
type SetSessionModelRequest struct {
	SessionId acpsdk.SessionId `json:"sessionId"`
	ModelId   ModelId          `json:"modelId"`
}

// SetSessionModelResponse is the (empty) result of "session/set_model".
// Mirrors the removed acpsdk.SetSessionModelResponse.
type SetSessionModelResponse struct{}

// newSessionResponse is fir's session/new result. acpsdk.NewSessionResponse
// lost its Models field in v0.13, so fir marshals its own struct to keep
// the legacy "models" object on the wire. Field order and tags match the
// SDK's remaining fields so nothing else about the response changes.
type newSessionResponse struct {
	SessionId acpsdk.SessionId         `json:"sessionId"`
	Modes     *acpsdk.SessionModeState `json:"modes,omitempty"`
	Models    *SessionModelState       `json:"models,omitempty"`
}
