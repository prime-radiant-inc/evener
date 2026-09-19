package interactiveartifacts

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"math/rand/v2"
	"os"
	"os/exec"
	"sync"
	"time"

	"primeradiant.com/evener/appwire"
)

// SupervisorOptions binds trusted startup policy and injectable timing. Policy
// is awaited on every run before any grant. A nil policy fails acquisitions
// closed; the Hub's durable association/deletion owner must supply it.
type SupervisorOptions struct {
	Policy     func(context.Context) ([]NamespacePolicy, error)
	Now        func() time.Time
	Wait       func(context.Context, time.Duration) error
	Jitter     func() float64
	command    []string
	extraFiles []*os.File
}

// ServiceStatus intentionally contains no endpoint credential or artifact data.
type ServiceStatus struct {
	State            string    `json:"state"`
	Readiness        Readiness `json:"readiness"`
	PID              int       `json:"pid"`
	ProcessesStarted int       `json:"processesStarted"`
	ProcessesReaped  int       `json:"processesReaped"`
	LiveProcesses    int       `json:"liveProcesses"`
	CircuitOpen      bool      `json:"circuitOpen"`
	Failures         int       `json:"failures"`
}
type ownedService struct {
	cmd    *exec.Cmd
	client *appwire.Client
	done   chan struct{}
	ready  Readiness
	cancel context.CancelFunc
}
type Supervisor struct {
	root         string
	options      SupervisorOptions
	ctx          context.Context
	cancel       context.CancelFunc
	mu           sync.Mutex
	status       ServiceStatus
	child        *ownedService
	changed      chan struct{}
	retry        chan struct{}
	started      bool
	closed       bool
	done         chan struct{}
	failures     []time.Time
	controlSlots chan struct{}
}

func NewSupervisor(root string, options SupervisorOptions) *Supervisor {
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Wait == nil {
		options.Wait = func(ctx context.Context, d time.Duration) error {
			timer := time.NewTimer(d)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-timer.C:
				return nil
			}
		}
	}
	if options.Jitter == nil {
		options.Jitter = rand.Float64
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Supervisor{root: root, options: options, ctx: ctx, cancel: cancel, status: ServiceStatus{State: "idle"}, changed: make(chan struct{}), retry: make(chan struct{}, 1), done: make(chan struct{}), controlSlots: make(chan struct{}, 32)}
}
func (s *Supervisor) signal()               { close(s.changed); s.changed = make(chan struct{}) }
func (s *Supervisor) Status() ServiceStatus { s.mu.Lock(); defer s.mu.Unlock(); return s.status }
func (s *Supervisor) Ensure(ctx context.Context) (Readiness, error) {
	for {
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return Readiness{}, errors.New("artifact supervisor closed")
		}
		if !s.started {
			s.started = true
			s.status.State = "starting"
			go s.run()
		}
		if s.status.State == "ready" {
			ready := s.child.ready
			s.mu.Unlock()
			return ready, nil
		}
		if s.status.State == "backoff" || s.status.CircuitOpen {
			s.mu.Unlock()
			return Readiness{}, &DomainError{Code: ServiceUnavailable, Retryable: true}
		}
		changed := s.changed
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return Readiness{}, ctx.Err()
		case <-changed:
		}
	}
}
func (s *Supervisor) run() {
	defer close(s.done)
	for {
		if s.ctx.Err() != nil {
			return
		}
		child, err := s.start()
		if err == nil {
			s.mu.Lock()
			s.child = child
			s.status.State = "ready"
			s.status.Readiness = child.ready
			s.signal()
			s.mu.Unlock()
			select {
			case <-s.ctx.Done():
				s.stop(child)
				return
			case <-child.done:
			}
			child.cancel()
			_ = child.client.Close()
		}
		s.mu.Lock()
		s.child = nil
		now := s.options.Now()
		kept := s.failures[:0]
		for _, at := range s.failures {
			if now.Sub(at) < time.Minute {
				kept = append(kept, at)
			}
		}
		s.failures = kept
		s.failures = append(s.failures, now)
		s.status.Failures = len(s.failures)
		s.status.State = "backoff"
		s.status.CircuitOpen = len(s.failures) >= 5
		if s.status.CircuitOpen {
			s.status.State = "circuit"
		}
		circuit := s.status.CircuitOpen
		attempt := len(s.failures)
		s.signal()
		s.mu.Unlock()
		if circuit {
			select {
			case <-s.ctx.Done():
				return
			case <-s.retry:
			}
		} else {
			delay := min(30*time.Second, time.Second*time.Duration(1<<min(attempt-1, 5)))
			jitter := max(0.0, min(1.0, s.options.Jitter()))
			delay = min(30*time.Second, time.Duration(float64(delay)*(0.5+jitter)))
			if err := s.options.Wait(s.ctx, delay); err != nil {
				return
			}
		}
		s.mu.Lock()
		s.status.State = "starting"
		s.signal()
		s.mu.Unlock()
	}
}
func (s *Supervisor) start() (_ *ownedService, resultErr error) {
	command := s.options.command
	if len(command) == 0 {
		executable, err := os.Executable()
		if err != nil {
			return nil, err
		}
		command = []string{executable, "hub", "artifact-service"}
	}
	childRead, parentWrite, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	defer func() { _ = childRead.Close() }()
	parentRead, childWrite, err := os.Pipe()
	if err != nil {
		_ = parentWrite.Close()
		return nil, err
	}
	defer func() { _ = childWrite.Close() }()
	life, cancel := context.WithCancel(s.ctx)
	stream := &pipeStream{reader: parentRead, writer: parentWrite}
	transport := &lifetimeTransport{Transport: appwire.NewStreamTransport(stream), ctx: life}
	child := &ownedService{cmd: exec.CommandContext(context.Background(), command[0], command[1:]...), client: appwire.NewClient(transport), done: make(chan struct{}), cancel: cancel}
	// The artifact process needs only temporary-file placement and optional
	// subprocess coverage output, never the Hub's provider or proxy credentials.
	child.cmd.Env = make([]string, 0, 2)
	for _, key := range []string{"TMPDIR", "GOCOVERDIR"} {
		if value, ok := os.LookupEnv(key); ok {
			child.cmd.Env = append(child.cmd.Env, key+"="+value)
		}
	}
	child.cmd.ExtraFiles = append([]*os.File{childRead, childWrite}, s.options.extraFiles...)
	if err := child.cmd.Start(); err != nil {
		cancel()
		_ = stream.Close()
		return nil, err
	}
	// The child owns these ends now; retaining a copy would mask parent EOF.
	_ = childRead.Close()
	_ = childWrite.Close()
	s.mu.Lock()
	s.status.ProcessesStarted++
	s.status.LiveProcesses++
	s.status.PID = child.cmd.Process.Pid
	s.mu.Unlock()
	go func() {
		_ = child.cmd.Wait()
		s.mu.Lock()
		s.status.ProcessesReaped++
		s.status.LiveProcesses--
		s.status.PID = 0
		s.mu.Unlock()
		close(child.done)
	}()
	defer func() {
		if resultErr != nil {
			s.stop(child)
		}
	}()
	child.client.Start(life)
	bootCtx, bootCancel := context.WithTimeout(life, 15*time.Second)
	defer bootCancel()
	if err := controlRequest(bootCtx, child.client, controlBootstrap, bootstrapParams{Root: s.root}, &child.ready); err != nil {
		return nil, err
	}
	if !compatible(child.ready) {
		return nil, errors.New("artifact service contract mismatch")
	}
	if s.options.Policy == nil {
		return nil, errors.New("artifact namespace policy unavailable")
	}
	policies, err := s.options.Policy(bootCtx)
	if err != nil {
		return nil, err
	}
	// Pending deletion is applied before any ensure or grant, regardless of the
	// durable association store's iteration order.
	for _, tombstone := range []bool{true, false} {
		for _, policy := range policies {
			if policy.Tombstone == tombstone {
				if err := controlRequest(bootCtx, child.client, controlNamespace, policy, nil); err != nil {
					return nil, err
				}
			}
		}
	}
	return child, nil
}
func compatible(ready Readiness) bool {
	return ready.ServiceID != "" && ready.ServiceRunID != "" && ready.SchemaVersion == StoreSchemaVersion && ready.ContractVersion == 1 && ready.CatalogVersion == 1 && ready.CoreVersion == "2025-11-25" && ready.ApplicationVersion == "2026-01-26"
}
func controlRequest(ctx context.Context, client *appwire.Client, method string, params, out any) error {
	var result controlResult
	if err := client.Request(ctx, method, params, &result); err != nil {
		if unsent, ok := errors.AsType[appwire.RequestNotSentError](err); ok {
			return unsent
		}
		return errors.New("artifact control unavailable; dispatched outcome may be uncertain")
	}
	if result.Error != nil {
		return result.Error
	}
	if result.Unavailable {
		return &DomainError{Code: ServiceUnavailable, Retryable: true}
	}
	if out != nil {
		return json.Unmarshal(result.Value, out)
	}
	return nil
}
func (s *Supervisor) request(ctx context.Context, method string, params, out any) error {
	if err := ctx.Err(); err != nil {
		return appwire.RequestNotSentError{Err: err}
	}
	if _, err := s.Ensure(ctx); err != nil {
		if ctx.Err() != nil {
			return appwire.RequestNotSentError{Err: ctx.Err()}
		}
		return err
	}
	select {
	case s.controlSlots <- struct{}{}:
		defer func() { <-s.controlSlots }()
	case <-ctx.Done():
		return appwire.RequestNotSentError{Err: ctx.Err()}
	}
	s.mu.Lock()
	child := s.child
	s.mu.Unlock()
	if child == nil {
		return &DomainError{Code: ServiceUnavailable, Retryable: true}
	}
	return controlRequest(ctx, child.client, method, params, out)
}
func (s *Supervisor) Grant(ctx context.Context, scope Scope) (Grant, error) {
	var grant Grant
	err := s.request(ctx, controlGrant, scope, &grant)
	return grant, err
}
func (s *Supervisor) ApplyNamespace(ctx context.Context, policy NamespacePolicy) error {
	return s.request(ctx, controlNamespace, policy, nil)
}
func (s *Supervisor) Revoke(ctx context.Context, token string) error {
	return s.request(ctx, controlRevoke, revokeParams{Hash: sha256.Sum256([]byte(token))}, nil)
}
func (s *Supervisor) Backup(ctx context.Context, path string) error {
	return s.request(ctx, controlBackup, backupParams{Path: path}, nil)
}
func (s *Supervisor) Retry(ctx context.Context) (Readiness, error) {
	s.mu.Lock()
	circuit := s.status.CircuitOpen
	s.failures = nil
	s.status.Failures = 0
	s.status.CircuitOpen = false
	if circuit {
		s.status.State = "starting"
		select {
		case s.retry <- struct{}{}:
		default:
		}
	}
	s.signal()
	s.mu.Unlock()
	return s.Ensure(ctx)
}
func (s *Supervisor) stop(child *ownedService) {
	// Closing private control is the orderly parent-liveness signal. Do not
	// cancel the connection lifetime until the child's drain deadline expires.
	_ = child.client.Close()
	timer := time.NewTimer(6 * time.Second)
	defer timer.Stop()
	select {
	case <-child.done:
	case <-timer.C:
		_ = child.cmd.Process.Kill()
		<-child.done
	}
	child.cancel()
}
func (s *Supervisor) Close() error {
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		s.cancel()
		s.signal()
	}
	started := s.started
	s.mu.Unlock()
	if started {
		<-s.done
	}
	s.mu.Lock()
	s.status.State = "closed"
	s.mu.Unlock()
	return nil
}

// Metrics returns the live owned child's admission counters over private IPC.
func (s *Supervisor) Metrics(ctx context.Context) (AdmissionStats, error) {
	var stats AdmissionStats
	err := s.request(ctx, controlMetrics, struct{}{}, &stats)
	return stats, err
}
