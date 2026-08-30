package agent

import (
	"fmt"
	"maps"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"

	// registryClientAt's live instances dispatch over the real Responses
	// protocol at an httptest server, which needs it registered.
	_ "primeradiant.com/evener/llm/providers/responses"
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
		if len(o.adapters) == 0 {
			// The default fixture needs no registry: openai is a curated
			// implicit id, so a bare client resolves it on the embedded one.
			o.client = llm.NewClient()
			o.client.Register(&fakeAdapter{name: "openai", steps: o.steps})
		} else {
			o.client = registryClient(t, nil, o.adapters...)
		}
	} else {
		for _, a := range o.adapters {
			o.client.Register(a)
		}
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

// registryClient builds a client whose resolutions come from the fixture
// registry extended with instances, with no user layer, no cache, and no
// environment. Supplied adapters are registered as overrides, and an adapter
// name the registry does not already know is injected as an instance so the
// profile for it resolves. Every remaining instance the registry knows (the
// credential-less implicit ones included) gets a mute fake, so no test client
// can reach a real transport.
func registryClient(t *testing.T, instances map[string]registry.Provider, adapters ...llm.ProviderAdapter) *llm.Client {
	t.Helper()
	return registryClientAt(t, "", instances, nil, adapters...)
}

// registryClientAt is registryClient with two additions the wire tests need:
// stateDir holds the continuation scope secret the plan is keyed from, and
// the instances named in live keep their own transport instead of the mute
// fake, so a session can dispatch at an httptest server for real.
func registryClientAt(t *testing.T, stateDir string, instances map[string]registry.Provider, live []string, adapters ...llm.ProviderAdapter) *llm.Client {
	t.Helper()
	merged := map[string]registry.Provider{}
	maps.Copy(merged, instances)
	for _, a := range adapters {
		name := a.Name()
		if _, ok := merged[name]; ok {
			continue
		}
		if _, ok := testRegistryInstances()[name]; ok {
			continue
		}
		merged[name] = adapterInstance(name)
	}
	r := testRegistry(t)
	if len(merged) > 0 {
		var err error
		if r, err = testRegistryWith(merged); err != nil {
			t.Fatalf("registryClient: %v", err)
		}
	}
	c := llm.NewClient(llm.WithRegistry(r), llm.WithClientStateDir(stateDir))
	covered := map[string]bool{}
	for _, a := range adapters {
		c.Register(a)
		covered[a.Name()] = true
	}
	for _, name := range live {
		covered[name] = true
	}
	for _, inst := range r.Instances() {
		if !covered[inst.Name] {
			c.Register(&fakeAdapter{name: inst.Name})
		}
	}
	return c
}

// resolveClientProfile resolves ref on the client's own registry, so the
// session's profile is the row that client will dispatch on.
func resolveClientProfile(t *testing.T, client *llm.Client, ref string) *provider.Profile {
	t.Helper()
	p, err := provider.Resolve(client.Registry(), ref)
	if err != nil {
		t.Fatalf("resolve %q: %v", ref, err)
	}
	return p
}

// adapterInstance is the registry entry a test adapter's name needs when it is
// not a curated id: an unreachable openai-compatible gateway, so a profile for
// it resolves and only the override ever serves it. The name rides in the host
// so a stray request names the adapter it escaped from.
func adapterInstance(name string) registry.Provider {
	return registry.Provider{
		Base: "openai-compatible", APIKey: "test",
		Transport: registry.Transport{BaseURL: "http://" + name + ".test.invalid/v1"},
	}
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
