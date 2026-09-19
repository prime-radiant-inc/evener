package llm

import "encoding/json"

// MCPResult is application-owned host data, separate from model-facing text.
// It contains no connection details or credentials. Producers bound and validate
// the JSON before handing it to the agent. Version identifies this envelope.
type MCPResult struct {
	Version           int               `json:"version"`
	Origin            MCPOrigin         `json:"origin"`
	Content           []json.RawMessage `json:"content,omitempty"`
	StructuredContent json.RawMessage   `json:"structured_content,omitempty"`
	ToolMetadata      json.RawMessage   `json:"tool_metadata,omitempty"`
	ResultMetadata    json.RawMessage   `json:"result_metadata,omitempty"`
	IsError           bool              `json:"is_error,omitempty"`
}

// MCPOrigin identifies the trusted logical binding that produced a host result.
// BindingID is stable configuration identity, never a lease or runtime handle.
// ServiceID is verified stable backend identity; unknown configured MCP servers
// may leave it empty and remain static. SessionID is the durable Evener engine
// session, never an MCP transport session or daemon incarnation.
type MCPOrigin struct {
	BindingID string `json:"binding_id"`
	ServiceID string `json:"service_id"`
	SessionID string `json:"session_id"`
}
