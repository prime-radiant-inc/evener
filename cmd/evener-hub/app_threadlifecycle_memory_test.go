package hub

import (
	"context"
	"slices"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/appwire/appwiretest"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/launchconfig"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/rendezvous"
)

func memoryTestSpawner(got *hubcore.SpawnRequest, threadID string) *fakeRPCSpawner {
	return &fakeRPCSpawner{
		spawn: func(_ context.Context, req hubcore.SpawnRequest) (rendezvous.Entry, error) {
			*got = req
			return rendezvous.Entry{
				PID:       210,
				Protocol:  appwire.ProtocolVersion,
				SourceID:  "local",
				ThreadID:  threadID,
				SessionID: threadID,
			}, nil
		},
	}
}

// TestThreadStart_MemoryOverrideReachesSpawn pins the whole launch path for a
// per-session memory opt-out: the launch override survives layer resolution to
// the spawn request, and the argv the daemon receives names the disable flag.
func TestThreadStart_MemoryOverrideReachesSpawn(t *testing.T) {
	var got hubcore.SpawnRequest
	hub := newHubRPCTestServer(t, hubcore.WebConfig{
		RunDir:       t.TempDir(),
		HubStateRoot: t.TempDir(),
		Spawner:      memoryTestSpawner(&got, "th_memory_off"),
		Past:         hubcore.NewPastIndex(""),
	})
	defer hub.Close()
	client := dialHubRPCInitialized(t, hub)
	if _, err := appwiretest.ThreadStart(context.Background(), client, appwire.ThreadStartParams{
		Model: "openai/gpt-5",
		CWD:   "/tmp",
		LaunchOverrides: &appwire.LaunchConfigLayer{
			MemoryEnabled: new(false),
		},
	}); err != nil {
		t.Fatalf("ThreadStart: %v", err)
	}
	eff := got.Resolved.Effective
	if eff.MemoryEnabled == nil || *eff.MemoryEnabled {
		t.Fatalf("MemoryEnabled = %v at the spawn seam, want false", eff.MemoryEnabled)
	}
	args := launchconfig.ToArgs(got.Resolved)
	if !slices.Contains(args, "--disable-memory=true") {
		t.Fatalf("spawn argv %v, want --disable-memory=true", args)
	}
}

// TestThreadStart_MemoryEnabledOverGlobalDefault: a global layer that turns
// memory off must not strand the user — a per-launch override re-enables it,
// and the argv then carries the explicit enable, not the disable.
func TestThreadStart_MemoryEnabledOverGlobalDefault(t *testing.T) {
	launchRoot := t.TempDir()
	cwd := t.TempDir()
	c := newHubLaunchController(launchRoot, false)
	if _, err := c.SetLayer(context.Background(), appwire.LaunchConfigSetLayerParams{
		CWD:   cwd,
		Layer: "global",
		Config: appwire.LaunchConfigLayer{
			MemoryEnabled: new(false),
		},
	}); err != nil {
		t.Fatalf("SetLayer: %v", err)
	}
	var got hubcore.SpawnRequest
	hub := newHubRPCTestServer(t, hubcore.WebConfig{
		RunDir:           t.TempDir(),
		HubStateRoot:     t.TempDir(),
		LaunchConfigRoot: launchRoot,
		Spawner:          memoryTestSpawner(&got, "th_memory_on"),
		Past:             hubcore.NewPastIndex(""),
	})
	defer hub.Close()
	client := dialHubRPCInitialized(t, hub)
	if _, err := appwiretest.ThreadStart(context.Background(), client, appwire.ThreadStartParams{
		Model: "openai/gpt-5",
		CWD:   cwd,
		LaunchOverrides: &appwire.LaunchConfigLayer{
			MemoryEnabled: new(true),
		},
	}); err != nil {
		t.Fatalf("ThreadStart: %v", err)
	}
	eff := got.Resolved.Effective
	if eff.MemoryEnabled == nil || !*eff.MemoryEnabled {
		t.Fatalf("MemoryEnabled = %v, want launch true over global false", eff.MemoryEnabled)
	}
	args := launchconfig.ToArgs(got.Resolved)
	if !slices.Contains(args, "--disable-memory=false") {
		t.Fatalf("spawn argv %v, want --disable-memory=false", args)
	}
	if slices.Contains(args, "--disable-memory=true") {
		t.Fatalf("spawn argv %v, must not disable memory", args)
	}
}

// TestResumeRequestCarriesNoMemoryLaunchLayer pins the resume side of the
// opt-out contract: resume resolves launch layers only for api_log, and a
// session's memory state stays whatever its saved meta says. Carrying
// memory_enabled here would silently re-decide a settled choice.
func TestResumeRequestCarriesNoMemoryLaunchLayer(t *testing.T) {
	// The launch root holds a global layer that disables memory and turns on
	// api_log: a resume that consulted memory_enabled the way it consults
	// api_log would carry the disable into a session whose saved meta already
	// settled the choice.
	launchRoot := t.TempDir()
	cwd := t.TempDir()
	c := newHubLaunchController(launchRoot, false)
	if _, err := c.SetLayer(context.Background(), appwire.LaunchConfigSetLayerParams{
		CWD:   cwd,
		Layer: "global",
		Config: appwire.LaunchConfigLayer{
			MemoryEnabled: new(false),
			APILog:        new(true),
		},
	}); err != nil {
		t.Fatalf("SetLayer: %v", err)
	}
	past := hubcore.NewPastIndex("")
	id := identifier.MustNewSessionID()
	past.SeedForTest([]schema.SessionMeta{{
		ID:        id,
		ProfileID: "openai",
		Model:     "gpt-test",
		EnvInfo:   schema.EnvironmentInfo{WorkingDir: cwd},
	}})
	req, err := resumeRequestForConfig(hubcore.WebConfig{Past: past, LaunchConfigRoot: launchRoot}, id)
	if err != nil {
		t.Fatalf("resumeRequestForConfig: %v", err)
	}
	if req.Resolved.Effective.Model == "" {
		t.Fatal("resume request carries no model; the meta-driven branch did not run")
	}
	if req.Resolved.Effective.APILog == nil || !*req.Resolved.Effective.APILog {
		t.Fatal("resume request carries no api_log; the launch layer resolution did not run")
	}
	if req.Resolved.Effective.MemoryEnabled != nil {
		t.Fatalf("resume Resolved.MemoryEnabled = %v, want nil: resume leaves memory to the session's saved meta", req.Resolved.Effective.MemoryEnabled)
	}
	if args := buildResumeArgs(req); slices.ContainsFunc(args, func(arg string) bool { return strings.Contains(arg, "disable-memory") }) {
		t.Fatalf("resume argv %v, want no memory token: the session's saved meta keeps the choice", args)
	}
}
