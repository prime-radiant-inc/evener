// Package daemonprocess binds force-stop requests to a verified OS process.
package daemonprocess

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ErrExited means the bound process has been confirmed absent.
var ErrExited = errors.New("daemon process exited")

// Identity is what one inspection can say about the process at a daemon's
// PID.
type Identity int

const (
	// IdentityUnknown: the inspection could not vouch either way - it could
	// not run, or a fact it read only fails to confirm the daemon (an argv
	// that is not an `evener serve` invocation, the API log not seen, a start
	// that may postdate the entry).
	IdentityUnknown Identity = iota
	// IdentityOwner: the process is the daemon the target names.
	IdentityOwner
	// IdentityNotOwner: the process is gone, or a fact that cannot hold for
	// the daemon does - another owner, a start after the entry was written,
	// an identity that changed under inspection.
	IdentityNotOwner
)

// Identify reports whether the process at t.PID is the daemon t names, with
// the reason when it is not Owner. Only positive evidence answers NotOwner;
// this is the reading a roster deciding what to list needs. Kill refuses on
// every failure alike (verify), owner or not.
func Identify(t Target) (Identity, error) {
	if err := t.validate(); err != nil {
		return IdentityUnknown, err
	}
	h, err := nativeBind(t.PID)
	if errors.Is(err, ErrExited) {
		return IdentityNotOwner, nil
	}
	if err != nil {
		return IdentityUnknown, err
	}
	defer func() { _ = h.close() }()
	v, err := h.inspect(t)
	if errors.Is(err, ErrExited) {
		return IdentityNotOwner, nil
	}
	if err != nil {
		return IdentityUnknown, err
	}
	return judge(v, t)
}

// judge applies the facts one inspection yields against the target.
func judge(v identity, t Target) (Identity, error) {
	if v.generation == "" {
		return IdentityUnknown, errors.New("daemon process generation unknown")
	}
	if v.uid != os.Geteuid() {
		return IdentityNotOwner, errors.New("daemon process belongs to another user")
	}
	if v.startedAt.IsZero() {
		return IdentityUnknown, errors.New("daemon process start time unknown")
	}
	// Positive evidence needs the EARLIEST the process can have started to be
	// after the entry; a start that merely may postdate the entry (Linux
	// knows it to the tick) fails to vouch.
	lower := v.startedAtLower
	if lower.IsZero() {
		lower = v.startedAt
	}
	if lower.After(t.StartedAt) {
		return IdentityNotOwner, errors.New("daemon process started after its rendezvous identity")
	}
	if v.startedAt.After(t.StartedAt) {
		return IdentityUnknown, errors.New("daemon process may have started after its rendezvous identity")
	}
	if len(v.argv) < 2 || v.argv[1] != "serve" {
		return IdentityUnknown, errors.New("daemon process is not a serve command")
	}
	if !v.ownsLog && !t.Retiring {
		return IdentityUnknown, errors.New("daemon process does not own the session API log")
	}
	return IdentityOwner, nil
}

// Target is the rendezvous identity and canonical session data location.
type Target struct {
	PID       int
	SessionID string
	StateDir  string
	StartedAt time.Time
	// Retiring names a daemon that has committed retirement. Retirement
	// releases the session API log before the process exits, so log ownership
	// is not required evidence for it: the process is bound by its generation,
	// owner, start time and command alone. That binding is enough to wait on
	// the exit, never to signal it, so the handle refuses Kill.
	Retiring bool
}

// Process owns a generation-bound OS handle. Close releases that handle.
type Process interface {
	Kill() error
	Wait(context.Context) error
	Close() error
}

// Controller verifies a daemon before exposing a termination handle.
type Controller interface{ Open(Target) (Process, error) }

type identity struct {
	generation string
	uid        int
	// startedAt is the latest the process can have started; startedAtLower
	// the earliest. A platform that knows the start exactly sets them equal;
	// Linux knows it to the clock tick and reports the tick's two bounds.
	startedAt      time.Time
	startedAtLower time.Time
	argv           []string
	ownsLog        bool
}

type processHandle interface {
	inspect(Target) (identity, error)
	kill() error
	exited() (bool, error)
	close() error
}

type controller struct {
	bind func(int) (processHandle, error)
}
type process struct {
	mu         sync.Mutex
	handle     processHandle
	target     Target
	generation string
	closed     bool
}

// validate refuses a target no inspection could be bound to.
func (t Target) validate() error {
	if t.PID <= 1 || t.PID > 1<<31-1 || t.PID == os.Getpid() {
		return errors.New("refuse invalid or self daemon PID")
	}
	if t.SessionID == "" || t.SessionID == "." || t.SessionID == ".." || strings.ContainsAny(t.SessionID, "/\\\x00") {
		return errors.New("missing or invalid daemon session identity")
	}
	if !filepath.IsAbs(t.StateDir) || t.StartedAt.IsZero() {
		return errors.New("missing canonical state directory or rendezvous start time")
	}
	return nil
}

func (c controller) Open(t Target) (Process, error) {
	if err := t.validate(); err != nil {
		return nil, err
	}
	h, err := c.bind(t.PID)
	if err != nil {
		return nil, err
	}
	p := &process{handle: h, target: t}
	if err := p.verify(); err != nil {
		if gone, exitErr := h.exited(); exitErr == nil && gone {
			err = ErrExited
		}
		_ = h.close()
		return nil, err
	}
	return p, nil
}

func (p *process) verify() error {
	if time.Now().Before(p.target.StartedAt) {
		return errors.New("rendezvous start time is in the future")
	}
	// Inspect on both sides of ownership verification. The OS handle also binds
	// signaling to this generation if a PID is reused after these checks.
	for range 2 {
		v, err := p.handle.inspect(p.target)
		if err != nil {
			return err
		}
		if p.generation != "" && v.generation != p.generation {
			return errors.New("daemon process generation changed")
		}
		if id, reason := judge(v, p.target); id != IdentityOwner {
			return reason
		}
		p.generation = v.generation
	}
	return nil
}

func (p *process) Kill() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return os.ErrClosed
	}
	if p.target.Retiring {
		return errors.New("refuse to signal a retiring daemon: its handle only confirms exit")
	}
	if err := p.verify(); err != nil {
		if errors.Is(err, ErrExited) {
			return nil
		}
		if gone, exitErr := p.handle.exited(); exitErr == nil && gone {
			return nil
		}
		return err
	}
	err := p.handle.kill()
	if errors.Is(err, ErrExited) {
		return nil
	}
	return err
}

func (p *process) Wait(ctx context.Context) error {
	// pidfd readiness (Linux) or unique process identity (Darwin) confirms exit;
	// signal delivery and a missing rendezvous file do not.
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		p.mu.Lock()
		if p.closed {
			p.mu.Unlock()
			return os.ErrClosed
		}
		gone, err := p.handle.exited()
		p.mu.Unlock()
		if err != nil {
			return fmt.Errorf("confirm daemon exit: %w", err)
		}
		if gone {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (p *process) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	p.closed = true
	return p.handle.close()
}
