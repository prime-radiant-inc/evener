package sshconn

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/execsupport/orphanpipe"
	"primeradiant.com/evener/execsupport/shellquote"
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
	// Run executes a one-shot command. On success it returns the command's
	// stdout alone: ssh writes benign notices (host-key additions, a login
	// message) to stderr, and a caller that parses the result must not see them.
	// On failure it returns stdout and stderr together for the diagnostic, and
	// the error is a *RunError carrying each stream separately, which is what
	// lets a caller classify the cause from ssh's own stderr rather than from a
	// concatenation the remote command's output can poison.
	Run(ctx context.Context, argv []string, stdin io.Reader) ([]byte, error)
}

// RunError reports a failed one-shot command with its two streams kept apart:
// ssh's own diagnostics (an auth refusal, an unreachable host) arrive on stderr,
// while the remote command's output arrives on stdout. A caller that classifies
// the failure reads Stderr, because the remote program's stdout can contain the
// same words for an unrelated reason.
type RunError struct {
	Stdout []byte
	Stderr []byte
	Err    error
}

func (e *RunError) Error() string { return fmt.Sprintf("run failed: %v", e.Err) }

func (e *RunError) Unwrap() error { return e.Err }

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
	if err := ctx.Err(); err != nil {
		// A caller that already gave up must not leave us a child to reap. The
		// child itself still outlives this call (see the note below); only the
		// spawn decision is bound to ctx.
		return nil, err
	}
	cmd := exec.Command(argv[0], argv[1:]...) //nolint:noctx // long-lived child must outlive ctx (see comment)
	cmd.Stderr = stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, err
	}
	return &execStdio{cmd: cmd, stdin: stdin, stdout: stdout}, nil
}

// Run executes a one-shot command with the two streams kept apart: success
// returns stdout only, so ssh's stderr chatter cannot corrupt a parse; failure
// returns both, so whatever explains the exit status is in hand.
func (execRunner) Run(ctx context.Context, argv []string, stdin io.Reader) ([]byte, error) {
	if len(argv) == 0 {
		return nil, errors.New("empty argv")
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdin = stdin
	stdout := cappedBuffer{limit: runOutputLimit}
	stderr := cappedBuffer{limit: runOutputLimit}
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.WaitDelay = runWaitDelay
	err := orphanpipe.ChildErr(cmd, cmd.Run())
	switch {
	case err != nil:
		return append(stdout.buf.Bytes(), stderr.buf.Bytes()...),
			&RunError{Stdout: stdout.buf.Bytes(), Stderr: stderr.buf.Bytes(), Err: err}
	case stdout.truncated || stderr.truncated:
		// A preflight answer is a few hundred bytes. Refusing what the cap cut
		// short is the honest option: parsing a silently truncated prefix is how a
		// host gets misread. The command itself is bounded by ctx, so a flooding
		// remote cannot outlive the attempt.
		return append(stdout.buf.Bytes(), stderr.buf.Bytes()...),
			&RunError{
				Stdout: stdout.buf.Bytes(),
				Stderr: stderr.buf.Bytes(),
				Err:    fmt.Errorf("output exceeded the %d byte limit", runOutputLimit),
			}
	}
	return stdout.buf.Bytes(), nil
}

// runWaitDelay bounds how long a one-shot command's output pipes may stay open
// after ssh exits or ctx ends. Whatever the user's ssh config starts alongside
// ssh (a ProxyCommand, a LocalCommand) inherits those pipes, and killing ssh on
// ctx expiry does not kill it, so without this bound ctx would not bound Run
// (see orphanpipe).
const runWaitDelay = time.Second

// runOutputLimit caps one stream from a one-shot command. Preflight output is a
// few hundred bytes; the cap exists so a hostile or broken remote command cannot
// make the controller buffer unbounded output before anything parses it.
const runOutputLimit = 1 << 20

// cappedBuffer accumulates at most limit bytes and remembers whether more
// arrived. Writes always report success: the child must not be blocked by our
// cap, and the caller learns about the truncation from the flag instead.
type cappedBuffer struct {
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if room := b.limit - b.buf.Len(); room > 0 {
		if len(p) <= room {
			b.buf.Write(p)
			return len(p), nil
		}
		b.buf.Write(p[:room])
	}
	if len(p) > 0 {
		b.truncated = true
	}
	return len(p), nil
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
			// A child that already exited on its own (a dropped link, a refused
			// handshake) has nothing left to signal: ErrProcessDone is the normal
			// outcome there, not a teardown failure.
			if err := s.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
				s.killErr = err
			}
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
	return strconv.Itoa(max(int(d/time.Second), 1))
}

// sshBaseArgv is the option prefix shared by every ssh invocation. -T refuses a
// pseudo-terminal even when a user's ssh_config asks for one: a PTY would rewrite
// newlines in the framed stream and fold remote diagnostics into stdout.
func sshBaseArgv(o Options) []string {
	return []string{
		"ssh",
		"-T",
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=" + sshSeconds(o.connectTimeout()),
		"-o", "ServerAliveInterval=" + sshSeconds(o.serverAliveInterval()),
		// ServerAliveCountMax is a unitless count, not a duration: render it
		// directly rather than routing it through seconds arithmetic, which can
		// overflow for a large configured value.
		"-o", "ServerAliveCountMax=" + strconv.Itoa(o.serverAliveCountMax()),
	}
}

// sshIdentityArgv is the host's SSH identity option: ["-i", key] when the entry
// carries a key path, else nothing (ssh resolves the identity the way the
// operator's ssh_config does). It is inserted before the "--" destination
// terminator like every other option, so a key path that begins with "-" is
// read as an argument to -i rather than as an ssh option.
func sshIdentityArgv(h hostreg.Host) []string {
	if p := strings.TrimSpace(h.KeyPath); p != "" {
		return []string{"-i", p}
	}
	return nil
}

// sshDest is ssh's destination argument, always placed after the "--" option
// terminator. A registry value that begins with "-" has to be read as a
// hostname and never as an ssh option: "-oProxyCommand=..." would otherwise run
// a command on the controller. ssh(1) ends option parsing at "--".
func sshDest(h hostreg.Host) []string {
	return []string{"--", composeDest(h)}
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

// rawCommandArgv builds `ssh <opts> -- <dest> <remote>` for a remote command
// that is not evener (uname, the environment probe). remote is a shell snippet
// rather than a word list, so it is the one argument passed through verbatim: a
// caller using it hands the remote shell an expression to evaluate on purpose.
func rawCommandArgv(o Options, h hostreg.Host, remote string) []string {
	argv := sshBaseArgv(o)
	argv = append(argv, sshIdentityArgv(h)...)
	argv = append(argv, sshDest(h)...)
	return append(argv, remote)
}

// evenerCommandArgv builds `ssh <opts> -- <dest> <evener> <args...>`. Every word
// is quoted for the remote login shell, because ssh joins the remote argv with
// spaces and hands the result to that shell: an evener_path with a space would
// otherwise split into two arguments, and a value carrying a metacharacter would
// be executed there instead of passed to evener.
func evenerCommandArgv(o Options, h hostreg.Host, args ...string) []string {
	argv := sshBaseArgv(o)
	argv = append(argv, sshIdentityArgv(h)...)
	argv = append(argv, sshDest(h)...)
	argv = append(argv, shellquote.RemoteWord(evenerCommand(h.EvenerPath)))
	for _, a := range args {
		argv = append(argv, shellquote.RemoteWord(a))
	}
	return argv
}

// channelArgv is the exact non-interactive bridge form:
//
//	ssh -o BatchMode=yes -o ConnectTimeout=<n> -o ServerAliveInterval=<n> \
//	    -o ServerAliveCountMax=<n> [-i <key_path>] -- <dest> <evener_path> \
//	    hub attach --stdio [--config <path>] [--addr <addr>]
//
// The optional flags carry the host's own hub.toml and the address the operator
// configured for it (per-host Addr, else Options.HubAddr). Passing --addr from
// the same resolution the restart and health probes use is what keeps a
// manager-wide HubAddr from making the bridge dial a different port than the
// probes address; with nothing configured the flag is omitted so the host
// resolves its own hub.toml address (hostreg's EvenerPath/ConfigPath/Addr docs;
// spec 04's corrected argv contract).
func channelArgv(o Options, h hostreg.Host) []string {
	args := []string{"hub", "attach", "--stdio"}
	if p := strings.TrimSpace(h.ConfigPath); p != "" {
		args = append(args, "--config", p)
	}
	if a := explicitHostAddr(o, h); a != "" {
		args = append(args, "--addr", a)
	}
	return evenerCommandArgv(o, h, args...)
}

// hubBootstrapArgv builds the argv for the ad hoc first-attach launch of a host
// hub that is not running: the resolved executable (an absolute path, so the
// launch does not depend on the non-interactive PATH), the hub subcommand, and
// the host's configured config path / address so the started hub matches the one
// the probes address. It mirrors channelArgv's optional flags.
func hubBootstrapArgv(o Options, h hostreg.Host, target string) []string {
	args := []string{target, "hub"}
	if p := strings.TrimSpace(h.ConfigPath); p != "" {
		args = append(args, "--config", p)
	}
	if a := explicitHostAddr(o, h); a != "" {
		args = append(args, "--addr", a)
	}
	return args
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

// syncWriter serializes writes to one underlying writer. The manager builds one
// over Options.Stderr and hands it to every attach: os/exec copies each ssh
// child's stderr on its own goroutine, and each attach owns a separate diagSink,
// so without a writer shared across hosts a caller-supplied sink that is not safe
// for concurrent use (bytes.Buffer, as tests use) would be corrupted or raced by
// concurrent attaches.
type syncWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func newSyncWriter(w io.Writer) *syncWriter { return &syncWriter{w: w} }

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
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
