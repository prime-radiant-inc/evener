package daemonprocess

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

// The fake replaces only OS inspection and signaling, so PID reuse and foreign
// ownership can be tested without touching any ambient process.
type kernelProcess struct {
	facts      identity
	snapshots  []identity
	signals    int
	closed     bool
	gone       bool
	signalErr  error
	inspectErr error
}

func (k *kernelProcess) inspect(Target) (identity, error) {
	if k.inspectErr != nil {
		return identity{}, k.inspectErr
	}
	if len(k.snapshots) > 0 {
		v := k.snapshots[0]
		k.snapshots = k.snapshots[1:]
		return v, nil
	}
	return k.facts, nil
}
func (k *kernelProcess) kill() error           { k.signals++; return k.signalErr }
func (k *kernelProcess) exited() (bool, error) { return k.gone, nil }
func (k *kernelProcess) close() error          { k.closed = true; return nil }
func validTarget() Target {
	return Target{PID: os.Getpid() + 100, SessionID: "session", StateDir: "/private/state", StartedAt: time.Unix(200, 0)}
}
func validIdentity() identity {
	return identity{generation: "generation-a", uid: os.Getuid(), startedAt: time.Unix(100, 0), argv: []string{"evener", "serve"}, ownsLog: true}
}
func testController(k *kernelProcess) controller {
	return controller{bind: func(int) (processHandle, error) { return k, nil }}
}

func TestOpenRefusesUnsafeTargets(t *testing.T) {
	tests := []struct {
		name   string
		change func(*Target)
	}{
		{"zero pid", func(v *Target) { v.PID = 0 }}, {"negative pid", func(v *Target) { v.PID = -1 }},
		{"overflow pid", func(v *Target) { alias := int64(1) << 32; v.PID = int(alias) + os.Getpid() }}, {"self pid", func(v *Target) { v.PID = os.Getpid() }},
		{"missing session", func(v *Target) { v.SessionID = "" }}, {"escaping session", func(v *Target) { v.SessionID = "../other" }}, {"missing state", func(v *Target) { v.StateDir = "" }}, {"relative state", func(v *Target) { v.StateDir = "relative" }}, {"missing start", func(v *Target) { v.StartedAt = time.Time{} }},
		{"future rendezvous", func(v *Target) { v.StartedAt = time.Now().Add(time.Hour) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := validTarget()
			tt.change(&v)
			k := &kernelProcess{facts: validIdentity()}
			p, err := testController(k).Open(v)
			if err == nil {
				_ = p.Close()
				t.Fatal("unsafe target accepted")
			}
			if k.signals != 0 {
				t.Fatal("signaled unsafe target")
			}
		})
	}
}
func TestOpenRefusesUnverifiedIdentity(t *testing.T) {
	tests := []struct {
		name   string
		change func(*identity)
	}{
		{"wrong owner", func(v *identity) { v.uid++ }}, {"wrong command", func(v *identity) { v.argv = []string{"evener", "hub", "serve"} }}, {"missing argv", func(v *identity) { v.argv = nil }}, {"missing log ownership", func(v *identity) { v.ownsLog = false }}, {"newer start", func(v *identity) { v.startedAt = time.Unix(201, 0) }}, {"missing generation", func(v *identity) { v.generation = "" }}, {"missing start", func(v *identity) { v.startedAt = time.Time{} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			k := &kernelProcess{facts: validIdentity()}
			tt.change(&k.facts)
			p, err := testController(k).Open(validTarget())
			if err == nil {
				_ = p.Close()
				t.Fatal("unverified identity accepted")
			}
			if !k.closed || k.signals != 0 {
				t.Fatal("refused process leaked or signaled")
			}
		})
	}
}
func TestOpenRefusesGenerationChange(t *testing.T) {
	a := validIdentity()
	b := a
	b.generation = "generation-b"
	k := &kernelProcess{facts: b, snapshots: []identity{a, b}}
	p, err := testController(k).Open(validTarget())
	if err == nil {
		_ = p.Close()
		t.Fatal("reused process accepted")
	}
	if k.signals != 0 {
		t.Fatal("reused process signaled")
	}
}
func TestKillRechecksIdentity(t *testing.T) {
	for _, change := range []string{"generation", "owner", "lock"} {
		t.Run(change, func(t *testing.T) {
			k := &kernelProcess{facts: validIdentity()}
			p, err := testController(k).Open(validTarget())
			if err != nil {
				t.Fatal(err)
			}
			defer p.Close()
			switch change {
			case "generation":
				k.facts.generation = "other"
			case "owner":
				k.facts.uid++
			case "lock":
				k.facts.ownsLog = false
			}
			if err := p.Kill(); err == nil {
				t.Fatal("kill accepted changed identity")
			}
			if k.signals != 0 {
				t.Fatal("unsafe signal sent")
			}
		})
	}
}
func TestKillAndConfirmedExit(t *testing.T) {
	k := &kernelProcess{facts: validIdentity()}
	p, err := testController(k).Open(validTarget())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if err := p.Kill(); err != nil {
		t.Fatal(err)
	}
	if k.signals != 1 {
		t.Fatal("verified process not signaled")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := p.Wait(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("unconfirmed exit: %v", err)
	}
	k.gone = true
	if err := p.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
}
func TestKillExitedIsIdempotent(t *testing.T) {
	k := &kernelProcess{facts: validIdentity()}
	p, err := testController(k).Open(validTarget())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	k.inspectErr = ErrExited
	if err := p.Kill(); err != nil {
		t.Fatal(err)
	}
	if k.signals != 0 {
		t.Fatal("signaled absent process")
	}
}
func TestClosedProcessRefusesUse(t *testing.T) {
	k := &kernelProcess{facts: validIdentity()}
	p, err := testController(k).Open(validTarget())
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if err := p.Kill(); err == nil {
		t.Fatal("closed process signaled")
	}
	if err := p.Wait(context.Background()); err == nil {
		t.Fatal("closed process treated as exited")
	}
}

func TestInspectionFailureAfterExitIsIdempotent(t *testing.T) {
	k := &kernelProcess{facts: validIdentity()}
	p, err := testController(k).Open(validTarget())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	k.inspectErr = errors.New("descriptor disappeared")
	k.gone = true
	if err := p.Kill(); err != nil {
		t.Fatalf("confirmed exit after inspection failure: %v", err)
	}
	if k.signals != 0 {
		t.Fatal("signaled an exited process")
	}
	if _, err := testController(k).Open(validTarget()); !errors.Is(err, ErrExited) {
		t.Fatalf("Open did not report confirmed exit: %v", err)
	}
}
