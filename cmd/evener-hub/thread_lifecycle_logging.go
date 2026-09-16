package hub

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"sync/atomic"
	"time"

	"primeradiant.com/evener/identifier"
)

// These records share the ordinary hub stderr, not the daemon output or the
// client's RPC lifetime. Values are metadata only: never format an error, argv,
// environment, endpoint, path, or daemon output into this stream.
type threadLifecycleLog struct {
	writer            io.Writer
	requestID         string
	operation         string
	sessionID         string
	resolvedSessionID string
	started           time.Time
}

type threadLifecycleLogKey struct{}

var threadLifecycleSequence atomic.Uint64

func withThreadLifecycleLog(ctx context.Context, operation, sessionID string, writer io.Writer) (context.Context, *threadLifecycleLog) {
	if trace, ok := ctx.Value(threadLifecycleLogKey{}).(*threadLifecycleLog); ok {
		return ctx, trace
	}
	if writer == nil {
		writer = os.Stderr
	}
	now := time.Now()
	trace := &threadLifecycleLog{
		writer:    writer,
		requestID: fmt.Sprintf("%d-%d-%d", os.Getpid(), now.UnixNano(), threadLifecycleSequence.Add(1)),
		operation: operation, sessionID: sessionID, started: now,
	}
	return context.WithValue(ctx, threadLifecycleLogKey{}, trace), trace
}

func threadLifecycleFromContext(ctx context.Context) *threadLifecycleLog {
	trace, _ := ctx.Value(threadLifecycleLogKey{}).(*threadLifecycleLog)
	return trace
}

func (l *threadLifecycleLog) resolved(ctx context.Context, sessionID string) (context.Context, *threadLifecycleLog) {
	resolved := *l
	resolved.resolvedSessionID = sessionID
	return context.WithValue(ctx, threadLifecycleLogKey{}, &resolved), &resolved
}

// Invalid caller-controlled identities are omitted, not merely escaped: even a
// printable token can be a secret. Valid session IDs are the correlation domain.
func lifecycleSessionID(id string) string {
	if identifier.ValidateSessionID(id) != nil {
		return "-"
	}
	return id
}

func (l *threadLifecycleLog) stage(ctx context.Context, stage string) func(error) {
	if l == nil {
		return func(error) {}
	}
	started := time.Now()
	l.record(ctx, stage, "begin", started, nil, 0, 0)
	return func(err error) { l.record(ctx, stage, "complete", started, err, 0, 0) }
}

func lifecycleErrorClass(ctx context.Context, err error) string {
	switch {
	case err == nil:
		return "none"
	case errors.Is(err, errRendezvousCanceled), errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, errRendezvousTimeout), errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	// A missing executable is not-found in both shapes: a path that does not
	// exist (fs.ErrNotExist) and a bare name absent from $PATH, which os/exec
	// reports as exec.ErrNotFound and which does not wrap fs.ErrNotExist.
	case errors.Is(err, fs.ErrNotExist), errors.Is(err, exec.ErrNotFound):
		return "not_found"
	case errors.Is(err, fs.ErrPermission):
		return "permission"
	default:
		if _, ok := errors.AsType[*exec.ExitError](err); ok {
			return "process_exit"
		}
		if errors.Is(ctx.Err(), context.Canceled) {
			return "canceled"
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "timeout"
		}
		return "failed"
	}
}

func (l *threadLifecycleLog) record(ctx context.Context, stage, state string, started time.Time, err error, pid, tailBytes int) {
	if l == nil {
		return
	}
	class := lifecycleErrorClass(ctx, err)
	result := "success"
	if state == "begin" {
		result = "pending"
	} else if class == "canceled" {
		result = "canceled"
	} else if err != nil {
		result = "error"
	}
	exitCode := -1
	if exit, ok := errors.AsType[*exec.ExitError](err); ok {
		exitCode = exit.ExitCode()
	}
	_, _ = fmt.Fprintf(l.writer, "[hub] lifecycle request_id=%s operation=%s session_id=%s resolved_session_id=%s stage=%s state=%s result=%s error_class=%s elapsed_ms=%d stage_elapsed_ms=%d pid=%d exit_code=%d tail_bytes=%d tail_at_limit=%t\n",
		l.requestID, l.operation, lifecycleSessionID(l.sessionID), lifecycleSessionID(l.resolvedSessionID), stage, state, result, class,
		time.Since(l.started).Milliseconds(), time.Since(started).Milliseconds(), pid, exitCode, tailBytes, tailBytes == daemonLaunchOutputLimit)
}
