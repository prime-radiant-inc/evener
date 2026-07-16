package llm

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"primeradiant.com/serf/identifier"
	apilog "primeradiant.com/serf/llm/apilog"
)

type APIAttemptSink interface {
	AppendAttempt(context.Context, apilog.APIAttemptRecord) error
	AppendSettlement(context.Context, apilog.APIAttemptGroupSettlement) error
}

type APILogFailure struct {
	Operation      string
	SessionID      string
	AttemptGroupID string
	AttemptID      string
	Err            error
}

// APILogCredentialMaterial identifies credential-bearing request material that
// the transport-boundary sanitizer must exclude from forensic records.
type APILogCredentialMaterial struct {
	HeaderNames map[string]struct{}
	QueryNames  map[string]struct{}
	Values      []string
}

type APIAttemptMeta struct {
	ProviderInstance   string
	RequestModel       string
	HistoryMode        HistoryMode
	EndpointFamily     string
	Method             string
	Endpoint           string
	Headers            http.Header
	RequestBody        []byte
	StartedAt          time.Time
	CredentialMaterial APILogCredentialMaterial
}

type APIAttemptResult struct {
	StatusCode   int
	ResponseBody []byte
	Response     *Response
	Outcome      apilog.AttemptOutcomeClass
	ErrorClass   string
	Err          error
	FinishedAt   time.Time
}

type apiAttemptGroupContextKey struct{}
type apiAttemptSinkContextKey struct{}

type apiAttemptSinkContext struct {
	sink APIAttemptSink
}

type apiLogFailureObserverSource interface {
	apiLogFailureObserver() func(APILogFailure)
}

type apiLogObservedFailure interface {
	apiLogFailureWasObserved()
}

func WithAPIAttemptGroup(ctx context.Context, group *APIAttemptGroup) context.Context {
	return context.WithValue(ctx, apiAttemptGroupContextKey{}, group)
}

func WithAPIAttemptSink(ctx context.Context, sink APIAttemptSink) context.Context {
	return context.WithValue(ctx, apiAttemptSinkContextKey{}, apiAttemptSinkContext{sink: sink})
}

// APIAttemptContextActive reports whether the caller explicitly supplied both
// canonical attempt coordination and persistence. Transports use it to leave
// ordinary calls entirely on their existing client path.
func APIAttemptContextActive(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	group, _ := ctx.Value(apiAttemptGroupContextKey{}).(*APIAttemptGroup)
	state, _ := ctx.Value(apiAttemptSinkContextKey{}).(apiAttemptSinkContext)
	return group != nil && state.sink != nil
}

type APIAttemptGroup struct {
	ID string

	mu                 sync.Mutex
	nextAttemptIndex   int
	finalAttemptID     string
	finalAttemptCount  int
	finalOutcome       apilog.AttemptOutcomeClass
	forensicIncomplete bool
	settling           bool
	sinkBound          bool
	sink               APIAttemptSink
	pendingAttempts    sync.WaitGroup
}

func NewAPIAttemptGroup(id string) *APIAttemptGroup {
	return &APIAttemptGroup{ID: id}
}

type APIAttempt struct {
	once  sync.Once
	mu    sync.Mutex
	group *APIAttemptGroup
	sink  APIAttemptSink
	ctx   context.Context
	meta  APIAttemptMeta
	id    string
	index int
}

// Active reports whether this attempt has an explicitly attached group and
// sink and therefore needs exact transport evidence retained.
func (a *APIAttempt) Active() bool {
	return a != nil && a.group != nil && a.sink != nil
}

// SetRequestBody supplies exact bytes observed at an HTTP transport boundary.
// It is used only when a request body cannot be cloned before RoundTrip.
func (a *APIAttempt) SetRequestBody(body []byte) {
	if !a.Active() {
		return
	}
	a.mu.Lock()
	a.meta.RequestBody = append([]byte(nil), body...)
	a.mu.Unlock()
}

func BeginAPIAttempt(ctx context.Context, meta APIAttemptMeta) *APIAttempt {
	group, _ := ctx.Value(apiAttemptGroupContextKey{}).(*APIAttemptGroup)
	if group == nil {
		return &APIAttempt{}
	}
	state, _ := ctx.Value(apiAttemptSinkContextKey{}).(apiAttemptSinkContext)

	group.mu.Lock()
	if group.settling {
		group.mu.Unlock()
		return &APIAttempt{}
	}
	group.bindSinkLocked(state)
	if group.sink == nil {
		group.mu.Unlock()
		return &APIAttempt{}
	}
	group.nextAttemptIndex++
	attemptID := identifier.MustNewAPIAttemptID()
	group.finalAttemptID = attemptID
	group.finalAttemptCount = group.nextAttemptIndex
	group.pendingAttempts.Add(1)
	attempt := &APIAttempt{
		group: group,
		sink:  group.sink,
		ctx:   ctx,
		meta:  meta,
		id:    attemptID,
		index: group.nextAttemptIndex,
	}
	group.mu.Unlock()
	return attempt
}

func (a *APIAttempt) Complete(result APIAttemptResult) {
	if a == nil {
		return
	}
	a.once.Do(func() {
		if a.group == nil {
			return
		}
		defer a.group.pendingAttempts.Done()
		if a.sink == nil {
			return
		}
		a.mu.Lock()
		meta := a.meta
		a.mu.Unlock()
		record := buildAPIAttemptRecord(a.group.ID, a.id, a.index, meta, result)
		a.group.mu.Lock()
		a.group.finalOutcome = result.Outcome
		a.group.mu.Unlock()
		appendCtx := context.WithoutCancel(a.ctx)
		if err := a.sink.AppendAttempt(appendCtx, record); err != nil {
			failureErr := err
			sanitized := SanitizeErrorForAPILog(err.Error(), meta.CredentialMaterial)
			if sanitized != err.Error() {
				failureErr = sanitizedAPILogError{text: sanitized, err: err}
			}
			a.group.recordFailure(APILogFailure{
				Operation:      "append_attempt",
				SessionID:      apiLogSessionID(a.ctx),
				AttemptGroupID: a.group.ID,
				AttemptID:      a.id,
				Err:            failureErr,
			})
		}
	})
}

// SettleResult settles the group from the outer logical call result. When the
// call made transport attempts, their terminal outcome remains authoritative;
// a caller cancellation overrides it because the caller owns that terminal
// boundary. A pre-transport failure is recorded as a zero-attempt transport
// failure.
func (g *APIAttemptGroup) SettleResult(ctx context.Context, err error) {
	if g == nil {
		return
	}
	outcome := apilog.AttemptSuccess
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || (ctx != nil && ctx.Err() != nil) {
			outcome = apilog.AttemptCallerCancel
		} else {
			g.mu.Lock()
			outcome = g.finalOutcome
			g.mu.Unlock()
			if outcome == "" {
				outcome = apilog.AttemptTransportFail
			}
		}
	}
	g.Settle(ctx, outcome)
}

func apiAttemptGroupFromContext(ctx context.Context) *APIAttemptGroup {
	if ctx == nil {
		return nil
	}
	group, _ := ctx.Value(apiAttemptGroupContextKey{}).(*APIAttemptGroup)
	return group
}

type sanitizedAPILogError struct {
	text string
	err  error
}

func (e sanitizedAPILogError) Error() string { return e.text }
func (e sanitizedAPILogError) Unwrap() error { return e.err }

func (g *APIAttemptGroup) Settle(ctx context.Context, outcome apilog.AttemptOutcomeClass) {
	if g == nil {
		return
	}
	state, _ := ctx.Value(apiAttemptSinkContextKey{}).(apiAttemptSinkContext)
	g.mu.Lock()
	if g.settling {
		g.mu.Unlock()
		return
	}
	g.settling = true
	g.bindSinkLocked(state)
	g.mu.Unlock()

	g.pendingAttempts.Wait()

	g.mu.Lock()
	settlement := apilog.APIAttemptGroupSettlement{
		Kind:               "attempt_group_settlement",
		SchemaVersion:      1,
		AttemptGroupID:     g.ID,
		FinalAttemptID:     g.finalAttemptID,
		FinalAttemptCount:  g.finalAttemptCount,
		Outcome:            outcome,
		ForensicIncomplete: g.forensicIncomplete,
		SettledAt:          time.Now().UTC(),
	}
	sink := g.sink
	g.mu.Unlock()
	if sink == nil {
		return
	}
	appendCtx := context.WithoutCancel(ctx)
	if err := sink.AppendSettlement(appendCtx, settlement); err != nil {
		g.recordFailure(APILogFailure{
			Operation:      "append_settlement",
			SessionID:      apiLogSessionID(ctx),
			AttemptGroupID: g.ID,
			Err:            err,
		})
	}
}

func (g *APIAttemptGroup) bindSinkLocked(state apiAttemptSinkContext) {
	if g.sinkBound {
		return
	}
	g.sinkBound = true
	g.sink = state.sink
}

func (g *APIAttemptGroup) recordFailure(failure APILogFailure) {
	g.mu.Lock()
	g.forensicIncomplete = true
	sink := g.sink
	g.mu.Unlock()
	var observed apiLogObservedFailure
	if failure.Err == nil || errors.As(failure.Err, &observed) {
		return
	}
	if source, ok := sink.(apiLogFailureObserverSource); ok {
		if observer := source.apiLogFailureObserver(); observer != nil {
			observer(failure)
		}
	}
}

func buildAPIAttemptRecord(groupID, attemptID string, index int, meta APIAttemptMeta, result APIAttemptResult) apilog.APIAttemptRecord {
	latency := result.FinishedAt.Sub(meta.StartedAt).Milliseconds()
	if latency < 0 {
		latency = 0
	}
	record := apilog.APIAttemptRecord{
		Kind:             "api_attempt",
		SchemaVersion:    1,
		AttemptID:        attemptID,
		AttemptGroupID:   groupID,
		AttemptIndex:     index,
		Timestamp:        meta.StartedAt.UTC(),
		LatencyMS:        latency,
		ProviderInstance: meta.ProviderInstance,
		RequestModel:     meta.RequestModel,
		Request: apilog.APIAttemptRequest{
			Method:         meta.Method,
			Endpoint:       meta.Endpoint,
			Headers:        cloneHTTPHeader(meta.Headers),
			Body:           apilog.EncodeBody(meta.RequestBody),
			Model:          meta.RequestModel,
			HistoryMode:    string(meta.HistoryMode),
			EndpointFamily: meta.EndpointFamily,
		},
		Outcome:    result.Outcome,
		ErrorClass: result.ErrorClass,
	}
	if result.Err != nil {
		record.ErrorMessage = SanitizeErrorForAPILog(result.Err.Error(), meta.CredentialMaterial)
	}
	if result.Response != nil || result.StatusCode != 0 || result.ResponseBody != nil {
		response := apilog.APIAttemptResponse{
			StatusCode: result.StatusCode,
			Body:       apilog.EncodeBody(result.ResponseBody),
		}
		if result.Response != nil {
			response.Model = result.Response.Model
			response.FinishReason = result.Response.Finish.Reason
			response.TextLength = len(result.Response.Text())
			response.ToolCallCount = len(result.Response.ToolCalls())
			response.Usage = apilog.Usage{
				InputTokens:      result.Response.Usage.InputTokens,
				OutputTokens:     result.Response.Usage.OutputTokens,
				TotalTokens:      result.Response.Usage.TotalTokens,
				CacheReadTokens:  result.Response.Usage.CacheReadTokens,
				CacheWriteTokens: result.Response.Usage.CacheWriteTokens,
			}
		}
		record.Response = &response
	}
	return record
}

func cloneHTTPHeader(header http.Header) map[string][]string {
	if header == nil {
		return nil
	}
	return header.Clone()
}

func apiLogSessionID(ctx context.Context) string {
	if logContext, ok := getAPILogContext(ctx); ok {
		return logContext.SessionID
	}
	return ""
}
