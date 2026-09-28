package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/sandbox"
	"primeradiant.com/evener/envvars"
	"primeradiant.com/evener/llm"
)

func boolPtr(b bool) *bool { return &b }

// enforceableSandboxHost is a host that can serve the full mode matrix: bwrap
// present and able to create its namespaces. Tests use it instead of probing
// the live host so the assertion never depends on the machine.
func enforceableSandboxHost(t *testing.T) sandbox.HostFacts {
	t.Helper()
	return sandbox.HostFacts{OS: "linux", Home: t.TempDir(), BwrapPath: "/bin/true", BwrapCapable: true}
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
}

// TestProvisionLiveSandboxEnforcesAndFailsClosed: off is a no-op; a declared
// mode on an executable host confines the environment; a declared mode on a
// host that cannot enforce fails closed and leaves the env unsandboxed.
func TestProvisionLiveSandboxEnforcesAndFailsClosed(t *testing.T) {
	work := t.TempDir()

	// Off: no host probe, env stays unsandboxed.
	env := execenv.NewLocalExecutionEnvironment(work)
	if err := provisionLiveSandbox(env, &agent.SessionConfig{}, work); err != nil {
		t.Fatalf("off provisioning: %v", err)
	}
	if env.Sandbox != nil || env.Wrapper != nil {
		t.Fatalf("off env must stay unsandboxed, got Sandbox=%v Wrapper=%v", env.Sandbox, env.Wrapper)
	}

	// Declared + enforceable host: the env's file tools and shell are confined.
	env = execenv.NewLocalExecutionEnvironment(work)
	cfg := agent.SessionConfig{Sandbox: "read-only", SandboxNet: boolPtr(true)}
	if err := provisionLiveSandboxWithHost(env, &cfg, work, enforceableSandboxHost(t)); err != nil {
		t.Fatalf("enforceable provisioning: %v", err)
	}
	if env.Sandbox == nil || !env.Sandbox.Enforced() {
		t.Fatalf("declared mode must confine the env, got %+v", env.Sandbox)
	}

	// Declared + a host that cannot enforce: fail closed (a typed refusal) and
	// leave the env unsandboxed so nothing can run native by accident.
	env = execenv.NewLocalExecutionEnvironment(work)
	cfg = agent.SessionConfig{Sandbox: "read-only", SandboxNet: boolPtr(true)}
	err := provisionLiveSandboxWithHost(env, &cfg, work, sandbox.HostFacts{OS: "linux"})
	if err == nil {
		t.Fatal("a missing sandbox backend must fail closed")
	}
	var refusal *sandbox.RefusalError
	if !errors.As(err, &refusal) {
		t.Fatalf("want *sandbox.RefusalError, got %T: %v", err, err)
	}
	if env.Sandbox != nil || env.Wrapper != nil {
		t.Fatalf("failed provisioning must leave the env unsandboxed, got Sandbox=%v Wrapper=%v", env.Sandbox, env.Wrapper)
	}
}

// TestCLIProbeArgsForwardSandbox: a declared mode reaches the spawned evener so
// the CLI harness confines its worker too; the default (off) forwards nothing.
func TestCLIProbeArgsForwardSandbox(t *testing.T) {
	args := cliProbeArgs(runConfig{model: "openai/m", sandbox: "restricted", sandboxNet: "off"}, probeFile{Prompt: "p"}, probeResult{WorkDir: "/w", StateDir: "/s"})
	assertSubsequence(t, args, []string{"--sandbox", "restricted", "--sandbox-net", "off"})

	off := cliProbeArgs(runConfig{model: "openai/m"}, probeFile{}, probeResult{})
	for _, a := range off {
		if a == "--sandbox" || a == "--sandbox-net" {
			t.Fatalf("off must not forward sandbox flags, got %v", off)
		}
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
	var refusal *sandbox.RefusalError
	if !errors.As(err, &refusal) {
		t.Fatalf("want *sandbox.RefusalError, got %T: %v", err, err)
	}
	if sessionCalled {
		t.Fatal("the session must not be created before the fail-closed refusal")
	}
}
