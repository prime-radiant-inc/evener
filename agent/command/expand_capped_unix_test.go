//go:build unix

package command

import (
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
)

// TestReadInlineFileRefusesAFIFOWithoutBlocking pins the FIFO hazard in @file
// expansion: a writerless named pipe must degrade to the inline error marker
// instead of blocking argument substitution on its open. The bounded wait is
// the same tripwire shape as execenv's offArmReadOrFailFast; the oracle is
// structural — the refusal names the file and stays bounded — not the
// marker's wording.
func TestReadInlineFileRefusesAFIFOWithoutBlocking(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fifo := filepath.Join(dir, "pipe")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}
	env := execenv.NewLocalExecutionEnvironment(dir)

	type result struct{ inline string }
	done := make(chan result, 1)
	go func() { done <- result{readInlineFile("pipe", env)} }()
	var got string
	select {
	case res := <-done:
		got = res.inline
	case <-time.After(5 * time.Second):
		t.Fatalf("readInlineFile(FIFO) did not return (a FIFO was opened without O_NONBLOCK)")
	}
	if len(got) > maxInlineBytes {
		t.Fatalf("readInlineFile(FIFO) returned %d bytes, want a bounded marker", len(got))
	}
	if !strings.Contains(got, "pipe") {
		t.Fatalf("readInlineFile(FIFO) = %q, want the refusal to name the file", got)
	}
}
