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
	capture, err := loginShellPATHOutput(ctx, fixture, "-lc", "echo $PATH")
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if !capture.truncated {
		t.Fatalf("a %d-line flood did not report truncation", floodLines)
	}
	if len(capture.view) > probeOutputCaptureCap+64 {
		t.Fatalf("probe captured %d bytes of a %d-line flood, want at most the %d-byte probe cap plus the marker", len(capture.view), floodLines, probeOutputCaptureCap)
	}
	if !strings.Contains(string(capture.view), "bytes omitted") {
		t.Fatalf("probe capture of a %d-line flood lacks the omission marker", floodLines)
	}
	if !strings.HasSuffix(string(capture.view), pathLine+"\n") {
		t.Fatalf("probe capture lost the PATH line; ends with %q", probeTailForPrint(capture.view))
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

	capture, err := osVersionOutput(context.Background(), floodFixture, "-rs")
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if !capture.truncated {
		t.Fatalf("a %d-line flood did not report truncation", floodLines)
	}
	if len(capture.view) > probeOutputCaptureCap+64 {
		t.Fatalf("probe captured %d bytes of a %d-line flood, want at most the %d-byte probe cap plus the marker", len(capture.view), floodLines, probeOutputCaptureCap)
	}
	if !strings.Contains(string(capture.view), "bytes omitted") {
		t.Fatalf("probe capture of a %d-line flood lacks the omission marker", floodLines)
	}
	if !strings.HasSuffix(string(capture.view), versionLine+"\n") {
		t.Fatalf("probe capture lost the version line; ends with %q", probeTailForPrint(capture.view))
	}

	capture, err = osVersionOutput(context.Background(), shortFixture, "-rs")
	if err != nil {
		t.Fatalf("short probe: %v", err)
	}
	if capture.truncated {
		t.Fatalf("short probe reported truncation on a %d-byte emission", len(capture.view))
	}
	if string(capture.view) != versionLine+"\n" {
		t.Fatalf("short probe capture = %q, want the exact %d-byte emission", string(capture.view), len(versionLine)+1)
	}
}

// probeTailForPrint returns the last few bytes of a capture for a failure
// message.
func probeTailForPrint(out []byte) string {
	return string(out[max(0, len(out)-40):])
}

// TestLoginShellPATHRefusesCutFragment pins the round-2 finding: when the
// capture truncated and the real PATH line is longer than the retained tail
// window, lastNonEmptyLine sees only the line's suffix with no newline before
// it. The resolver must refuse that fragment rather than cache a truncated
// PATH (directories silently vanish; the first entry can even become relative).
// The refusal reuses the existing empty-return path, which means "fall back to
// the inherited PATH".
func TestLoginShellPATHRefusesCutFragment(t *testing.T) {
	probeLongTailGuards(t)
	const floodLines = 4000
	fixture := filepath.Join(t.TempDir(), "flood-longtail-shell")
	probeLongTailFixture(t, fixture, floodLines)
	t.Setenv("SHELL", fixture)
	if got := resolveLoginShellPATH(); got != "" {
		t.Fatalf("resolveLoginShellPATH() = %q (%d bytes), want the cut fragment refused as \"\"", probeTailForPrint([]byte(got)), len(got))
	}
}

// probeLongTailFixture floods past the probe cap and then emits one line longer
// than the retained tail window, so the window holds only that line's suffix
// with no newline before it — the cut-fragment shape both probes must refuse.
func probeLongTailFixture(t *testing.T, path string, floodLines int) {
	t.Helper()
	script := "#!/bin/sh\n" +
		"yes 'FLOOD-FILLER-LINE-0000000000000000000000' | head -n " + strconv.Itoa(floodLines) + "\n" +
		"printf '%s\\n' \"$(head -c " + strconv.Itoa(probeOutputCaptureCap) + " /dev/zero | tr '\\0' 'x')\"\n"
	writeExecFixture(t, path, script)
}

// TestLoginShellPATHUnderCapIsByteExact keeps the under-cap path unchanged: a
// short answer is not truncated, reaches the resolver whole, and is returned
// byte for byte.
func TestLoginShellPATHUnderCapIsByteExact(t *testing.T) {
	probeOutputFixtureGuards(t)
	const pathLine = "/probe-cap/bin:/usr/local/bin:/usr/bin:/bin"
	fixture := filepath.Join(t.TempDir(), "short-shell")
	probeOutputFixture(t, fixture, pathLine, 0)

	capture, err := loginShellPATHOutput(context.Background(), fixture, "-lc", "echo $PATH")
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if capture.truncated {
		t.Fatalf("short probe reported truncation on a %d-byte emission", len(capture.view))
	}
	if string(capture.view) != pathLine+"\n" {
		t.Fatalf("short probe capture = %q, want the exact %d-byte emission", string(capture.view), len(pathLine)+1)
	}
	t.Setenv("SHELL", fixture)
	if got := resolveLoginShellPATH(); got != pathLine {
		t.Fatalf("resolveLoginShellPATH() = %q, want the exact PATH line %q", got, pathLine)
	}
}

// TestOSVersionRefusesCutFragment applies the same guard to the OS probe's
// consumer: a banner line longer than the retained tail window reaches the
// consumer as a fragment and must be refused (the caller then falls back to the
// GOOS/GOARCH default) rather than returned as the version.
func TestOSVersionRefusesCutFragment(t *testing.T) {
	probeLongTailGuards(t)
	fixture := filepath.Join(t.TempDir(), "flood-longtail-uname")
	probeLongTailFixture(t, fixture, 4000)

	capture, err := osVersionOutput(context.Background(), fixture, "-rs")
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if !capture.truncated {
		t.Fatalf("a long-tail flood did not report truncation")
	}
	if version, ok := probeTrustedAnswer(capture); ok {
		t.Fatalf("cut fragment of %d bytes trusted as version %q, want it refused", len(capture.tail), probeTailForPrint([]byte(version)))
	}
}

// TestOSVersionTruncatedButCompleteStillAccepted pins the other half: when the
// truncation did not cut the answer's start — the normal version line is a
// later line of the retained tail window — the answer is trusted exactly as
// before, for both a truncated and an untruncated capture.
func TestOSVersionTruncatedButCompleteStillAccepted(t *testing.T) {
	probeOutputFixtureGuards(t)
	const versionLine = "FakeOS 9.9-probe #1 SMP PREEMPT"
	dir := t.TempDir()
	floodFixture := filepath.Join(dir, "flood-uname")
	probeOutputFixture(t, floodFixture, versionLine, 4000)
	shortFixture := filepath.Join(dir, "short-uname")
	probeOutputFixture(t, shortFixture, versionLine, 0)

	capture, err := osVersionOutput(context.Background(), floodFixture, "-rs")
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	version, ok := probeTrustedAnswer(capture)
	if !ok || version != versionLine {
		t.Fatalf("truncated-but-complete answer = %q (ok=%v), want %q", version, ok, versionLine)
	}

	capture, err = osVersionOutput(context.Background(), shortFixture, "-rs")
	if err != nil {
		t.Fatalf("short probe: %v", err)
	}
	version, ok = probeTrustedAnswer(capture)
	if !ok || version != versionLine {
		t.Fatalf("under-cap answer = %q (ok=%v), want %q", version, ok, versionLine)
	}
}

// probeLongTailGuards skips when the long-tail fixture's tools are unavailable.
func probeLongTailGuards(t *testing.T) {
	t.Helper()
	for _, tool := range []string{"sh", "yes", "head", "tr"} {
		if _, err := execLookPath(tool); err != nil {
			t.Skip(tool + " not available")
		}
	}
}

// TestProbeTrustedAnswerTailShapes pins the trust decision structurally, on the
// authoritative windows rather than any rendered text: an answer that is the
// retained tail's first line (its start may be cut) is refused even when a later
// blank line contributes a newline, while a line preceded by a retained newline,
// or a head-prefix line under a blank tail, is trusted. The view carries
// marker-shaped text that must never be consulted.
func TestProbeTrustedAnswerTailShapes(t *testing.T) {
	t.Parallel()
	const fakeMarker = "head\n[... 5 bytes omitted ...]\n"
	cases := []struct {
		name      string
		head      []byte
		tail      []byte
		truncated bool
		view      []byte
		want      string
		wantOK    bool
	}{
		{"cut suffix then blank line", nil, []byte("suffix\n \n"), true, []byte(fakeMarker + "suffix\n \n"), "", false},
		{"cut suffix then crlf blank", nil, []byte("suffix\r\n \r\n"), true, []byte(fakeMarker + "suffix\r\n \r\n"), "", false},
		{"cut suffix no newline", nil, []byte("suffix"), true, []byte(fakeMarker + "suffix"), "", false},
		{"answer after a retained newline", nil, []byte("\nanswer\n"), true, []byte(fakeMarker + "\nanswer\n"), "answer", true},
		{"answer not the tail's first line", nil, []byte("frag\nanswer\n"), true, []byte(fakeMarker + "frag\nanswer\n"), "answer", true},
		{"blank tail falls back to head", []byte("banner\nANSWER\n"), []byte("\n \n"), true, []byte("banner\nANSWER\n[... 5 bytes omitted ...]\n\n \n"), "ANSWER", true},
		{"empty tail falls back to head", []byte("banner\nANSWER\n"), []byte(""), true, []byte("banner\nANSWER\n[... 5 bytes omitted ...]"), "ANSWER", true},
		{"marker-shaped view text does not rescue a cut suffix", nil, []byte("suffix\n"), true, []byte("[... 123 bytes omitted ...]\n[... 5 bytes omitted ...]\nsuffix\n"), "", false},
		{"under cap uses the view", nil, nil, false, []byte("only\nANSWER\n"), "ANSWER", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := probeTrustedAnswer(probeCapture{view: tc.view, head: tc.head, tail: tc.tail, truncated: tc.truncated})
			if ok != tc.wantOK || got != tc.want {
				t.Fatalf("probeTrustedAnswer = (%q, %v), want (%q, %v)", got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

// TestLoginShellPATHRefusesCutFragmentWithTrailingBlankLine pins round 3's
// first finding: the guard must ask whether a newline precedes the SELECTED
// answer's start, not whether the tail contains a newline anywhere. A cut PATH
// suffix followed by a whitespace-only line must still be refused.
func TestLoginShellPATHRefusesCutFragmentWithTrailingBlankLine(t *testing.T) {
	probeLongTailGuards(t)
	for _, tc := range []struct{ name, trailing string }{
		{"newline blank", "\\n \\n"},
		{"crlf blank", "\\r\\n \\r\\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := filepath.Join(t.TempDir(), "flood-cut-then-blank")
			script := "#!/bin/sh\n" +
				"yes 'FLOOD-FILLER-LINE-0000000000000000000000' | head -n 4000\n" +
				// The cut PATH suffix, with NO newline of its own...
				"printf '%s' \"$(head -c " + strconv.Itoa(probeOutputCaptureCap) + " /dev/zero | tr '\\0' 'x')\"\n" +
				// ... then a whitespace-only line, the newline the old guard saw.
				"printf '" + tc.trailing + "'\n"
			writeExecFixture(t, fixture, script)
			t.Setenv("SHELL", fixture)
			if got := resolveLoginShellPATH(); got != "" {
				t.Fatalf("resolveLoginShellPATH() = %q (%d bytes), want the cut fragment refused as \"\" despite the trailing blank line", probeTailForPrint([]byte(got)), len(got))
			}
		})
	}
}

// TestLoginShellPATHRefusesCutFragmentAfterMarkerShapedRCText pins round 3's
// second finding: marker-shaped text in the rc output must not move the guard's
// reference point. The old view-parsing cut at the rc's own "[... 123 bytes
// omitted ...]" and mis-located the tail, trusting the cut answer.
func TestLoginShellPATHRefusesCutFragmentAfterMarkerShapedRCText(t *testing.T) {
	probeLongTailGuards(t)
	fixture := filepath.Join(t.TempDir(), "marker-shaped-rc")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' 'RC-BANNER'\n" +
		"printf '%s\\n' '[... 123 bytes omitted ...]'\n" +
		"yes 'FLOOD-FILLER-LINE-0000000000000000000000' | head -n 4000\n" +
		"printf '%s' \"$(head -c " + strconv.Itoa(probeOutputCaptureCap) + " /dev/zero | tr '\\0' 'x')\"\n"
	writeExecFixture(t, fixture, script)
	t.Setenv("SHELL", fixture)
	if got := resolveLoginShellPATH(); got != "" {
		t.Fatalf("resolveLoginShellPATH() = %q (%d bytes), want the cut fragment refused as \"\" despite the marker-shaped rc text", probeTailForPrint([]byte(got)), len(got))
	}
}

// TestLoginShellPATHMarkerShapedTextUnderCapAccepted is the other half of the
// marker-shaped case: without truncation the fake marker is ordinary text and
// the probe's answer is returned normally.
func TestLoginShellPATHMarkerShapedTextUnderCapAccepted(t *testing.T) {
	probeOutputFixtureGuards(t)
	const pathLine = "/probe-cap/bin:/usr/local/bin:/usr/bin:/bin"
	fixture := filepath.Join(t.TempDir(), "marker-shaped-under-cap")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' '[... 123 bytes omitted ...]'\n" +
		"printf '%s\\n' '" + pathLine + "'\n"
	writeExecFixture(t, fixture, script)
	t.Setenv("SHELL", fixture)
	if got := resolveLoginShellPATH(); got != pathLine {
		t.Fatalf("resolveLoginShellPATH() = %q, want the PATH line %q under the cap", got, pathLine)
	}
}

// TestLoginShellPATHEmptyRetainedTailAcceptsHeadLine pins round 3's improvement
// over round 2's conservative refusal: when the whole retained tail is blank,
// the last non-empty line sits in the byte-exact head prefix (its start is
// visible), so the answer is trusted rather than dropped.
func TestLoginShellPATHEmptyRetainedTailAcceptsHeadLine(t *testing.T) {
	probeOutputFixtureGuards(t)
	const pathLine = "/probe-cap/bin:/usr/local/bin:/usr/bin:/bin"
	fixture := filepath.Join(t.TempDir(), "head-answer-blank-tail")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' 'filler-line'\n" +
		"printf '%s\\n' '" + pathLine + "'\n" +
		// 70000 newlines: the stream trips the 64KiB cap and the last 32KiB
		// tail window holds no non-empty line, so the answer is in the head.
		"yes '' | head -n 70000\n"
	writeExecFixture(t, fixture, script)
	t.Setenv("SHELL", fixture)
	if got := resolveLoginShellPATH(); got != pathLine {
		t.Fatalf("resolveLoginShellPATH() = %q, want the head's PATH line %q to survive a blank retained tail", got, pathLine)
	}
}
