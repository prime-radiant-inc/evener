package agent

import (
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
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
	client          *llm.Client
	adapters        []llm.ProviderAdapter
	steps           []func(req llm.Request) llm.Response
	profile         *provider.Profile
	dir             string
	cfg             SessionConfig
	cfgSet          bool
	skipGitSnapshot bool
}

type sessionOpt func(*sessionOpts)

// withClient supplies a client whose main provider adapters are already
// configured. The builder may still add its dedicated session-namer adapter.
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
func withoutGitSnapshot() sessionOpt { return func(o *sessionOpts) { o.skipGitSnapshot = true } }

// newSession builds a *Session for tests. The zero-option form yields the
// canonical default: scripted "openai" and dedicated session-namer adapters, a
// gpt-5.2 profile explicitly routed to the latter for fast-cheap calls, a fresh
// temp workspace, and SessionConfig{MaxSubagentDepth: 1}. Close is registered
// with t.Cleanup.
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
		o.dir = sharedSessionWorkspace
		if o.dir == "" {
			o.dir = t.TempDir()
		}
	}
	profile := o.profile
	if profile == nil {
		profile = NewOpenAIProfile("gpt-5.2")
	}
	if profile.ConfiguredCheapModel() == "" {
		profile = withTestSessionNamer(o.client, profile)
	}
	cfg := o.cfg
	if !o.cfgSet {
		cfg = SessionConfig{MaxSubagentDepth: 1}
	}
	if o.skipGitSnapshot {
		cfg.testOnly.skipGitSnapshot = true
	}
	sess, err := NewSession(o.client, profile, execenv.NewLocalExecutionEnvironment(o.dir), cfg)
	if err != nil {
		t.Fatalf("newSession: %v", err)
	}
	t.Cleanup(func() { sess.Close() })
	return sess
}

// registryClient builds a client whose resolutions come from a hermetic
// registry carrying instances, with no user layer, no cache, and no
// environment. Supplied adapters are registered as overrides; every remaining
// instance the registry knows (the credential-less implicit ones included)
// gets a mute fake, so no test client can reach a real transport.
//
// An instance the caller declares with no models endpoint is left uncovered:
// it lists from the registry alone, which needs no transport. Such an
// instance must never be completed against.
func registryClient(t *testing.T, instances map[string]registry.Provider, adapters ...llm.ProviderAdapter) *llm.Client {
	t.Helper()
	r, err := registry.Load(
		registry.WithOffline(true), registry.WithoutCache(), registry.WithNoUserLayer(),
		registry.WithStateRoot(t.TempDir()),
		registry.WithEnv(func(string) (string, bool) { return "", false }),
		registry.WithInstances(instances),
	)
	if err != nil {
		t.Fatalf("registryClient: %v", err)
	}
	c := llm.NewClient(llm.WithRegistry(r))
	covered := map[string]bool{}
	for _, a := range adapters {
		c.Register(a)
		covered[a.Name()] = true
	}
	for _, inst := range r.Instances() {
		if declared, ok := instances[inst.Name]; ok && declared.Transport.ModelsEndpoint == registry.EndpointUnsupported {
			continue
		}
		if !covered[inst.Name] {
			c.Register(&fakeAdapter{name: inst.Name})
		}
	}
	return c
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
