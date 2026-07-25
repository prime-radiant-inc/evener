package hubcore

import (
	"encoding/json"
	"time"
)

// ModelDescriptor is a provider/model pair used by the spawn chip and /api/models.
type ModelDescriptor struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

type ReplayHeader struct {
	SessionID string `json:"session_id"`
	ProfileID string `json:"profile_id"`
	Model     string `json:"model"`
}

type ReplayEntry struct {
	Turn ReplayTurn `json:"turn"`
}

// ReplayTurn is a partial mirror of schema.Turn: only the fields named here
// survive a reload, so any schema.Turn field the client needs must be added
// both here and in replayTurnToAgentTurn.
type ReplayTurn struct {
	Kind    string        `json:"kind"`
	Message ReplayMessage `json:"message"`
	// Timestamp is the turn's recorded time, carried through reload so
	// replayed tool items can be stamped with real server times (issue #37).
	Timestamp time.Time `json:"timestamp,omitempty"`
	// SteeringSource carries a steering turn's provenance through reload:
	// "user" for a steer the human typed, empty for a daemon nudge. Without
	// it a reloaded human steer arrives anonymous and renders as the grey
	// divider rather than as the person's own speech (issue #24).
	SteeringSource string `json:"steering_source,omitempty"`

	// Error is the diagnostic of a failed turn, carried through reload so a
	// returning reader sees the failure the live client saw (kata mcgh).
	Error *ReplayTurnError `json:"error,omitempty"`
}

// ReplayTurnError mirrors schema.TurnFailureInfo.
type ReplayTurnError struct {
	Message string                `json:"message"`
	Source  string                `json:"source,omitempty"`
	Title   string                `json:"title,omitempty"`
	Hint    string                `json:"hint,omitempty"`
	Cause   *ReplayTurnErrorCause `json:"cause,omitempty"`
}

// ReplayTurnErrorCause mirrors schema.TurnFailureCause.
type ReplayTurnErrorCause struct {
	Kind     string `json:"kind"`
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	Status   int    `json:"status,omitempty"`
}

type ReplayMessage struct {
	Role    string       `json:"role"`
	Content []ReplayPart `json:"content"`
}

type ReplayPart struct {
	Kind       string            `json:"kind"`
	Text       string            `json:"text,omitempty"`
	Thinking   *ReplayThinking   `json:"thinking,omitempty"`
	Image      *ReplayImage      `json:"image,omitempty"`
	Audio      *ReplayMedia      `json:"audio,omitempty"`
	Document   *ReplayMedia      `json:"document,omitempty"`
	ToolCall   *ReplayToolCall   `json:"tool_call,omitempty"`
	ToolResult *ReplayToolResult `json:"tool_result,omitempty"`
	WebSearch  *ReplayWebSearch  `json:"web_search,omitempty"`
}

type ReplayThinking struct {
	Text     string `json:"text,omitempty"`
	Redacted bool   `json:"redacted,omitempty"`
}

type ReplayMedia struct {
	URL       string `json:"url,omitempty"`
	MediaType string `json:"media_type,omitempty"`
	FileName  string `json:"file_name,omitempty"`
}

type ReplayWebSearch struct {
	Query string          `json:"query,omitempty"`
	Raw   json.RawMessage `json:"raw,omitempty"`
}

type ReplayImage struct {
	Data      []byte `json:"data,omitempty"`
	MediaType string `json:"media_type,omitempty"`
	Name      string `json:"name,omitempty"`
}

type ReplayToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

type ReplayToolResult struct {
	ToolCallID string          `json:"tool_call_id"`
	Name       string          `json:"name,omitempty"`
	Content    any             `json:"content,omitempty"`
	IsError    bool            `json:"is_error,omitempty"`
	ToolState  json.RawMessage `json:"tool_state,omitempty"`

	ImageData      []byte `json:"image_data,omitempty"`
	ImageMediaType string `json:"image_media_type,omitempty"`
}

// Per-request limits for image attachments. Match the browser-side cap so
// REST and AppWire accept the same image payload surface.
const (
	SendMaxImageItems   = 8
	SendMaxImageBytes   = 8 * 1024 * 1024  // per-image
	SendMaxRequestBytes = 96 * 1024 * 1024 // total request body
)
