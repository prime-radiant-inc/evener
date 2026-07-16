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
	sink            APIAttemptSink
	failureObserver func(APILogFailure)
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
	state := apiAttemptSinkContext{sink: sink}
	if source, ok := sink.(apiLogFailureObserverSource); ok {
		state.failureObserver = source.apiLogFailureObserver()
	}
	return context.WithValue(ctx, apiAttemptSinkContextKey{}, state)
}

type APIAttemptGroup struct {
	ID string

	mu                 sync.Mutex
	nextAttemptIndex   int
	finalAttemptID     string
	finalAttemptCount  int
	forensicIncomplete bool
	settling           bool
	sink               APIAttemptSink
	failureObserver    func(APILogFailure)
	pendingAttempts    sync.WaitGroup
}

func NewAPIAttemptGroup(id string) *APIAttemptGroup {
	return &APIAttemptGroup{ID: id}
}

type APIAttempt struct {
	once  sync.Once
	group *APIAttemptGroup
	sink  APIAttemptSink
	ctx   context.Context
	meta  APIAttemptMeta
	id    string
	index int
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
		record := buildAPIAttemptRecord(a.group.ID, a.id, a.index, a.meta, result)
		appendCtx := context.WithoutCancel(a.ctx)
		if err := a.sink.AppendAttempt(appendCtx, record); err != nil {
			a.group.recordFailure(APILogFailure{
				Operation:      "append_attempt",
				SessionID:      apiLogSessionID(a.ctx),
				AttemptGroupID: a.group.ID,
				AttemptID:      a.id,
				Err:            err,
			})
		}
	})
}

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
	if g.sink == nil && state.sink != nil {
		g.sink = state.sink
	}
	if g.failureObserver == nil && state.failureObserver != nil {
		g.failureObserver = state.failureObserver
	}
}

func (g *APIAttemptGroup) recordFailure(failure APILogFailure) {
	g.mu.Lock()
	g.forensicIncomplete = true
	observer := g.failureObserver
	g.mu.Unlock()
	var observed apiLogObservedFailure
	if failure.Err != nil && !errors.As(failure.Err, &observed) && observer != nil {
		observer(failure)
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
		record.ErrorMessage = result.Err.Error()
	}
	if result.Response != nil || result.StatusCode != 0 || result.ResponseBody != nil {
		response := apilog.APIAttemptResponse{
			StatusCode: result.StatusCode,
			Body:       apilog.EncodeBody(result.ResponseBody),
		}
		if result.Response != nil {
			response.Model = result.Response.Model
			response.FinishReason = result.Response.Finish.Reason
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
