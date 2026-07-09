package agent

import (
	"errors"
	"testing"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/sandbox"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// restoreWithSandbox restores a session whose persisted ConfigSnapshot carries the
// given sandbox mode, re-resolving it against the injected prober's host facts so
// the resume path stays hermetic (it never shells out to bwrap). It returns the env
// it restored onto — to assert the enforced state — and the restore error. It
// exercises the SAME entry point a real resume uses.
func restoreWithSandbox(t *testing.T, mode string, prober sandbox.Prober) (*execenv.LocalExecutionEnvironment, error) {
	t.Helper()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	meta := schema.SessionMeta{
		ID:        "restored-sandbox-session",
		ProfileID: "openai",
		Model:     "gpt-5.2",
		Config:    (SessionConfig{Sandbox: mode, NoProjectPrompts: true}).toSnapshot(),
	}
	env := execenv.NewLocalExecutionEnvironment(dir)
	sess, err := RestoreSessionFromMetaWithConfig(c, NewOpenAIProfile("gpt-5.2"), env, meta, RestoreSessionConfig{
		testOnly: testConfig{sandboxProber: prober},
	})
	if sess != nil {
		t.Cleanup(func() { sess.Close() })
	}
	return env, err
}

// bwrapCapableProber returns a FakeProber reporting a bwrap-capable Linux host
// anchored at home, so the resume path resolves + builds a real wrapper hermetically
// (NewWrapper validates the path is absolute but never stats or spawns bwrap).
func bwrapCapableProber(home string) sandbox.Prober {
	return sandbox.FakeProber{Facts: sandbox.HostFacts{OS: "linux", Home: home, BwrapPath: "/usr/bin/bwrap", BwrapCapable: true}}
}

// TestRestoreProvisionsPersistedSandbox: with the M5 flip, restore re-resolves a
// persisted non-off mode against freshly-probed host facts and builds an ENFORCED
// env (the immutable-across-restart guarantee), instead of the pre-M5 feature-gate
// refusal. A persisted "sandbox":"restricted" now resumes genuinely sandboxed, not
// claiming a mode with nothing enforced.
func TestRestoreProvisionsPersistedSandbox(t *testing.T) {
	for _, mode := range []string{"read-only", "workspace-write", "restricted"} {
		t.Run(mode, func(t *testing.T) {
			env, err := restoreWithSandbox(t, mode, bwrapCapableProber(t.TempDir()))
			if err != nil {
				t.Fatalf("restore with persisted sandbox=%q must now provision, not refuse: %v", mode, err)
			}
			if env.Sandbox == nil || !env.Sandbox.Enforced() {
				t.Fatalf("restore sandbox=%q must build an enforced env", mode)
			}
			if env.Sandbox.Mode.String() != mode {
				t.Errorf("restored policy mode = %q, want %q", env.Sandbox.Mode, mode)
			}
			if env.Wrapper == nil {
				t.Errorf("restore sandbox=%q on a bwrap host must attach a kernel wrapper", mode)
			}
		})
	}
}

// TestRestoreFailsClosedWhenHostCannotEnforce: the fail-closed floor is now
// user-reachable on resume — a persisted non-off mode on a host that cannot enforce
// it refuses the restore with the resolver's typed *sandbox.RefusalError, rather
// than resuming unconfined.
func TestRestoreFailsClosedWhenHostCannotEnforce(t *testing.T) {
	bare := sandbox.FakeProber{Facts: sandbox.HostFacts{OS: "linux", Home: t.TempDir()}} // no bwrap
	_, err := restoreWithSandbox(t, "restricted", bare)
	if err == nil {
		t.Fatal("restore on a host that cannot enforce restricted must fail closed")
	}
	var ref *sandbox.RefusalError
	if !errors.As(err, &ref) {
		t.Errorf("want a *sandbox.RefusalError, got %T: %v", err, err)
	}
}

// TestRestoreOffSandboxUnchanged proves the flip does not disturb the common path:
// an off or empty persisted mode restores byte-identically — no host probe, no
// enforced env.
func TestRestoreOffSandboxUnchanged(t *testing.T) {
	for _, mode := range []string{"", "off"} {
		env, err := restoreWithSandbox(t, mode, nil)
		if err != nil {
			t.Errorf("restore with sandbox=%q must succeed, got %v", mode, err)
		}
		if env.Sandbox != nil {
			t.Errorf("off restore must leave the env unsandboxed, got %v", env.Sandbox)
		}
	}
}
