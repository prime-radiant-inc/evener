package syncio_test

import (
	"bytes"
	"fmt"
	"sync"
	"testing"

	"primeradiant.com/evener/execsupport/syncio"
)

// TestWriterSerializesConcurrentWrites drives many goroutines writing to a
// shared bytes.Buffer (not safe for concurrent use on its own) through one
// syncio.Writer. Run with -race: this is the reproduction for the DATA RACE
// two goroutines writing an unwrapped cfg.stderr hit in evener run
// (cmd/evener/run_ask_responder.go's stderr logging vs. run.go's
// event-drain goroutine) and in the ssh runner (cmd/evener-hub/internal/
// sshconn/runner.go's diagSink vs. an attach's own stderr write).
func TestWriterSerializesConcurrentWrites(t *testing.T) {
	var buf bytes.Buffer
	w := syncio.NewWriter(&buf)

	const goroutines = 50
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := range goroutines {
		go func(i int) {
			defer wg.Done()
			line := fmt.Sprintf("line-%d\n", i)
			if _, err := w.Write([]byte(line)); err != nil {
				t.Errorf("Write: %v", err)
			}
		}(i)
	}
	wg.Wait()

	got := buf.String()
	for i := range goroutines {
		want := fmt.Sprintf("line-%d\n", i)
		if !bytes.Contains([]byte(got), []byte(want)) {
			t.Errorf("output missing %q; got %d bytes total", want, len(got))
		}
	}
	// Every write's bytes landed whole (a race or unsynchronized interleave
	// would corrupt or drop some), so the total byte count is exactly the
	// sum of what each goroutine wrote.
	wantLen := 0
	for i := range goroutines {
		wantLen += len(fmt.Sprintf("line-%d\n", i))
	}
	if len(got) != wantLen {
		t.Errorf("output length = %d, want %d", len(got), wantLen)
	}
}

// TestWriterDelegatesToUnderlyingWriter: a single write's return value and
// content pass straight through.
func TestWriterDelegatesToUnderlyingWriter(t *testing.T) {
	var buf bytes.Buffer
	w := syncio.NewWriter(&buf)
	n, err := w.Write([]byte("hello"))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if n != 5 {
		t.Fatalf("Write returned n=%d, want 5", n)
	}
	if buf.String() != "hello" {
		t.Fatalf("buf = %q, want %q", buf.String(), "hello")
	}
}
