package sshconn

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
)

// Runner is the process seam. Production is execRunner; tests inject a fake so
// the whole package is unit-testable without ssh, a network, or a host.
//
// Both calls receive a fully-built argv whose argv[0] is "ssh"; turning
// (dest, remote argv) into local ssh argv is the caller's job (channelArgv /
// evenerCommandArgv / rawCommandArgv below).
type Runner interface {
	// Start spawns a long-lived child; stderr is the caller's diagnostic sink.
	Start(ctx context.Context, argv []string, stderr io.Writer) (Stdio, error)
	// Run executes a one-shot command and returns combined stdout+stderr.
	Run(ctx context.Context, argv []string, stdin io.Reader) ([]byte, error)
}

// Stdio is the byte-stream surface of a started child.
type Stdio interface {
	Stdin() io.WriteCloser
	Stdout() io.ReadCloser
	Kill() error
	Wait() error
}

// execRunner is the production Runner.
type execRunner struct{}

// Start spawns the child without CommandContext: the channel's lifetime is the
// attach, not one request's ctx — the same reasoning spawnDaemon records
// (cmd/evener-hub/spawn.go:378-381, "NOT CommandContext: the spawned daemon
// must outlive this call's ctx"). ctx bounds this call, not the child;
// Channel.Close (or the reaper watching Wait) kills the child.
func (execRunner) Start(ctx context.Context, argv []string, stderr io.Writer) (Stdio, error) {
	if len(argv) == 0 {
		return nil, errors.New("empty argv")
	}
	cmd := exec.Command(argv[0], argv[1:]...) //nolint:noctx // long-lived child must outlive ctx (see comment)
	cmd.Stderr = stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &execStdio{cmd: cmd, stdin: stdin, stdout: stdout}, nil
}

// Run executes a one-shot command. It returns combined output so an operator
// diagnostic (ssh's stderr, launch-check's message) is available on failure.
func (execRunner) Run(ctx context.Context, argv []string, stdin io.Reader) ([]byte, error) {
	if len(argv) == 0 {
		return nil, errors.New("empty argv")
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdin = stdin
	return cmd.CombinedOutput()
}

// execStdio owns one exec.Cmd's pipes. Wait and Kill are each run at most once
// so a reaper goroutine and Channel.Close can both call them safely.
type execStdio struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser

	killOnce sync.Once
	killErr  error
	waitOnce sync.Once
	waitErr  error
}

func (s *execStdio) Stdin() io.WriteCloser { return s.stdin }
func (s *execStdio) Stdout() io.ReadCloser { return s.stdout }

func (s *execStdio) Kill() error {
	s.killOnce.Do(func() {
		if s.cmd.Process != nil {
			s.killErr = s.cmd.Process.Kill()
		}
	})
	return s.killErr
}

func (s *execStdio) Wait() error {
	s.waitOnce.Do(func() { s.waitErr = s.cmd.Wait() })
	return s.waitErr
}

// Default ssh options. BatchMode and a connect timeout make the channel
// strictly non-interactive; ServerAlive* is the only keepalive, because
// StreamTransport deliberately does not implement appwire.Pinger
// (appwire/stream_transport.go:17-21).
const (
	defaultConnectTimeout      = 10 * time.Second
	defaultServerAliveInterval = 15 * time.Second
	defaultServerAliveCountMax = 3
)

func (o Options) connectTimeout() time.Duration {
	if o.ConnectTimeout > 0 {
		return o.ConnectTimeout
	}
	return defaultConnectTimeout
}

func (o Options) serverAliveInterval() time.Duration {
	if o.ServerAliveInterval > 0 {
		return o.ServerAliveInterval
	}
	return defaultServerAliveInterval
}

func (o Options) serverAliveCountMax() int {
	if o.ServerAliveCountMax > 0 {
		return o.ServerAliveCountMax
	}
	return defaultServerAliveCountMax
}

// sshSeconds renders a duration as whole seconds for an ssh -o option, never
// below one: ssh rejects "0" for ConnectTimeout/ServerAliveInterval.
func sshSeconds(d time.Duration) string {
	s := int(d / time.Second)
	if s < 1 {
		s = 1
	}
	return strconv.Itoa(s)
}

// sshBaseArgv is the option prefix shared by every ssh invocation.
func sshBaseArgv(o Options) []string {
	return []string{
		"ssh",
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=" + sshSeconds(o.connectTimeout()),
		"-o", "ServerAliveInterval=" + sshSeconds(o.serverAliveInterval()),
		"-o", "ServerAliveCountMax=" + sshSeconds(time.Duration(o.serverAliveCountMax())*time.Second),
	}
}

// composeDest turns a registry host into ssh's destination argument. A host
// reaching here normally has no "@" when User is set (hostreg rejects the
// ambiguous pair at add time with ErrAmbiguousSSHUser), but the composition is
// still guarded so a direct caller cannot produce "user@user@host".
func composeDest(h hostreg.Host) string {
	ssh := strings.TrimSpace(h.SSH)
	if h.User == "" || strings.ContainsRune(ssh, '@') {
		return ssh
	}
	return h.User + "@" + ssh
}

// evenerCommand is the remote evener invocation: the registry's evener_path, or
// the literal "evener" to resolve on the remote PATH when it is empty.
func evenerCommand(path string) string {
	if p := strings.TrimSpace(path); p != "" {
		return p
	}
	return "evener"
}

// rawCommandArgv builds `ssh <opts> <dest> <remote>` for a remote command that
// is not evener (uname, the environment probe).
func rawCommandArgv(o Options, h hostreg.Host, remote string) []string {
	argv := sshBaseArgv(o)
	return append(argv, composeDest(h), remote)
}

// evenerCommandArgv builds `ssh <opts> <dest> <evener> <args...>`.
func evenerCommandArgv(o Options, h hostreg.Host, args ...string) []string {
	argv := sshBaseArgv(o)
	argv = append(argv, composeDest(h), evenerCommand(h.EvenerPath))
	return append(argv, args...)
}

// channelArgv is the exact non-interactive bridge form:
//
//	ssh -o BatchMode=yes -o ConnectTimeout=<n> -o ServerAliveInterval=<n> \
//	    -o ServerAliveCountMax=<n> <dest> <evener_path> hub attach --stdio
func channelArgv(o Options, h hostreg.Host) []string {
	return evenerCommandArgv(o, h, "hub", "attach", "--stdio")
}

// diagSink forwards ssh stderr to the configured diagnostic writer and keeps a
// bounded tail so a failed attach can classify the cause (auth vs a generic
// start failure) without buffering unbounded output.
type diagSink struct {
	mu    sync.Mutex
	w     io.Writer
	buf   []byte
	limit int
}

const diagTailLimit = 8 * 1024

func newDiagSink(w io.Writer) *diagSink {
	return &diagSink{w: w, limit: diagTailLimit}
}

func (d *diagSink) Write(p []byte) (int, error) {
	d.mu.Lock()
	d.buf = appendTail(d.buf, p, d.limit)
	w := d.w
	d.mu.Unlock()
	if w != nil {
		_, _ = w.Write(p)
	}
	return len(p), nil
}

func (d *diagSink) tail() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return string(d.buf)
}

// appendTail appends p to buf while retaining at most limit trailing bytes.
func appendTail(buf, p []byte, limit int) []byte {
	if limit <= 0 {
		return nil
	}
	if len(p) >= limit {
		return append(buf[:0], p[len(p)-limit:]...)
	}
	if extra := len(buf) + len(p) - limit; extra > 0 {
		copy(buf, buf[extra:])
		buf = buf[:len(buf)-extra]
	}
	return append(buf, p...)
}

// tail renders a bounded, trimmed diagnostic string from combined output.
func tail(out []byte) string {
	if len(out) > diagTailLimit {
		out = out[len(out)-diagTailLimit:]
	}
	return strings.TrimSpace(string(out))
}
