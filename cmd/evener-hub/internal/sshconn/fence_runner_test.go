package sshconn

// Tests for the fencing helper's one-shot remote runner: the stream and exit
// mapping the helper layer needs, without ssh or a host.

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"testing"

	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
)

// TestFenceCommandRunnerMapsStreamsAndExit pins the mapping: a success returns
// stdout with exit 0, and a failed remote command returns its two streams kept
// apart with ssh's forwarded exit status — never a transport error the helper
// layer cannot classify.
func TestFenceCommandRunnerMapsStreamsAndExit(t *testing.T) {
	reg, err := hostreg.New(nil)
	if err != nil {
		t.Fatalf("hostreg.New: %v", err)
	}
	host := hostreg.Host{Name: "side", SSH: "side.example"}

	success := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
		if len(argv) == 0 || argv[0] != "ssh" {
			t.Fatalf("sys argv = %v, want an ssh invocation", argv)
		}
		if got := argv[len(argv)-1]; got != "fence version" {
			t.Fatalf("remote command = %q, want %q", got, "fence version")
		}
		return []byte("1\n"), nil
	}}
	stdout, stderr, exit, err := New(reg, Options{Runner: success}).FenceCommandRunnerFor(host).Run(context.Background(), "fence version")
	if err != nil || exit != 0 || stdout != "1\n" || stderr != "" {
		t.Fatalf("success mapping = (%q, %q, %d, %v), want (\"1\\n\", \"\", 0, nil)", stdout, stderr, exit, err)
	}

	// ssh forwards the remote command's exit status unchanged; a failed run
	// arrives as RunError with the two streams kept apart. A real exit status is
	// the only way to produce the *exec.ExitError the mapping reads, so the
	// command runs locally in place of ssh.
	cause := exec.Command("sh", "-c", "exit 7").Run()
	if _, ok := errors.AsType[*exec.ExitError](cause); !ok {
		t.Fatalf("sh -c 'exit 7' produced %v, want an *exec.ExitError", cause)
	}
	failing := &fakeRunner{runFn: func(_ context.Context, _ []string, _ io.Reader) ([]byte, error) {
		return nil, &RunError{Stdout: []byte("out"), Stderr: []byte("helper refuses"), Err: cause}
	}}
	stdout, stderr, exit, err = New(reg, Options{Runner: failing}).FenceCommandRunnerFor(host).Run(context.Background(), "fence entries")
	if err != nil {
		t.Fatalf("failed-run mapping returned a transport error: %v", err)
	}
	if exit != 7 || stdout != "out" || stderr != "helper refuses" {
		t.Fatalf("failed-run mapping = (%q, %q, %d), want (\"out\", \"helper refuses\", 7)", stdout, stderr, exit)
	}

	// A failure that never ran ssh stays an error.
	plain := &fakeRunner{runFn: func(_ context.Context, _ []string, _ io.Reader) ([]byte, error) {
		return nil, errors.New("no such file")
	}}
	if _, _, _, err := New(reg, Options{Runner: plain}).FenceCommandRunnerFor(host).Run(context.Background(), "fence version"); err == nil {
		t.Fatal("a local spawn failure was mapped to a remote exit status")
	}
}
