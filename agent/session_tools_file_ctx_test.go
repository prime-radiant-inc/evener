package agent

import (
	"context"
	"testing"

	"primeradiant.com/evener/agent/execenv"
)

// ctxRecordingFileEnv records the context a ctx-carrying entry received and
// answers the ctx-less entry with a marker, so a test can tell which entry the
// tool used.
type ctxRecordingFileEnv struct {
	execenv.ExecutionEnvironment
	readCtx chan context.Context
	editCtx chan context.Context
}

func (e ctxRecordingFileEnv) ReadFileCtx(ctx context.Context, _ string, _, _ *int) (string, error) {
	e.readCtx <- ctx
	return "ctx-read", nil
}

func (e ctxRecordingFileEnv) EditFileCtx(ctx context.Context, _, _, _ string, _ bool) (string, error) {
	e.editCtx <- ctx
	return "ctx-edit", nil
}

func (e ctxRecordingFileEnv) ReadFile(_ string, _, _ *int) (string, error) {
	return "ctxless-read", nil
}

func (e ctxRecordingFileEnv) EditFile(_, _, _ string, _ bool) (string, error) {
	return "ctxless-edit", nil
}

func nopReadGuard() readGuard {
	return readGuard{trackRead: func(string) {}, readBeforeWriteWarning: func(string) string { return "" }}
}

type fileCtxKey struct{}

// TestExecFileReadThreadsItsContext pins item 3: read_file must pass the tool's
// context to the ctx-carrying read entry, so a cancelled call reaches the read.
// Pre-fix the tool discarded ctx and called the ctx-less entry.
func TestExecFileReadThreadsItsContext(t *testing.T) {
	t.Parallel()
	ctx := context.WithValue(context.Background(), fileCtxKey{}, "sentinel")
	env := ctxRecordingFileEnv{readCtx: make(chan context.Context, 1)}
	if _, err := execFileRead(ctx, env, map[string]any{"file_path": "x"}, nopReadGuard()); err != nil {
		t.Fatalf("execFileRead: %v", err)
	}
	select {
	case got := <-env.readCtx:
		if got.Value(fileCtxKey{}) != "sentinel" {
			t.Fatal("read_file passed a different context to ReadFileCtx: the caller's cancellation cannot reach the read")
		}
	default:
		t.Fatal("read_file did not call the ctx-carrying read entry: the tool discarded ctx (#4611)")
	}
}

// TestExecFileEditThreadsItsContext pins the same threading for edit_file.
func TestExecFileEditThreadsItsContext(t *testing.T) {
	t.Parallel()
	ctx := context.WithValue(context.Background(), fileCtxKey{}, "sentinel")
	env := ctxRecordingFileEnv{editCtx: make(chan context.Context, 1)}
	args := map[string]any{"file_path": "x", "old_string": "a", "new_string": "b"}
	if _, err := execFileEdit(ctx, env, args, nopReadGuard()); err != nil {
		t.Fatalf("execFileEdit: %v", err)
	}
	select {
	case got := <-env.editCtx:
		if got.Value(fileCtxKey{}) != "sentinel" {
			t.Fatal("edit_file passed a different context to EditFileCtx: the caller's cancellation cannot reach the read")
		}
	default:
		t.Fatal("edit_file did not call the ctx-carrying edit entry: the tool discarded ctx (#4611)")
	}
}
