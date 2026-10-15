package execenv

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBufferedCommandCaptureIsBounded pins the exec-side half of the single
// capture sink: a buffered command whose stdout runs past the cap keeps its
// head and tail around an omission marker and reports StdoutTruncated, so an
// output firehose cannot grow the capture with the stream (#4557). The rg arm
// consumes exactly this flag to discard a corrupted capture.
func TestBufferedCommandCaptureIsBounded(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	content := strings.Repeat("A", 20000-19) + "\nTAIL-MARKER-12345\n"
	flood := filepath.Join(dir, "flood.bin")
	if err := os.WriteFile(flood, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "flood")
	writeExecFixture(t, script, "#!/bin/sh\nexec cat "+shellQuotePath(flood)+"\n")

	capBytes := 4096
	env := NewLocalExecutionEnvironment(dir)
	defer env.Cleanup()
	env.outputCaptureCap = &capBytes
	res, err := env.ExecArgv(context.Background(), script, nil, 10_000, dir, nil)
	if err != nil {
		t.Fatalf("ExecArgv: %v", err)
	}
	if !res.StdoutTruncated {
		t.Fatalf("stdout of %d bytes under a %d-byte cap reported untruncated", len(content), capBytes)
	}
	if len(res.Stdout) > capBytes+len(fmt.Sprintf(outputOmissionMarker, 0))+16 {
		t.Fatalf("capped stdout = %d bytes, want at most the %d-byte cap plus the marker", len(res.Stdout), capBytes)
	}
	if !strings.Contains(res.Stdout, "bytes omitted") {
		t.Fatalf("capped stdout = %q, want an omission marker", res.Stdout)
	}
	if !strings.HasSuffix(res.Stdout, "TAIL-MARKER-12345\n") {
		t.Fatalf("capped stdout lost the tail; ends with %q", lastChars(res.Stdout, 32))
	}
	if res.StderrTruncated {
		t.Fatal("stderr reported truncated on an empty stream")
	}
}

// lastChars is a bounded tail for failure messages.
func lastChars(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
