package ai

import (
	"bytes"
	"compress/flate"
	_ "embed"
	"encoding/json"
	"io"
	"log/slog"
)

// models_catalog.bin is the built-in model catalog, written by
// cmd/generate-models: a flate-compressed JSON array of catalogEntry.
// It is decoded lazily on first registry access (ensureCatalog in
// models.go); decoding ~1500 entries costs ~10 ms, too much for every start.
//
//go:embed models_catalog.bin
var catalogData []byte

// catalogEntry is a Model whose Compat is typed, so the `any` field
// round-trips as *OpenAICompletionsCompat (the only kind the catalog uses).
// The outer Compat shadows Model.Compat for encoding/json.
type catalogEntry struct {
	Model
	Compat *OpenAICompletionsCompat `json:"compat,omitempty"`
}

// EncodeCatalog serialises models into the embedded catalog format. Each
// model's Compat must be nil or *OpenAICompletionsCompat. Marshalling plain
// data and writing to a bytes.Buffer cannot fail, so there is no error.
func EncodeCatalog(models []*Model) []byte {
	entries := make([]catalogEntry, len(models))
	for i, m := range models {
		entries[i].Model = *m
		entries[i].Model.Compat = nil
		if c, ok := m.Compat.(*OpenAICompletionsCompat); ok {
			entries[i].Compat = c
		}
	}
	raw, _ := json.Marshal(entries)
	var buf bytes.Buffer
	w, _ := flate.NewWriter(&buf, flate.BestCompression)
	_, _ = w.Write(raw)
	_ = w.Close()
	return buf.Bytes()
}

// decodeCatalog inverts EncodeCatalog.
func decodeCatalog(data []byte) ([]*Model, error) {
	raw, err := io.ReadAll(flate.NewReader(bytes.NewReader(data)))
	if err != nil {
		return nil, err
	}
	var entries []catalogEntry
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, err
	}
	out := make([]*Model, len(entries))
	for i := range entries {
		m := entries[i].Model
		if entries[i].Compat != nil {
			m.Compat = entries[i].Compat
		}
		out[i] = &m
	}
	return out, nil
}

// registerCatalog decodes data and registers every model in it. A corrupt
// blob is logged and skipped rather than aborting startup.
func registerCatalog(data []byte) {
	models, err := decodeCatalog(data)
	if err != nil {
		slog.Error("built-in model catalog is corrupt", "err", err)
		return
	}
	modelRegistryMu.Lock()
	defer modelRegistryMu.Unlock()
	for _, m := range models {
		registerLocked(m)
	}
}
