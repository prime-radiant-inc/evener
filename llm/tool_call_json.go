package llm

import (
	"bytes"
	"encoding/json"
)

type toolCallFields ToolCallData

// MarshalJSON keeps arguments available as structured JSON. If embedding
// that JSON changes the bytes the model supplied, keep those bytes too so a
// restored conversation can replay the same tool-call argument string.
func (tc ToolCallData) MarshalJSON() ([]byte, error) {
	record := struct {
		toolCallFields
		ArgumentBytes []byte `json:"arguments_bytes,omitempty"`
	}{toolCallFields: toolCallFields(tc)}
	if len(tc.Arguments) != 0 && tc.RawArguments == "" {
		stored, err := json.Marshal(tc.Arguments)
		if err != nil {
			return nil, err
		}
		if !bytes.Equal(stored, tc.Arguments) {
			record.ArgumentBytes = tc.Arguments
		}
	}
	return json.Marshal(record)
}

// UnmarshalJSON restores the original spelling only for the same structured
// argument value. A missing or inconsistent companion leaves arguments intact.
func (tc *ToolCallData) UnmarshalJSON(data []byte) error {
	record := struct {
		*toolCallFields
		ArgumentBytes json.RawMessage `json:"arguments_bytes"`
	}{toolCallFields: (*toolCallFields)(tc)}
	if err := json.Unmarshal(data, &record); err != nil {
		return err
	}
	if len(record.ArgumentBytes) == 0 || len(tc.Arguments) == 0 || tc.RawArguments != "" {
		return nil
	}
	var original []byte
	if err := json.Unmarshal(record.ArgumentBytes, &original); err != nil || len(original) == 0 {
		return nil
	}
	encoded, err := json.Marshal(json.RawMessage(original))
	if err == nil {
		stored, err := json.Marshal(tc.Arguments)
		if err == nil && bytes.Equal(encoded, stored) {
			tc.Arguments = original
		}
	}
	return nil
}
