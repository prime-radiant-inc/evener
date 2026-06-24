package llm

import (
	"context"
	"encoding/json"
	"errors"
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
	SessionID         string
	Round             int
	AttemptIndex      int
	AttemptCount      int
	FinalAttemptCount *int
	HistoryMode       HistoryMode
}

// WithAPILogContext returns a context carrying API log metadata.
func WithAPILogContext(ctx context.Context, sessionID string, round int) context.Context {
	return context.WithValue(ctx, apiLogKey{}, APILogContext{SessionID: sessionID, Round: round})
}

func WithAPILogAttemptContext(ctx context.Context, meta APILogContext) context.Context {
	if existing, ok := getAPILogContext(ctx); ok {
		if meta.SessionID == "" {
			meta.SessionID = existing.SessionID
		}
		if meta.Round == 0 {
			meta.Round = existing.Round
		}
	}
	return context.WithValue(ctx, apiLogKey{}, meta)
}

func getAPILogContext(ctx context.Context) (APILogContext, bool) {
	v, ok := ctx.Value(apiLogKey{}).(APILogContext)
	return v, ok
}

// APILogEntry is a single JSONL line in the API log.
type APILogEntry struct {
	Timestamp         string          `json:"ts"`
	SessionID         string          `json:"session_id,omitempty"`
	Round             int             `json:"round,omitempty"`
	AttemptIndex      int             `json:"attempt_index,omitempty"`
	AttemptCount      int             `json:"attempt_count,omitempty"`
	FinalAttemptCount *int            `json:"final_attempt_count,omitempty"`
	HistoryMode       HistoryMode     `json:"history_mode,omitempty"`
	Request           APILogRequest   `json:"request"`
	Response          *APILogResponse `json:"response,omitempty"`
	Error             string          `json:"error,omitempty"`
	LatencyMs         int64           `json:"latency_ms"`
}

// APILogRequest captures request metadata and tool definitions.
type APILogRequest struct {
	Model                   string           `json:"model"`
	Provider                string           `json:"provider"`
	MessageCount            int              `json:"message_count"`
	ToolCount               int              `json:"tool_count"`
	ToolNames               []string         `json:"tool_names,omitempty"`
	Tools                   []ToolDefinition `json:"tools,omitempty"`
	ReasoningEffort         string           `json:"reasoning_effort,omitempty"`
	HistoryMode             HistoryMode      `json:"history_mode,omitempty"`
	PreviousResponseIDHash  string           `json:"previous_response_id_hash,omitempty"`
	ConversationIDHash      string           `json:"conversation_id_hash,omitempty"`
	AnchorTurnIndex         int              `json:"anchor_turn_index,omitempty"`
	DeltaTurnCount          int              `json:"delta_turn_count,omitempty"`
	DeltaTurnKinds          []string         `json:"delta_turn_kinds,omitempty"`
	EndpointFamily          string           `json:"endpoint_family,omitempty"`
	RequestFingerprint      string           `json:"request_fingerprint,omitempty"`
	ContextMarker           string           `json:"context_marker,omitempty"`
	StoragePolicyLabel      string           `json:"storage_policy_label,omitempty"`
	StorageScopeFingerprint string           `json:"storage_scope_fingerprint,omitempty"`
	ChatFallbackHistoryLen  int              `json:"chat_fallback_history_len,omitempty"`
}

// APILogResponse captures the full response including raw provider data.
type APILogResponse struct {
	ID            string `json:"id,omitempty"`
	IDHash        string `json:"id_hash,omitempty"`
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

		entry := buildAPILogEntry(ctx, req, start)

		if err != nil {
			entry.Error = err.Error()
		} else {
			entry.Response = buildLogResponse(resp)
		}

		l.write(entry)
		if err != nil {
			l.writeRawError(entry, req, "complete", err)
		} else {
			l.writeRawResponse(entry, req, "complete", resp)
		}

		return resp, err
	}
}

// WrapStream wraps streaming calls so the final streamed Response is recorded
// with the same request/response metadata and optional raw HTTP bodies as
// non-streaming calls.
func (l *APILogger) WrapStream(next StreamFunc) StreamFunc {
	return func(ctx context.Context, req Request) (Stream, error) {
		start := time.Now()

		st, err := next(ctx, req)
		if err != nil {
			entry := buildAPILogEntry(ctx, req, start)
			entry.Error = err.Error()
			l.write(entry)
			l.writeRawError(entry, req, "stream", err)
			return nil, err
		}
		if st == nil {
			return nil, nil
		}
		return newAPILogStream(ctx, st, l, req, start), nil
	}
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

func buildAPILogEntry(ctx context.Context, req Request, start time.Time) APILogEntry {
	entry := APILogEntry{
		Timestamp: start.UTC().Format(time.RFC3339),
		LatencyMs: time.Since(start).Milliseconds(),
		Request:   BuildAPILogRequest(req),
	}
	if lc, ok := getAPILogContext(ctx); ok {
		entry.SessionID = lc.SessionID
		entry.Round = lc.Round
		entry.AttemptIndex = lc.AttemptIndex
		entry.AttemptCount = lc.AttemptCount
		entry.FinalAttemptCount = lc.FinalAttemptCount
		entry.HistoryMode = lc.HistoryMode
	}
	return entry
}

func (l *APILogger) writeRawResponse(entry APILogEntry, req Request, mode string, resp Response) {
	if l.rawFile == nil || (resp.RawRequestBody == "" && resp.RawResponseBody == "") {
		return
	}
	l.writeRaw(APIRawLogEntry{
		Timestamp:    entry.Timestamp,
		SessionID:    entry.SessionID,
		Round:        entry.Round,
		Provider:     req.Provider,
		Model:        req.Model,
		Mode:         mode,
		RequestBody:  resp.RawRequestBody,
		ResponseBody: resp.RawResponseBody,
		LatencyMs:    entry.LatencyMs,
	})
}

func (l *APILogger) writeRawError(entry APILogEntry, req Request, mode string, err error) {
	if l.rawFile == nil || err == nil {
		return
	}
	var rawErr RawHTTPBodyError
	if !errors.As(err, &rawErr) {
		return
	}
	requestBody, responseBody := rawErr.RawHTTPBodies()
	if requestBody == "" && responseBody == "" {
		return
	}
	l.writeRaw(APIRawLogEntry{
		Timestamp:    entry.Timestamp,
		SessionID:    entry.SessionID,
		Round:        entry.Round,
		Provider:     req.Provider,
		Model:        req.Model,
		Mode:         mode,
		RequestBody:  requestBody,
		ResponseBody: responseBody,
		LatencyMs:    entry.LatencyMs,
	})
}

// BuildAPILogRequest projects an LLM request into the metadata recorded in API
// logs and transcript api_call entries.
func BuildAPILogRequest(req Request) APILogRequest {
	lr := APILogRequest{
		Model:        req.Model,
		Provider:     req.Provider,
		MessageCount: len(req.Messages),
		ToolCount:    len(req.Tools),
		HistoryMode:  req.HistoryMode,
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
	if req.Continuation != nil {
		lr.PreviousResponseIDHash = req.Continuation.PreviousResponseIDHash
		lr.ConversationIDHash = req.Continuation.ConversationIDHash
		lr.AnchorTurnIndex = req.Continuation.AnchorTurnIndex
		lr.DeltaTurnCount = req.Continuation.DeltaTurnCount
		lr.DeltaTurnKinds = append([]string(nil), req.Continuation.DeltaTurnKinds...)
		lr.EndpointFamily = req.Continuation.EndpointFamily
		lr.RequestFingerprint = req.Continuation.RequestFingerprint
		lr.ContextMarker = req.Continuation.ContextMarker
		lr.StoragePolicyLabel = req.Continuation.StoragePolicyLabel
		lr.StorageScopeFingerprint = req.Continuation.StorageScopeFingerprint
		lr.ChatFallbackHistoryLen = req.Continuation.ChatFallbackHistoryLen
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
	var idHash string
	if resp.Raw != nil {
		if v, ok := resp.Raw["endpoint_url"].(string); ok {
			endpoint = v
		}
		if v, ok := resp.Raw["id_hash"].(string); ok {
			idHash = v
		}
	}
	return &APILogResponse{
		ID:            resp.ID,
		IDHash:        idHash,
		Model:         resp.Model,
		FinishReason:  resp.Finish.Reason,
		TextLength:    len(resp.Text()),
		ToolCallCount: len(resp.ToolCalls()),
		Usage:         resp.Usage,
		EndpointURL:   endpoint,
		Raw:           resp.Raw,
	}
}

type apiLogStream struct {
	inner   Stream
	logger  *APILogger
	ctx     context.Context
	req     Request
	start   time.Time
	out     chan StreamEvent
	once    sync.Once
	logOnce sync.Once
	done    chan struct{}
	closing chan struct{}
}

func newAPILogStream(ctx context.Context, inner Stream, logger *APILogger, req Request, start time.Time) *apiLogStream {
	s := &apiLogStream{
		inner:   inner,
		logger:  logger,
		ctx:     ctx,
		req:     req,
		start:   start,
		out:     make(chan StreamEvent, 128),
		done:    make(chan struct{}),
		closing: make(chan struct{}),
	}
	go s.pump()
	return s
}

func (s *apiLogStream) pump() {
	defer close(s.done)
	defer close(s.out)
	acc := NewStreamAccumulator()
	for {
		select {
		case <-s.closing:
			return
		case ev, ok := <-s.inner.Events():
			if !ok {
				return
			}
			acc.Process(ev)
			if ev.Type == StreamEventFinish {
				var resp Response
				if ev.Response != nil {
					resp = *ev.Response
				} else if accumulated := acc.Response(); accumulated != nil {
					resp = *accumulated
				}
				if resp.Model == "" {
					resp.Model = s.req.Model
				}
				if resp.Provider == "" {
					resp.Provider = s.req.Provider
				}
				s.logFinish(resp)
			}
			if ev.Type == StreamEventError && ev.Err != nil {
				s.logError(ev.Err)
			}
			select {
			case s.out <- ev:
			case <-s.closing:
				return
			}
		}
	}
}

func (s *apiLogStream) logFinish(resp Response) {
	s.logOnce.Do(func() {
		entry := buildAPILogEntry(s.ctx, s.req, s.start)
		entry.Response = buildLogResponse(resp)
		s.logger.write(entry)
		s.logger.writeRawResponse(entry, s.req, "stream", resp)
	})
}

func (s *apiLogStream) logError(err error) {
	s.logOnce.Do(func() {
		entry := buildAPILogEntry(s.ctx, s.req, s.start)
		entry.Error = err.Error()
		s.logger.write(entry)
		s.logger.writeRawError(entry, s.req, "stream", err)
	})
}

func (s *apiLogStream) Events() <-chan StreamEvent { return s.out }

func (s *apiLogStream) Close() error {
	var err error
	s.once.Do(func() {
		close(s.closing)
		err = s.inner.Close()
	})
	<-s.done
	return err
}
