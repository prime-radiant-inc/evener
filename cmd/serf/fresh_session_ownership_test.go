//go:build darwin || linux

package main

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/provider"
	"primeradiant.com/serf/cmd/serf/internal/rvreg"
	"primeradiant.com/serf/cmdutil"
	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/rendezvous"
)

func TestRunFreshIdleSessionsOwnDistinctResumeTargets(t *testing.T) {
	stateDir := t.TempDir()
	installRunScriptedProvider(t, &scriptedProvider{name: "openai"})

	oldProcessInput := runProcessInput
	ready := make(chan string, 2)
	release := make(chan struct{})
	runProcessInput = func(sess *agent.Session, _ context.Context, _ string) (string, error) {
		ready <- sess.ID()
		<-release
		return "", context.Canceled
	}
	t.Cleanup(func() { runProcessInput = oldProcessInput })

	runOne := func(workDir string) error {
		return run(context.Background(), runConfig{
			prompt:                "wait",
			model:                 "openai/gpt-test",
			workDir:               workDir,
			stateDir:              stateDir,
			stdout:                &bytes.Buffer{},
			stderr:                &bytes.Buffer{},
			noDefaultMarketplaces: true,
		})
	}
	done := make(chan error, 2)
	firstWorkDir, secondWorkDir := t.TempDir(), t.TempDir()
	go func() { done <- runOne(firstWorkDir) }()
	go func() { done <- runOne(secondWorkDir) }()

	ids := make([]string, 0, 2)
	for len(ids) < 2 {
		select {
		case id := <-ready:
			ids = append(ids, id)
		case err := <-done:
			close(release)
			t.Fatalf("run exited before exposing its fresh session: %v", err)
		}
	}
	firstID, secondID := ids[0], ids[1]
	if firstID == secondID {
		close(release)
		<-done
		<-done
		t.Fatalf("fresh sessions reused ID %q", firstID)
	}

	reservationErrors := make([]error, 0, 2)
	for _, sessionID := range []string{firstID, secondID} {
		closeResume, err := cmdutil.AttachAPILogger(llm.NewClient(), stateDir, nil, sessionID)
		if closeResume != nil {
			if closeErr := closeResume(); closeErr != nil {
				t.Errorf("close competing resume logger for %s: %v", sessionID, closeErr)
			}
		}
		reservationErrors = append(reservationErrors, err)
	}

	close(release)
	for range 2 {
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Errorf("run result = %v, want context cancellation", err)
		}
	}
	for i, err := range reservationErrors {
		if err == nil || !strings.Contains(err.Error(), "already running") {
			t.Errorf("resume reservation %d error = %v, want already-running refusal", i, err)
		}
	}
}

func TestServeFreshIdleSessionOwnsResumeTarget(t *testing.T) {
	stateDir := t.TempDir()
	installServeScriptedProvider(t, &scriptedProvider{name: "openai"})

	deps := defaultServeDeps()
	deps.ensureConfigDirs = func() error { return nil }
	deps.seedMarketplaces = func() error { return nil }
	deps.listen = func(context.Context, string, string) (net.Listener, error) {
		return newFreshOwnerListener(), nil
	}
	deps.register = func(*rvreg.Registration, string, rendezvous.Entry) error { return nil }

	ready := make(chan string, 1)
	newSession := deps.newSession
	deps.newSession = func(client *llm.Client, profile *provider.Profile, env execenv.ExecutionEnvironment, cfg agent.SessionConfig) (*agent.Session, error) {
		sess, err := newSession(client, profile, env, cfg)
		if err == nil {
			ready <- sess.ID()
		}
		return sess, err
	}
	release := make(chan struct{})
	var cancelMu sync.Mutex
	var cancel context.CancelFunc
	deps.notifyContext = func(ctx context.Context, _ ...os.Signal) (context.Context, context.CancelFunc) {
		next, stop := context.WithCancel(ctx)
		cancelMu.Lock()
		cancel = stop
		cancelMu.Unlock()
		return next, stop
	}
	deps.serveHTTP = func(*http.Server, net.Listener) error {
		<-release
		cancelMu.Lock()
		stop := cancel
		cancelMu.Unlock()
		stop()
		return http.ErrServerClosed
	}

	done := make(chan error, 1)
	workDir, runDir := t.TempDir(), t.TempDir()
	go func() {
		done <- runServeWithDeps([]string{
			"--model", "openai/gpt-test",
			"--dir", workDir,
			"--state-dir", stateDir,
			"--run-dir", runDir,
		}, deps)
	}()

	var sessionID string
	select {
	case sessionID = <-ready:
	case err := <-done:
		close(release)
		t.Fatalf("serve exited before exposing its fresh session: %v", err)
	}
	closeResume, reservationErr := cmdutil.AttachAPILogger(llm.NewClient(), stateDir, nil, sessionID)
	if closeResume != nil {
		if err := closeResume(); err != nil {
			t.Errorf("close competing resume logger: %v", err)
		}
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("serve result = %v, want clean shutdown", err)
	}
	if reservationErr == nil || !strings.Contains(reservationErr.Error(), "already running") {
		t.Fatalf("resume reservation error = %v, want already-running refusal", reservationErr)
	}
}

type freshOwnerListener struct {
	closed chan struct{}
	once   sync.Once
}

func newFreshOwnerListener() *freshOwnerListener {
	return &freshOwnerListener{closed: make(chan struct{})}
}

func (l *freshOwnerListener) Accept() (net.Conn, error) {
	<-l.closed
	return nil, net.ErrClosed
}

func (l *freshOwnerListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}

func (*freshOwnerListener) Addr() net.Addr { return freshOwnerAddr("127.0.0.1:49132") }

type freshOwnerAddr string

func (a freshOwnerAddr) Network() string { return "tcp" }
func (a freshOwnerAddr) String() string  { return string(a) }
