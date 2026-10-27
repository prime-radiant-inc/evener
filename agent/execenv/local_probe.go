package execenv

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"primeradiant.com/evener/agent/sandbox"
	"primeradiant.com/evener/envvars"
	"primeradiant.com/evener/execsupport/orphanpipe"
)

// Platform returns the operating system family as one of "darwin", "windows",
// or "linux" (the default for any other GOOS).
func (e *LocalExecutionEnvironment) Platform() string {
	switch runtimeGOOS {
	case "darwin":
		return "darwin"
	case "windows":
		return "windows"
	default:
		return "linux"
	}
}

var (
	osVersionOnce  sync.Once
	osVersionValue string
)

// OSVersion returns the OS version string. On darwin and linux it shells out to
// "uname -rs"; on windows it runs "ver". If the command fails it falls back to
// "GOOS/GOARCH". The host OS version is constant for the process, so it is
// resolved once and shared rather than forking a subprocess per session.
func (e *LocalExecutionEnvironment) OSVersion() string {
	osVersionOnce.Do(func() { osVersionValue = resolveOSVersion() })
	return osVersionValue
}

// execLookPath, execCommandContext, and execCommand are the exec entry points
// execenv shells out through. Production binds them to the real os/exec
// functions; a test swaps them to force the ripgrep-present/absent branch or
// an OS-probe failure without depending on the host's tools. Byte-identical
// to calling exec.* directly. execCommand (not execCommandContext) backs the
// Argv command-runtime factory specifically: it must not carry its own
// ctx-triggered kill, which would race execPreparedCommand's process-group
// termination — see systemCommandRuntimeFactory.Argv's doc comment.
var (
	execLookPath       = exec.LookPath
	execCommandContext = exec.CommandContext
	execCommand        = exec.Command
	runtimeGOOS        = runtime.GOOS
	osVersionOutput    = func(ctx context.Context, name string, args ...string) (probeCapture, error) {
		cmd := execCommandContext(ctx, name, args...)
		out := newCappedOutputBuffer(probeOutputCaptureCap)
		cmd.Stdout = out
		err := cmd.Run()
		return newProbeCapture(out), err
	}
	shellStat              = os.Stat
	grepReadFile           = grepReadFileCapped
	grepWalk               = fs.WalkDir
	listReadDir            = os.ReadDir
	streamBeforeSignalOnce = func(func()) {}
	streamAfterTimer       = func(func()) {}
	streamOutputCopyStart  = func() {}
	wrapperWithPolicy      = func(w *sandbox.Wrapper, p sandbox.ResolvedPolicy) (*sandbox.Wrapper, error) { return w.WithPolicy(p) }
	splitEditLines         = strings.Split
	venvCandidateDirs      = func(root, binDir string) []string {
		return []string{filepath.Join(root, ".venv", binDir), filepath.Join(root, "venv", binDir)}
	}
	loginShellPATHOutput = func(ctx context.Context, name string, args ...string) (probeCapture, error) {
		cmd := execCommandContext(ctx, name, args...)
		cmd.WaitDelay = loginShellPATHWaitDelay
		out := newCappedOutputBuffer(probeOutputCaptureCap)
		cmd.Stdout = out
		err := cmd.Run()
		return newProbeCapture(out), orphanpipe.ChildErr(cmd, err)
	}
)

var (
	loginShellPATHOnce  sync.Once
	loginShellPATHValue string
)

// loginShellPATHTimeout bounds the login-shell probe so a wedged or slow rc
// file can never block session/daemon launch — a short timeout, never a hard
// failure surfaced to the user.
const loginShellPATHTimeout = 2 * time.Second

// loginShellPATHWaitDelay bounds how long the probe's stdout may stay open
// after the shell exits or loginShellPATHTimeout ends. A job the rc chain
// backgrounds inherits that pipe and is not killed with the shell, so without
// this bound the timeout would not bound the probe (see orphanpipe).
const loginShellPATHWaitDelay = time.Second

// probeOutputCaptureCap bounds the stdout a launch probe captures. The probes
// extract one short line — the login shell's PATH, a uname banner — but the
// login-shell probe runs the USER'S rc chain, whose emissions the probe
// timeouts bound only in time: a runaway rc file printing at pipe speed OOMs
// the agent exactly like an uncapped command capture (#4557). 64KiB is orders
// of magnitude past any real PATH or banner, so the bound never trips on a
// well-formed probe answer.
const probeOutputCaptureCap = 64 * 1024

// LoginShellPATH resolves the PATH the user's login shell would produce
// ($SHELL -lc 'echo $PATH'), so a session or daemon launched from a context
// that skips the shell rc chain (macOS launchd, a GUI app, systemd) can still
// give spawned commands the developer's real PATH — including entries like
// /opt/homebrew/bin that only the rc chain adds (kata 31gh). Resolved once
// per process and cached, exactly like OSVersion: a wedged or slow rc file
// costs at most one short-timeout probe, never one per session or per spawn.
// Returns "" when $SHELL is unset, the probe fails, it times out, or the
// capture truncated across the answer's start (a cut fragment) — callers then
// leave the inherited process PATH unchanged.
func LoginShellPATH() string {
	loginShellPATHOnce.Do(func() { loginShellPATHValue = resolveLoginShellPATH() })
	return loginShellPATHValue
}

func resolveLoginShellPATH() string {
	shell := strings.TrimSpace(os.Getenv(envvars.Shell.Name))
	if shell == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), loginShellPATHTimeout)
	defer cancel()
	capture, err := loginShellPATHOutput(ctx, shell, "-lc", "echo $"+envvars.Path.Name)
	if err != nil {
		return ""
	}
	answer, ok := probeTrustedAnswer(capture)
	if !ok {
		return ""
	}
	return answer
}

// lastNonEmptyLine returns the last non-blank line of s, trimmed. An rc file
// that prints a banner (nvm/conda version notices, a MOTD-style .zshrc echo)
// before the login-shell probe's own "echo $PATH" would otherwise prepend
// that banner text as garbage into the resolved PATH; the probe's own output
// is always the LAST thing the shell prints, so taking the last line skips
// any such noise.
func lastNonEmptyLine(s string) string {
	lines := strings.Split(s, "\n")
	for _, v := range slices.Backward(lines) {
		if line := strings.TrimSpace(v); line != "" {
			return line
		}
	}
	return ""
}

// probeCapture is a launch probe's captured output: the rendered view (the exact
// stream under the cap, else head + omission marker + tail), the buffer's
// authoritative retained tail window when the cap tripped, and whether it
// tripped.
type probeCapture struct {
	view      []byte
	tail      []byte
	truncated bool
}

// newProbeCapture snapshots a probe's capture. The trust decision reads the
// authoritative head and tail windows directly rather than parsing the rendered
// omission marker, so marker-shaped text in the command's own output cannot
// move the guard's reference point.
func newProbeCapture(b *cappedOutputBuffer) probeCapture {
	c := probeCapture{view: []byte(b.String()), truncated: b.truncated()}
	if c.truncated {
		c.tail = b.tailView()
	}
	return c
}

// lastNonEmptyLineAt returns the last non-blank line of b, trimmed, and the byte
// offset in b where that line starts. ok is false when b has no non-blank line.
func lastNonEmptyLineAt(b []byte) (line string, start int, ok bool) {
	start = -1
	for off := 0; off < len(b); {
		nl := bytes.IndexByte(b[off:], '\n')
		end := len(b)
		if nl >= 0 {
			end = off + nl
		}
		if trimmed := strings.TrimSpace(string(b[off:end])); trimmed != "" {
			line, start = trimmed, off
		}
		if nl < 0 {
			break
		}
		off = end + 1
	}
	if start < 0 {
		return "", 0, false
	}
	return line, start, true
}

// probeTrustedAnswer extracts a probe's answer line and reports whether it is
// trustworthy. Under the cap the whole stream is present, so the last non-blank
// line is returned exactly as before.
//
// A truncated capture is different: the cap may have fallen across the answer's
// start, leaving only its suffix in the retained tail window. When the tail has
// any non-blank line, the tail is the stream's end, so its last non-blank line
// is the view's last non-blank line — trusted only when a newline precedes its
// start (it is not the tail's first line, whose beginning may be cut). When the
// whole retained tail is blank nothing is trusted: the omitted middle may hold
// the stream's real last line, and the byte-exact head can end midway through
// a line, so its last line is a fragment — the caller falls back to the
// inherited PATH rather than cache either.
func probeTrustedAnswer(c probeCapture) (string, bool) {
	if !c.truncated {
		return lastNonEmptyLine(string(c.view)), true
	}
	if answer, start, ok := lastNonEmptyLineAt(c.tail); ok {
		if start > 0 {
			return answer, true
		}
		return "", false
	}
	return "", false
}

func resolveOSVersion() string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	switch runtimeGOOS {
	case "darwin", "linux":
		capture, err := osVersionOutput(ctx, "uname", "-rs")
		if err == nil {
			if version, ok := probeTrustedAnswer(capture); ok {
				return version
			}
		}
	case "windows":
		capture, err := osVersionOutput(ctx, "cmd", "/c", "ver")
		if err == nil {
			if version, ok := probeTrustedAnswer(capture); ok {
				return version
			}
		}
	}
	return runtimeGOOS + "/" + runtime.GOARCH // fallback
}
