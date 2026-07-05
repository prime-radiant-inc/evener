package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
	"primeradiant.com/serf/llm/providercfg"
	"primeradiant.com/serf/rendezvous"
)

// sandboxSessionID is the local past session the sandbox seeds so routes that
// resolve a session cwd (e.g. /doc/file, resume) have a real one to work with.
const sandboxSessionID = "01FUZZDOCSESSION0000000000"

// sandboxOutOfRootSecret is planted ONE LEVEL ABOVE the seeded session's cwd. A
// correctly contained hub must never serve it through any path or query a fuzzer
// supplies; finding it in a response body is a path-escape defect.
const sandboxOutOfRootSecret = "FUZZ-OUT-OF-ROOT-SECRET-do-not-serve-9c1f2a"

// sandboxGitBranch is the fixed branch the git-head seam reports. Its presence
// in an /api/git/head response proves the seam ran instead of a real `git`.
const sandboxGitBranch = "sandbox-branch"

// sandbox is a fully contained hub for fuzzing/testing the MUTATING handlers.
// The backend it wires cannot spawn a real agent, shell out, hit the network,
// or touch the real filesystem outside Root. The escapes a read-only harness
// cannot drive are each neutralized:
//
//   - spawn (/api/spawn, thread/start) → Spawner records the request and returns
//     a synthetic rendezvous entry with no address; no subprocess, no dial.
//   - /api/git/head → GitHeadBranch seam returns sandboxGitBranch; no `git`.
//   - /api/models   → LiveModels seam returns a fixed list; no provider network.
//   - /api/dirs/create → MkdirAll seam records the path and creates nothing.
//   - the action verbs (send/steer/queue/clear/...) → an empty Roster and an
//     empty live-source set, so every verb resolves "thread not found" before it
//     can dial a daemon.
//   - serf/marketplace/* and serf/plugin/* → PluginRoot points the
//     internal/plugins.Manager backing hubPluginsController and the
//     auto-upgrade checkNow handler inside Root; a git-backed marketplace add
//     or upgrade still shells out to `git` (not caught by the deny-transport
//     network oracle), but its clone and registry files land under Root, not
//     the real ~/.config/serf/plugins.
//
// Workstreams B1 (appwire end-to-end), B2 (HTTP mutating routes) and B3 (tool
// execution) all stand up on this: B1/B2 drive Web.Handler()/Sources directly
// against this config; B3 pairs the same temp dirs with agenttest.DenyEnv for
// tool-handler execution (see sandbox_selftest_test.go for the DenyEnv wiring).
type sandbox struct {
	Web           *WebServer
	Config        hubcore.WebConfig
	Spawner       *recordingSpawner
	Mkdir         *recordingMkdir
	Root          string // temp root; the only filesystem subtree the hub may mutate
	CWD           string // the seeded session's working dir, inside Root
	Secret        []byte // planted ABOVE CWD; the path-escape oracle's tripwire
	ProvidersPath string // providers.toml the instances controller mutates, inside Root
}

// newSandbox builds the contained hub. It accepts testing.TB so both the B0
// self-test (*testing.T) and the B1–B3 fuzz targets (*testing.F) can stand it up.
func newSandbox(tb testing.TB) *sandbox {
	tb.Helper()
	root := tb.TempDir()
	cwd := filepath.Join(root, "cwd")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		tb.Fatal(err)
	}
	// A benign file inside the cwd so /doc/file has something legitimate to serve.
	if err := os.WriteFile(filepath.Join(cwd, "notes.txt"), []byte("hello"), 0o644); err != nil {
		tb.Fatal(err)
	}
	// The secret lives ABOVE the cwd and must never be reachable.
	secret := []byte(sandboxOutOfRootSecret)
	if err := os.WriteFile(filepath.Join(root, "fuzz-secret.txt"), secret, 0o600); err != nil {
		tb.Fatal(err)
	}

	proj := filepath.Join(root, "projects", "x")
	if err := os.MkdirAll(filepath.Join(proj, "sessions"), 0o755); err != nil {
		tb.Fatal(err)
	}
	if err := schema.SaveSessionMeta(proj, schema.SessionMeta{
		ID:             sandboxSessionID,
		UpdatedAt:      time.Now(),
		OriginalPrompt: "fuzz session",
		EnvInfo:        schema.EnvironmentInfo{WorkingDir: cwd},
	}); err != nil {
		tb.Fatal(err)
	}
	idx := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if err := idx.Rebuild(); err != nil {
		tb.Fatal(err)
	}

	// Seed a providers.toml inside Root so the serf/instance/* methods register
	// (they are gated on ProvidersConfigPath != "") and so the instances
	// controller's atomic writes land in the sandbox temp tree, never on the real
	// providers.toml. Two instances give Edit/Remove/SetDefault real targets.
	providersPath := filepath.Join(root, "providers.toml")
	if err := providercfg.WriteFile(providersPath, providercfg.Config{
		Default: "work",
		Instances: []providercfg.InstanceConfig{
			{Name: "work", Type: "openai"},
			{Name: "key", Type: "anthropic"},
		},
	}); err != nil {
		tb.Fatal(err)
	}

	spawner := &recordingSpawner{}
	mkdir := &recordingMkdir{}
	cfg := hubcore.WebConfig{
		HubAddr:             "127.0.0.1:9180",
		HubStateRoot:        filepath.Join(root, "state"),
		Past:                idx,
		Roster:              hubcore.NewRoster(filepath.Join(root, "roster"), nil),
		RunDir:              filepath.Join(root, "run"), // empty rendezvous dir → no live daemons to reach
		StateDir:            filepath.Join(root, "projects"),
		ProvidersConfigPath: providersPath,
		PluginRoot:          filepath.Join(root, "plugins"), // contain the marketplace/plugin store; "" would resolve to the real ~/.config/serf/plugins
		Spawner:             spawner,
		GitHeadBranch: func(context.Context, string) (string, error) {
			return sandboxGitBranch, nil
		},
		LiveModels: func(context.Context) []map[string]any {
			return []map[string]any{{"provider": "sandbox", "model": "fake-model"}}
		},
		MkdirAll: mkdir.MkdirAll,
		// AuthToken empty: the auth guard is disabled, so a fuzzed request reaches
		// the real routes (per the Phase 4 spec).
	}
	return &sandbox{
		Web:           NewWebServer(cfg),
		Config:        cfg,
		Spawner:       spawner,
		Mkdir:         mkdir,
		Root:          root,
		CWD:           cwd,
		Secret:        secret,
		ProvidersPath: providersPath,
	}
}

// recordingSpawner is a hubcore.Spawner that records every Spawn/Resume request
// and returns a synthetic rendezvous entry WITHOUT an address — so the hub's
// post-spawn thread read finds no live daemon and never dials one. It starts no
// subprocess: standing in for the real fork-and-exec launcher is the whole point.
type recordingSpawner struct {
	mu      sync.Mutex
	spawns  []hubcore.SpawnRequest
	resumes []hubcore.ResumeRequest
}

func (r *recordingSpawner) syntheticEntry(kind string) rendezvous.Entry {
	id := fmt.Sprintf("01SANDBOX%s%010d", kind, len(r.spawns)+len(r.resumes))
	// PID -1 and no Address: nothing in the roster will ever match it, and no
	// source can dial it.
	return rendezvous.Entry{PID: -1, ThreadID: id, SessionID: id}
}

func (r *recordingSpawner) Spawn(_ context.Context, req hubcore.SpawnRequest) (rendezvous.Entry, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry := r.syntheticEntry("SPN")
	r.spawns = append(r.spawns, req)
	return entry, nil
}

func (r *recordingSpawner) Resume(_ context.Context, req hubcore.ResumeRequest) (rendezvous.Entry, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry := r.syntheticEntry("RSM")
	r.resumes = append(r.resumes, req)
	return entry, nil
}

// Spawns returns a copy of every Spawn request recorded.
func (r *recordingSpawner) Spawns() []hubcore.SpawnRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]hubcore.SpawnRequest(nil), r.spawns...)
}

// Resumes returns a copy of every Resume request recorded.
func (r *recordingSpawner) Resumes() []hubcore.ResumeRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]hubcore.ResumeRequest(nil), r.resumes...)
}

// recordingMkdir is the MkdirAll seam: it records each requested path and
// creates nothing, so a fuzzed /api/dirs/create can never materialize a
// directory on the real filesystem.
type recordingMkdir struct {
	mu    sync.Mutex
	paths []string
}

func (m *recordingMkdir) MkdirAll(path string, _ os.FileMode) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.paths = append(m.paths, path)
	return nil
}

// Paths returns a copy of every path a handler asked to create.
func (m *recordingMkdir) Paths() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.paths...)
}
