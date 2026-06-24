package agent

import (
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/provider"
	"primeradiant.com/serf/llm"
)

// Shared test fixtures for the agent package.
//
// Hundreds of tests need a *Session, a deterministic clock, or an injected
// persistence failure. Before this file each test hand-rolled those: the full
// llm.NewClient()/Register/NewSession/err-check/Close dance, a `jm.now = func()`
// reassignment, or a save-restore appendEvent closure. That coupled the suite to
// the exact constructor signature and seam shapes at every call site. These
// helpers centralize the vocabulary so a signature change touches one place.

// --- session construction ---

type sessionOpts struct {
	client   *llm.Client
	adapters []llm.ProviderAdapter
	steps    []func(req llm.Request) llm.Response
	profile  *provider.Profile
	dir      string
	cfg      SessionConfig
	cfgSet   bool
}

type sessionOpt func(*sessionOpts)

// withClient supplies a fully configured client; the builder registers no
// adapters of its own when a client is given.
func withClient(c *llm.Client) sessionOpt { return func(o *sessionOpts) { o.client = c } }

// withSteps drives the default "openai" fake adapter's scripted responses.
func withSteps(steps ...func(req llm.Request) llm.Response) sessionOpt {
	return func(o *sessionOpts) { o.steps = steps }
}

// withAdapter registers an additional provider adapter (e.g. a second provider
// for cross-provider switch tests). Supplying any adapter suppresses the default
// openai fake.
func withAdapter(a llm.ProviderAdapter) sessionOpt {
	return func(o *sessionOpts) { o.adapters = append(o.adapters, a) }
}

func withProfile(p *provider.Profile) sessionOpt { return func(o *sessionOpts) { o.profile = p } }
func withDir(dir string) sessionOpt              { return func(o *sessionOpts) { o.dir = dir } }
func withConfig(cfg SessionConfig) sessionOpt {
	return func(o *sessionOpts) { o.cfg = cfg; o.cfgSet = true }
}

// newSession builds a *Session for tests. The zero-option form yields the
// canonical default: a single scripted "openai" fake adapter, a gpt-5.2 profile,
// a fresh temp workspace, and SessionConfig{MaxSubagentDepth: 1}. Close is
// registered with t.Cleanup.
func newSession(t *testing.T, opts ...sessionOpt) *Session {
	t.Helper()
	var o sessionOpts
	for _, fn := range opts {
		fn(&o)
	}
	if o.client == nil {
		o.client = llm.NewClient()
		if len(o.adapters) == 0 {
			o.client.Register(&fakeAdapter{name: "openai", steps: o.steps})
		}
	}
	for _, a := range o.adapters {
		o.client.Register(a)
	}
	if o.dir == "" {
		o.dir = t.TempDir()
	}
	profile := o.profile
	if profile == nil {
		profile = NewOpenAIProfile("gpt-5.2")
	}
	cfg := o.cfg
	if !o.cfgSet {
		cfg = SessionConfig{MaxSubagentDepth: 1}
	}
	sess, err := NewSession(o.client, profile, execenv.NewLocalExecutionEnvironment(o.dir), cfg)
	if err != nil {
		t.Fatalf("newSession: %v", err)
	}
	t.Cleanup(func() { sess.Close() })
	return sess
}

// --- deterministic clock ---

// frozenTestTime is the canonical fixed instant for tests that need a
// deterministic job-manager clock. It matches the long-standing
// time.Unix(1000, 0) constant the hand-rolled `jm.now = ...` patches used, so
// freezeClock is a drop-in for them.
var frozenTestTime = time.Unix(1000, 0).UTC()

// freezeClock pins a job manager's clock to frozenTestTime and returns it, so
// timestamps are deterministic without re-rolling the now seam by hand. Call it
// before the job manager begins concurrent work (same point the manual
// assignment sat), since jm.now is read from manager goroutines.
func freezeClock(jm *jobManager) time.Time {
	jm.now = func() time.Time { return frozenTestTime }
	return frozenTestTime
}

// freezeClockAt pins a job manager's clock to a specific instant.
func freezeClockAt(jm *jobManager, at time.Time) {
	jm.now = func() time.Time { return at }
}

// --- persistence fault injection ---

// failAppendN makes the next n appends of the given event kind fail before the
// seam heals and delegates to the real store, exercising the durable-retry
// paths. It returns the attempt counter (incremented once per matching-kind
// append) so callers can assert the retry actually happened. Sites that assert
// on the injected error's identity keep their own closure instead.
func failAppendN(jm *jobManager, kind jobstore.EventKind, n int) *atomic.Int32 {
	var attempts atomic.Int32
	orig := jm.appendEvent
	err := fmt.Errorf("injected %s append failure", kind)
	jm.appendEvent = func(e jobstore.Event) error {
		if e.Kind == kind && attempts.Add(1) <= int32(n) {
			return err
		}
		return orig(e)
	}
	return &attempts
}
