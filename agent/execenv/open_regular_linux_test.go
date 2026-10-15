//go:build linux

package execenv

import (
	"os"
	"testing"
)

// TestOpenRegularFollowClassifiesTheDescriptorNotTheSpelling is finding 2's
// regression: the off arm decided O_NONBLOCK from the caller's pre-open
// pathname classification, so a procfs regular reached under an ordinary
// spelling — a bind mount under /tmp, or a leaf swapped between classify and
// open — lost the flag and its read could block in read(2) past both the
// deadline and cancellation. The opened descriptor's own mount must decide:
// /proc/self/status is a readable procfs regular even when the caller's
// spelled hint says it is not a stream.
func TestOpenRegularFollowClassifiesTheDescriptorNotTheSpelling(t *testing.T) {
	t.Parallel()
	if _, err := os.Stat("/proc/self/status"); err != nil {
		t.Skipf("/proc/self/status unavailable: %v", err)
	}
	f, err := openRegularFollow("/proc/self/status", false, false)
	if err != nil {
		t.Fatalf("openRegularFollow(/proc/self/status): %v", err)
	}
	defer func() { _ = f.Close() }()
	if !fdNonblocking(t, f) {
		t.Fatal("a procfs regular opened with a non-pseudo spelling lost O_NONBLOCK: its read can block past the deadline and cancellation")
	}
}
