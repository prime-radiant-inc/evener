package main

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/cmdutil"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/llm"
)

func memoryLaunchFixture(t *testing.T) string {
	t.Helper()
	// Not parallel: launch hooks and all ambient config/state locations are process-wide.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	for _, name := range []string{"EVENER_STATE_DIR", "EVENER_PROVIDERS_CONFIG", "EVENER_CREDENTIALS_CONFIG", "EVENER_MODEL", "EVENER_REASONING_EFFORT"} {
		t.Setenv(name, "")
	}
	dir := t.TempDir()
	if out, err := exec.Command("git", "init", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %s: %v", out, err)
	}
	return dir
}

// Catches missing CLI flag and binding/handoff before session construction.
func TestMemoryRunAndServeLaunch(t *testing.T) {
	for _, entry := range []string{"run", "serve"} {
		for _, disabled := range []bool{false, true} {
			t.Run(entry+map[bool]string{false: "/enabled", true: "/disabled"}[disabled], func(t *testing.T) {
				dir := memoryLaunchFixture(t)
				project, err := identifier.ResolveProject(dir)
				if err != nil {
					t.Fatal(err)
				}
				state := t.TempDir()
				stop := errors.New("fixture constructor reached")
				captured := false
				capture := func(_ *llm.Client, _ *provider.Profile, env execenv.ExecutionEnvironment, cfg agent.SessionConfig) (*agent.Session, error) {
					defer env.Cleanup()
					captured = true
					if cfg.MemoryStateRoot != cmdutil.DefaultStateRoot() || cfg.MemoryStateRoot == state || cfg.MemoryProjectID != project.ID || cfg.DisableMemory != disabled {
						t.Errorf("root=%q project=%q disabled=%t, want root=%q project=%q disabled=%t", cfg.MemoryStateRoot, cfg.MemoryProjectID, cfg.DisableMemory, cmdutil.DefaultStateRoot(), project.ID, disabled)
					}
					return nil, stop
				}
				args := []string{"--dir", dir, "--state-dir", state, "--model", "openai/gpt-test"}
				if disabled {
					args = append(args, "--disable-memory")
				}
				if entry == "serve" {
					installServeScriptedProvider(t, &scriptedProvider{name: "openai"})
					deps := defaultServeDeps()
					deps.newSession = capture
					err = runServeWithDeps(append(args, "--run-dir", t.TempDir()), deps)
				} else {
					installRunScriptedProvider(t, &scriptedProvider{name: "openai"})
					old := runNewSession
					runNewSession = capture
					t.Cleanup(func() { runNewSession = old })
					deps := defaultMainDeps()
					deps.args = append(args, "fixture input")
					deps.stdin, deps.stdout, deps.stderr = strings.NewReader(""), io.Discard, io.Discard
					deps.stdinMode = func() (os.FileMode, error) { return os.ModeCharDevice, nil }
					deps.exit = func(int) {}
					deps.run = func(ctx context.Context, cfg runConfig) error { err = run(ctx, cfg); return err }
					mainWithDeps(deps)
				}
				if !captured || !errors.Is(err, stop) {
					t.Fatalf("constructor captured=%t err=%v", captured, err)
				}
			})
		}
	}
}

// Catches omitted resume runtime host binding or disable override at both real parsers.
func TestMemoryRunAndServeResumeLaunch(t *testing.T) {
	for _, entry := range []string{"run", "serve"} {
		for _, disabled := range []bool{false, true} {
			t.Run(entry+map[bool]string{false: "/omitted", true: "/disable"}[disabled], func(t *testing.T) {
				dir := memoryLaunchFixture(t)
				state := t.TempDir()
				id := identifier.MustNewSessionID()
				if err := schema.SaveSessionMeta(state, schema.SessionMeta{ID: id, ProfileID: "openai", Model: "gpt-test", Config: schema.ConfigSnapshot{MemoryProjectID: "saved-binding"}}); err != nil {
					t.Fatal(err)
				}
				stop := errors.New("fixture resume reached")
				captured := false
				capture := func(_ *llm.Client, _ *provider.Profile, env execenv.ExecutionEnvironment, meta schema.SessionMeta, cfg agent.RestoreSessionConfig) (*agent.Session, error) {
					defer env.Cleanup()
					captured = true
					if cfg.MemoryStateRoot != cmdutil.DefaultStateRoot() || cfg.DisableMemory != disabled || meta.Config.MemoryProjectID != "saved-binding" {
						t.Errorf("resume root=%q disable=%t saved project=%q", cfg.MemoryStateRoot, cfg.DisableMemory, meta.Config.MemoryProjectID)
					}
					return nil, stop
				}
				args := []string{"--dir", dir, "--state-dir", state, "--resume", id}
				if disabled {
					args = append(args, "--disable-memory")
				}
				var err error
				if entry == "serve" {
					installServeScriptedProvider(t, &scriptedProvider{name: "openai"})
					deps := defaultServeDeps()
					deps.restoreSession = capture
					err = runServeWithDeps(append(args, "--run-dir", t.TempDir()), deps)
				} else {
					installRunScriptedProvider(t, &scriptedProvider{name: "openai"})
					old := runRestoreSession
					runRestoreSession = capture
					t.Cleanup(func() { runRestoreSession = old })
					deps := defaultMainDeps()
					deps.args = args
					deps.stdin = strings.NewReader("")
					deps.stdout, deps.stderr = io.Discard, io.Discard
					deps.stdinMode = func() (os.FileMode, error) { return os.ModeCharDevice, nil }
					deps.exit = func(int) {}
					deps.run = func(ctx context.Context, cfg runConfig) error { err = run(ctx, cfg); return err }
					mainWithDeps(deps)
				}
				if !captured || !errors.Is(err, stop) {
					t.Fatalf("resume captured=%t err=%v", captured, err)
				}
			})
		}
	}
}

// Catches launch identity following a linked cwd rather than its main Git checkout.
func TestMemoryLaunchLinkedBinding(t *testing.T) {
	main := memoryLaunchFixture(t)
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", main}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s %v", args, out, err)
		}
	}
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-m", "fixture")
	linked := filepath.Join(t.TempDir(), "linked")
	git("worktree", "add", "-b", "fixture-linked", linked)
	mainProject, err := identifier.ResolveProject(main)
	if err != nil {
		t.Fatal(err)
	}
	linkedProject, err := identifier.ResolveProject(linked)
	if err != nil {
		t.Fatal(err)
	}
	if mainProject.ID != linkedProject.ID {
		t.Fatalf("linked identity=%q main=%q", linkedProject.ID, mainProject.ID)
	}
	installRunScriptedProvider(t, &scriptedProvider{name: "openai"})
	old := runNewSession
	t.Cleanup(func() { runNewSession = old })
	stop := errors.New("linked launch reached")
	runNewSession = func(_ *llm.Client, _ *provider.Profile, env execenv.ExecutionEnvironment, cfg agent.SessionConfig) (*agent.Session, error) {
		defer env.Cleanup()
		if cfg.MemoryProjectID != mainProject.ID || env.WorkingDirectory() != linked {
			t.Errorf("linked launch project=%q cwd=%q", cfg.MemoryProjectID, env.WorkingDirectory())
		}
		return nil, stop
	}
	if err := run(context.Background(), runConfig{workDir: linked, stateDir: t.TempDir(), model: "openai/gpt-test", prompt: "fixture", stdout: io.Discard, stderr: io.Discard}); !errors.Is(err, stop) {
		t.Fatal(err)
	}
}

// A strict Git-resolution failure must not stop ordinary work or personal memory.
func TestMemoryLaunchBindingFailure(t *testing.T) {
	_ = memoryLaunchFixture(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("broken git pointer\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	adapter := &scriptedProvider{name: "openai", steps: []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response {
			return scriptedToolCalls(scriptedWriteFileCall("ordinary-write", "ordinary.txt", "opaque-ordinary-46"))
		},
		func(llm.Request) llm.Response {
			return scriptedToolCalls(llm.ToolCallData{ID: "personal-write", Name: "memory_write", Type: "function", Arguments: []byte(`{"scope":"personal","file_path":"topic.txt","content":"opaque-personal-58"}`)})
		},
		func(llm.Request) llm.Response { return scriptedCommunicate("fixture complete") },
	}}
	installRunScriptedProvider(t, adapter)
	old := runNewSession
	t.Cleanup(func() { runNewSession = old })
	constructed := false
	runNewSession = func(client *llm.Client, profile *provider.Profile, env execenv.ExecutionEnvironment, cfg agent.SessionConfig) (*agent.Session, error) {
		constructed = true
		if cfg.MemoryProjectID != "" || cfg.MemoryStateRoot != cmdutil.DefaultStateRoot() {
			t.Errorf("failure binding=%q root=%q", cfg.MemoryProjectID, cfg.MemoryStateRoot)
		}
		return old(client, profile, env, cfg)
	}
	if err := run(context.Background(), runConfig{workDir: dir, stateDir: t.TempDir(), model: "openai/gpt-test", prompt: "fixture", stdout: io.Discard, stderr: io.Discard}); err != nil {
		t.Fatal(err)
	}
	if !constructed {
		t.Fatal("ordinary session not constructed")
	}
	for path, want := range map[string]string{filepath.Join(dir, "ordinary.txt"): "opaque-ordinary-46", filepath.Join(cmdutil.DefaultStateRoot(), "memory/personal/topic.txt"): "opaque-personal-58"} {
		bytes, err := os.ReadFile(path)
		if err != nil || string(bytes) != want {
			t.Fatalf("path=%s bytes=%q err=%v", path, bytes, err)
		}
	}
}
