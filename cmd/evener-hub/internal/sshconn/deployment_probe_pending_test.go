package sshconn

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/cmd/evener-hub/internal/hostops"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
)

func TestRetainedDeploymentExecutableProbeIsBounded(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener"}
	entered := make(chan bool, 1)
	var probeErr error
	base := cannedRun(map[string][]byte{"launch-check": []byte("{")})
	fr := &fakeRunner{runFn: func(ctx context.Context, argv []string, stdin io.Reader) ([]byte, error) {
		if strings.HasSuffix(strings.Join(argv, " "), "if test -x /opt/evener; then exit 0; else exit 1; fi") {
			_, bounded := ctx.Deadline()
			entered <- bounded
			<-ctx.Done()
			probeErr = ctx.Err()
			return nil, probeErr
		}
		return base(ctx, argv, stdin)
	}}
	m := newTestManager(t, testRegistry(t, host), fr, Options{attemptTimeout: 20 * time.Millisecond})
	pending := pendingRestartState{deployment: true, selected: &selectedOwner{host: host}}
	m.pendingRestarts[host.Name] = pending
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := m.Ensure(ctx, host.Name); done <- err }()
	var bounded bool
	select {
	case bounded = <-entered:
	case err := <-done:
		t.Fatalf("Ensure did not reach retained executable probe: %v", err)
	case <-time.After(time.Second):
		cancel()
		<-done
		t.Fatal("Ensure did not reach retained executable probe")
	}
	select {
	case err := <-done:
		if !errors.Is(err, ErrPreflightDecode) {
			t.Fatalf("Ensure = %v, want preflight decode refusal", err)
		}
	case <-time.After(time.Second):
		cancel()
		<-done
		t.Fatal("deadline-free Ensure held the host gate past the probe budget")
	}
	if !bounded || !errors.Is(probeErr, context.DeadlineExceeded) {
		t.Fatalf("probe bounded=%v error=%v", bounded, probeErr)
	}
	if !reflect.DeepEqual(m.pendingRestart(host.Name), pending) || m.Attached(host.Name) {
		t.Fatal("probe failure changed retained intent or attached")
	}
	release, err := m.TryAcquire(host.Name, hostops.Holder{Kind: hostops.HolderPlan})
	if err != nil {
		t.Fatalf("later plan could not acquire released host gate: %v", err)
	}
	release()
}
