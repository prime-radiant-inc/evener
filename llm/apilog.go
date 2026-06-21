package llm

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"primeradiant.com/serf/envvars"
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

// APILogRequest captures request metadata and tool definitions.
type APILogRequest struct {
	Model           string           `json:"model"`
	Provider        string           `json:"provider"`
	MessageCount    int              `json:"message_count"`
	ToolCount       int              `json:"tool_count"`
	ToolNames       []string         `json:"tool_names,omitempty"`
	Tools           []ToolDefinition `json:"tools,omitempty"`
	ReasoningEffort string           `json:"reasoning_effort,omitempty"`
}

// APILogResponse captures the full response including raw provider data.
type APILogResponse struct {
	ID            string `json:"id,omitempty"`
	Model         string `json:"model"`
	FinishReason  string `json:"finish_reason"`
	TextLength    int    `json:"text_length"`
	ToolCallCount int    `json:"tool_call_count"`
	Usage         Usage  `json:"usage"`
	// EndpointURL is the full HTTP URL the adapter dialed for this call.
	// Promoted from Raw["endpoint_url"] (string) so QA can tell, e.g., whether
	// an OpenAI call went to /v1/responses (API key) vs /backend-api/codex/responses
	// (ChatGPT OAuth). Empty when the adapter did not stash it.
	EndpointURL string         `json:"endpoint_url,omitempty"`
	Raw         map[string]any `json:"raw"`
}

// APIRawLogEntry is a JSONL line in the raw HTTP body log.
type APIRawLogEntry struct {
	Timestamp    string `json:"ts"`
	SessionID    string `json:"session_id,omitempty"`
	Round        int    `json:"round,omitempty"`
	Provider     string `json:"provider"`
	Model        string `json:"model"`
	Mode         string `json:"mode"` // "complete" or "stream"
	RequestBody  string `json:"request_body,omitempty"`
	ResponseBody string `json:"response_body,omitempty"`
	LatencyMs    int64  `json:"latency_ms"`
}

// RawBodyEnabled returns true when SERF_LOG_RAW_HTTP is set.
// Adapters check this before populating RawRequestBody/RawResponseBody.
func RawBodyEnabled() bool { return rawBodyEnabled }

var rawBodyEnabled = func() bool {
	return rawHTTPLogEnabled(envvars.SERFLogRawHTTP.Getenv())
}()

func rawHTTPLogEnabled(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// APILogger is middleware that logs every LLM API call to a JSONL file.
type APILogger struct {
	file    *os.File
	rawFile *os.File // nil when raw logging is disabled
	mu      sync.Mutex

	// SyncInterval controls how often write calls fsync.
	// If 0, every write fsyncs (backward-compatible default for tests).
	// If >0, write only fsyncs when this duration has elapsed since the last sync.
	SyncInterval time.Duration

	dirty    bool
	lastSync time.Time
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
	return &APILogger{file: f, lastSync: time.Now()}, nil
}

// EnableRawLogging opens a separate JSONL file for raw HTTP request/response bodies.
func (l *APILogger) EnableRawLogging(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	l.rawFile = f
	return nil
}

// WrapComplete wraps a CompleteFunc to log request metadata and full response.
func (l *APILogger) WrapComplete(next CompleteFunc) CompleteFunc {
	return func(ctx context.Context, req Request) (Response, error) {
		start := time.Now()

		resp, err := next(ctx, req)

		entry := APILogEntry{
			Timestamp: start.UTC().Format(time.RFC3339),
			LatencyMs: time.Since(start).Milliseconds(),
			Request:   BuildAPILogRequest(req),
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

		if l.rawFile != nil && (resp.RawRequestBody != "" || resp.RawResponseBody != "") {
			rawEntry := APIRawLogEntry{
				Timestamp:    entry.Timestamp,
				SessionID:    entry.SessionID,
				Round:        entry.Round,
				Provider:     req.Provider,
				Model:        req.Model,
				Mode:         "complete",
				RequestBody:  resp.RawRequestBody,
				ResponseBody: resp.RawResponseBody,
				LatencyMs:    entry.LatencyMs,
			}
			l.writeRaw(rawEntry)
		}

		return resp, err
	}
}

// WrapStream is a passthrough — sessions use Complete, not Stream.
func (l *APILogger) WrapStream(next StreamFunc) StreamFunc {
	return next
}

// Close flushes and closes the log file(s).
func (l *APILogger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	var firstErr error
	if l.file != nil {
		if l.dirty {
			if err := l.file.Sync(); err != nil && firstErr == nil {
				firstErr = err
			}
			l.dirty = false
		}
		if err := l.file.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		l.file = nil
	}
	if l.rawFile != nil {
		if err := l.rawFile.Sync(); err != nil && firstErr == nil {
			firstErr = err
		}
		if err := l.rawFile.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		l.rawFile = nil
	}
	return firstErr
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

	l.dirty = true
	if l.SyncInterval == 0 || time.Since(l.lastSync) >= l.SyncInterval {
		l.file.Sync() //nolint:errcheck
		l.lastSync = time.Now()
		l.dirty = false
	}
}

func (l *APILogger) writeRaw(entry APIRawLogEntry) {
	data, err := json.Marshal(entry)
	if err != nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.rawFile == nil {
		return
	}
	l.rawFile.Write(append(data, '\n')) //nolint:errcheck
	l.rawFile.Sync()                    //nolint:errcheck
}

// BuildAPILogRequest projects an LLM request into the metadata recorded in API
// logs and transcript api_call entries.
func BuildAPILogRequest(req Request) APILogRequest {
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
		lr.Tools = append([]ToolDefinition(nil), req.Tools...)
	}
	return lr
}

// StampEndpointURL records the full URL an adapter dialed onto resp.Raw so
// buildLogResponse can promote it to a top-level field in the api_call
// transcript. It initialises Raw if nil so adapters that build responses
// incrementally don't have to special-case it, and is a no-op when resp is nil
// or endpoint is empty. Callers pass the URL they want logged; for providers
// that carry secrets in the URL (e.g. Google's API key as a query parameter),
// pass the pre-query base form (host + path) only to avoid leaking the secret.
func StampEndpointURL(resp *Response, endpoint string) {
	if resp == nil || endpoint == "" {
		return
	}
	if resp.Raw == nil {
		resp.Raw = map[string]any{}
	}
	resp.Raw["endpoint_url"] = endpoint
}

func buildLogResponse(resp Response) *APILogResponse {
	var endpoint string
	if resp.Raw != nil {
		if v, ok := resp.Raw["endpoint_url"].(string); ok {
			endpoint = v
		}
	}
	return &APILogResponse{
		ID:            resp.ID,
		Model:         resp.Model,
		FinishReason:  resp.Finish.Reason,
		TextLength:    len(resp.Text()),
		ToolCallCount: len(resp.ToolCalls()),
		Usage:         resp.Usage,
		EndpointURL:   endpoint,
		Raw:           resp.Raw,
	}
}
