package execenv

// Ported scenarios from the parked PR #4568 branch (commits 556d751cb5,
// e974047618, 9408a02d35) that main's capped_output_test.go and
// command_capture_cap_test.go do not already cover. Each was adapted to main's
// API: main's cappedOutputBuffer exposes truncated()/String() and the raw
// buf/head/tail windows, so the assertions read those instead of the dropped
// branch's retainedBytes()/omittedBytes() accessors. The omitted count is
// checked against main's own outputOmissionMarker format, matching main's
// existing capped_output_test.go convention.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

// cappedTestEnv returns an environment whose buffered-command capture cap is
// capBytes, the injectable seam production code reads per command.
func cappedTestEnv(t *testing.T, root string, capBytes int) *LocalExecutionEnvironment {
	t.Helper()
	env := NewLocalExecutionEnvironment(root)
	env.outputCaptureCap = &capBytes
	return env
}

// retainedBufBytes is the memory a cappedOutputBuffer holds right now: the
// under-cap buffer while the stream fits, else the frozen head plus the tail
// window. Mirrors main's own memory-bound assertion.
func retainedBufBytes(b *cappedOutputBuffer) int {
	return len(b.buf) + len(b.head) + len(b.tail)
}

// TestCappedOutputBufferCutEdgesNeverSplitARune pins the rune trims on both cut
// edges, which main's capped_output_test.go does not exercise: a cap whose head
// cut lands mid-rune and whose tail cut lands on the rune's other half must
// drop the straddling bytes rather than render a replacement character, and the
// omission count must still be every byte not present.
func TestCappedOutputBufferCutEdgesNeverSplitARune(t *testing.T) {
	t.Parallel()
	const capBytes = 103
	// 150 two-byte runes then a one-byte rune: the head cut at byte 51 lands
	// on the first half of a rune and the tail cut at byte 249 on its second
	// half, so both trimmed edges drop exactly one byte.
	content := strings.Repeat("é", 150) + "x" // 301 bytes
	buf := newCappedOutputBuffer(capBytes)
	if _, err := buf.Write([]byte(content)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if !buf.truncated() {
		t.Fatalf("capture of %d bytes at cap %d reports untruncated", len(content), capBytes)
	}
	got := buf.String()
	if !utf8.ValidString(got) {
		t.Fatalf("capture is not valid UTF-8 after the rune trims: %q", got)
	}
	if !strings.HasPrefix(got, strings.Repeat("é", 25)) {
		t.Fatalf("capture lost the trimmed head window; starts with %q", got[:min(len(got), 12)])
	}
	if !strings.HasSuffix(got, strings.Repeat("é", 25)+"x") {
		t.Fatalf("capture lost the trimmed tail window; ends with %q", got[max(0, len(got)-12):])
	}
	if want := fmt.Sprintf(outputOmissionMarker, 200); !strings.Contains(got, want) {
		t.Fatalf("capture = %q, want the two trimmed edges' 200 bytes reported omitted", got)
	}
}

// TestCappedOutputBufferInvalidUTF8StaysBoundedAndUntrimmable covers the fuzz
// seed shapes with bytes that are not valid UTF-8 at all: the trims must not
// panic, and the capture must still honour the cap.
func TestCappedOutputBufferInvalidUTF8StaysBoundedAndUntrimmable(t *testing.T) {
	t.Parallel()
	const capBytes = 8
	buf := newCappedOutputBuffer(capBytes)
	if _, err := buf.Write(bytes.Repeat([]byte{0x80}, 20)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if _, err := buf.Write([]byte("x")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if !buf.truncated() {
		t.Fatal("a 21-byte stream under an 8-byte cap reports untruncated")
	}
	if got := retainedBufBytes(buf); got > capBytes {
		t.Fatalf("retained %d bytes at cap %d, want the capture to never exceed the cap", got, capBytes)
	}
	if got := len(buf.String()); got > capBytes+len(fmt.Sprintf(outputOmissionMarker, 0)) {
		t.Fatalf("capture rendered %d bytes, want at most the cap plus the marker", got)
	}
}

// TestCappedOutputBufferWritePatternIndependence pins ring order and tail-window
// seeding across chunk boundaries: any chunking of the same stream must render
// exactly the single-write capture. Main's memory test writes only uniform
// 16-byte chunks; this covers the boundary-adjacent chunk sizes where a
// mis-ordered ring or mis-seeded tail shows up.
func TestCappedOutputBufferWritePatternIndependence(t *testing.T) {
	t.Parallel()
	const capBytes = 4096
	const total = 100 * 1024
	content := strings.Repeat("0123456789", total/10)
	ref := newCappedOutputBuffer(capBytes)
	if _, err := ref.Write([]byte(content)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	want := ref.String()
	if !ref.truncated() {
		t.Fatalf("capture of %d bytes at cap %d reports untruncated", total, capBytes)
	}
	if got := retainedBufBytes(ref); got != capBytes {
		t.Fatalf("retained %d bytes after a %d-byte stream, want exactly the %d-byte cap", got, total, capBytes)
	}
	if wantMarker := fmt.Sprintf(outputOmissionMarker, total-capBytes); !strings.Contains(want, wantMarker) {
		t.Fatalf("reference capture lacks the %d-omitted marker", total-capBytes)
	}
	for _, chunkLen := range []int{1, 7, 2047, 4095, 4097, 12288, capBytes / 2} {
		t.Run(fmt.Sprintf("chunks of %d", chunkLen), func(t *testing.T) {
			t.Parallel()
			buf := newCappedOutputBuffer(capBytes)
			for off := 0; off < total; off += chunkLen {
				end := min(off+chunkLen, total)
				if _, err := buf.Write([]byte(content[off:end])); err != nil {
					t.Fatalf("Write: %v", err)
				}
			}
			if got := buf.String(); got != want {
				t.Fatalf("capture in chunks of %d = %d bytes, want the single-write capture's %d", chunkLen, len(got), len(want))
			}
		})
	}
}

// TestCappedOutputBufferExactlyAtCapIsByteIdentical pins the boundary main's
// under-cap test (a tiny stream well under the cap) does not: a stream of
// exactly cap bytes is not truncated and passes through byte for byte.
func TestCappedOutputBufferExactlyAtCapIsByteIdentical(t *testing.T) {
	t.Parallel()
	const capBytes = 100
	content := strings.Repeat("Z", capBytes)
	buf := newCappedOutputBuffer(capBytes)
	if _, err := buf.Write([]byte(content)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if buf.truncated() {
		t.Fatal("a stream of exactly cap bytes reports truncated")
	}
	if got := buf.String(); got != content {
		t.Fatalf("capture at exactly the cap = %d bytes, want the exact %d-byte stream", len(got), capBytes)
	}
	if got := retainedBufBytes(buf); got != capBytes {
		t.Fatalf("retained %d bytes, want the whole %d-byte stream", got, capBytes)
	}

	// One byte more trips the cap with exactly one byte omitted.
	buf = newCappedOutputBuffer(capBytes)
	if _, err := buf.Write([]byte(content + "Y")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if !buf.truncated() {
		t.Fatal("a stream one byte past the cap reports untruncated")
	}
	if want := fmt.Sprintf(outputOmissionMarker, 1); !strings.Contains(buf.String(), want) {
		t.Fatalf("capture = %q, want the single byte past the cap reported omitted", buf.String())
	}
}

// TestCappedOutputBufferNonPositiveCapFallsBackToProductionDefault pins the
// fallback main's newCappedOutputBuffer implements but does not test: a
// non-positive cap keeps the bounded production default instead of silently
// removing the bound.
func TestCappedOutputBufferNonPositiveCapFallsBackToProductionDefault(t *testing.T) {
	t.Parallel()
	for _, capBytes := range []int{0, -1} {
		t.Run(fmt.Sprintf("cap %d", capBytes), func(t *testing.T) {
			t.Parallel()
			buf := newCappedOutputBuffer(capBytes)
			overflow := make([]byte, maxCapturedOutputBytes+1)
			if _, err := buf.Write(overflow); err != nil {
				t.Fatalf("Write: %v", err)
			}
			if !buf.truncated() {
				t.Fatalf("a %d-byte stream past the %d-byte production cap reports untruncated", len(overflow), maxCapturedOutputBytes)
			}
			if got := retainedBufBytes(buf); got != maxCapturedOutputBytes {
				t.Fatalf("retained %d bytes, want the %d-byte production cap", got, maxCapturedOutputBytes)
			}
			if want := fmt.Sprintf(outputOmissionMarker, 1); !strings.Contains(buf.String(), want) {
				t.Fatalf("capture lacks the single-omitted-byte marker for the production cap")
			}
		})
	}
}

// TestOutputCaptureCapSeamDefaultsAndRidesWorkingDirectory pins the seam
// contract and its WithWorkingDirectory propagation, which main landed (via
// #4621) without a test: the production default is 8MiB, a set seam overrides
// it, a non-positive seam falls back, and the re-rooting clone carries it.
func TestOutputCaptureCapSeamDefaultsAndRidesWorkingDirectory(t *testing.T) {
	t.Parallel()
	env := NewLocalExecutionEnvironment(t.TempDir())
	if got := env.outputCaptureCapBytes(); got != maxCapturedOutputBytes {
		t.Fatalf("default capture cap = %d, want the %d-byte production default", got, maxCapturedOutputBytes)
	}
	small := 4096
	env.outputCaptureCap = &small
	if got := env.outputCaptureCapBytes(); got != small {
		t.Fatalf("capture cap = %d, want the seam's %d", got, small)
	}
	for _, invalid := range []int{0, -1} {
		env.outputCaptureCap = &invalid
		if got := env.outputCaptureCapBytes(); got != maxCapturedOutputBytes {
			t.Fatalf("capture cap for seam %d = %d, want the production default", invalid, got)
		}
	}
	env.outputCaptureCap = &small
	child := env.WithWorkingDirectory(t.TempDir())
	if got := child.outputCaptureCapBytes(); got != small {
		t.Fatalf("re-rooted child capture cap = %d, want the parent's %d", got, small)
	}
}

// endlessWriterRuntime is the scripted stand-in for a firehose command that
// never exits on its own: Start floods the captured stdout with more than the
// cap in one deterministic in-process write — no dependence on how fast a real
// writer fills the pipe, the flake `yes` brought to loaded runners — and Wait
// blocks until the runtime is signalled, the way a real endless writer runs
// until the timeout kills it.
type endlessWriterRuntime struct {
	config     commandRuntimeConfig
	terminated chan struct{}
	stop       sync.Once
}

func newEndlessWriterRuntime() *endlessWriterRuntime {
	return &endlessWriterRuntime{terminated: make(chan struct{})}
}

func (c *endlessWriterRuntime) Args() []string                        { return []string{"endless-writer"} }
func (c *endlessWriterRuntime) Configure(config commandRuntimeConfig) { c.config = config }
func (c *endlessWriterRuntime) Start() error {
	_, _ = c.config.Stdout.Write(bytes.Repeat([]byte("ENDLESS-WRITE-"), 200_000)) // ~2.8MiB, far past any test cap
	return nil
}
func (c *endlessWriterRuntime) Wait() error {
	<-c.terminated
	return nil
}
func (c *endlessWriterRuntime) PID() int                   { return 0 }
func (c *endlessWriterRuntime) ExitCode(error) (int, bool) { return 0, false }
func (c *endlessWriterRuntime) Terminate() {
	c.stop.Do(func() { close(c.terminated) })
}
func (c *endlessWriterRuntime) Kill() { c.Terminate() }

// endlessWriterFactory routes both command shapes to the endless writer.
type endlessWriterFactory struct{}

func (endlessWriterFactory) Shell(string) commandRuntime { return newEndlessWriterRuntime() }
func (endlessWriterFactory) Argv(string, ...string) commandRuntime {
	return newEndlessWriterRuntime()
}

// TestExecArgv_InfiniteOutputStaysBoundedUntilTheTimeoutKillsIt is the scripted
// replacement for the parked branch's real-`yes` version: it proves the capture
// stays bounded and flagged while an endless writer runs past the 100ms timeout,
// with no dependence on writer speed. Main has no timeout-with-cap test.
func TestExecArgv_InfiniteOutputStaysBoundedUntilTheTimeoutKillsIt(t *testing.T) {
	const capBytes = 64 * 1024
	env := cappedTestEnv(t, t.TempDir(), capBytes)
	env.commandFactory = endlessWriterFactory{}

	start := time.Now()
	res, err := env.ExecArgv(context.Background(), "endless-writer", nil, 100, "", nil)
	dur := time.Since(start)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
	if !res.TimedOut || res.ExitCode != 124 {
		t.Fatalf("res = %+v, want the timeout kill (timed_out, exit 124)", res)
	}
	if dur > 3*time.Second {
		t.Fatalf("timeout handling took %s, want the 100ms timeout plus teardown", dur)
	}
	if !res.StdoutTruncated {
		t.Fatalf("stdout capture is %d bytes without the truncated flag, want the cap to have tripped", len(res.Stdout))
	}
	if len(res.Stdout) > capBytes+64 {
		t.Fatalf("stdout capture is %d bytes, want at most the %d-byte cap plus the marker", len(res.Stdout), capBytes)
	}
	if !strings.HasPrefix(res.Stdout, "ENDLESS-WRITE-") {
		t.Fatalf("stdout capture lost the head of the stream; starts with %q", res.Stdout[:min(len(res.Stdout), 16)])
	}
}

// TestExecCommand_CapsStderrIndependentlyOfStdout pins the per-stream bound main
// does not test: a stdout-only command keeps an exact stdout while a stderr
// firehose is capped and flagged on its own.
func TestExecCommand_CapsStderrIndependentlyOfStdout(t *testing.T) {
	if _, err := execLookPath("head"); err != nil {
		t.Skip("head not available")
	}
	const capBytes = 64 * 1024
	const firehose = 67108864 // 64MiB, far past the cap
	env := cappedTestEnv(t, t.TempDir(), capBytes)

	res, err := env.ExecCommand(context.Background(),
		"printf %s 'OUT-ONLY'; head -c "+strconv.Itoa(firehose)+" /dev/zero 1>&2",
		10_000, "", nil)
	if err != nil {
		t.Fatalf("ExecCommand: %v (res=%+v)", err, res)
	}
	if res.Stdout != "OUT-ONLY" {
		t.Fatalf("stdout = %q, want the untouched %d-byte stream", res.Stdout, len("OUT-ONLY"))
	}
	if res.StdoutTruncated {
		t.Fatal("stdout was reported truncated, want the untouched stream")
	}
	if !res.StderrTruncated {
		t.Fatalf("stderr capture of a %d-byte stream reports untruncated", firehose)
	}
	if len(res.Stderr) <= capBytes || len(res.Stderr) > capBytes+64 {
		t.Fatalf("stderr capture is %d bytes, want the %d-byte cap plus a bounded marker between the windows", len(res.Stderr), capBytes)
	}
}

// TestExecCommand_UnderCapPassesThroughWhole pins the end-to-end under-cap path
// main does not test: a command whose output fits the cap reaches the caller
// byte for byte and reports untruncated.
func TestExecCommand_UnderCapPassesThroughWhole(t *testing.T) {
	if _, err := execLookPath("head"); err != nil {
		t.Skip("head not available")
	}
	const capBytes = 256 * 1024
	const headMark = "U-HEAD-9k"
	const tailMark = "-U-TAIL4z"
	nullLen := 200_000
	env := cappedTestEnv(t, t.TempDir(), capBytes)

	res, err := env.ExecCommand(context.Background(),
		"printf %s '"+headMark+"'; head -c "+strconv.Itoa(nullLen)+" /dev/zero; printf %s '"+tailMark+"'",
		10_000, "", nil)
	if err != nil {
		t.Fatalf("ExecCommand: %v (res=%+v)", err, res)
	}
	want := headMark + strings.Repeat("\x00", nullLen) + tailMark
	if res.Stdout != want {
		t.Fatalf("under-cap capture is %d bytes, want the exact %d-byte stream byte for byte", len(res.Stdout), len(want))
	}
	if res.StdoutTruncated {
		t.Fatalf("under-cap capture of %d bytes at cap %d reports truncated", len(want), capBytes)
	}
	if res.TimedOut || res.ExitCode != 0 {
		t.Fatalf("command did not complete on its own: %+v", res)
	}
}
