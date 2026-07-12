package llm

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type blockingAPILogStream struct {
	events chan StreamEvent
	closed chan struct{}
}

func (s *blockingAPILogStream) Events() <-chan StreamEvent { return s.events }
func (s *blockingAPILogStream) Close() error {
	close(s.closed)
	return errors.New("close sentinel")
}

// FuzzAPILogEdges replays filesystem failures, suppressed adapter attempts,
// raw-error filtering, and precisely controlled stream shutdown paths. All I/O
// remains inside t.TempDir and no provider or network boundary is crossed.
func FuzzAPILogEdges(f *testing.F) {
	f.Add(byte(0))
	f.Fuzz(func(t *testing.T, _ byte) {
		dir := t.TempDir()
		blocker := filepath.Join(dir, "blocker")
		if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := NewAPILogger(filepath.Join(blocker, "child", "api")); err == nil {
			t.Fatal("NewAPILogger mkdir unexpectedly succeeded")
		}
		if _, err := NewAPILogger(dir); err == nil {
			t.Fatal("NewAPILogger open unexpectedly succeeded")
		}
		if _, err := NewSessionAPILogger(blocker); err == nil {
			t.Fatal("NewSessionAPILogger unexpectedly succeeded")
		}

		logger := &APILogger{dirty: map[*os.File]struct{}{}}
		if err := logger.EnableRawLogging(filepath.Join(blocker, "child", "raw")); err == nil {
			t.Fatal("EnableRawLogging mkdir unexpectedly succeeded")
		}
		if err := logger.EnableRawLogging(dir); err == nil {
			t.Fatal("EnableRawLogging open unexpectedly succeeded")
		}

		// Both writers must silently drop entries that cannot be marshaled.
		oldMarshal := apiLogJSONMarshal
		apiLogJSONMarshal = func(any) ([]byte, error) { return nil, errors.New("marshal sentinel") }
		logger.write(APILogEntry{})
		logger.writeRaw(APIRawLogEntry{})
		apiLogJSONMarshal = oldMarshal

		// Exercise nil destinations and disabled per-session raw routing.
		logger.write(APILogEntry{})
		logger.writeRaw(APIRawLogEntry{})
		logger.sessionsDir = blocker
		logger.sessionFiles = map[string]*os.File{}
		logger.sessionRawFiles = map[string]*os.File{}
		logger.write(APILogEntry{SessionID: "s"})
		logger.writeRaw(APIRawLogEntry{SessionID: "s"})
		logger.sessionRaw = true
		logger.writeRaw(APIRawLogEntry{SessionID: "s"})

		// Ordinary errors and raw errors with empty bodies are intentionally absent.
		logger.sessionsDir = ""
		logger.rawFile, _ = os.Create(filepath.Join(dir, "raw.jsonl"))
		logger.writeRawError(APILogEntry{}, Request{}, "complete", errors.New("plain"))
		rawEmpty := NewStreamErrorWithRawBodies("p", "empty", nil, "", "")
		logger.writeRawError(APILogEntry{}, Request{}, "complete", rawEmpty)
		logger.writeAdapterAttempt(APILogContext{}, AdapterAttemptRecord{Request: Request{}})

		apiFile, err := os.Create(filepath.Join(dir, "api.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		logger.file = apiFile
		ctx := logger.withAdapterAttemptLogging(context.Background(), &apiLogAdapterAttemptState{})
		nextErr := errors.New("attempt failure")
		_, gotErr := logger.WrapStream(func(ctx context.Context, req Request) (Stream, error) {
			RecordAdapterAttempt(ctx, AdapterAttemptRecord{Request: req, Error: nextErr})
			return nil, nextErr
		})(ctx, Request{})
		if !errors.Is(gotErr, nextErr) {
			t.Fatalf("got %v", gotErr)
		}
		st, err := logger.WrapStream(func(context.Context, Request) (Stream, error) { return nil, nil })(ctx, Request{})
		if err != nil || st != nil {
			t.Fatalf("nil stream = %v, %v", st, err)
		}

		attempts := &apiLogAdapterAttemptState{}
		attempts.mark()
		s := &apiLogStream{logger: logger, ctx: context.Background(), attempts: attempts, logOnce: sync.Once{}}
		s.logError(errors.New("suppressed"))

		// A delta followed by a nil-response finish uses the accumulated response.
		acc := newSliceStream(
			StreamEvent{Type: StreamEventTextDelta, Delta: "x"},
			StreamEvent{Type: StreamEventFinish},
		)
		wrapped := newAPILogStream(context.Background(), acc, logger, Request{Model: "m", Provider: "p"}, time.Now(), nil)
		for range wrapped.Events() {
		}
		_ = wrapped.Close()

		// Fill the output channel so pump blocks in its forwarding select, then close.
		inner := &blockingAPILogStream{events: make(chan StreamEvent), closed: make(chan struct{})}
		closing := make(chan struct{})
		out := make(chan StreamEvent, 1)
		out <- StreamEvent{}
		manual := &apiLogStream{inner: inner, logger: logger, ctx: context.Background(), out: out,
			done: make(chan struct{}), closing: closing, attempts: &apiLogAdapterAttemptState{}}
		go manual.pump()
		inner.events <- StreamEvent{Type: StreamEventTextDelta, Delta: "blocked"}
		close(closing)
		<-manual.done
		alreadyClosing := make(chan struct{})
		close(alreadyClosing)
		cancelled := &apiLogStream{inner: &blockingAPILogStream{events: make(chan StreamEvent), closed: make(chan struct{})}, out: make(chan StreamEvent),
			done: make(chan struct{}), closing: alreadyClosing}
		cancelled.pump()

		// File-operation seams isolate both independent Close failure paths.
		bad, _ := os.Create(filepath.Join(dir, "closed"))
		_ = bad.Close()
		closer := &APILogger{file: bad, dirty: map[*os.File]struct{}{}, sessionFiles: map[string]*os.File{}, sessionRawFiles: map[string]*os.File{}}
		if err := closer.Close(); err == nil {
			t.Fatal("Close unexpectedly succeeded")
		}
		good, _ := os.Create(filepath.Join(dir, "close-error"))
		oldSync, oldClose := apiLogFileSync, apiLogFileClose
		apiLogFileSync = func(*os.File) error { return nil }
		apiLogFileClose = func(*os.File) error { return errors.New("close sentinel") }
		closer = &APILogger{file: good, dirty: map[*os.File]struct{}{}, sessionFiles: map[string]*os.File{}, sessionRawFiles: map[string]*os.File{}}
		if err := closer.Close(); err == nil {
			t.Fatal("injected Close unexpectedly succeeded")
		}
		apiLogFileSync, apiLogFileClose = oldSync, oldClose
		_ = good.Close()
	})
}
