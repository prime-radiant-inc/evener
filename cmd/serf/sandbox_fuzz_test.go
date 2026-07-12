//go:build serffuzz

package main

import (
	"errors"
	"strings"
	"testing"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/sandbox"
)

func FuzzSandboxHelpers(f *testing.F) {
	for scenario := uint8(0); scenario < 11; scenario++ {
		f.Add(scenario)
	}

	f.Fuzz(func(t *testing.T, scenario uint8) {
		switch scenario % 11 {
		case 0:
			cfg := agent.SessionConfig{}
			if err := configureSandbox(&cfg, "", ""); err != nil {
				t.Fatal(err)
			}
			if cfg.Sandbox != "" || cfg.SandboxNet != nil {
				t.Fatalf("off carrier changed: %#v", cfg)
			}
		case 1:
			if err := configureSandbox(&agent.SessionConfig{}, "invalid", "on"); err == nil {
				t.Fatal("invalid mode accepted")
			}
		case 2:
			if err := configureSandbox(&agent.SessionConfig{}, "off", "invalid"); err == nil {
				t.Fatal("invalid network mode accepted")
			}
		case 3:
			cfg := agent.SessionConfig{}
			if err := configureSandbox(&cfg, " restricted ", " OFF "); err != nil {
				t.Fatal(err)
			}
			if cfg.Sandbox != "restricted" || cfg.SandboxNet == nil || *cfg.SandboxNet {
				t.Fatalf("unexpected carrier: %#v", cfg)
			}
		case 4:
			probed := false
			oldProbe := probeSandboxHost
			probeSandboxHost = func() sandbox.HostFacts {
				probed = true
				return sandbox.HostFacts{}
			}
			t.Cleanup(func() { probeSandboxHost = oldProbe })
			env := execenv.NewLocalExecutionEnvironment(t.TempDir())
			if err := provisionSandbox(env, &agent.SessionConfig{}, env.WorkingDirectory()); err != nil {
				t.Fatal(err)
			}
			if probed {
				t.Fatal("off mode probed host")
			}
		case 5:
			cwd := t.TempDir()
			env := execenv.NewLocalExecutionEnvironment(cwd)
			t.Cleanup(env.Cleanup)
			oldProbe := probeSandboxHost
			probeSandboxHost = func() sandbox.HostFacts {
				return sandbox.HostFacts{OS: "linux", Home: t.TempDir(), BwrapPath: "/bin/true", BwrapCapable: true}
			}
			t.Cleanup(func() { probeSandboxHost = oldProbe })
			net := true
			if err := provisionSandbox(env, &agent.SessionConfig{Sandbox: "read-only", SandboxNet: &net}, cwd); err != nil {
				t.Fatal(err)
			}
			if env.Sandbox == nil || env.Wrapper == nil {
				t.Fatal("sandbox was not provisioned")
			}
		case 6:
			cwd := t.TempDir()
			env := execenv.NewLocalExecutionEnvironment(cwd)
			if err := provisionSandboxWithHost(env, &agent.SessionConfig{Sandbox: "invalid"}, cwd, sandbox.HostFacts{}); err == nil {
				t.Fatal("invalid persisted mode accepted")
			}
			if err := provisionSandboxWithHost(env, &agent.SessionConfig{}, cwd, sandbox.HostFacts{}); err != nil {
				t.Fatal(err)
			}
		case 7:
			cwd := t.TempDir()
			env := execenv.NewLocalExecutionEnvironment(cwd)
			net := false
			err := provisionSandboxWithHost(env, &agent.SessionConfig{Sandbox: "restricted", SandboxNet: &net}, cwd, sandbox.HostFacts{OS: "linux"})
			if !errors.As(err, new(*sandbox.RefusalError)) {
				t.Fatalf("got %v, want refusal", err)
			}
		case 8:
			cfg := agent.SessionConfig{Sandbox: "restricted"}
			reconcileClearSandbox(&cfg, nil)
			env := execenv.NewLocalExecutionEnvironment(t.TempDir())
			reconcileClearSandbox(&cfg, env)
			off := sandbox.ResolvedPolicy{}
			env.Sandbox = &off
			reconcileClearSandbox(&cfg, env)

			net := false
			rp, err := sandbox.Resolve(sandbox.SandboxPolicy{Mode: sandbox.ModeRestricted, Network: &net}, sandbox.HostFacts{OS: "linux", Home: t.TempDir(), BwrapPath: "/bin/true", BwrapCapable: true}, env.WorkingDirectory())
			if err != nil {
				t.Fatal(err)
			}
			env.Sandbox = &rp
			reconcileClearSandbox(&cfg, env)
			if cfg.Sandbox != "restricted" || cfg.SandboxNet == nil || *cfg.SandboxNet {
				t.Fatalf("unexpected reconciled carrier: %#v", cfg)
			}
		case 9:
			if got := sandboxEnforcementLine(nil); got != "" {
				t.Fatalf("nil env line = %q", got)
			}
			env := execenv.NewLocalExecutionEnvironment(t.TempDir())
			if got := sandboxEnforcementLine(env); got != "" {
				t.Fatalf("off env line = %q", got)
			}
			net := true
			rp, err := sandbox.Resolve(sandbox.SandboxPolicy{Mode: sandbox.ModeReadOnly, Network: &net}, sandbox.HostFacts{OS: "linux", Home: t.TempDir(), BwrapPath: "/bin/true", BwrapCapable: true}, env.WorkingDirectory())
			if err != nil {
				t.Fatal(err)
			}
			env.Sandbox = &rp
			if got := sandboxEnforcementLine(env); !strings.Contains(got, "read-only") {
				t.Fatalf("enforcement line = %q", got)
			}
		case 10:
			// Exercise the production probe closure; its result is intentionally
			// unconstrained because host capabilities are not the behavior under test.
			_ = probeSandboxHost()
		}
	})
}
