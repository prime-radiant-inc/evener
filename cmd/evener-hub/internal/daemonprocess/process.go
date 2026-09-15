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

// ErrNotDaemon means verification positively found another process at the
// daemon's PID: its owner, command or start time is not the daemon's, or its
// generation changed under inspection. A refusal that only means the
// inspection could not vouch for the process (no generation or start time
// read, the log descriptor not seen, an inspection error) does not carry it.
var ErrNotDaemon = errors.New("process is not the daemon its rendezvous entry names")

// notDaemonError keeps each refusal's own message and answers errors.Is for
// ErrNotDaemon.
type notDaemonError struct{ reason string }

func (e notDaemonError) Error() string        { return e.reason }
func (e notDaemonError) Is(target error) bool { return target == ErrNotDaemon }

// Target is the rendezvous identity and canonical session data location.
type Target struct {
	PID       int
	SessionID string
	StateDir  string
	StartedAt time.Time
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
	startedAt  time.Time
	argv       []string
	ownsLog    bool
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

func (c controller) Open(t Target) (Process, error) {
	if t.PID <= 1 || t.PID > 1<<31-1 || t.PID == os.Getpid() {
		return nil, errors.New("refuse invalid or self daemon PID")
	}
	if t.SessionID == "" || t.SessionID == "." || t.SessionID == ".." || strings.ContainsAny(t.SessionID, "/\\\x00") {
		return nil, errors.New("missing or invalid daemon session identity")
	}
	if !filepath.IsAbs(t.StateDir) || t.StartedAt.IsZero() {
		return nil, errors.New("missing canonical state directory or rendezvous start time")
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
		if v.generation == "" {
			return errors.New("daemon process generation unknown")
		}
		if p.generation != "" && v.generation != p.generation {
			return notDaemonError{"daemon process generation changed"}
		}
		if v.uid != os.Geteuid() {
			return notDaemonError{"daemon process belongs to another user"}
		}
		if len(v.argv) < 2 || v.argv[1] != "serve" {
			return notDaemonError{"daemon process is not a serve command"}
		}
		if v.startedAt.IsZero() {
			return errors.New("daemon process start time unknown")
		}
		if v.startedAt.After(p.target.StartedAt) {
			return notDaemonError{"daemon process started after its rendezvous identity"}
		}
		if !v.ownsLog {
			return errors.New("daemon process does not own the session API log")
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
