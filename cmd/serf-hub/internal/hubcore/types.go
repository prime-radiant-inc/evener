package hubcore

import "encoding/json"

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

type ReplayTurn struct {
	Kind    string        `json:"kind"`
	Message ReplayMessage `json:"message"`
}

type ReplayMessage struct {
	Role    string       `json:"role"`
	Content []ReplayPart `json:"content"`
}

type ReplayPart struct {
	Kind       string            `json:"kind"`
	Text       string            `json:"text,omitempty"`
	Image      *ReplayImage      `json:"image,omitempty"`
	ToolCall   *ReplayToolCall   `json:"tool_call,omitempty"`
	ToolResult *ReplayToolResult `json:"tool_result,omitempty"`
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
	ToolCallID string `json:"tool_call_id"`
	Name       string `json:"name,omitempty"`
	Content    any    `json:"content,omitempty"`
	IsError    bool   `json:"is_error,omitempty"`
}

// Per-request limits for image attachments. Match the browser-side cap so
// REST and AppWire accept the same image payload surface.
const (
	SendMaxImageItems   = 8
	SendMaxImageBytes   = 8 * 1024 * 1024  // per-image
	SendMaxRequestBytes = 96 * 1024 * 1024 // total request body
)
