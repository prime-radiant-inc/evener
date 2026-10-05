package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/sandbox"
	"primeradiant.com/evener/envvars"
	"primeradiant.com/evener/llm"
)

// enforceableSandboxHost is a host that can serve the full mode matrix: bwrap
// present and able to create its namespaces. Tests use it instead of probing
// the live host so the assertion never depends on the machine.
func enforceableSandboxHost(t *testing.T) sandbox.HostFacts {
	t.Helper()
	return sandbox.HostFacts{OS: "linux", Home: t.TempDir(), BwrapPath: "/bin/true", BwrapCapable: true}
}

// newSandboxTestEnv builds a local environment rooted at dir and registers its
// teardown, so a provisioned scratch (lease and directory) never outlives the
// test.
func newSandboxTestEnv(t *testing.T, dir string) *execenv.LocalExecutionEnvironment {
	t.Helper()
	env := execenv.NewLocalExecutionEnvironment(dir)
	t.Cleanup(func() {
		env.Cleanup()
		_ = env.DisposeSessionScratch()
	})
	return env
}

// TestConfigureLiveSandbox: the live harness's sandbox carrier is off unless a
// mode is declared, records mode + network when one is, and rejects an unknown
// mode or net value instead of silently falling back to a native run.
func TestConfigureLiveSandbox(t *testing.T) {
	// Off (empty, the flag default) leaves the carrier zero, so a default live
	// run is byte-identical to today.
	cfg := agent.SessionConfig{}
	if err := configureLiveSandbox(runConfig{}, &cfg); err != nil {
		t.Fatalf("off mode: %v", err)
	}
	if cfg.Sandbox != "" || cfg.SandboxNet != nil {
		t.Fatalf("off mode must leave the carrier zero, got Sandbox=%q Net=%v", cfg.Sandbox, cfg.SandboxNet)
	}

	// A declared mode records the mode and the resolved network decision so
	// provisioning can enforce exactly what was asked.
	cfg = agent.SessionConfig{}
	if err := configureLiveSandbox(runConfig{sandbox: "read-only", sandboxNet: "off"}, &cfg); err != nil {
		t.Fatalf("declared mode: %v", err)
	}
	if cfg.Sandbox != "read-only" {
		t.Fatalf("declared mode = %q, want read-only", cfg.Sandbox)
	}
	if cfg.SandboxNet == nil || *cfg.SandboxNet {
		t.Fatalf("declared net = %v, want false", cfg.SandboxNet)
	}

	// An unknown mode is a legible error, not a silent native run.
	cfg = agent.SessionConfig{}
	if err := configureLiveSandbox(runConfig{sandbox: "bogus"}, &cfg); err == nil {
		t.Fatal("unknown sandbox mode must error, not run native")
	}
	// An unknown net value errors too.
	if err := configureLiveSandbox(runConfig{sandbox: "read-only", sandboxNet: "yes"}, &cfg); err == nil {
		t.Fatal("unknown sandbox-net value must error")
	}
	// --sandbox-net is validated even when the mode is off, matching the
	// production CLI's configureSandbox.
	if err := configureLiveSandbox(runConfig{sandbox: "off", sandboxNet: "yes"}, &agent.SessionConfig{}); err == nil {
		t.Fatal("off mode must still validate --sandbox-net")
	}
}

// TestProvisionLiveSandboxEnforcesAndFailsClosed: off is a no-op; a declared
// mode on an executable host confines the environment; a declared mode on a
// host that cannot enforce fails closed and leaves the env unsandboxed.
func TestProvisionLiveSandboxEnforcesAndFailsClosed(t *testing.T) {
	work := t.TempDir()

	// Off: no host probe, env stays unsandboxed.
	env := newSandboxTestEnv(t, work)
	if err := provisionLiveSandbox(env, &agent.SessionConfig{}, work); err != nil {
		t.Fatalf("off provisioning: %v", err)
	}
	if env.Sandbox != nil || env.Wrapper != nil {
		t.Fatalf("off env must stay unsandboxed, got Sandbox=%v Wrapper=%v", env.Sandbox, env.Wrapper)
	}

	// Declared + enforceable host: the env's file tools and shell are confined.
	env = newSandboxTestEnv(t, work)
	cfg := agent.SessionConfig{Sandbox: "read-only", SandboxNet: new(true)}
	if err := provisionLiveSandboxWithHost(env, &cfg, work, enforceableSandboxHost(t)); err != nil {
		t.Fatalf("enforceable provisioning: %v", err)
	}
	if env.Sandbox == nil || !env.Sandbox.Enforced() {
		t.Fatalf("declared mode must confine the env, got %+v", env.Sandbox)
	}

	// Declared + a host that cannot enforce: fail closed (a typed refusal) and
	// leave the env unsandboxed so nothing can run native by accident.
	env = newSandboxTestEnv(t, work)
	cfg = agent.SessionConfig{Sandbox: "read-only", SandboxNet: new(true)}
	err := provisionLiveSandboxWithHost(env, &cfg, work, sandbox.HostFacts{OS: "linux"})
	if err == nil {
		t.Fatal("a missing sandbox backend must fail closed")
	}
	if _, ok := errors.AsType[*sandbox.RefusalError](err); !ok {
		t.Fatalf("want *sandbox.RefusalError, got %T: %v", err, err)
	}
	if env.Sandbox != nil || env.Wrapper != nil {
		t.Fatalf("failed provisioning must leave the env unsandboxed, got Sandbox=%v Wrapper=%v", env.Sandbox, env.Wrapper)
	}

	// Declared + a host that resolves but has no backend binary: EnableSandbox
	// mints the session scratch and then refuses. It must dispose that mint on
	// the way out, so a failed probe leaves no scratch directory or lease behind
	// (the same invariant cmd/evener's TestLaunchProvisioningFailureLeavesNoScratch
	// pins for the production launch path).
	env = newSandboxTestEnv(t, work)
	cfg = agent.SessionConfig{Sandbox: "read-only", SandboxNet: new(true)}
	err = provisionLiveSandboxWithHost(env, &cfg, work, sandbox.HostFacts{OS: "linux", Home: t.TempDir(), BwrapCapable: true})
	if err == nil {
		t.Fatal("a host with no backend binary must fail closed")
	}
	if _, ok := errors.AsType[*sandbox.RefusalError](err); !ok {
		t.Fatalf("want *sandbox.RefusalError, got %T: %v", err, err)
	}
	if scratch := env.SessionScratchDir(); scratch != "" {
		t.Fatalf("failed provisioning leaked scratch %q", scratch)
	}
}

// TestCLIProbeArgsForwardSandbox: a declared mode reaches the spawned evener so
// the CLI harness confines its worker too; the default (off) forwards nothing.
func TestCLIProbeArgsForwardSandbox(t *testing.T) {
	args, err := cliProbeArgs(runConfig{model: "openai/m", sandbox: "restricted", sandboxNet: "off"}, probeFile{Prompt: "p"}, probeResult{WorkDir: "/w", StateDir: "/s"})
	if err != nil {
		t.Fatalf("cliProbeArgs: %v", err)
	}
	assertSubsequence(t, args, []string{"--sandbox", "restricted", "--sandbox-net", "off"})

	// The forwarded mode is normalized to the wire name the child expects.
	normalized, err := cliProbeArgs(runConfig{model: "openai/m", sandbox: "READ-ONLY"}, probeFile{}, probeResult{})
	if err != nil {
		t.Fatalf("cliProbeArgs: %v", err)
	}
	assertSubsequence(t, normalized, []string{"--sandbox", "read-only"})

	off, err := cliProbeArgs(runConfig{model: "openai/m"}, probeFile{}, probeResult{})
	if err != nil {
		t.Fatalf("cliProbeArgs: %v", err)
	}
	for _, a := range off {
		if a == "--sandbox" || a == "--sandbox-net" {
			t.Fatalf("off must not forward sandbox flags, got %v", off)
		}
	}

	// An invalid mode is an error, not a silent drop into an unsandboxed run.
	if _, err := cliProbeArgs(runConfig{model: "openai/m", sandbox: "bogus"}, probeFile{}, probeResult{}); err == nil {
		t.Fatal("an invalid sandbox mode must make cliProbeArgs fail closed")
	}
}

// TestRunLiveProbeFailsClosedBeforeSession: when a sandbox mode is declared but
// the host cannot enforce it, runLiveProbe returns the refusal BEFORE creating
// the session, so no provider call can run outside the requested sandbox.
func TestRunLiveProbeFailsClosedBeforeSession(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(envvars.XDGConfigHome.Name, dir)

	oldLoad := runnerLoadClient
	oldAttach := runnerAttachAPILogger
	oldNew := runnerNewSession
	oldHost := runnerProbeSandboxHost
	t.Cleanup(func() {
		runnerLoadClient = oldLoad
		runnerAttachAPILogger = oldAttach
		runnerNewSession = oldNew
		runnerProbeSandboxHost = oldHost
	})

	runnerLoadClient = func(string) (*llm.Client, error) { return llm.NewClient(), nil }
	runnerAttachAPILogger = func(*llm.Client, string, io.Writer, ...string) (func() error, error) {
		return func() error { return nil }, nil
	}
	runnerProbeSandboxHost = func() sandbox.HostFacts { return sandbox.HostFacts{OS: "linux"} }

	sessionCalled := false
	runnerNewSession = func(*llm.Client, *provider.Profile, execenv.ExecutionEnvironment, agent.SessionConfig) (*agent.Session, error) {
		sessionCalled = true
		return nil, errors.New("session must not be reached")
	}

	res := probeResult{StateDir: filepath.Join(dir, "state"), WorkDir: filepath.Join(dir, "work")}
	err := runLiveProbe(context.Background(), runConfig{model: "openai/m", reasoningEffort: "low", sandbox: "read-only"}, probeFile{Prompt: "hi"}, &res, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("runLiveProbe must fail closed when the declared sandbox backend is unavailable")
	}
	if _, ok := errors.AsType[*sandbox.RefusalError](err); !ok {
		t.Fatalf("want *sandbox.RefusalError, got %T: %v", err, err)
	}
	if sessionCalled {
		t.Fatal("the session must not be created before the fail-closed refusal")
	}
}

type failingLiveAdapter struct{}

func (failingLiveAdapter) Name() string { return "openai" }
func (failingLiveAdapter) Complete(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{}, &llm.ConfigurationError{Message: "no provider in this test"}
}
func (failingLiveAdapter) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, &llm.ConfigurationError{Message: "no provider in this test"}
}

// A live probe's session is named, so its scratch outlives the session's Close,
// and no hub ever archives a probe. The harness removes the session's scratch
// tree itself once the probe ends, as a one-shot run does at exit.
func TestRunLiveProbeRemovesItsSessionScratch(t *testing.T) {
	// Resolved, so the scratch path the probe reports matches what os.Stat sees
	// through macOS's symlinked temp dir.
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(envvars.XDGConfigHome.Name, dir)
	t.Setenv("TMPDIR", dir)

	oldLoad := runnerLoadClient
	oldAttach := runnerAttachAPILogger
	oldNew := runnerNewSession
	t.Cleanup(func() {
		runnerLoadClient = oldLoad
		runnerAttachAPILogger = oldAttach
		runnerNewSession = oldNew
	})
	client := llm.NewClient()
	client.Register(failingLiveAdapter{})
	runnerLoadClient = func(string) (*llm.Client, error) { return client, nil }
	runnerAttachAPILogger = func(*llm.Client, string, io.Writer, ...string) (func() error, error) {
		return func() error { return nil }, nil
	}
	var scratch string
	runnerNewSession = func(c *llm.Client, p *provider.Profile, env execenv.ExecutionEnvironment, cfg agent.SessionConfig) (*agent.Session, error) {
		sess, err := agent.NewSession(c, p, env, cfg)
		if err != nil {
			return nil, err
		}
		local, _ := env.(*execenv.LocalExecutionEnvironment)
		if _, err := local.ExecCommand(context.Background(), "true", 5000, "", nil); err != nil {
			t.Errorf("mint the probe session's scratch: %v", err)
		}
		scratch = local.SessionScratchDir()
		return sess, nil
	}

	res := probeResult{StateDir: filepath.Join(dir, "state"), WorkDir: filepath.Join(dir, "work")}
	for _, d := range []string{res.StateDir, res.WorkDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	_ = runLiveProbe(context.Background(), runConfig{model: "openai/m", reasoningEffort: "low"}, probeFile{Prompt: "hi"}, &res, &bytes.Buffer{}, &bytes.Buffer{})
	if !strings.Contains(scratch, "evener-scratch-") {
		t.Fatalf("probe session scratch = %q, want a named scratch in the session's tree", scratch)
	}
	if _, err := os.Stat(filepath.Dir(scratch)); !os.IsNotExist(err) {
		t.Errorf("the probe left its session scratch tree %s (stat: %v)", filepath.Dir(scratch), err)
	}
}

// TestRunLiveProbeSettlesScratchOnSessionFailure: after the sandbox provisions a
// scratch for the live env, a failing session creation must settle that scratch
// (via DisposeRootScratchAfterFailure) rather than leak the directory and its
// lease.
func TestRunLiveProbeSettlesScratchOnSessionFailure(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(envvars.XDGConfigHome.Name, dir)

	oldLoad := runnerLoadClient
	oldAttach := runnerAttachAPILogger
	oldNew := runnerNewSession
	oldHost := runnerProbeSandboxHost
	t.Cleanup(func() {
		runnerLoadClient = oldLoad
		runnerAttachAPILogger = oldAttach
		runnerNewSession = oldNew
		runnerProbeSandboxHost = oldHost
	})

	runnerLoadClient = func(string) (*llm.Client, error) { return llm.NewClient(), nil }
	runnerAttachAPILogger = func(*llm.Client, string, io.Writer, ...string) (func() error, error) {
		return func() error { return nil }, nil
	}
	runnerProbeSandboxHost = func() sandbox.HostFacts { return enforceableSandboxHost(t) }

	var provisioned *execenv.LocalExecutionEnvironment
	runnerNewSession = func(_ *llm.Client, _ *provider.Profile, env execenv.ExecutionEnvironment, _ agent.SessionConfig) (*agent.Session, error) {
		provisioned, _ = env.(*execenv.LocalExecutionEnvironment)
		return nil, errors.New("session creation failed")
	}

	res := probeResult{StateDir: filepath.Join(dir, "state"), WorkDir: filepath.Join(dir, "work")}
	for _, d := range []string{res.StateDir, res.WorkDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	err := runLiveProbe(context.Background(), runConfig{model: "openai/m", reasoningEffort: "low", sandbox: "restricted"}, probeFile{Prompt: "hi"}, &res, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("runLiveProbe must return the session-creation error")
	}
	if provisioned == nil {
		t.Fatalf("the session constructor was never reached (runLiveProbe error: %v)", err)
	}
	// An enforced env keeps reporting the wrapper's scratch path after its
	// allocation is settled, so assert on the directory itself: the scratch
	// must be gone, not merely unreported.
	if scratch := provisioned.SessionScratchDir(); scratch != "" {
		if _, statErr := os.Stat(scratch); !os.IsNotExist(statErr) {
			t.Fatalf("a failed session leaked the provisioned scratch %q (stat: %v)", scratch, statErr)
		}
	}
}
