package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"primeradiant.com/serf/identifier"
	apilog "primeradiant.com/serf/llm/apilog"
)

type apiLogKey struct{}

// APILogContext carries the session that owns canonical attempt evidence.
type APILogContext struct {
	SessionID string
}

func WithAPILogContext(ctx context.Context, sessionID string, _ int) context.Context {
	return context.WithValue(ctx, apiLogKey{}, APILogContext{SessionID: sessionID})
}

func getAPILogContext(ctx context.Context) (APILogContext, bool) {
	v, ok := ctx.Value(apiLogKey{}).(APILogContext)
	return v, ok
}

var apiLogJSONMarshal = json.Marshal
var apiLogFileSync = func(f *os.File) error { return f.Sync() }
var apiLogFileClose = func(f *os.File) error { return f.Close() }

var errAPILoggerClosed = errors.New("API logger is closed")

type observedAPILogError struct {
	err error
}

func (e observedAPILogError) Error() string           { return e.err.Error() }
func (e observedAPILogError) Unwrap() error           { return e.err }
func (observedAPILogError) apiLogFailureWasObserved() {}

type pendingAPILogFailure struct {
	id                 uint64
	failure            APILogFailure
	credentialMaterial APILogCredentialMaterial
}

// APILogger persists canonical transport attempts and logical-call settlements.
// NewAPILogger writes one file; NewSessionAPILogger routes by session id.
type APILogger struct {
	file *os.File
	mu   sync.Mutex

	canonicalAdmissionMu sync.Mutex
	canonicalClosing     bool
	canonicalAppends     sync.WaitGroup
	failureMu            sync.RWMutex
	failureObserver      func(APILogFailure)

	sessionsDir  string
	sessionFiles map[string]*os.File

	SyncInterval time.Duration
	dirty        map[*os.File]struct{}
	lastSync     time.Time

	canonicalPending map[*os.File][]pendingAPILogFailure
	nextPendingID    uint64
}

func NewAPILogger(path string) (*APILogger, error) {
	if err := ensurePrivateAPILogDirectory(filepath.Dir(path)); err != nil {
		return nil, err
	}
	f, err := openPrivateAPILogFile(path)
	if err != nil {
		return nil, err
	}
	return &APILogger{
		file:             f,
		dirty:            map[*os.File]struct{}{},
		canonicalPending: map[*os.File][]pendingAPILogFailure{},
		lastSync:         time.Now(),
	}, nil
}

func NewSessionAPILogger(stateDir string) (*APILogger, error) {
	sessionsDir := filepath.Join(stateDir, "sessions")
	if err := ensurePrivateAPILogDirectory(sessionsDir); err != nil {
		return nil, err
	}
	return &APILogger{
		sessionsDir:      sessionsDir,
		sessionFiles:     map[string]*os.File{},
		dirty:            map[*os.File]struct{}{},
		canonicalPending: map[*os.File][]pendingAPILogFailure{},
		lastSync:         time.Now(),
	}, nil
}

func sessionLogBaseName(sessionID string) string {
	if sessionID == "" {
		return "unattributed"
	}
	for _, r := range sessionID {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return "unattributed"
		}
	}
	return sessionID
}

func (l *APILogger) sessionFileWithError(sessionID string) (*os.File, error) {
	base := sessionLogBaseName(sessionID)
	if f, ok := l.sessionFiles[base]; ok {
		if f == nil {
			return nil, fmt.Errorf("API log for %q is unavailable", base)
		}
		return f, nil
	}
	f, err := openPrivateAPILogFile(filepath.Join(l.sessionsDir, base+".api.jsonl"))
	if err != nil {
		l.sessionFiles[base] = nil
		return nil, err
	}
	l.sessionFiles[base] = f
	return f, nil
}

func ensurePrivateAPILogDirectory(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	return os.MkdirAll(path, 0o700)
}

func openPrivateAPILogFile(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

// WrapComplete binds the canonical sink and owns settlement only when the
// caller did not supply an outer logical attempt group.
func (l *APILogger) WrapComplete(next CompleteFunc) CompleteFunc {
	return func(ctx context.Context, req Request) (Response, error) {
		ctx, group, ownsSettlement := l.bindAPIAttemptGroup(ctx)
		resp, err := next(ctx, req)
		if ownsSettlement {
			group.SettleResult(ctx, err)
		}
		return resp, err
	}
}

// WrapStream settles an implicit group only at terminal stream state or Close.
func (l *APILogger) WrapStream(next StreamFunc) StreamFunc {
	return func(ctx context.Context, req Request) (Stream, error) {
		ctx, group, ownsSettlement := l.bindAPIAttemptGroup(ctx)
		stream, err := next(ctx, req)
		if err != nil {
			if ownsSettlement {
				group.SettleResult(ctx, err)
			}
			return nil, err
		}
		if stream == nil {
			if ownsSettlement {
				group.SettleResult(ctx, nil)
			}
			return nil, nil
		}
		if !ownsSettlement {
			return stream, nil
		}
		return newAPIAttemptSettlementStream(ctx, stream, group), nil
	}
}

func (l *APILogger) bindAPIAttemptGroup(ctx context.Context) (context.Context, *APIAttemptGroup, bool) {
	group := apiAttemptGroupFromContext(ctx)
	ownsSettlement := group == nil
	if group == nil {
		group = NewAPIAttemptGroup(identifier.MustNewAgentCallID())
		ctx = WithAPIAttemptGroup(ctx, group)
	}
	return WithAPIAttemptSink(ctx, l), group, ownsSettlement
}

type apiAttemptSettlementStream struct {
	inner   Stream
	ctx     context.Context
	group   *APIAttemptGroup
	out     chan StreamEvent
	done    chan struct{}
	closing chan struct{}
	close   sync.Once
	settle  sync.Once
}

func newAPIAttemptSettlementStream(ctx context.Context, inner Stream, group *APIAttemptGroup) *apiAttemptSettlementStream {
	stream := &apiAttemptSettlementStream{
		inner: inner, ctx: ctx, group: group,
		out: make(chan StreamEvent, 128), done: make(chan struct{}), closing: make(chan struct{}),
	}
	go stream.pump()
	return stream
}

func (s *apiAttemptSettlementStream) pump() {
	defer close(s.done)
	defer close(s.out)
	for {
		select {
		case <-s.closing:
			return
		case event, ok := <-s.inner.Events():
			if !ok {
				s.settleResult(errors.New("stream ended without terminal event"))
				return
			}
			switch event.Type {
			case StreamEventFinish:
				s.settleResult(nil)
			case StreamEventError:
				s.settleResult(event.Err)
			}
			select {
			case s.out <- event:
			case <-s.closing:
				return
			}
		}
	}
}

func (s *apiAttemptSettlementStream) settleResult(err error) {
	s.settle.Do(func() { s.group.SettleResult(s.ctx, err) })
}

func (s *apiAttemptSettlementStream) Events() <-chan StreamEvent { return s.out }

func (s *apiAttemptSettlementStream) Close() error {
	var err error
	s.close.Do(func() {
		close(s.closing)
		err = s.inner.Close()
		s.settleResult(context.Canceled)
	})
	<-s.done
	return err
}

func (l *APILogger) AppendAttempt(ctx context.Context, rec apilog.APIAttemptRecord) error {
	failure := APILogFailure{
		Operation: "append_attempt", SessionID: apiLogSessionID(ctx),
		AttemptGroupID: rec.AttemptGroupID, AttemptID: rec.AttemptID,
	}
	if err := l.appendCanonicalRecord(ctx, rec, failure); err != nil {
		failure.Err = err
		return l.observeClosedAppend(ctx, failure)
	}
	return nil
}

func (l *APILogger) AppendSettlement(ctx context.Context, rec apilog.APIAttemptGroupSettlement) error {
	failure := APILogFailure{
		Operation: "append_settlement", SessionID: apiLogSessionID(ctx), AttemptGroupID: rec.AttemptGroupID,
	}
	if err := l.appendCanonicalRecord(ctx, rec, failure); err != nil {
		failure.Err = err
		return l.observeClosedAppend(ctx, failure)
	}
	return nil
}

func (l *APILogger) SetFailureObserver(observer func(APILogFailure)) {
	l.failureMu.Lock()
	l.failureObserver = observer
	l.failureMu.Unlock()
}

func (l *APILogger) apiLogFailureObserver() func(APILogFailure) {
	l.failureMu.RLock()
	defer l.failureMu.RUnlock()
	return l.failureObserver
}

func (l *APILogger) observeAPILogFailure(failure APILogFailure) {
	if observer := l.apiLogFailureObserver(); observer != nil {
		observer(failure)
	}
}

func (l *APILogger) observeClosedAppend(ctx context.Context, failure APILogFailure) error {
	if !errors.Is(failure.Err, errAPILoggerClosed) {
		return failure.Err
	}
	if _, coordinatorManaged := apiLogCredentialMaterialFromContext(ctx); coordinatorManaged {
		return failure.Err
	}
	l.observeAPILogFailure(failure)
	return observedAPILogError{err: failure.Err}
}

func (l *APILogger) appendCanonicalRecord(ctx context.Context, record any, failure APILogFailure) error {
	if err := l.admitCanonicalAppend(); err != nil {
		return err
	}
	defer l.canonicalAppends.Done()

	data, err := apiLogJSONMarshal(record)
	if err != nil {
		return fmt.Errorf("marshal API-log record: %w", err)
	}
	l.mu.Lock()
	f := l.file
	if l.sessionsDir != "" {
		f, err = l.sessionFileWithError(apiLogSessionID(ctx))
		if err != nil {
			l.mu.Unlock()
			return fmt.Errorf("open session API log: %w", err)
		}
	}
	if f == nil {
		l.mu.Unlock()
		return fmt.Errorf("API logger has no destination")
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		l.mu.Unlock()
		return fmt.Errorf("append API-log record: %w", err)
	}
	l.dirty[f] = struct{}{}
	credentialMaterial, coordinatorManaged := apiLogCredentialMaterialFromContext(ctx)
	l.nextPendingID++
	pendingID := l.nextPendingID
	l.canonicalPending[f] = append(l.canonicalPending[f], pendingAPILogFailure{
		id:                 pendingID,
		failure:            failure,
		credentialMaterial: credentialMaterial,
	})
	syncFailures, observations := l.syncDirtyLocked()
	ownSyncErr := syncFailures[f]
	l.mu.Unlock()
	ownObserved := false
	for _, observation := range observations {
		if observation.id == pendingID && coordinatorManaged {
			continue
		}
		if observation.id == pendingID {
			ownObserved = true
		}
		l.observeAPILogFailure(observation.failure)
	}
	if ownSyncErr != nil {
		if ownObserved {
			return observedAPILogError{err: ownSyncErr}
		}
		return ownSyncErr
	}
	return nil
}

func (l *APILogger) admitCanonicalAppend() error {
	l.canonicalAdmissionMu.Lock()
	defer l.canonicalAdmissionMu.Unlock()
	if l.canonicalClosing {
		return errAPILoggerClosed
	}
	l.canonicalAppends.Add(1)
	return nil
}

func (l *APILogger) syncDirtyLocked() (map[*os.File]error, []pendingAPILogFailure) {
	if l.SyncInterval > 0 && time.Since(l.lastSync) < l.SyncInterval {
		return nil, nil
	}
	var syncFailures map[*os.File]error
	var observations []pendingAPILogFailure
	for dirtyFile := range l.dirty {
		if err := apiLogFileSync(dirtyFile); err != nil {
			if syncFailures == nil {
				syncFailures = map[*os.File]error{}
			}
			syncErr := fmt.Errorf("sync API-log record: %w", err)
			syncFailures[dirtyFile] = syncErr
			observations = append(observations, l.takeCanonicalSyncFailuresLocked(dirtyFile, syncErr)...)
			continue
		}
		delete(l.dirty, dirtyFile)
		delete(l.canonicalPending, dirtyFile)
	}
	l.lastSync = time.Now()
	return syncFailures, observations
}

func (l *APILogger) takeCanonicalSyncFailuresLocked(file *os.File, err error) []pendingAPILogFailure {
	failures := l.canonicalPending[file]
	delete(l.canonicalPending, file)
	for i := range failures {
		failures[i].failure.Err = sanitizeAPILogError(err, failures[i].credentialMaterial)
	}
	return failures
}

func (l *APILogger) Close() error {
	l.canonicalAdmissionMu.Lock()
	l.canonicalClosing = true
	l.canonicalAdmissionMu.Unlock()
	l.canonicalAppends.Wait()

	l.mu.Lock()
	var firstErr error
	var observations []pendingAPILogFailure
	closeFile := func(f *os.File) {
		if f == nil {
			return
		}
		if err := apiLogFileSync(f); err != nil {
			observations = append(observations, l.takeCanonicalSyncFailuresLocked(f, fmt.Errorf("sync API-log record: %w", err))...)
			if firstErr == nil {
				firstErr = err
			}
		} else {
			delete(l.dirty, f)
			delete(l.canonicalPending, f)
		}
		if err := apiLogFileClose(f); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	closeFile(l.file)
	l.file = nil
	for _, f := range l.sessionFiles {
		closeFile(f)
	}
	l.sessionFiles = map[string]*os.File{}
	l.dirty = map[*os.File]struct{}{}
	l.canonicalPending = map[*os.File][]pendingAPILogFailure{}
	l.sessionsDir = ""
	l.mu.Unlock()
	for _, observation := range observations {
		l.observeAPILogFailure(observation.failure)
	}
	return firstErr
}

// StampEndpointURL records a sanitized dialed endpoint on a response.
func StampEndpointURL(resp *Response, endpoint string) {
	if resp == nil || endpoint == "" {
		return
	}
	if resp.Raw == nil {
		resp.Raw = map[string]any{}
	}
	resp.Raw["endpoint_url"] = endpoint
}
