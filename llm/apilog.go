package llm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"primeradiant.com/serf/identifier"
	apilog "primeradiant.com/serf/llm/apilog"
)

type apiLogKey struct{}

// APILogContext carries the session that owns canonical attempt evidence.
type APILogContext struct {
	SessionID string
}

// WithAPILogContext attributes canonical API-log records to sessionID.
func WithAPILogContext(ctx context.Context, sessionID string) context.Context {
	return context.WithValue(ctx, apiLogKey{}, APILogContext{SessionID: sessionID})
}

func getAPILogContext(ctx context.Context) (APILogContext, bool) {
	v, ok := ctx.Value(apiLogKey{}).(APILogContext)
	return v, ok
}

var apiLogMarshalRecord = apilog.MarshalRecord
var apiLogOpenFile = openPrivateAPILogFile
var apiLogFileWrite = func(f *os.File, data []byte) (int, error) { return f.Write(data) }
var apiLogFileSync = func(f *os.File) error { return f.Sync() }
var apiLogFileClose = func(f *os.File) error { return f.Close() }

var errAPILoggerClosed = errors.New("API logger is closed")

// ErrAPILogTargetLocked reports that another process owns the target API log.
var ErrAPILogTargetLocked = errors.New("API log target is already running")

const canonicalAPILogMaxLineBytes = 128 << 20

type observedAPILogError struct {
	text string
}

func (e observedAPILogError) Error() string           { return e.text }
func (observedAPILogError) apiLogFailureWasObserved() {}

func markAPILogErrorObserved(err error) error {
	if err == nil {
		return nil
	}
	return observedAPILogError{text: renderAPILogError(err)}
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

	sessionsDir   string
	sessionFiles  map[string]*os.File
	quarantineErr error
}

// NewAPILogger opens one private canonical API-log file for durable appends.
func NewAPILogger(path string) (*APILogger, error) {
	if err := ensurePrivateAPILogDirectory(filepath.Dir(path)); err != nil {
		return nil, err
	}
	f, err := openPrivateAPILogFile(path)
	if err != nil {
		return nil, err
	}
	return &APILogger{file: f}, nil
}

// NewSessionAPILogger routes private canonical API-log appends by session ID
// beneath the state's sessions directory.
func NewSessionAPILogger(stateDir string) (*APILogger, error) {
	sessionsDir := filepath.Join(stateDir, "sessions")
	if err := ensurePrivateAPILogDirectory(sessionsDir); err != nil {
		return nil, err
	}
	return &APILogger{
		sessionsDir:  sessionsDir,
		sessionFiles: map[string]*os.File{},
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
	f, err := apiLogOpenFile(filepath.Join(l.sessionsDir, base+".api.jsonl"))
	if err != nil {
		l.sessionFiles[base] = nil
		return nil, err
	}
	l.sessionFiles[base] = f
	return f, nil
}

// ReserveSession eagerly acquires ownership of a known resumed session's API
// log. Fresh sessions remain lazy and open their unique target on first append.
func (l *APILogger) ReserveSession(sessionID string) error {
	if err := l.admitCanonicalAppend(); err != nil {
		return err
	}
	defer l.canonicalAppends.Done()

	l.mu.Lock()
	defer l.mu.Unlock()
	if l.quarantineErr != nil {
		return markAPILogErrorObserved(l.quarantineErr)
	}
	if l.sessionsDir == "" {
		return errors.New("API logger does not route session files")
	}
	_, err := l.sessionFileWithError(sessionID)
	return err
}

func ensurePrivateAPILogDirectory(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	return os.MkdirAll(path, 0o700)
}

func recoverCanonicalAPILogTail(file *os.File, maxLineBytes int) error {
	completeOffset, partialTail, err := apilog.ScanRecovery(file, maxLineBytes)
	if err != nil {
		return fmt.Errorf("scan API log for recovery: %w", err)
	}
	if !partialTail {
		return nil
	}
	if err := file.Truncate(completeOffset); err != nil {
		return fmt.Errorf("truncate partial API-log tail at offset %d: %w", completeOffset, err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync recovered API log: %w", err)
	}
	return nil
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

// AppendAttempt durably appends one canonical provider-attempt record.
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

// AppendSettlement durably appends the final outcome for one attempt group.
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

// SetFailureObserver installs the callback for API-log storage failures.
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
	return markAPILogErrorObserved(failure.Err)
}

func (l *APILogger) appendCanonicalRecord(ctx context.Context, record apilog.APILogRecord, failure APILogFailure) error {
	if err := l.admitCanonicalAppend(); err != nil {
		return err
	}
	var observation *APILogFailure
	defer func() {
		if observation != nil {
			l.observeAPILogFailure(*observation)
		}
	}()
	defer l.canonicalAppends.Done()

	data, err := apiLogMarshalRecord(record)
	if err != nil {
		return fmt.Errorf("marshal API-log record: %w", err)
	}
	l.mu.Lock()
	if l.quarantineErr != nil {
		err := l.quarantineErr
		l.mu.Unlock()
		return markAPILogErrorObserved(err)
	}
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
	line := append(data, '\n')
	written, writeErr := apiLogFileWrite(f, line)
	if writeErr != nil {
		appendErr, observedFailure := l.quarantineCanonicalAppendLocked(ctx, failure, fmt.Errorf("append API-log record: %w", writeErr))
		observation = &observedFailure
		return appendErr
	}
	if written != len(line) {
		appendErr, observedFailure := l.quarantineCanonicalAppendLocked(ctx, failure, fmt.Errorf("append API-log record: wrote %d of %d bytes: %w", written, len(line), io.ErrShortWrite))
		observation = &observedFailure
		return appendErr
	}
	if err := apiLogFileSync(f); err != nil {
		appendErr, observedFailure := l.quarantineCanonicalAppendLocked(ctx, failure, fmt.Errorf("sync API-log record: %w", err))
		observation = &observedFailure
		return appendErr
	}
	l.mu.Unlock()
	return nil
}

func (l *APILogger) quarantineCanonicalAppendLocked(ctx context.Context, failure APILogFailure, err error) (error, APILogFailure) {
	credentialMaterial, _ := apiLogCredentialMaterialFromContext(ctx)
	err = sanitizeAPILogError(err, credentialMaterial)
	l.quarantineErr = err
	failure.Err = err
	l.mu.Unlock()
	return markAPILogErrorObserved(err), failure
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

// Close stops new appends, waits for admitted appends, and closes every
// canonical API-log file owned by the logger.
func (l *APILogger) Close() error {
	l.canonicalAdmissionMu.Lock()
	l.canonicalClosing = true
	l.canonicalAdmissionMu.Unlock()
	l.canonicalAppends.Wait()

	l.mu.Lock()
	firstErr := l.quarantineErr
	closeFile := func(f *os.File) {
		if f == nil {
			return
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
	l.sessionsDir = ""
	l.mu.Unlock()
	return firstErr
}

// StampEndpointURL records a credential-free dialed endpoint on a response.
func StampEndpointURL(resp *Response, endpoint string) {
	if resp == nil || endpoint == "" {
		return
	}
	endpoint = SanitizeEndpointURL(endpoint)
	if endpoint == "" {
		return
	}
	if resp.Raw == nil {
		resp.Raw = map[string]any{}
	}
	resp.Raw["endpoint_url"] = endpoint
}
