package execenv

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// probeOutputFixture writes an executable standing in for a login shell or an
// uname that floods probeOutputCaptureCap's worth of output and then prints
// the short line the probe exists to extract. The flood is bounded (well under
// a MiB), not the 512MiB a hostile rc file could emit: enough to trip the probe
// cap the fix installs, cheap enough for the default suite. It is line-oriented
// — whole lines, like a real rc-chain banner — so the line the probe extracts
// is a line of its own rather than glued to a mid-line cut.
func probeOutputFixture(t *testing.T, path, finalLine string, floodLines int) {
	t.Helper()
	script := "#!/bin/sh\n"
	if floodLines > 0 {
		script += "yes 'FLOOD-FILLER-LINE-0000000000000000000000' | head -n " + strconv.Itoa(floodLines) + "\n"
	}
	script += "printf '%s\\n' '" + finalLine + "'\n"
	writeExecFixture(t, path, script)
}

func probeOutputFixtureGuards(t *testing.T) {
	t.Helper()
	for _, tool := range []string{"sh", "yes", "head"} {
		if _, err := execLookPath(tool); err != nil {
			t.Skip(tool + " not available")
		}
	}
}

// TestLoginShellPATHOutputCapturesBounded reproduces #4557's class on the
// login-shell probe: cmd.Output() captured the rc chain's emissions unbounded
// (a 512MiB rc emission measured 2576MiB peak RSS while returning 13 bytes),
// and the probe's 2s timeout bounds time, not memory — at session launch, on
// the user's real login shell. The capture must stay bounded while the probe
// still extracts the short PATH line it exists for.
func TestLoginShellPATHOutputCapturesBounded(t *testing.T) {
	probeOutputFixtureGuards(t)
	const pathLine = "/probe-cap/bin:/usr/local/bin:/usr/bin:/bin"
	const floodLines = 4000 // 4000 × 41-byte lines ≈ 160KiB, far past the 64KiB probe cap
	fixture := filepath.Join(t.TempDir(), "flood-shell")
	probeOutputFixture(t, fixture, pathLine, floodLines)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := loginShellPATHOutput(ctx, fixture, "-lc", "echo $PATH")
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if len(out) > probeOutputCaptureCap+64 {
		t.Fatalf("probe captured %d bytes of a %d-line flood, want at most the %d-byte probe cap plus the marker", len(out), floodLines, probeOutputCaptureCap)
	}
	if !strings.Contains(string(out), "bytes omitted") {
		t.Fatalf("probe capture of a %d-line flood lacks the omission marker", floodLines)
	}
	if !strings.HasSuffix(string(out), pathLine+"\n") {
		t.Fatalf("probe capture lost the PATH line; ends with %q", probeTailForPrint(out))
	}

	t.Setenv("SHELL", fixture)
	if got := resolveLoginShellPATH(); got != pathLine {
		t.Fatalf("resolveLoginShellPATH() = %q, want the PATH line %q to survive the flood", got, pathLine)
	}
}

// TestOSVersionOutputCapturesBounded pins the same bound on the OS probe: its
// output is one short line, but the capture was the same unbounded
// cmd.Output() shape, and the cap must not change what the probe returns for
// the short emission it actually expects.
func TestOSVersionOutputCapturesBounded(t *testing.T) {
	probeOutputFixtureGuards(t)
	const versionLine = "FakeOS 9.9-probe #1 SMP PREEMPT"
	const floodLines = 4000
	dir := t.TempDir()
	floodFixture := filepath.Join(dir, "flood-uname")
	probeOutputFixture(t, floodFixture, versionLine, floodLines)
	shortFixture := filepath.Join(dir, "short-uname")
	probeOutputFixture(t, shortFixture, versionLine, 0)

	out, err := osVersionOutput(context.Background(), floodFixture, "-rs")
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if len(out) > probeOutputCaptureCap+64 {
		t.Fatalf("probe captured %d bytes of a %d-line flood, want at most the %d-byte probe cap plus the marker", len(out), floodLines, probeOutputCaptureCap)
	}
	if !strings.Contains(string(out), "bytes omitted") {
		t.Fatalf("probe capture of a %d-line flood lacks the omission marker", floodLines)
	}
	if !strings.HasSuffix(string(out), versionLine+"\n") {
		t.Fatalf("probe capture lost the version line; ends with %q", probeTailForPrint(out))
	}

	out, err = osVersionOutput(context.Background(), shortFixture, "-rs")
	if err != nil {
		t.Fatalf("short probe: %v", err)
	}
	if string(out) != versionLine+"\n" {
		t.Fatalf("short probe capture = %q, want the exact %d-byte emission", string(out), len(versionLine)+1)
	}
}

// probeTailForPrint returns the last few bytes of a capture for a failure
// message.
func probeTailForPrint(out []byte) string {
	return string(out[max(0, len(out)-40):])
}
