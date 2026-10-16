package execenv

import (
	"fmt"
	"testing"

	"primeradiant.com/evener/agent/sandbox"
)

// TestCappedOutputBufferKeepsHeadAndTailAroundTheMarker pins the single capture
// sink's shape: the exact stream at or under the cap, else the first and last
// windows around an omission marker that counts every byte it does not return.
func TestCappedOutputBufferKeepsHeadAndTailAroundTheMarker(t *testing.T) {
	t.Parallel()
	exact := []byte("short stream")
	b := newCappedOutputBuffer(64)
	if _, err := b.Write(exact); err != nil {
		t.Fatal(err)
	}
	if b.truncated() {
		t.Fatal("under-cap capture reports truncated")
	}
	if got := b.String(); got != string(exact) {
		t.Fatalf("under-cap capture = %q, want the exact stream", got)
	}

	// A 4-byte cap on a 10-byte stream keeps the first two and the last two
	// bytes around a marker disclosing the six it dropped.
	b = newCappedOutputBuffer(4)
	if _, err := b.Write([]byte("0123456789")); err != nil {
		t.Fatal(err)
	}
	if !b.truncated() {
		t.Fatal("past-cap capture reports untruncated")
	}
	want := "01" + fmt.Sprintf(outputOmissionMarker, 6) + "89"
	if got := b.String(); got != want {
		t.Fatalf("past-cap capture = %q, want %q", got, want)
	}
}

// TestCappedOutputBufferBoundsMemoryPastTheCap pins the memory bound: however
// much streams through, the capture holds the cap plus the marker, never the
// stream. Chunked writes exercise the rolling tail window across boundaries.
func TestCappedOutputBufferBoundsMemoryPastTheCap(t *testing.T) {
	t.Parallel()
	const capBytes = 64
	b := newCappedOutputBuffer(capBytes)
	chunk := []byte("0123456789abcdef") // 16 bytes
	for range 1000 {                    // 16000 bytes total
		if _, err := b.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}
	if !b.truncated() {
		t.Fatal("a 16000-byte stream under a 64-byte cap reports untruncated")
	}
	if len(b.buf)+len(b.head)+len(b.tail) > capBytes {
		t.Fatalf("capture retained %d bytes, want at most the %d-byte cap", len(b.buf)+len(b.head)+len(b.tail), capBytes)
	}
	if got := b.String(); len(got) > capBytes+len(fmt.Sprintf(outputOmissionMarker, 0))+16 {
		t.Fatalf("capped capture rendered %d bytes, want the retained windows around the marker", len(got))
	}
}

func TestOutputCaptureCapRidesSandboxInvocationGrant(t *testing.T) {
	t.Parallel()
	small := 4096
	env := &LocalExecutionEnvironment{
		RootDir:          t.TempDir(),
		Sandbox:          &sandbox.ResolvedPolicy{Mode: sandbox.ModeWorkspaceWrite},
		outputCaptureCap: &small,
	}
	granted, ok := env.WithSandboxInvocationGrant("/granted/path").(*LocalExecutionEnvironment)
	if !ok {
		t.Fatalf("WithSandboxInvocationGrant returned %T, want *LocalExecutionEnvironment", granted)
	}
	if got := granted.outputCaptureCapBytes(); got != small {
		t.Fatalf("granted clone capture cap = %d, want the parent's %d", got, small)
	}
}
