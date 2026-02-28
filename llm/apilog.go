package llm

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Context keys for API log metadata.

type apiLogKey struct{}

// APILogContext carries session-level metadata into the API logger middleware.
type APILogContext struct {
	SessionID string
	Round     int
}

// WithAPILogContext returns a context carrying API log metadata.
func WithAPILogContext(ctx context.Context, sessionID string, round int) context.Context {
	return context.WithValue(ctx, apiLogKey{}, APILogContext{SessionID: sessionID, Round: round})
}

func getAPILogContext(ctx context.Context) (APILogContext, bool) {
	v, ok := ctx.Value(apiLogKey{}).(APILogContext)
	return v, ok
}

// APILogEntry is a single JSONL line in the API log.
type APILogEntry struct {
	Timestamp string          `json:"ts"`
	SessionID string          `json:"session_id,omitempty"`
	Round     int             `json:"round,omitempty"`
	Request   APILogRequest   `json:"request"`
	Response  *APILogResponse `json:"response,omitempty"`
	Error     string          `json:"error,omitempty"`
	LatencyMs int64           `json:"latency_ms"`
}

// APILogRequest captures request metadata (not full content).
type APILogRequest struct {
	Model           string   `json:"model"`
	Provider        string   `json:"provider"`
	MessageCount    int      `json:"message_count"`
	ToolCount       int      `json:"tool_count"`
	ToolNames       []string `json:"tool_names,omitempty"`
	ReasoningEffort string   `json:"reasoning_effort,omitempty"`
}

// APILogResponse captures the full response including raw provider data.
type APILogResponse struct {
	ID            string         `json:"id,omitempty"`
	Model         string         `json:"model"`
	FinishReason  string         `json:"finish_reason"`
	TextLength    int            `json:"text_length"`
	ToolCallCount int            `json:"tool_call_count"`
	Usage         Usage          `json:"usage"`
	Raw           map[string]any `json:"raw"`
}

// APILogger is middleware that logs every LLM API call to a JSONL file.
type APILogger struct {
	file *os.File
	mu   sync.Mutex
}

// NewAPILogger creates an API logger that writes to the given path.
// Creates the parent directory if it doesn't exist.
func NewAPILogger(path string) (*APILogger, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	return &APILogger{file: f}, nil
}

// WrapComplete wraps a CompleteFunc to log request metadata and full response.
func (l *APILogger) WrapComplete(next CompleteFunc) CompleteFunc {
	return func(ctx context.Context, req Request) (Response, error) {
		start := time.Now()

		resp, err := next(ctx, req)

		entry := APILogEntry{
			Timestamp: start.UTC().Format(time.RFC3339),
			LatencyMs: time.Since(start).Milliseconds(),
			Request:   buildLogRequest(req),
		}

		if lc, ok := getAPILogContext(ctx); ok {
			entry.SessionID = lc.SessionID
			entry.Round = lc.Round
		}

		if err != nil {
			entry.Error = err.Error()
		} else {
			entry.Response = buildLogResponse(resp)
		}

		l.write(entry)

		return resp, err
	}
}

// WrapStream is a passthrough — sessions use Complete, not Stream.
func (l *APILogger) WrapStream(next StreamFunc) StreamFunc {
	return next
}

// Close flushes and closes the log file.
func (l *APILogger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	return err
}

func (l *APILogger) write(entry APILogEntry) {
	data, err := json.Marshal(entry)
	if err != nil {
		return
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return
	}
	l.file.Write(append(data, '\n')) //nolint:errcheck
	l.file.Sync()                    //nolint:errcheck
}

func buildLogRequest(req Request) APILogRequest {
	lr := APILogRequest{
		Model:        req.Model,
		Provider:     req.Provider,
		MessageCount: len(req.Messages),
		ToolCount:    len(req.Tools),
	}
	if req.ReasoningEffort != nil {
		lr.ReasoningEffort = *req.ReasoningEffort
	}
	if len(req.Tools) > 0 {
		names := make([]string, len(req.Tools))
		for i, t := range req.Tools {
			names[i] = t.Name
		}
		lr.ToolNames = names
	}
	return lr
}

func buildLogResponse(resp Response) *APILogResponse {
	return &APILogResponse{
		ID:            resp.ID,
		Model:         resp.Model,
		FinishReason:  resp.Finish.Reason,
		TextLength:    len(resp.Text()),
		ToolCallCount: len(resp.ToolCalls()),
		Usage:         resp.Usage,
		Raw:           resp.Raw,
	}
}
