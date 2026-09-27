package hub

// evener/host/plan tests (deploy pipeline 08b §6, §10). Two layers: the real
// server construction path drives the planned arm over /rpc — proving the
// method is registered on the host surface through the same manager and the
// same origin guard the settings mutations register through — and direct
// manager calls pin every no-token arm, the supersede-on-mint rule, and the
// drift refusals this slice can reach without the per-host gate.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/buildinfo"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostops"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

// planTestHost is the configured host the plan tests plan against.
func planTestHost() hostreg.Host {
	return hostreg.Host{
		Name:       "m4",
		SSH:        "m4.example",
		User:       "ops",
		EvenerPath: "/opt/evener/bin/evener",
		Roots:      []string{"/srv/work"},
	}
}

// planTestHubTOML renders a machine-managed hub.toml declaring entries, so the
// plan's entry-fingerprint binding is exercised against a file that really
// carries the host rather than the absent marker.
func planTestHubTOML(entries []hostreg.Host) string {
	var b strings.Builder
	b.WriteString(hostTOMLBanner)
	for _, entry := range entries {
		b.WriteString("\n[[hosts]]\n")
		fmt.Fprintf(&b, "name = %q\nssh = %q\n", entry.Name, entry.SSH)
		if entry.User != "" {
			fmt.Fprintf(&b, "user = %q\n", entry.User)
		}
		if entry.EvenerPath != "" {
			fmt.Fprintf(&b, "evener_path = %q\n", entry.EvenerPath)
		}
		for _, root := range entry.Roots {
			fmt.Fprintf(&b, "roots = [%q]\n", root)
		}
	}
	return b.String()
}

// planTestFacts is the refreshed fact set the happy path plans from.
func planTestFacts(host hostreg.Host) hubcore.HostPlanFacts {
	return hubcore.HostPlanFacts{
		OS:          "linux",
		Arch:        "amd64",
		Home:        "/home/ops",
		Roots:       append([]string(nil), host.Roots...),
		UID:         "1000",
		Version:     "v1.0.0",
		Protocol:    appwire.ProtocolVersion,
		LaunchFlags: []string{"--config", "/etc/evener.toml"},
		CapturedAt:  time.Now().UTC(),
	}
}

// planTestProbe is the running-state probe the happy path plans from.
func planTestProbe() hubcore.HostRuntimeProbe {
	return hubcore.HostRuntimeProbe{Version: "v0.9.0", RunningHealthy: true}
}

// planSeams is the test's control over the plan's seams: nil fields take the
// happy-path values (facts, probe) or leave the seam unwired (dirty), which is
// what a hub with no seam reports.
type planSeams struct {
	facts func(ctx context.Context, host hostreg.Host) (hubcore.HostPlanFacts, error)
	probe func(ctx context.Context, host hostreg.Host, client *appwire.Client) (hubcore.HostRuntimeProbe, error)
	dirty func() bool
}

// planTestManager builds a manager wired the way the plan tests need it: the
// live registry, an operation store of its own, an attached-only client seam
// that reports the host attached, and the test's seams.
func planTestManager(t *testing.T, configPath string, entries []hostreg.Host, seams planSeams) (*hubHostManager, *hostops.Store, *hostreg.Registry) {
	t.Helper()
	registry, err := hostreg.New(entries)
	if err != nil {
		t.Fatalf("hostreg.New: %v", err)
	}
	store, err := hostops.Open(hostops.StorePath(t.TempDir()))
	if err != nil {
		t.Fatalf("hostops.Open: %v", err)
	}
	facts := seams.facts
	if facts == nil {
		facts = func(_ context.Context, host hostreg.Host) (hubcore.HostPlanFacts, error) {
			return planTestFacts(host), nil
		}
	}
	probe := seams.probe
	if probe == nil {
		probe = func(context.Context, hostreg.Host, *appwire.Client) (hubcore.HostRuntimeProbe, error) {
			return planTestProbe(), nil
		}
	}
	cfg := hubcore.WebConfig{
		RemoteHostRegistry:         registry,
		RemoteHostOpsStore:         store,
		RemoteHostConfigPath:       configPath,
		RemoteHostClientIfAttached: func(string) (*appwire.Client, bool) { return &appwire.Client{}, true },
		RemoteHostPlanFacts:        facts,
		RemoteHostPlanProbe:        probe,
	}
	m := newHubHostManager(nil, nil, cfg, configPath, registry, nil)
	if seams.dirty != nil {
		m.cfg.planControllerDirty = seams.dirty
	}
	return m, store, registry
}

// planNoTokenReasonOf fails the test unless result carries the no-token arm the
// caller expected.
func planNoTokenReasonOf(t *testing.T, result appwire.HostPlanResult, reason string, terminal bool) appwire.HostPlanNoToken {
	t.Helper()
	if result.HostPlanPlanned != nil {
		t.Fatalf("result carried the planned arm, want the %q no-token arm: %+v", reason, result)
	}
	if result.HostPlanNoToken == nil {
		t.Fatalf("result carried no arm at all, want the %q no-token arm", reason)
	}
	arm := *result.HostPlanNoToken
	if arm.Outcome != appwire.HostPlanOutcomeNoToken {
		t.Fatalf("outcome = %q, want %q", arm.Outcome, appwire.HostPlanOutcomeNoToken)
	}
	if arm.StaleFacts.Reason != reason {
		t.Fatalf("reason = %q, want %q", arm.StaleFacts.Reason, reason)
	}
	if arm.Terminal != terminal {
		t.Fatalf("terminal = %v, want %v for %q", arm.Terminal, terminal, reason)
	}
	if arm.StaleFacts.Message == "" {
		t.Fatalf("the %q arm carries no message", reason)
	}
	return arm
}

// TestHostPlanMintsThroughTheRealServer is the proof the plan is wired, not a
// placeholder: the real server construction path installs it, a real /rpc
// dispatch plans against a configured host, the answer is the planned arm with
// a token the operation store holds and validates, and the plan's bindings are
// the ones the store persisted.
func TestHostPlanMintsThroughTheRealServer(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "hub.toml")
	entry := planTestHost()
	if err := os.WriteFile(configPath, []byte(planTestHubTOML([]hostreg.Host{entry})), 0o600); err != nil {
		t.Fatalf("write hub.toml: %v", err)
	}
	store, err := hostops.Open(hostops.StorePath(t.TempDir()))
	if err != nil {
		t.Fatalf("hostops.Open: %v", err)
	}
	facts := planTestFacts(entry)
	// The config is the server constructor's own shape, with the attached-only
	// client seam standing in for a live channel: the host surface resolves
	// attachment through it when no SSH manager is wired (the same fallback the
	// row path documents), so nothing here dials.
	cfg, registry, _ := hostManageWiringConfig(t, configPath, []hostreg.Host{entry}, detachRefusingRunner{})
	cfg.RemoteHostSSHManager = nil
	cfg.RemoteHostOpsStore = store
	cfg.RemoteHostClient = nil
	cfg.RemoteHostClientIfAttached = func(string) (*appwire.Client, bool) { return &appwire.Client{}, true }
	cfg.RemoteHostPlanFacts = func(context.Context, hostreg.Host) (hubcore.HostPlanFacts, error) {
		return facts, nil
	}
	cfg.RemoteHostPlanProbe = func(context.Context, hostreg.Host, *appwire.Client) (hubcore.HostRuntimeProbe, error) {
		return planTestProbe(), nil
	}
	hub, web := newHubRPCTestServerWithWeb(t, cfg)
	defer hub.Close()
	if web.hostManage == nil {
		t.Fatal("the real server construction did not install the host-management surface")
	}
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	var result appwire.HostPlanResult
	if err := client.Request(context.Background(), appwire.MethodEvenerHostPlan, appwire.HostPlanParams{Name: "m4"}, &result); err != nil {
		t.Fatalf("evener/host/plan: %v", err)
	}
	if result.HostPlanPlanned == nil || result.HostPlanNoToken != nil {
		t.Fatalf("result = %+v, want exactly the planned arm", result)
	}
	planned := *result.HostPlanPlanned
	if planned.Outcome != appwire.HostPlanOutcomePlanned {
		t.Fatalf("outcome = %q, want %q", planned.Outcome, appwire.HostPlanOutcomePlanned)
	}
	if len(planned.Token) < 32 {
		t.Fatalf("token = %q, want at least 32 characters", planned.Token)
	}
	live, ok := registry.Get("m4")
	if !ok {
		t.Fatal("the configured host vanished from the registry")
	}
	wantFingerprint := hostEntryFingerprint(hostreg.Host{Name: "m4", SSH: "m4.example", User: "ops", EvenerPath: "/opt/evener/bin/evener", Roots: []string{"/srv/work"}})
	plan := planned.Plan
	switch {
	case plan.Host != "m4":
		t.Fatalf("plan host = %q", plan.Host)
	case plan.Generation != live.Generation:
		t.Fatalf("plan generation = %d, want the registry's %d", plan.Generation, live.Generation)
	case plan.TargetPath != "/opt/evener/bin/evener":
		t.Fatalf("plan targetPath = %q, want the configured evener_path", plan.TargetPath)
	case plan.ControllerRevision != buildinfo.Version():
		t.Fatalf("plan controllerRevision = %q, want the running controller's %q", plan.ControllerRevision, buildinfo.Version())
	case plan.FactsRevision != facts.Revision():
		t.Fatalf("plan factsRevision = %q, want the refreshed facts' digest %q", plan.FactsRevision, facts.Revision())
	case plan.HubTOMLFingerprint != wantFingerprint:
		t.Fatalf("plan hubTomlFingerprint = %q, want the file entry's %q", plan.HubTOMLFingerprint, wantFingerprint)
	case plan.FactsCapturedAt != facts.CapturedAt.Format(time.RFC3339):
		t.Fatalf("plan factsCapturedAt = %q, want %q", plan.FactsCapturedAt, facts.CapturedAt.Format(time.RFC3339))
	case plan.FactsAgeSec < 0 || plan.FactsAgeSec > 60:
		t.Fatalf("plan factsAgeSec = %d, want the real age of freshly captured facts", plan.FactsAgeSec)
	case plan.RunningVersion != "v0.9.0" || !plan.RunningHealthy:
		t.Fatalf("plan running state = %q/%v, want the probe's", plan.RunningVersion, plan.RunningHealthy)
	case plan.RestartFollows:
		t.Fatalf("plan restartFollows = true, want the deploy ladder's default until it ships")
	}

	// The token the plan returned is the store's outstanding row for the host,
	// and it validates: the mint behind the wire is durable.
	row, err := store.ValidateToken("m4", planned.Token)
	if err != nil {
		t.Fatalf("ValidateToken(%q): %v", planned.Token, err)
	}
	if row.Generation != live.Generation || row.IncarnationID != live.IncarnationID {
		t.Fatalf("stored token identity = %d/%q, want the registry's %d/%q", row.Generation, row.IncarnationID, live.Generation, live.IncarnationID)
	}
	if row.FactsRevision != plan.FactsRevision || row.TargetPath != plan.TargetPath ||
		row.HubTOMLFingerprint != plan.HubTOMLFingerprint || row.RunningVersion != plan.RunningVersion {
		t.Fatalf("stored token bindings = %+v, want the plan's", row)
	}

	// Unknown names and remote-originated requests are refused before anything
	// is planned, exactly like the settings mutations.
	var unknown appwire.HostPlanResult
	assertWireCode(t, client.Request(context.Background(), appwire.MethodEvenerHostPlan, appwire.HostPlanParams{Name: "nope"}, &unknown), appwire.CodeInvalidParams)
}

// TestHostPlanNoTokenArms pins every no-token arm this slice can reach: each
// names its reason, carries whether the host was attached, reports the right
// terminal flag, and mints nothing.
func TestHostPlanNoTokenArms(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "hub.toml")
	if err := os.WriteFile(configPath, []byte(planTestHubTOML([]hostreg.Host{planTestHost()})), 0o600); err != nil {
		t.Fatalf("write hub.toml: %v", err)
	}
	cases := []struct {
		name     string
		seams    planSeams
		entry    hostreg.Host
		detached bool
		reason   string
		attached bool
		terminal bool
	}{
		{
			name:   "a controller built from a dirty tree",
			seams:  planSeams{dirty: func() bool { return true }},
			reason: appwire.HostPlanReasonControllerDirty,
			// The dirty arm is read before any channel work, so it reports the
			// host as not attached: nothing was looked at.
			terminal: true,
		},
		{
			name:  "a host with no live channel",
			seams: planSeams{},
			// The manager without the attached-only seam reports every host
			// detached; the arm is retryable.
			detached: true,
			reason:   appwire.HostPlanReasonUnattached,
		},
		{
			name: "a facts refresh that fails",
			seams: planSeams{facts: func(context.Context, hostreg.Host) (hubcore.HostPlanFacts, error) {
				return hubcore.HostPlanFacts{}, errors.New("the channel dropped mid-read")
			}},
			reason:   appwire.HostPlanReasonRefreshFailed,
			attached: true,
		},
		{
			name: "facts that went stale before the mint",
			seams: planSeams{facts: func(_ context.Context, host hostreg.Host) (hubcore.HostPlanFacts, error) {
				stale := planTestFacts(host)
				stale.CapturedAt = time.Now().UTC().Add(-hostops.DefaultFreshnessBound - time.Minute)
				return stale, nil
			}},
			reason:   appwire.HostPlanReasonRefreshFailed,
			attached: true,
		},
		{
			name: "a running probe that fails",
			seams: planSeams{probe: func(context.Context, hostreg.Host, *appwire.Client) (hubcore.HostRuntimeProbe, error) {
				return hubcore.HostRuntimeProbe{}, errors.New("probe read timed out")
			}},
			reason:   appwire.HostPlanReasonProbeFailed,
			attached: true,
		},
		{
			name: "a remote that predates the probe handler",
			seams: planSeams{probe: func(context.Context, hostreg.Host, *appwire.Client) (hubcore.HostRuntimeProbe, error) {
				return hubcore.HostRuntimeProbe{}, fmt.Errorf("%w (running: method not found)", PlanHandlerAbsentError{})
			}},
			reason:   appwire.HostPlanReasonHandlerAbsent,
			attached: true,
		},
		{
			name:     "a run target a hub cannot serve",
			seams:    planSeams{},
			entry:    hostreg.Host{Name: "m4", SSH: "m4.example", User: "ops", EvenerPath: "/opt/evener-dev"},
			reason:   appwire.HostPlanReasonTargetMissingPrereq,
			attached: true,
			terminal: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			entry := tc.entry
			if entry.Name == "" {
				entry = planTestHost()
			}
			m, store, _ := planTestManager(t, configPath, []hostreg.Host{entry}, tc.seams)
			if tc.detached {
				m.cfg.clientIfAttached = nil
			}
			result, err := m.Plan(context.Background(), appwire.HostPlanParams{Name: "m4"})
			if err != nil {
				t.Fatalf("Plan: %v", err)
			}
			arm := planNoTokenReasonOf(t, result, tc.reason, tc.terminal)
			if arm.StaleFacts.Attached != tc.attached {
				t.Fatalf("attached = %v, want %v", arm.StaleFacts.Attached, tc.attached)
			}
			if _, ok := store.OutstandingToken("m4"); ok {
				t.Fatal("a refusing plan left a token behind")
			}
		})
	}
}

// TestHostPlanRestartFollowsUnverifiableRevision pins §6's one stated
// restart-follows rule: a probed unverifiable revision — `dev` or a dirty
// `<sha>-dirty` — reads as outdated, so the plan the human confirms says a
// restart follows. A verifiable revision does not.
func TestHostPlanRestartFollowsUnverifiableRevision(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "hub.toml")
	for _, tc := range []struct {
		revision string
		want     bool
	}{
		{"dev", true},
		{"0123abc-dirty", true},
		{"v1.2.3", false},
		{"0123abc", false},
	} {
		t.Run(tc.revision, func(t *testing.T) {
			seams := planSeams{probe: func(context.Context, hostreg.Host, *appwire.Client) (hubcore.HostRuntimeProbe, error) {
				probe := planTestProbe()
				probe.Version = tc.revision
				return probe, nil
			}}
			m, _, _ := planTestManager(t, configPath, []hostreg.Host{planTestHost()}, seams)
			result, err := m.Plan(context.Background(), appwire.HostPlanParams{Name: "m4"})
			if err != nil || result.HostPlanPlanned == nil {
				t.Fatalf("Plan = %+v/%v, want the planned arm", result, err)
			}
			if got := result.Plan.RestartFollows; got != tc.want {
				t.Fatalf("restartFollows = %v for revision %q, want %v", got, tc.revision, tc.want)
			}
		})
	}
}

// TestHostPlanNoProbeSeamIsHandlerAbsent pins the hub's honest answer while the
// gated probe ships: with no probe seam wired at all, a plan refuses
// handler-absent rather than inventing a running state.
func TestHostPlanNoProbeSeamIsHandlerAbsent(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "hub.toml")
	m, store, _ := planTestManager(t, configPath, []hostreg.Host{planTestHost()}, planSeams{})
	m.cfg.planProbe = nil
	result, err := m.Plan(context.Background(), appwire.HostPlanParams{Name: "m4"})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	planNoTokenReasonOf(t, result, appwire.HostPlanReasonHandlerAbsent, false)
	if _, ok := store.OutstandingToken("m4"); ok {
		t.Fatal("a plan with no probe seam stored a token")
	}
}

// TestHostPlanSupersedesOutstandingToken pins supersede-on-mint through the
// handler: a second plan replaces the first token, and the first reads as
// superseded, never as valid and never as missing.
func TestHostPlanSupersedesOutstandingToken(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "hub.toml")
	if err := os.WriteFile(configPath, []byte(planTestHubTOML([]hostreg.Host{planTestHost()})), 0o600); err != nil {
		t.Fatalf("write hub.toml: %v", err)
	}
	m, store, _ := planTestManager(t, configPath, []hostreg.Host{planTestHost()}, planSeams{})
	first, err := m.Plan(context.Background(), appwire.HostPlanParams{Name: "m4"})
	if err != nil || first.HostPlanPlanned == nil {
		t.Fatalf("first Plan = %+v/%v, want the planned arm", first, err)
	}
	second, err := m.Plan(context.Background(), appwire.HostPlanParams{Name: "m4"})
	if err != nil || second.HostPlanPlanned == nil {
		t.Fatalf("second Plan = %+v/%v, want the planned arm", second, err)
	}
	firstToken, secondToken := first.Token, second.Token
	if firstToken == secondToken {
		t.Fatal("two plans minted the same token")
	}
	if _, err := store.ValidateToken("m4", firstToken); !errors.Is(err, hostops.ErrTokenSuperseded) {
		t.Fatalf("the superseded token = %v, want ErrTokenSuperseded", err)
	}
	if _, err := store.ValidateToken("m4", secondToken); err != nil {
		t.Fatalf("the current token does not validate: %v", err)
	}
}

// TestHostPlanRefusesDriftAndConcurrentTerminalOp pins §6 step 2's re-checks as
// far as this slice can run them: an entry that moves and an operation that
// finishes while the plan is being built are typed stale-entry refusals with the
// binding named, and nothing is minted.
func TestHostPlanRefusesDriftAndConcurrentTerminalOp(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "hub.toml")
	if err := os.WriteFile(configPath, []byte(planTestHubTOML([]hostreg.Host{planTestHost()})), 0o600); err != nil {
		t.Fatalf("write hub.toml: %v", err)
	}

	t.Run("the entry moves while the plan is being built", func(t *testing.T) {
		m, store, registry := planTestManager(t, configPath, []hostreg.Host{planTestHost()}, planSeams{})
		m.cfg.planFacts = func(_ context.Context, host hostreg.Host) (hubcore.HostPlanFacts, error) {
			edited := host
			edited.SSH = "m4.new.example"
			if err := registry.Update(edited); err != nil {
				return hubcore.HostPlanFacts{}, err
			}
			return planTestFacts(host), nil
		}
		_, err := m.Plan(context.Background(), appwire.HostPlanParams{Name: "m4"})
		if data := wantStaleEntry(t, err); data.Binding != appwire.StaleEntryBindingGeneration {
			t.Fatalf("binding = %q, want %q", data.Binding, appwire.StaleEntryBindingGeneration)
		}
		if _, ok := store.OutstandingToken("m4"); ok {
			t.Fatal("a plan against a moved entry stored a token")
		}
	})

	t.Run("an operation on the host finishes while the plan is being built", func(t *testing.T) {
		m, store, registry := planTestManager(t, configPath, []hostreg.Host{planTestHost()}, planSeams{})
		m.cfg.planFacts = func(_ context.Context, host hostreg.Host) (hubcore.HostPlanFacts, error) {
			live, ok := registry.Get(host.Name)
			if !ok {
				return hubcore.HostPlanFacts{}, errors.New("host vanished")
			}
			record, err := store.Create(hostops.NewRecord{
				ClientOperationID: "client-1", Host: host.Name, Kind: hostops.KindDeploy,
				Generation: live.Generation, IncarnationID: live.IncarnationID,
			})
			if err != nil {
				return hubcore.HostPlanFacts{}, err
			}
			if _, err := store.Transition(record.ID, hostops.StateComplete, nil); err != nil {
				return hubcore.HostPlanFacts{}, err
			}
			return planTestFacts(host), nil
		}
		_, err := m.Plan(context.Background(), appwire.HostPlanParams{Name: "m4"})
		if data := wantStaleEntry(t, err); data.Binding != appwire.StaleEntryBindingConcurrentTerminalOp {
			t.Fatalf("binding = %q, want %q", data.Binding, appwire.StaleEntryBindingConcurrentTerminalOp)
		}
		if _, ok := store.OutstandingToken("m4"); ok {
			t.Fatal("a plan after a concurrent terminal operation stored a token")
		}
	})
}

// TestHostPlanRefusesRemoteOriginAndUnknownNames directly pins the two
// refusals every host-management method owes: the origin guard and the unknown
// name.
func TestHostPlanRefusesRemoteOriginAndUnknownNames(t *testing.T) {
	m, _, _ := planTestManager(t, filepath.Join(t.TempDir(), "hub.toml"), []hostreg.Host{planTestHost()}, planSeams{})
	remote := withHostRoutingOrigin(context.Background(), hostRoutingOriginBridge)
	assertWireCode(t, errOf(m.Plan(remote, appwire.HostPlanParams{Name: "m4"})), appwire.CodeInvalidParams)
	assertWireCode(t, errOf(m.Plan(context.Background(), appwire.HostPlanParams{Name: "nope"})), appwire.CodeInvalidParams)
	assertWireCode(t, errOf(m.Plan(context.Background(), appwire.HostPlanParams{Name: "  "})), appwire.CodeInvalidParams)
}

// TestPlanNoTokenBuilderCoversTheSpecSet pins the builder against §10's closed
// reason set: every reason renders, the terminal flag is exactly the spec's four
// terminal arms, and a reason outside the set is refused rather than rendered
// with a wrong flag.
func TestPlanNoTokenBuilderCoversTheSpecSet(t *testing.T) {
	terminal := map[string]bool{
		appwire.HostPlanReasonControllerDirty:     true,
		appwire.HostPlanReasonTargetUnwritable:    true,
		appwire.HostPlanReasonTargetMissingPrereq: true,
		appwire.HostPlanReasonTargetUnitFindings:  true,
		appwire.HostPlanReasonUnattached:          false,
		appwire.HostPlanReasonRefreshFailed:       false,
		appwire.HostPlanReasonProbeFailed:         false,
		appwire.HostPlanReasonHandlerAbsent:       false,
		appwire.HostPlanReasonRemnantOpen:         false,
	}
	for reason, wantTerminal := range terminal {
		result, err := planNoToken(reason, "because", true)
		if err != nil {
			t.Fatalf("planNoToken(%q): %v", reason, err)
		}
		planNoTokenReasonOf(t, result, reason, wantTerminal)
	}
	if _, err := planNoToken("not-a-reason", "because", true); err == nil {
		t.Fatal("planNoToken accepted a reason outside §10's set")
	}
}

// errOf returns the error half of a (result, error) pair, failing the test when
// no error came back.
func errOf(result appwire.HostPlanResult, err error) error {
	if err == nil {
		return fmt.Errorf("call returned %+v, want an error", result)
	}
	return err
}

// wantStaleEntry fails the test unless err is §11's stale-entry refusal, and
// returns its data.
func wantStaleEntry(t *testing.T, err error) appwire.StaleEntryErrorData {
	t.Helper()
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("err = %T (%v), want an appwire.WireError", err, err)
	}
	if wire.Code != appwire.CodeConflict {
		t.Fatalf("code = %d, want the conflict class %d", wire.Code, appwire.CodeConflict)
	}
	data, ok := wire.Data.(appwire.StaleEntryErrorData)
	if !ok {
		t.Fatalf("data = %T, want appwire.StaleEntryErrorData", wire.Data)
	}
	if data.EvenerErrorInfo != appwire.ErrorStaleEntry {
		t.Fatalf("evenerErrorInfo = %q, want %q", data.EvenerErrorInfo, appwire.ErrorStaleEntry)
	}
	return data
}
