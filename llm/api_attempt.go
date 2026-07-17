package llm

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"primeradiant.com/serf/identifier"
	apilog "primeradiant.com/serf/llm/apilog"
)

// APIAttemptSink durably appends canonical provider attempts and their group
// settlements.
type APIAttemptSink interface {
	AppendAttempt(context.Context, apilog.APIAttemptRecord) error
	AppendSettlement(context.Context, apilog.APIAttemptGroupSettlement) error
}

// APILogFailure describes a forensic storage failure without changing the
// provider result that triggered the write.
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
	secretNames []string
	patterns    []string
}

// APIAttemptMeta carries provider provenance and exact request evidence for one
// transport attempt.
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

// APIAttemptResult carries exact response evidence and the adapter-owned result
// classification for one transport attempt.
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
type apiLogCredentialMaterialContextKey struct{}

type apiAttemptSinkContext struct {
	sink APIAttemptSink
}

type apiLogCredentialMaterialContext struct {
	material APILogCredentialMaterial
}

type apiLogFailureObserverSource interface {
	apiLogFailureObserver() func(APILogFailure)
}

type apiLogObservedFailure interface {
	apiLogFailureWasObserved()
}

// WithAPIAttemptGroup attaches logical provider-attempt coordination to ctx.
func WithAPIAttemptGroup(ctx context.Context, group *APIAttemptGroup) context.Context {
	return context.WithValue(ctx, apiAttemptGroupContextKey{}, group)
}

// WithAPIAttemptSink attaches canonical provider-attempt persistence to ctx.
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

// APIAttemptGroup coordinates attempt identity, append ordering, and final
// settlement for one logical model call.
type APIAttemptGroup struct {
	ID string

	mu                 sync.Mutex
	nextAttemptIndex   int
	finalAttemptID     string
	finalAttemptCount  int
	finalOutcome       apilog.AttemptOutcomeClass
	forensicIncomplete bool
	credentialMaterial APILogCredentialMaterial
	settling           bool
	sinkBound          bool
	sink               APIAttemptSink
	pendingAttempts    sync.WaitGroup
}

// NewAPIAttemptGroup returns an empty logical attempt group with the supplied
// stable identifier.
func NewAPIAttemptGroup(id string) *APIAttemptGroup {
	return &APIAttemptGroup{ID: id}
}

// APIAttempt owns the canonical record for one actual provider transport call.
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

// SetWireRequestMetadata replaces the preliminary request snapshot with the
// credential-free method, endpoint, headers, and credential material observed
// after the HTTP transport wrote the request.
func (a *APIAttempt) SetWireRequestMetadata(method, endpoint string, headers http.Header, material APILogCredentialMaterial) {
	if !a.Active() {
		return
	}
	a.mu.Lock()
	a.meta.Method = method
	a.meta.Endpoint = endpoint
	a.meta.Headers = headers.Clone()
	a.meta.CredentialMaterial = material
	a.mu.Unlock()

	a.group.mu.Lock()
	a.group.credentialMaterial = mergeAPILogCredentialMaterial(a.group.credentialMaterial, material)
	a.group.mu.Unlock()
}

// BeginAPIAttempt allocates the next attempt in the group attached to ctx. It
// returns an inert attempt when canonical coordination is not attached.
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
	group.credentialMaterial = mergeAPILogCredentialMaterial(group.credentialMaterial, meta.CredentialMaterial)
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
		if a.index == a.group.finalAttemptCount {
			a.group.finalOutcome = record.Outcome
		}
		a.group.mu.Unlock()
		appendCtx := withAPILogCredentialMaterial(context.WithoutCancel(a.ctx), meta.CredentialMaterial)
		if err := a.sink.AppendAttempt(appendCtx, record); err != nil {
			a.group.recordFailure(APILogFailure{
				Operation:      "append_attempt",
				SessionID:      apiLogSessionID(a.ctx),
				AttemptGroupID: a.group.ID,
				AttemptID:      a.id,
				Err:            sanitizeAPILogError(err, meta.CredentialMaterial),
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
		if ctx != nil && ctx.Err() != nil {
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

type sanitizedObservedAPILogError struct {
	sanitizedAPILogError
}

func (sanitizedObservedAPILogError) apiLogFailureWasObserved() {}

func sanitizeAPILogError(err error, material APILogCredentialMaterial) error {
	if err == nil {
		return nil
	}
	_, observed := err.(apiLogObservedFailure)
	rendered := renderAPILogError(err)
	sanitized := SanitizeErrorForAPILog(rendered, material)
	if sanitized == "" {
		sanitized = "API-log failure details omitted"
	}
	flat := sanitizedAPILogError{text: sanitized, err: err}
	if observed {
		return sanitizedObservedAPILogError{sanitizedAPILogError: flat}
	}
	return flat
}

func renderAPILogError(err error) (text string) {
	text = "error text unavailable"
	defer func() {
		if recover() != nil {
			text = "error text unavailable"
		}
	}()
	return err.Error()
}

func withAPILogCredentialMaterial(ctx context.Context, material APILogCredentialMaterial) context.Context {
	return context.WithValue(ctx, apiLogCredentialMaterialContextKey{}, apiLogCredentialMaterialContext{material: material})
}

func apiLogCredentialMaterialFromContext(ctx context.Context) (APILogCredentialMaterial, bool) {
	if ctx == nil {
		return APILogCredentialMaterial{}, false
	}
	state, ok := ctx.Value(apiLogCredentialMaterialContextKey{}).(apiLogCredentialMaterialContext)
	return state.material, ok
}

func mergeAPILogCredentialMaterial(left, right APILogCredentialMaterial) APILogCredentialMaterial {
	headerNames := make([]string, 0, len(left.HeaderNames)+len(right.HeaderNames))
	for name := range left.HeaderNames {
		headerNames = append(headerNames, name)
	}
	for name := range right.HeaderNames {
		headerNames = append(headerNames, name)
	}
	queryNames := make([]string, 0, len(left.QueryNames)+len(right.QueryNames))
	for name := range left.QueryNames {
		queryNames = append(queryNames, name)
	}
	for name := range right.QueryNames {
		queryNames = append(queryNames, name)
	}
	values := append([]string(nil), left.Values...)
	values = append(values, right.Values...)
	return NewAPILogCredentialMaterial(headerNames, queryNames, values...)
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
	credentialMaterial := g.credentialMaterial
	g.mu.Unlock()
	if sink == nil {
		return
	}
	appendCtx := withAPILogCredentialMaterial(context.WithoutCancel(ctx), credentialMaterial)
	if err := sink.AppendSettlement(appendCtx, settlement); err != nil {
		g.recordFailure(APILogFailure{
			Operation:      "append_settlement",
			SessionID:      apiLogSessionID(ctx),
			AttemptGroupID: g.ID,
			Err:            sanitizeAPILogError(err, credentialMaterial),
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
	if failure.Err == nil {
		return
	}
	if _, observed := failure.Err.(apiLogObservedFailure); observed {
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
	patterns := credentialEvidencePatterns(meta.CredentialMaterial)
	record := apilog.APIAttemptRecord{
		Kind:             "api_attempt",
		SchemaVersion:    1,
		AttemptID:        attemptID,
		AttemptGroupID:   groupID,
		AttemptIndex:     index,
		Timestamp:        meta.StartedAt.UTC(),
		LatencyMS:        latency,
		ProviderInstance: omitCredentialString(meta.ProviderInstance, patterns),
		RequestModel:     omitCredentialString(meta.RequestModel, patterns),
		Request: apilog.APIAttemptRequest{
			Method:         omitCredentialString(meta.Method, patterns),
			Endpoint:       omitCredentialString(sanitizeEndpointForAPILog(meta.Endpoint), patterns),
			Headers:        cloneCredentialFreeHTTPHeader(meta.Headers, patterns),
			Body:           encodeProviderBody(meta.RequestBody, patterns),
			Model:          omitCredentialString(meta.RequestModel, patterns),
			HistoryMode:    omitCredentialString(string(meta.HistoryMode), patterns),
			EndpointFamily: omitCredentialString(meta.EndpointFamily, patterns),
		},
		Outcome:    canonicalAPIAttemptOutcome(result),
		ErrorClass: omitCredentialString(result.ErrorClass, patterns),
	}
	if result.Err != nil {
		record.ErrorMessage = SanitizeErrorForAPILog(renderAPILogError(result.Err), meta.CredentialMaterial)
	}
	if result.Response != nil || result.StatusCode != 0 || result.ResponseBody != nil {
		response := apilog.APIAttemptResponse{
			StatusCode: omitCredentialInt(result.StatusCode, patterns),
			Body:       encodeProviderBody(result.ResponseBody, patterns),
		}
		if result.Response != nil {
			response.Model = omitCredentialString(result.Response.Model, patterns)
			response.FinishReason = omitCredentialString(result.Response.Finish.Reason, patterns)
			response.TextLength = omitCredentialInt(len(result.Response.Text()), patterns)
			response.ToolCallCount = omitCredentialInt(len(result.Response.ToolCalls()), patterns)
			response.Usage = apilog.Usage{
				InputTokens:      omitCredentialInt(result.Response.Usage.InputTokens, patterns),
				OutputTokens:     omitCredentialInt(result.Response.Usage.OutputTokens, patterns),
				TotalTokens:      omitCredentialInt(result.Response.Usage.TotalTokens, patterns),
				CacheReadTokens:  omitCredentialIntPointer(result.Response.Usage.CacheReadTokens, patterns),
				CacheWriteTokens: omitCredentialIntPointer(result.Response.Usage.CacheWriteTokens, patterns),
			}
		}
		record.Response = &response
	}
	return record.WithForbiddenProviderEvidence(patterns)
}

func canonicalAPIAttemptOutcome(result APIAttemptResult) apilog.AttemptOutcomeClass {
	switch result.Outcome {
	case apilog.AttemptSuccess,
		apilog.AttemptProviderReject,
		apilog.AttemptTransportFail,
		apilog.AttemptProviderTimeout,
		apilog.AttemptCallerCancel,
		apilog.AttemptDecodeFail:
		return result.Outcome
	default:
		if result.Err != nil {
			return apilog.AttemptTransportFail
		}
		return apilog.AttemptSuccess
	}
}

func omitCredentialString(value string, patterns []string) string {
	if containsCredentialPattern(value, patterns) {
		return ""
	}
	return value
}

func omitCredentialInt(value int, patterns []string) int {
	if containsCredentialPattern(strconv.Itoa(value), patterns) {
		return 0
	}
	return value
}

func omitCredentialIntPointer(value *int, patterns []string) *int {
	if value == nil || containsCredentialPattern(strconv.Itoa(*value), patterns) {
		return nil
	}
	copy := *value
	return &copy
}

func encodeProviderBody(body []byte, patterns []string) apilog.EncodedBody {
	if containsCredentialPattern(string(body), patterns) {
		return apilog.EncodedBody{CredentialValuesExcluded: true}
	}
	return apilog.EncodeBody(body)
}

func cloneCredentialFreeHTTPHeader(header http.Header, patterns []string) apilog.EncodedHeader {
	var cloned apilog.EncodedHeader
	for name, values := range header {
		if containsCredentialPattern(name, patterns) {
			continue
		}
		credentialBearing := false
		for _, value := range values {
			if containsCredentialPattern(value, patterns) {
				credentialBearing = true
				break
			}
		}
		if credentialBearing {
			continue
		}
		if cloned == nil {
			cloned = make(apilog.EncodedHeader)
		}
		cloned[name] = append([]string(nil), values...)
	}
	return cloned
}

func containsCredentialPattern(value string, patterns []string) bool {
	for _, pattern := range patterns {
		if pattern != "" && strings.Contains(value, pattern) {
			return true
		}
	}
	return false
}

func apiLogSessionID(ctx context.Context) string {
	if logContext, ok := getAPILogContext(ctx); ok {
		return logContext.SessionID
	}
	return ""
}
