package hub

// The startup reconnect tests: the attached-host persister's serialization and
// shutdown flush, the startup selection policy (including degraded boots and
// remnant fences), and full runMain boot cycles over a shared hub.toml and
// state root. The SSH layer is the one stub: a fake sshconn.Runner hands the
// real manager in-process AppWire bridges, so everything below sshconn is the
// production path.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostops"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/sshconn"
	"primeradiant.com/evener/internal/testwait"
)

// lockedBuffer is a concurrency-safe stderr sink: runMain logs from its own
// goroutine and from sshconn supervisors.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// ---------------------------------------------------------------------------
// persister
// ---------------------------------------------------------------------------

func TestAttachedHostsPersisterOrderingAndShutdownFlush(t *testing.T) {
	t.Run("applies queued mutations in enqueue order", func(t *testing.T) {
		store, err := hubcore.NewAttachedHostsStore(t.TempDir())
		if err != nil {
			t.Fatalf("open attached hosts store: %v", err)
		}
		p := newAttachedHostsPersister(store, func(string, ...any) {})
		var applied []attachedHostMutation
		p.mu.Lock()
		p.apply = func(m attachedHostMutation) { applied = append(applied, m) }
		p.mu.Unlock()

		first := hubcore.AttachedHostRecord{Generation: 1, IncarnationID: "inc-1"}
		last := hubcore.AttachedHostRecord{Generation: 3, IncarnationID: "inc-3"}
		p.Record("alpha", first)
		p.Remove("alpha")
		p.Record("alpha", last)
		p.Remove("beta")
		p.RecordDegraded("gamma")
		p.Close()

		want := []attachedHostMutation{
			{name: "alpha", record: first},
			{name: "alpha", remove: true},
			{name: "alpha", record: last},
			{name: "beta", remove: true},
			{name: "gamma", degraded: true},
		}
		if len(applied) != len(want) {
			t.Fatalf("applied %d mutation(s), want %d: %+v", len(applied), len(want), applied)
		}
		for i := range want {
			if applied[i] != want[i] {
				t.Fatalf("mutation %d = %+v, want %+v (queue not applied in enqueue order)", i, applied[i], want[i])
			}
		}

		// A mutation enqueued after Close is dropped: nothing may write after
		// the flush Close promised.
		p.Record("delta", last)
		p.Remove("beta")
		p.Close()
		if len(applied) != len(want) {
			t.Fatalf("a mutation enqueued after Close was applied: %+v", applied[len(want):])
		}
	})

	t.Run("Close flushes a mutation queued while the worker is busy", func(t *testing.T) {
		store, err := hubcore.NewAttachedHostsStore(t.TempDir())
		if err != nil {
			t.Fatalf("open attached hosts store: %v", err)
		}
		p := newAttachedHostsPersister(store, func(string, ...any) {})

		applying := make(chan attachedHostMutation, 1)
		release := make(chan struct{})
		var appliedMu sync.Mutex
		var applied []attachedHostMutation
		p.mu.Lock()
		p.apply = func(m attachedHostMutation) {
			appliedMu.Lock()
			applied = append(applied, m)
			appliedMu.Unlock()
			select {
			case applying <- m:
			default:
			}
			<-release
		}
		p.mu.Unlock()

		first := hubcore.AttachedHostRecord{Generation: 1, IncarnationID: "inc-1"}
		late := hubcore.AttachedHostRecord{Generation: 2, IncarnationID: "inc-2"}
		p.Record("alpha", first)
		select {
		case <-applying:
		case <-time.After(testwait.Budget(t)):
			t.Fatal("the persister worker never applied the first record")
		}
		p.Record("beta", late)

		closed := make(chan struct{})
		go func() {
			p.Close()
			close(closed)
		}()
		testwait.Until(t, "the persister accepts Close", func() bool {
			p.mu.Lock()
			defer p.mu.Unlock()
			return p.closed
		})
		close(release)
		select {
		case <-closed:
		case <-time.After(testwait.Budget(t)):
			t.Fatal("Close did not return after the queued mutations were flushed")
		}

		appliedMu.Lock()
		defer appliedMu.Unlock()
		if len(applied) != 2 || applied[0].record != first || applied[1].name != "beta" || applied[1].record != late {
			t.Fatalf("Close returned with %+v applied, want the record queued while the worker was busy", applied)
		}
	})

	t.Run("RecordDegraded never overwrites a durable identity", func(t *testing.T) {
		root := t.TempDir()
		seedAttachedHosts(t, root, map[string]hubcore.AttachedHostRecord{
			"alpha": {Generation: 7, IncarnationID: "durable-inc"},
		})
		store, err := hubcore.NewAttachedHostsStore(root)
		if err != nil {
			t.Fatalf("open attached hosts store: %v", err)
		}
		p := newAttachedHostsPersister(store, func(string, ...any) {})
		p.RecordDegraded("alpha")
		p.RecordDegraded("beta")
		p.Close()

		snap := attachedHostsNow(t, root)
		if got := snap["alpha"]; got != (hubcore.AttachedHostRecord{Generation: 7, IncarnationID: "durable-inc"}) {
			t.Fatalf("RecordDegraded overwrote the durable identity: %+v", got)
		}
		if got, ok := snap["beta"]; !ok || got != (hubcore.AttachedHostRecord{}) {
			t.Fatalf("RecordDegraded did not record the new name: %+v (present %v)", got, ok)
		}
	})

	t.Run("a nil persister is a no-op", func(t *testing.T) {
		var p *attachedHostsPersister
		p.Record("alpha", hubcore.AttachedHostRecord{})
		p.RecordDegraded("alpha")
		p.Remove("alpha")
		p.Close()
	})
}

func TestSelectStartupReconnectHostsPolicyAndDegradedMode(t *testing.T) {
	const durable = "inc-alpha"
	type selection struct {
		name     string
		entries  []hostreg.Host
		saved    map[string]hubcore.AttachedHostRecord
		degraded bool
		remnants map[string]bool
		want     []string
		keep     []string
	}
	cases := []selection{
		{
			name:    "a host that never reached attached is excluded",
			entries: []hostreg.Host{startupHostEntry("alpha", 1, durable)},
		},
		{
			name:    "a matching registration is reconnected",
			entries: []hostreg.Host{startupHostEntry("alpha", 1, durable)},
			saved:   map[string]hubcore.AttachedHostRecord{"alpha": {Generation: 1, IncarnationID: durable}},
			want:    []string{"alpha"},
			keep:    []string{"alpha"},
		},
		{
			name:    "a mismatched generation is a different registration",
			entries: []hostreg.Host{startupHostEntry("alpha", 2, durable)},
			saved:   map[string]hubcore.AttachedHostRecord{"alpha": {Generation: 1, IncarnationID: durable}},
		},
		{
			name:    "a mismatched incarnation is a removed-and-re-added host",
			entries: []hostreg.Host{startupHostEntry("alpha", 1, "inc-other")},
			saved:   map[string]hubcore.AttachedHostRecord{"alpha": {Generation: 1, IncarnationID: durable}},
		},
		{
			name:     "an open remnant fences the name but keeps its record",
			entries:  []hostreg.Host{startupHostEntry("alpha", 1, durable)},
			saved:    map[string]hubcore.AttachedHostRecord{"alpha": {Generation: 1, IncarnationID: durable}},
			remnants: map[string]bool{"alpha": true},
			keep:     []string{"alpha"},
		},
		{
			name:     "a degraded boot matches by name alone",
			entries:  []hostreg.Host{startupHostEntry("alpha", 2, "inc-fresh")},
			saved:    map[string]hubcore.AttachedHostRecord{"alpha": {Generation: 1, IncarnationID: durable}},
			degraded: true,
			want:     []string{"alpha"},
			keep:     []string{"alpha"},
		},
		{
			name:     "a degraded boot still skips a remnant-fenced name",
			entries:  []hostreg.Host{startupHostEntry("alpha", 2, "inc-fresh")},
			saved:    map[string]hubcore.AttachedHostRecord{"alpha": {Generation: 1, IncarnationID: durable}},
			degraded: true,
			remnants: map[string]bool{"alpha": true},
			keep:     []string{"alpha"},
		},
		{
			name:    "an empty saved incarnation matches by name on a healthy boot",
			entries: []hostreg.Host{startupHostEntry("alpha", 1, "inc-fresh")},
			saved:   map[string]hubcore.AttachedHostRecord{"alpha": {}},
			want:    []string{"alpha"},
			keep:    []string{"alpha"},
		},
		{
			name:    "only saved names in the registry are kept",
			entries: []hostreg.Host{startupHostEntry("alpha", 1, durable)},
			saved: map[string]hubcore.AttachedHostRecord{
				"alpha": {Generation: 1, IncarnationID: durable},
				"ghost": {Generation: 1, IncarnationID: "ghost-inc"},
			},
			want: []string{"alpha"},
			keep: []string{"alpha"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var remnant func(string) bool
			if tc.remnants != nil {
				remnant = func(name string) bool { return tc.remnants[name] }
			}
			toReconnect, keep := selectStartupReconnectHosts(tc.entries, tc.saved, tc.degraded, remnant)
			if got := startupHostNames(toReconnect); !sameStrings(got, tc.want) {
				t.Fatalf("reconnect set = %v, want %v", got, tc.want)
			}
			kept := make([]string, 0, len(keep))
			for name, ok := range keep {
				if !ok {
					t.Fatalf("keepInStore[%q] = false; only retained names may appear", name)
				}
				kept = append(kept, name)
			}
			if !sameStrings(kept, tc.keep) {
				t.Fatalf("kept set = %v, want %v", kept, tc.keep)
			}
		})
	}

	if !shouldAutoReconnect(startupHostEntry("alpha", 1, durable)) {
		t.Fatal("a configured host is not eligible for startup reconnect")
	}
}

// ---------------------------------------------------------------------------
// runMain fixtures
// ---------------------------------------------------------------------------

type reconnectFixture struct {
	t          *testing.T
	root       string
	configPath string
	cfg        Config
	deps       mainDeps
}

func hostConfig(name string) HostConfig {
	return HostConfig{Name: name, SSH: name + ".example"}
}

func newReconnectFixture(t *testing.T, hosts []HostConfig, extraTOML string) *reconnectFixture {
	t.Helper()
	// A boot records the address it bound in package globals that the restart
	// path reads. An ephemeral fixture address must not rewrite the restart
	// arguments a later test builds.
	previousBoundAddr, previousBindWasEphemeral := hubBoundAddr, hubBindWasEphemeral
	t.Cleanup(func() { hubBoundAddr, hubBindWasEphemeral = previousBoundAddr, previousBindWasEphemeral })
	root, cfg, deps := newTraceMainTestDeps(t)
	cfg.Hosts = hosts
	f := &reconnectFixture{
		t:          t,
		root:       root,
		configPath: filepath.Join(root, "hub.toml"),
		cfg:        cfg,
		deps:       deps,
	}
	var b strings.Builder
	b.WriteString("# hub.toml for the startup reconnect tests\n\n")
	for _, h := range hosts {
		fmt.Fprintf(&b, "[[hosts]]\nname = %q\nssh = %q\n\n", h.Name, h.SSH)
	}
	b.WriteString(extraTOML)
	f.writeTOML(b.String())
	return f
}

func (f *reconnectFixture) writeTOML(content string) {
	f.t.Helper()
	if err := os.WriteFile(f.configPath, []byte(content), 0o600); err != nil {
		f.t.Fatalf("write hub.toml: %v", err)
	}
}

// seedAttachedHosts writes the attached-host state file directly, the way an
// earlier boot would have left it.
func seedAttachedHosts(t *testing.T, stateRoot string, hosts map[string]hubcore.AttachedHostRecord) {
	t.Helper()
	path := filepath.Join(stateRoot, "attached-hosts", "state.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("create attached hosts dir: %v", err)
	}
	payload := struct {
		Version uint64                                `json:"version"`
		Hosts   map[string]hubcore.AttachedHostRecord `json:"hosts"`
	}{Version: 1, Hosts: hosts}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal attached hosts seed: %v", err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		t.Fatalf("write attached hosts seed: %v", err)
	}
}

func attachedHostsNow(t *testing.T, stateRoot string) map[string]hubcore.AttachedHostRecord {
	t.Helper()
	store, err := hubcore.NewAttachedHostsStore(stateRoot)
	if err != nil {
		t.Fatalf("open attached hosts store: %v", err)
	}
	return store.Snapshot()
}

func startupHostEntry(name string, generation uint64, incarnation string) hostreg.Host {
	return hostreg.Host{Name: name, SSH: name + ".example", Generation: generation, IncarnationID: incarnation}
}

func startupHostNames(entries []hostreg.Host) []string {
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name)
	}
	return names
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	as := append([]string(nil), a...)
	bs := append([]string(nil), b...)
	sortStrings(as)
	sortStrings(bs)
	for i := range as {
		if as[i] != bs[i] {
			return false
		}
	}
	return true
}

func sortStrings(names []string) {
	for i := 1; i < len(names); i++ {
		for j := i; j > 0 && names[j] < names[j-1]; j-- {
			names[j], names[j-1] = names[j-1], names[j]
		}
	}
}

// ---------------------------------------------------------------------------
// fake ssh runner
// ---------------------------------------------------------------------------

type dialPolicy int

const (
	dialHealthy dialPolicy = iota
	dialTransientFailure
	dialTerminalProtocol
)

// reconnectRunner is the sshconn.Runner seam for the boot tests: Run answers
// the attach preflight from the shared probe fixtures, Start hands back one
// in-process AppWire bridge under the per-host policy.
type reconnectRunner struct {
	probeFakeRunner

	mu      sync.Mutex
	policy  map[string]dialPolicy
	dials   map[string]int
	gate    map[string]chan struct{}
	started map[string]chan struct{}
	bridges []*probeFakeStdio
}

func newReconnectRunner() *reconnectRunner {
	return &reconnectRunner{
		policy:  map[string]dialPolicy{},
		dials:   map[string]int{},
		gate:    map[string]chan struct{}{},
		started: map[string]chan struct{}{},
	}
}

// dialedHostName recovers the registry name from the ssh argv the manager
// builds: the destination follows "--", and the fixture hosts share the
// ".example" suffix.
func dialedHostName(argv []string) string {
	for i, arg := range argv {
		if arg != "--" || i+1 >= len(argv) {
			continue
		}
		dest := argv[i+1]
		if at := strings.LastIndex(dest, "@"); at >= 0 {
			dest = dest[at+1:]
		}
		return strings.TrimSuffix(dest, ".example")
	}
	return ""
}

func (r *reconnectRunner) Start(ctx context.Context, argv []string, _ io.Writer) (sshconn.Stdio, error) {
	name := dialedHostName(argv)
	r.mu.Lock()
	policy := r.policy[name]
	r.dials[name]++
	gate := r.gate[name]
	started := r.started[name]
	r.mu.Unlock()
	if started != nil {
		select {
		case started <- struct{}{}:
		default:
		}
	}
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	switch policy {
	case dialTransientFailure:
		return nil, errors.New("the ssh bridge refused")
	case dialTerminalProtocol:
		return r.bridge(func(id appwire.ID) appwire.Message {
			return appwire.ErrorMessage(id, appwire.UpgradeRequired(appwire.ProtocolVersion, "999.0.0"))
		}), nil
	default:
		return r.bridge(func(id appwire.ID) appwire.Message {
			return appwire.ResponseMessage(id, appwire.InitializeResponse{
				ProtocolVersion: appwire.ProtocolVersion,
				SourceID:        "local",
			})
		}), nil
	}
}

func (r *reconnectRunner) bridge(initialize func(appwire.ID) appwire.Message) *probeFakeStdio {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	stdio := &probeFakeStdio{inW: inW, outR: outR, inR: inR, outW: outW, waitDone: make(chan struct{})}
	r.mu.Lock()
	r.bridges = append(r.bridges, stdio)
	r.mu.Unlock()
	go func() {
		server := appwire.NewStreamTransport(&probeStream{in: outW, out: inR})
		defer func() { _ = server.Close() }()
		for {
			msg, err := server.Recv(context.Background())
			if err != nil {
				return
			}
			if msg.Request == nil {
				continue
			}
			var response appwire.Message
			if msg.Request.Method == appwire.MethodInitialize {
				response = initialize(msg.Request.ID)
			} else {
				response = appwire.ErrorMessage(msg.Request.ID, appwire.MethodNotFound(msg.Request.Method))
			}
			if err := server.Send(context.Background(), response); err != nil {
				return
			}
		}
	}()
	return stdio
}

func (r *reconnectRunner) dialsFor(name string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.dials[name]
}

func (r *reconnectRunner) setPolicy(name string, policy dialPolicy) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.policy[name] = policy
}

func (r *reconnectRunner) waitForDials(t *testing.T, name string, want int) {
	t.Helper()
	testwait.Until(t, name+" dials", func() bool { return r.dialsFor(name) >= want })
}

func (r *reconnectRunner) killBridges() {
	r.mu.Lock()
	bridges := append([]*probeFakeStdio(nil), r.bridges...)
	r.mu.Unlock()
	for _, stdio := range bridges {
		_ = stdio.Kill()
	}
}

// ---------------------------------------------------------------------------
// one runMain boot
// ---------------------------------------------------------------------------

type runningHub struct {
	t      *testing.T
	stderr *lockedBuffer

	cancel     context.CancelFunc
	cancelOnce sync.Once
	done       chan error

	serving   chan struct{}
	serveOnce sync.Once
	finished  chan struct{}
	err       error

	webCh     chan *WebServer
	managerCh chan *sshconn.Manager
	readyOnce sync.Once
	web       *WebServer
	manager   *sshconn.Manager

	// atRelease runs when runMain releases the hub lock; anything it records is
	// safe to read once stop returns.
	atRelease       func()
	releaseSnapshot map[string]hubcore.AttachedHostRecord
	releaseErr      error
}

func (h *runningHub) closeServing() {
	h.serveOnce.Do(func() { close(h.serving) })
}

func (h *runningHub) beginShutdown() {
	h.cancelOnce.Do(h.cancel)
}

func (h *runningHub) ready() (*WebServer, *sshconn.Manager) {
	h.readyOnce.Do(func() {
		select {
		case h.web = <-h.webCh:
		case <-time.After(testwait.Budget(h.t)):
			h.t.Fatal("runMain never built its web server")
		}
		select {
		case h.manager = <-h.managerCh:
		case <-time.After(testwait.Budget(h.t)):
			h.t.Fatal("runMain never built its SSH manager")
		}
	})
	return h.web, h.manager
}

func (h *runningHub) waitServing() {
	h.t.Helper()
	select {
	case <-h.serving:
	case <-time.After(testwait.Budget(h.t)):
		h.t.Fatal("runMain never reached serving")
	}
}

func (h *runningHub) stop() error {
	h.beginShutdown()
	select {
	case <-h.finished:
		return h.err
	case <-time.After(testwait.Budget(h.t)):
		h.t.Fatal("runMain did not return after shutdown")
		return nil
	}
}

func (f *reconnectFixture) startBoot(t *testing.T, runner *reconnectRunner) *runningHub {
	t.Helper()
	t.Cleanup(runner.killBridges)
	serveCtx, cancel := context.WithCancel(context.Background())
	h := &runningHub{
		t:         t,
		stderr:    &lockedBuffer{},
		cancel:    cancel,
		done:      make(chan error, 1),
		serving:   make(chan struct{}),
		finished:  make(chan struct{}),
		webCh:     make(chan *WebServer, 1),
		managerCh: make(chan *sshconn.Manager, 1),
	}
	deps := f.deps
	deps.loadConfig = func(string, bool) (Config, error) { return f.cfg, nil }
	deps.notifyContext = func(context.Context, ...os.Signal) (context.Context, context.CancelFunc) {
		return serveCtx, cancel
	}
	deps.serve = func(ctx context.Context, _ hubHTTPServer) error {
		h.closeServing()
		<-ctx.Done()
		return nil
	}
	deps.afterWeb = func(web *WebServer) { h.webCh <- web }
	deps.newSSHManager = func(reg *hostreg.Registry, opts sshconn.Options) *sshconn.Manager {
		opts.Runner = runner
		opts.BackoffBase = time.Millisecond
		opts.BackoffMax = time.Millisecond
		m := sshconn.New(reg, opts)
		h.managerCh <- m
		return m
	}
	deps.acquireLock = func(string) (func(), error) {
		return func() {
			if h.atRelease != nil {
				h.atRelease()
			}
		}, nil
	}
	args := []string{"-addr", f.cfg.Addr, "-evener", "/bin/evener", "-config", f.configPath}
	go func() {
		h.err = runMain(args, h.stderr, deps)
		close(h.finished)
	}()
	t.Cleanup(func() {
		if err := h.stop(); err != nil {
			t.Errorf("runMain: %v\n%s", err, h.stderr.String())
		}
	})
	return h
}

func (f *reconnectFixture) boot(t *testing.T, runner *reconnectRunner) *runningHub {
	t.Helper()
	h := f.startBoot(t, runner)
	h.waitServing()
	return h
}

func attachConfiguredHost(t *testing.T, h *runningHub, name string) appwire.HostAttachResponse {
	t.Helper()
	web, manager := h.ready()
	resp, err := hubHostAttach(context.Background(), web.cfg, web.sources, manager.Registry(), appwire.HostAttachParams{Host: name})
	if err != nil {
		t.Fatalf("attach %s: %v", name, err)
	}
	if !resp.Attached {
		t.Fatalf("attach %s = %+v, want attached", name, resp)
	}
	return resp
}

func waitHostAttached(t *testing.T, h *runningHub, name string) {
	t.Helper()
	_, manager := h.ready()
	testwait.Until(t, "host "+name+" attaches", func() bool { return manager.Attached(name) })
}

func hostRow(t *testing.T, h *runningHub, name string) appwire.HostRow {
	t.Helper()
	web, _ := h.ready()
	resp, err := web.hostManage.Status(context.Background(), appwire.HostStatusParams{Name: name})
	if err != nil {
		t.Fatalf("status %s: %v", name, err)
	}
	return resp.Host
}

func registryHost(t *testing.T, h *runningHub, name string) hostreg.Host {
	t.Helper()
	_, manager := h.ready()
	entry, ok := manager.Registry().Get(name)
	if !ok {
		t.Fatalf("the registry lost host %q", name)
	}
	return entry
}

func stopCleanly(t *testing.T, h *runningHub) {
	t.Helper()
	if err := h.stop(); err != nil {
		t.Fatalf("runMain: %v", err)
	}
}

func requireRecord(t *testing.T, snap map[string]hubcore.AttachedHostRecord, name string, entry hostreg.Host) {
	t.Helper()
	want := hubcore.AttachedHostRecord{Generation: entry.Generation, IncarnationID: entry.IncarnationID}
	if got, ok := snap[name]; !ok || got != want {
		t.Fatalf("attached-host record for %q = %+v (present %v), want %+v", name, got, ok, want)
	}
}

// waitAttachedRecord waits for the asynchronous persister write that follows an
// attach event.
func waitAttachedRecord(t *testing.T, stateRoot, name string, entry hostreg.Host) {
	t.Helper()
	want := hubcore.AttachedHostRecord{Generation: entry.Generation, IncarnationID: entry.IncarnationID}
	testwait.Until(t, "the attached-host record for "+name, func() bool {
		return attachedHostsNow(t, stateRoot)[name] == want
	})
}

// ---------------------------------------------------------------------------
// boot cycles
// ---------------------------------------------------------------------------

func TestRunMainStartupAutoReconnectTwoBootCycle(t *testing.T) {
	f := newReconnectFixture(t, []HostConfig{hostConfig("alpha"), hostConfig("beta")}, "")

	runner1 := newReconnectRunner()
	h1 := f.boot(t, runner1)
	attachConfiguredHost(t, h1, "alpha")
	alpha1 := registryHost(t, h1, "alpha")

	waitAttachedRecord(t, f.cfg.HubStateRoot, "alpha", alpha1)
	snap := attachedHostsNow(t, f.cfg.HubStateRoot)
	if _, ok := snap["beta"]; ok {
		t.Fatal("a host that never attached was persisted")
	}
	if runner1.dialsFor("beta") != 0 {
		t.Fatalf("the dormant host was dialed %d time(s) on its first boot", runner1.dialsFor("beta"))
	}
	stopCleanly(t, h1)

	snap = attachedHostsNow(t, f.cfg.HubStateRoot)
	requireRecord(t, snap, "alpha", alpha1)

	runner2 := newReconnectRunner()
	h2 := f.boot(t, runner2)
	waitHostAttached(t, h2, "alpha")
	alpha2 := registryHost(t, h2, "alpha")
	if alpha2.Generation != alpha1.Generation || alpha2.IncarnationID != alpha1.IncarnationID {
		t.Fatalf("the boot restored identity %+v, want %+v", alpha2, alpha1)
	}
	if runner2.dialsFor("beta") != 0 {
		t.Fatalf("the dormant host was dialed %d time(s) after a restart", runner2.dialsFor("beta"))
	}
	if !hostRow(t, h2, "alpha").Attached {
		t.Fatal("the auto-reconnected host does not report attached")
	}
	stopCleanly(t, h2)
}

func TestRunMainStartupAutoReconnectSurvivesBackToBackRestart(t *testing.T) {
	f := newReconnectFixture(t, []HostConfig{hostConfig("alpha")}, "")

	h1 := f.boot(t, newReconnectRunner())
	attachConfiguredHost(t, h1, "alpha")
	alpha := registryHost(t, h1, "alpha")
	stopCleanly(t, h1)
	want := hubcore.AttachedHostRecord{Generation: alpha.Generation, IncarnationID: alpha.IncarnationID}
	if got := attachedHostsNow(t, f.cfg.HubStateRoot)["alpha"]; got != want {
		t.Fatalf("record after the first boot = %+v, want %+v", got, want)
	}

	runner2 := newReconnectRunner()
	runner2.setPolicy("alpha", dialTransientFailure)
	h2 := f.boot(t, runner2)
	runner2.waitForDials(t, "alpha", 1)
	stopCleanly(t, h2)
	if got := attachedHostsNow(t, f.cfg.HubStateRoot)["alpha"]; got != want {
		t.Fatalf("a retrying boot changed the record to %+v, want %+v", got, want)
	}

	runner3 := newReconnectRunner()
	h3 := f.boot(t, runner3)
	waitHostAttached(t, h3, "alpha")
	if got := registryHost(t, h3, "alpha"); got.Generation != alpha.Generation || got.IncarnationID != alpha.IncarnationID {
		t.Fatalf("the third boot restored identity %+v, want %+v", got, alpha)
	}
	stopCleanly(t, h3)
}

func TestRunMainStartupAutoReconnectRetainsEntryAcrossTerminalFailure(t *testing.T) {
	f := newReconnectFixture(t, []HostConfig{hostConfig("alpha")}, "")

	h1 := f.boot(t, newReconnectRunner())
	attachConfiguredHost(t, h1, "alpha")
	alpha := registryHost(t, h1, "alpha")
	stopCleanly(t, h1)
	want := hubcore.AttachedHostRecord{Generation: alpha.Generation, IncarnationID: alpha.IncarnationID}

	runner2 := newReconnectRunner()
	runner2.setPolicy("alpha", dialTerminalProtocol)
	h2 := f.boot(t, runner2)
	web, _ := h2.ready()
	testwait.Until(t, "the terminal attach failure reaches the host row", func() bool {
		resp, err := web.hostManage.Status(context.Background(), appwire.HostStatusParams{Name: "alpha"})
		return err == nil && resp.Host.LastAttachErr != ""
	})
	_, manager2 := h2.ready()
	if manager2.Attached("alpha") {
		t.Fatal("a terminal protocol failure left the host attached")
	}
	stopCleanly(t, h2)
	if got := attachedHostsNow(t, f.cfg.HubStateRoot)["alpha"]; got != want {
		t.Fatalf("a terminal failure changed the record to %+v, want %+v", got, want)
	}

	runner3 := newReconnectRunner()
	h3 := f.boot(t, runner3)
	waitHostAttached(t, h3, "alpha")
	stopCleanly(t, h3)
}

func TestRunMainHostRemoveAndUpdateClearAutoReconnect(t *testing.T) {
	t.Run("remove and re-add", func(t *testing.T) {
		f := newReconnectFixture(t, []HostConfig{hostConfig("alpha")}, "")
		h1 := f.boot(t, newReconnectRunner())
		attachConfiguredHost(t, h1, "alpha")
		row := hostRow(t, h1, "alpha")
		web, _ := h1.ready()

		removed, err := web.hostManage.RemoveResult(context.Background(), appwire.HostRemoveParams{
			Name:                  "alpha",
			MutationID:            "remove-1",
			ExpectedGeneration:    row.Generation,
			ExpectedIncarnationID: row.IncarnationID,
		})
		if err != nil || removed.HostMutationCommittedRemoved == nil {
			t.Fatalf("remove = (%+v, %v), want the committed-removed arm", removed, err)
		}
		added, err := web.hostManage.AddResult(context.Background(), appwire.HostAddParams{
			Entry:      appwire.HostEntry{Name: "alpha", Address: "alpha.example"},
			MutationID: "add-1",
		})
		if err != nil || added.HostMutationCommitted == nil {
			t.Fatalf("re-add = (%+v, %v), want the committed arm", added, err)
		}
		stopCleanly(t, h1)
		if _, ok := attachedHostsNow(t, f.cfg.HubStateRoot)["alpha"]; ok {
			t.Fatal("a removed-and-re-added host kept its auto-reconnect record")
		}

		runner2 := newReconnectRunner()
		h2 := f.boot(t, runner2)
		if runner2.dialsFor("alpha") != 0 {
			t.Fatalf("the re-added host was dialed %d time(s) on the next boot", runner2.dialsFor("alpha"))
		}
		if hostRow(t, h2, "alpha").Attached {
			t.Fatal("the re-added host attached without a client request")
		}
		stopCleanly(t, h2)
	})

	t.Run("update retires the channel", func(t *testing.T) {
		f := newReconnectFixture(t, []HostConfig{hostConfig("alpha")}, "")
		h1 := f.boot(t, newReconnectRunner())
		attachConfiguredHost(t, h1, "alpha")
		row := hostRow(t, h1, "alpha")
		web, _ := h1.ready()

		updated, err := web.hostManage.UpdateResult(context.Background(), appwire.HostUpdateParams{
			Name:                  "alpha",
			Entry:                 appwire.HostEntry{Address: "alpha.example", User: "ops"},
			MutationID:            "update-1",
			ExpectedGeneration:    row.Generation,
			ExpectedIncarnationID: row.IncarnationID,
		})
		if err != nil || updated.HostMutationCommitted == nil {
			t.Fatalf("update = (%+v, %v), want the committed arm", updated, err)
		}
		if updated.HostMutationCommitted.Host.Generation <= row.Generation {
			t.Fatalf("update generation = %d, want an advance past %d", updated.HostMutationCommitted.Host.Generation, row.Generation)
		}
		stopCleanly(t, h1)
		if _, ok := attachedHostsNow(t, f.cfg.HubStateRoot)["alpha"]; ok {
			t.Fatal("an edited host kept its auto-reconnect record")
		}

		runner2 := newReconnectRunner()
		h2 := f.boot(t, runner2)
		if runner2.dialsFor("alpha") != 0 {
			t.Fatalf("the edited host was dialed %d time(s) on the next boot", runner2.dialsFor("alpha"))
		}
		if hostRow(t, h2, "alpha").Attached {
			t.Fatal("the edited host attached without a client request")
		}
		stopCleanly(t, h2)
	})
}

func TestRunMainStartupAutoReconnectSkipsOpenRemnantAndPrunesStale(t *testing.T) {
	const remnantTOML = `
[teardown_remnants."rem-1"]
host = "rem"
kind = "update"
seam = "update-host"
generation = 1
incarnation_id = "inc-1"
mutation_key = "mut-1/rem/update/1/inc-1"
committed_at = "2026-09-26T00:00:00Z"

[teardown_remnants."rem-1".pending_teardown]
name = "rem"
kind = "update"
generation = 1
incarnation_id = "inc-1"
supervisor = true
channel = true

[teardown_remnants."rem-1".cleanup_handle]
kind = "local-boundary"
generation = 1
incarnation_id = "inc-1"
presence_epoch = 1
`
	f := newReconnectFixture(t, []HostConfig{hostConfig("alpha"), hostConfig("stale"), hostConfig("rem")}, remnantTOML)
	seedAttachedHosts(t, f.cfg.HubStateRoot, map[string]hubcore.AttachedHostRecord{
		"ghost": {Generation: 3, IncarnationID: "ghost-inc"},
		"stale": {Generation: 999, IncarnationID: "stale-inc"},
		"alpha": {},
		"rem":   {},
	})

	runner := newReconnectRunner()
	h := f.boot(t, runner)
	waitHostAttached(t, h, "alpha")
	alpha := registryHost(t, h, "alpha")
	testwait.Until(t, "alpha's real identity is recorded", func() bool {
		return attachedHostsNow(t, f.cfg.HubStateRoot)["alpha"].IncarnationID == alpha.IncarnationID
	})

	snap := attachedHostsNow(t, f.cfg.HubStateRoot)
	if _, ok := snap["ghost"]; ok {
		t.Fatal("a removed host's stale record survived the boot prune")
	}
	if _, ok := snap["stale"]; ok {
		t.Fatal("a mismatched-generation record survived the boot prune")
	}
	if _, ok := snap["rem"]; !ok {
		t.Fatal("a remnant-fenced host's record was pruned; resolving the remnant must preserve intent")
	}
	if runner.dialsFor("rem") != 0 {
		t.Fatalf("the remnant-fenced host was dialed %d time(s)", runner.dialsFor("rem"))
	}
	stopCleanly(t, h)
}

func TestRunMainDegradedOpsStorePreservesDurableIdentityAndCorruptStateStarts(t *testing.T) {
	t.Run("degraded operation store preserves the durable identity", func(t *testing.T) {
		previous := hostOpsOpenRetryDelays
		hostOpsOpenRetryDelays = []time.Duration{time.Millisecond}
		t.Cleanup(func() { hostOpsOpenRetryDelays = previous })

		f := newReconnectFixture(t, []HostConfig{hostConfig("alpha")}, "")
		// A regular file where the operation store's directory belongs makes
		// every open attempt fail, so the hub starts with opsUnavailable.
		blocker := filepath.Dir(hostops.StorePath(f.cfg.HubStateRoot))
		if err := os.WriteFile(blocker, []byte("not a directory"), 0o600); err != nil {
			t.Fatalf("block the operation store: %v", err)
		}
		seedAttachedHosts(t, f.cfg.HubStateRoot, map[string]hubcore.AttachedHostRecord{
			"alpha": {Generation: 1, IncarnationID: "durable-inc"},
		})

		runner := newReconnectRunner()
		h := f.boot(t, runner)
		waitHostAttached(t, h, "alpha")
		if runner.dialsFor("alpha") == 0 {
			t.Fatal("a degraded boot did not fall back to reconnecting by name")
		}
		stopCleanly(t, h)

		if got := attachedHostsNow(t, f.cfg.HubStateRoot)["alpha"]; got != (hubcore.AttachedHostRecord{Generation: 1, IncarnationID: "durable-inc"}) {
			t.Fatalf("a degraded boot overwrote the durable identity: %+v", got)
		}
	})

	t.Run("a corrupt state file does not stop startup", func(t *testing.T) {
		f := newReconnectFixture(t, []HostConfig{hostConfig("alpha")}, "")
		path := filepath.Join(f.cfg.HubStateRoot, "attached-hosts", "state.json")
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatalf("create attached hosts dir: %v", err)
		}
		corrupt := []byte("{")
		if err := os.WriteFile(path, corrupt, 0o600); err != nil {
			t.Fatalf("write corrupt attached hosts state: %v", err)
		}

		h := f.boot(t, newReconnectRunner())
		attachConfiguredHost(t, h, "alpha")
		stopCleanly(t, h)

		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read attached hosts state: %v", err)
		}
		if !bytes.Equal(got, corrupt) {
			t.Fatalf("the corrupt state file was rewritten: %q", got)
		}
	})
}

func TestRunMainShutdownFlushesLateAttachedHostBeforeLockRelease(t *testing.T) {
	f := newReconnectFixture(t, []HostConfig{hostConfig("alpha")}, "")
	h := f.startBoot(t, newReconnectRunner())
	h.atRelease = func() {
		store, err := hubcore.NewAttachedHostsStore(f.cfg.HubStateRoot)
		h.releaseErr = err
		if err == nil {
			h.releaseSnapshot = store.Snapshot()
		}
	}
	h.waitServing()
	attachConfiguredHost(t, h, "alpha")
	alpha := registryHost(t, h, "alpha")
	if err := h.stop(); err != nil {
		t.Fatalf("runMain: %v", err)
	}

	if h.releaseErr != nil {
		t.Fatalf("read attached hosts state at lock release: %v", h.releaseErr)
	}
	requireRecord(t, h.releaseSnapshot, "alpha", alpha)
	requireRecord(t, attachedHostsNow(t, f.cfg.HubStateRoot), "alpha", alpha)
}
