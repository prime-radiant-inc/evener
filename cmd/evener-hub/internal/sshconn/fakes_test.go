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
	return nil
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

const goodLaunchCheck = `{"protocol":"evener-appwire-v5","version":"dev","launch_flags":["api-log"]}`

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
		case strings.Contains(joined, "launch-check"):
			return []byte(goodLaunchCheck), nil
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
