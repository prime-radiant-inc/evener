package sshconn

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
)

// fakeRunner records every call and delegates to injected functions. Production
// execRunner is deliberately excluded from the default unit tests.
type fakeRunner struct {
	mu      sync.Mutex
	runs    [][]string
	starts  [][]string
	runFn   func(ctx context.Context, argv []string, stdin io.Reader) ([]byte, error)
	startFn func(ctx context.Context, argv []string, stderr io.Writer) (Stdio, error)
}

func (f *fakeRunner) Run(ctx context.Context, argv []string, stdin io.Reader) ([]byte, error) {
	f.mu.Lock()
	f.runs = append(f.runs, append([]string(nil), argv...))
	f.mu.Unlock()
	if f.runFn == nil {
		return nil, fmt.Errorf("fakeRunner: unexpected Run %v", argv)
	}
	return f.runFn(ctx, argv, stdin)
}

func (f *fakeRunner) Start(ctx context.Context, argv []string, stderr io.Writer) (Stdio, error) {
	f.mu.Lock()
	f.starts = append(f.starts, append([]string(nil), argv...))
	f.mu.Unlock()
	if f.startFn == nil {
		return nil, errors.New("fakeRunner: no startFn")
	}
	return f.startFn(ctx, argv, stderr)
}

func (f *fakeRunner) recordedRuns() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]string(nil), f.runs...)
}

func (f *fakeRunner) recordedStarts() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]string(nil), f.starts...)
}

// fakeStdio is an in-memory Stdio. Stdout is fed by a fakeBridge server; Stdin
// is drained by it.
type fakeStdio struct {
	inW  *io.PipeWriter
	outR *io.PipeReader
	inR  *io.PipeReader
	outW *io.PipeWriter

	// waitErr is what Wait reports, standing in for the child's exit status: a
	// *exec.ExitError with ssh's own 255 is what makes an attach handshake's auth
	// refusal terminal (see exitStatus).
	waitErr  error
	killOnce sync.Once
	waitDone chan struct{}
}

func (s *fakeStdio) Stdin() io.WriteCloser { return s.inW }
func (s *fakeStdio) Stdout() io.ReadCloser { return s.outR }

func (s *fakeStdio) Kill() error {
	s.killOnce.Do(func() {
		_ = s.inW.Close()
		_ = s.outR.Close()
		_ = s.outW.Close()
		_ = s.inR.Close()
		close(s.waitDone)
	})
	return nil
}

func (s *fakeStdio) Wait() error {
	<-s.waitDone
	return s.waitErr
}

// drop simulates the SSH link dying without a deliberate Close: the server stops
// writing, so the controller's read loop sees EOF.
func (s *fakeStdio) drop() {
	_ = s.outW.Close()
	_ = s.inR.Close()
}

// fakeBridge is a minimal in-process AppWire server behind a fakeStdio. It
// answers initialize so Manager.Ensure can complete a real handshake.
type fakeBridge struct {
	stdio        *fakeStdio
	initProtocol string
}

func newFakeBridge(initProtocol string) *fakeBridge {
	inR, inW := io.Pipe()   // controller writes stdin -> server reads
	outR, outW := io.Pipe() // server writes stdout -> controller reads
	b := &fakeBridge{
		stdio:        &fakeStdio{inW: inW, outR: outR, inR: inR, outW: outW, waitDone: make(chan struct{})},
		initProtocol: initProtocol,
	}
	go b.serve(inR, outW)
	return b
}

// newSilentBridge models a hub whose AppWire handshake never answers: it drains
// the framed request stream and never writes a response, so a client's
// Initialize can only end at its own deadline.
func newSilentBridge() *fakeBridge {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	b := &fakeBridge{
		stdio: &fakeStdio{inW: inW, outR: outR, inR: inR, outW: outW, waitDone: make(chan struct{})},
	}
	go func() {
		buf := make([]byte, 4096)
		for {
			if _, err := inR.Read(buf); err != nil {
				return
			}
		}
	}()
	return b
}

// exitStatus returns a real *exec.ExitError carrying code, the fixture for a
// child that exited with that status. ssh(1) exits 255 when ssh itself fails
// (a refused connection, an authentication refusal) and otherwise reports the
// remote command's own status.
func exitStatus(t *testing.T, code int) error {
	t.Helper()
	err := exec.Command("sh", "-c", "exit "+strconv.Itoa(code)).Run()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != code {
		t.Fatalf("building an exit-status-%d fixture: %v", code, err)
	}
	return err
}

func (b *fakeBridge) serve(inR *io.PipeReader, outW *io.PipeWriter) {
	srv := appwire.NewStreamTransport(&stdioReadWriter{in: outW, out: inR})
	for {
		msg, err := srv.Recv(context.Background())
		if err != nil {
			return
		}
		if msg.Request != nil && msg.Request.Method == appwire.MethodInitialize {
			resp := appwire.InitializeResponse{ProtocolVersion: b.initProtocol, SourceID: "local"}
			if err := srv.Send(context.Background(), appwire.ResponseMessage(msg.Request.ID, resp)); err != nil {
				return
			}
		}
	}
}

// floodNotifications writes n AppWire notification frames to the bridge's
// stdout, so a client that never drains its bounded notification buffer overflows.
// An error means the stream broke first (the client tears it down on overflow);
// the caller only cares that the frames it did write arrived, so it is reported
// rather than fatal.
func (b *fakeBridge) floodNotifications(n int) error {
	srv := appwire.NewStreamTransport(&stdioReadWriter{in: b.stdio.outW, out: b.stdio.inR})
	for i := range n {
		if err := srv.Send(context.Background(), appwire.NotificationMessage("noop", map[string]int{"seq": i})); err != nil {
			return err
		}
	}
	return nil
}

const goodLaunchCheck = `{"protocol":"evener-appwire-v6","version":"dev","launch_flags":["api-log"]}`

// cannedRun returns a runFn answering the standard preflight commands, with
// optional substring-keyed overrides consulted first.
func cannedRun(overrides map[string][]byte) func(context.Context, []string, io.Reader) ([]byte, error) {
	return func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
		joined := strings.Join(argv, " ")
		for key, out := range overrides {
			if strings.Contains(joined, key) {
				return append([]byte(nil), out...), nil
			}
		}
		switch {
		case strings.HasSuffix(joined, "uname -s"):
			return []byte("Linux\n"), nil
		case strings.HasSuffix(joined, "uname -m"):
			return []byte("x86_64\n"), nil
		case strings.Contains(joined, "XDG_STATE_HOME"):
			return []byte("HOME=/home/dev\nXDG_STATE_HOME=\nXDG_CONFIG_HOME=\n"), nil
		case strings.HasSuffix(joined, "id -u"):
			return []byte("1000\n"), nil
		case strings.Contains(joined, "launch-check"):
			return []byte(goodLaunchCheck), nil
		case strings.Contains(joined, "api/health"):
			// A running hub matching the on-disk build, so Ensure attaches
			// without deploying, restarting, or taking the first-attach
			// bootstrap start. Tests that need "nothing answered" use their own
			// runFn or a health override.
			return []byte(`{"version":"dev","mobile_api_version":1,"hub_addr":"127.0.0.1:9180"}`), nil
		default:
			return nil, fmt.Errorf("unexpected remote command: %v", argv)
		}
	}
}

func testRegistry(t *testing.T, hosts ...hostreg.Host) *hostreg.Registry {
	t.Helper()
	reg, err := hostreg.New(hosts)
	if err != nil {
		t.Fatalf("hostreg.New: %v", err)
	}
	return reg
}

// writeStageBinary is a BuildBinary seam that writes a stand-in binary to the
// staging path the deploy opened.
func writeStageBinary(_ context.Context, _, _, out string) error {
	return os.WriteFile(out, []byte("staged-binary"), 0o755)
}

// deployRunner answers the whole ensure sequence for a linux host whose evener is
// installed at /opt/evener/bin/evener: preflight, deploy target resolution and
// push, a systemd restart, and /api/health. launch answers successive
// launch-check calls by index (the last answer repeats), and health answers
// successive health probes.
func deployRunner(t *testing.T, launch func(call int) ([]byte, error), health func(call int) ([]byte, error)) *fakeRunner {
	t.Helper()
	launchCalls, healthCalls := 0, 0
	return &fakeRunner{
		runFn: func(_ context.Context, argv []string, stdin io.Reader) ([]byte, error) {
			joined := strings.Join(argv, " ")
			switch {
			case strings.HasSuffix(joined, "uname -s"):
				return []byte("Linux\n"), nil
			case strings.HasSuffix(joined, "uname -m"):
				return []byte("x86_64\n"), nil
			case strings.Contains(joined, "XDG_STATE_HOME"):
				return []byte("HOME=/home/dev\nXDG_STATE_HOME=\nXDG_CONFIG_HOME=\n"), nil
			case strings.HasSuffix(joined, "id -u"):
				return []byte("1000\n"), nil
			case strings.Contains(joined, "launch-check"):
				out, err := launch(launchCalls)
				launchCalls++
				return out, err
			case strings.Contains(joined, "test -d /opt/evener/bin"):
				return nil, nil
			case strings.Contains(joined, "evener_resolve"):
				return []byte("/opt/evener/bin/evener\n"), nil
			case strings.Contains(joined, "list-units"):
				return []byte("evener-hub.service loaded active running Evener Hub\n"), nil
			case strings.Contains(joined, "systemctl restart"):
				return nil, nil
			case strings.Contains(joined, "api/health"):
				out, err := health(healthCalls)
				healthCalls++
				return out, err
			case strings.Contains(joined, "cat >"):
				if stdin != nil {
					_, _ = io.ReadAll(stdin)
				}
				return nil, nil
			default:
				return nil, fmt.Errorf("unexpected remote command: %v", argv)
			}
		},
		startFn: goodStartFn(t),
	}
}
