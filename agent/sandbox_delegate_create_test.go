package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/sandbox"
	"primeradiant.com/serf/llm"
)

// sandboxScratchDirs lists the per-session sandbox scratch dirs (serf-sandbox-*)
// directly under base — the leak surface for a per-delegate sandbox whose spawn
// fails after EnableSandbox provisioned one.
func sandboxScratchDirs(t *testing.T, base string) []string {
	t.Helper()
	entries, err := os.ReadDir(base)
	if err != nil {
		t.Fatalf("read tmp base %q: %v", base, err)
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), "serf-sandbox-") {
			out = append(out, e.Name())
		}
	}
	return out
}

// sbxSetParentMode sets the session's currentEnv() to a resolved sandbox policy of
// the given mode (no kernel wrapper — the floor path only reads mode/network), so a
// createDelegate floor test can run under a concrete parent box.
func sbxSetParentMode(t *testing.T, s *Session, facts sandbox.HostFacts, cwd string, mode sandbox.Mode) {
	t.Helper()
	net := true
	rp, err := sandbox.Resolve(sandbox.SandboxPolicy{Mode: mode, Network: &net}, facts, cwd)
	if err != nil {
		t.Fatalf("resolve parent mode %v: %v", mode, err)
	}
	env := execenv.NewLocalExecutionEnvironment(cwd)
	env.Sandbox = &rp
	s.mu.Lock()
	s.env = env
	s.mu.Unlock()
}

// prepareWithDelegateSandbox threads a per-delegate sandbox policy (and a lane
// working dir) through prepareSubagentRun, returning the prepared run. The caller
// owns cleanup.
func prepareWithDelegateSandbox(t *testing.T, s *Session, lane string, pol *sandbox.SandboxPolicy) *preparedSubagentRun {
	t.Helper()
	ctx := context.WithValue(context.Background(), ctxDelegationAllowance, 0)
	ctx = context.WithValue(ctx, ctxParentJobID, "job_sbx")
	ctx = context.WithValue(ctx, ctxDelegateSandboxPolicy, pol)
	prepared, err := s.prepareSubagentRun(ctx, "child task", "", lane, 0, "", "", nil, nil)
	if err != nil {
		t.Fatalf("prepareSubagentRun: %v", err)
	}
	t.Cleanup(func() {
		releasePreparedTreeSlot(prepared)
		prepared.sub.sess.Close()
	})
	return prepared
}

// TestPrepareSubagentRun_PerDelegateSandboxEnforcedAndPersisted: a delegate under an
// OFF parent that requests its own restricted box gets an env enforced at ITS lane,
// and the persisted descriptor carries the REQUESTED box (not the parent's off).
func TestPrepareSubagentRun_PerDelegateSandboxEnforcedAndPersisted(t *testing.T) {
	lane, home := sbxLane(t)
	facts := sbxBwrapFacts(home)
	s := sbxDelegateSession(t, facts) // parent env is off

	prepared := prepareWithDelegateSandbox(t, s, lane, &sandbox.SandboxPolicy{Mode: sandbox.ModeRestricted, Network: boolPtr(true)})

	// Persisted snapshot reflects the requested box.
	if prepared.sandboxSnapshot == nil || prepared.sandboxSnapshot.Mode != "restricted" {
		t.Fatalf("prepared snapshot must reflect the requested restricted box, got %+v", prepared.sandboxSnapshot)
	}
	if prepared.sandboxSnapshot.Network == nil || !*prepared.sandboxSnapshot.Network {
		t.Errorf("prepared snapshot must persist net on, got %+v", prepared.sandboxSnapshot.Network)
	}

	// The live child env is enforced restricted, anchored at its own lane.
	le, ok := prepared.sub.sess.currentEnv().(*execenv.LocalExecutionEnvironment)
	if !ok {
		t.Fatal("child env must be a LocalExecutionEnvironment")
	}
	if le.Sandbox == nil || !le.Sandbox.Enforced() || le.Sandbox.Mode != sandbox.ModeRestricted {
		t.Fatalf("child env must be enforced restricted, got %+v", le.Sandbox)
	}
	if le.Sandbox.Git.WorktreeRoot != lane {
		t.Errorf("child box must anchor at the lane %q, got %q", lane, le.Sandbox.Git.WorktreeRoot)
	}
	if le.Wrapper == nil {
		t.Error("an enforced child box must provision a kernel wrapper")
	}

	// The persisted DelegateRestoreDescriptor carries the requested box.
	desc := s.delegateRestoreDescriptor("job_sbx", prepared.sub.id, "child task", encodeRef("", prepared.sub.id), nil, prepared)
	if desc.Sandbox == nil || desc.Sandbox.Mode != "restricted" {
		t.Errorf("persisted descriptor must carry the requested restricted box, got %+v", desc.Sandbox)
	}
}

// TestPrepareSubagentRun_PerDelegateSandboxOverridesSandboxedParent: a tighter
// per-delegate box (restricted) OVERRIDES a looser sandboxed parent (workspace-write
// + an out-of-lane extra writable root). The child is restricted and the parent's
// extra writable root does NOT leak onto it — the delegate's box is a pure function
// of ITS OWN policy.
func TestPrepareSubagentRun_PerDelegateSandboxOverridesSandboxedParent(t *testing.T) {
	lane, home := sbxLane(t)
	facts := sbxBwrapFacts(home)
	s := sbxDelegateSession(t, facts)
	parentExtra := sbxSandboxedParent(t, s, facts, lane) // parent is workspace-write + extra root

	prepared := prepareWithDelegateSandbox(t, s, lane, &sandbox.SandboxPolicy{Mode: sandbox.ModeRestricted, Network: boolPtr(true)})

	le := prepared.sub.sess.currentEnv().(*execenv.LocalExecutionEnvironment)
	if le.Sandbox == nil || le.Sandbox.Mode != sandbox.ModeRestricted {
		t.Fatalf("child must be restricted (tighter than the workspace-write parent), got %+v", le.Sandbox)
	}
	if slices.Contains(le.Sandbox.FileTool.WriteRoots, parentExtra) {
		t.Errorf("the parent's extra writable root %q leaked onto the delegate: %v", parentExtra, le.Sandbox.FileTool.WriteRoots)
	}
	if prepared.sandboxSnapshot == nil || prepared.sandboxSnapshot.Mode != "restricted" {
		t.Errorf("persisted snapshot must be the delegate's restricted box, not the parent's: %+v", prepared.sandboxSnapshot)
	}
}

// TestCreateDelegate_SandboxFloorRefusedEarly: a delegate that requests a LOOSER box
// than its parent is refused with a legible invalid_request error, and the refusal
// happens BEFORE minting any IDs (no delegate id is returned).
func TestCreateDelegate_SandboxFloorRefusedEarly(t *testing.T) {
	lane, home := sbxLane(t)
	facts := sbxBwrapFacts(home)
	s := sbxDelegateSession(t, facts)
	sbxSetParentMode(t, s, facts, lane, sandbox.ModeRestricted) // parent restricted

	res := s.createDelegate(context.Background(), delegateArgs{Task: "do work", Sandbox: "workspace-write"})
	if res.Err == nil {
		t.Fatal("a looser delegate box under a restricted parent must be refused")
	}
	if !strings.Contains(res.Err.Error(), "invalid_request:") {
		t.Errorf("refusal must be an invalid_request error, got %v", res.Err)
	}
	if !strings.Contains(res.Err.Error(), "not at least as confining") {
		t.Errorf("refusal must explain the confinement failure, got %v", res.Err)
	}
	if res.DelegateID != "" {
		t.Errorf("floor refusal must not mint a delegate id, got %q", res.DelegateID)
	}
}

// TestPrepareSubagentRun_PerDelegateSandboxCleansScratchOnSpawnFailure: when a
// per-delegate sandbox EnableSandbox's a fresh env and the spawn then fails at
// NewSession, the provisioned scratch dir must be disposed, not leaked. A nil child
// client (via childClientFactory) forces NewSession to fail AFTER EnableSandbox. Not
// parallel: it isolates TMPDIR to observe the sandbox scratch base.
func TestPrepareSubagentRun_PerDelegateSandboxCleansScratchOnSpawnFailure(t *testing.T) {
	isolated := t.TempDir()
	t.Setenv("TMPDIR", isolated)

	lane, home := sbxLane(t)
	facts := sbxBwrapFacts(home)
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	s := newSession(t, withClient(c), withConfig(SessionConfig{
		StateDir:         packageFixtureTempDir(t, "sbx-leak-*"),
		MaxSubagentDepth: 1,
		NoProjectPrompts: true,
		testOnly: testConfig{
			skipGitSnapshot:     true,
			minimalSystemPrompt: true,
			noSyncJobStore:      true,
			sandboxProber:       sandbox.FakeProber{Facts: facts},
			childClientFactory:  func() *llm.Client { return nil },
		},
	}))

	if before := sandboxScratchDirs(t, isolated); len(before) != 0 {
		t.Fatalf("isolated tmp base must start free of sandbox scratch, got %v", before)
	}

	ctx := context.WithValue(context.Background(), ctxDelegationAllowance, 0)
	ctx = context.WithValue(ctx, ctxParentJobID, "job_leak")
	ctx = context.WithValue(ctx, ctxDelegateSandboxPolicy, &sandbox.SandboxPolicy{Mode: sandbox.ModeRestricted, Network: boolPtr(true)})

	prepared, err := s.prepareSubagentRun(ctx, "child task", "", lane, 0, "", "", nil, nil)
	if err == nil {
		releasePreparedTreeSlot(prepared)
		prepared.sub.sess.Close()
		t.Fatal("expected NewSession to fail with a nil child client")
	}
	if left := sandboxScratchDirs(t, isolated); len(left) != 0 {
		t.Errorf("per-delegate sandbox scratch leaked on spawn failure: %v", left)
	}
}

// TestParentClose_DisposesRetainedPerDelegateSandboxScratch: a completed+retained
// per-delegate-sandbox delegate owns a FRESH env whose EnableSandbox provisioned a
// scratch dir. At PARENT close, retained children are torn down via close(false),
// which skips env cleanup (children historically shared the parent env), so the
// sandboxed child's scratch would leak. The parent teardown must dispose owned child
// scratches. Not parallel: isolates TMPDIR to observe the scratch base.
func TestParentClose_DisposesRetainedPerDelegateSandboxScratch(t *testing.T) {
	isolated := t.TempDir()
	t.Setenv("TMPDIR", isolated)

	lane, home := sbxLane(t)
	facts := sbxBwrapFacts(home)
	c := delegateTestClient(func(req llm.Request) llm.Response { return communicateWithDefaultOutput("done") })
	s := newSession(t, withClient(c), withDir(lane), withConfig(SessionConfig{
		StateDir:         packageFixtureTempDir(t, "sbx-close-*"),
		MaxSubagentDepth: 1,
		NoProjectPrompts: true,
		testOnly: testConfig{
			skipGitSnapshot:     true,
			minimalSystemPrompt: true,
			noSyncJobStore:      true,
			sandboxProber:       sandbox.FakeProber{Facts: facts},
		},
	}))

	res := s.createDelegate(context.Background(), delegateArgs{
		Task:           "do sandboxed work",
		Sandbox:        "restricted",
		Background:     false,
		BlockTimeoutMS: 5000,
	})
	if res.Err != nil {
		t.Fatalf("createDelegate: %v", res.Err)
	}
	// The sandboxed child provisioned a scratch dir.
	if got := sandboxScratchDirs(t, isolated); len(got) == 0 {
		t.Fatalf("expected a per-delegate sandbox scratch dir after spawn, found none in %s", isolated)
	}

	// Closing the parent must dispose the retained child's owned scratch.
	s.Close()
	if left := sandboxScratchDirs(t, isolated); len(left) != 0 {
		t.Errorf("retained per-delegate-sandbox scratch leaked at parent close: %v", left)
	}
}

// TestSandboxPromptLine: an unsandboxed env sources no environment sandbox line; a
// sandboxed env sources the mode + network line.
func TestSandboxPromptLine(t *testing.T) {
	if line := sandboxPromptLine(newSession(t).currentEnv()); line != "" {
		t.Errorf("an unsandboxed env must have no sandbox line, got %q", line)
	}

	lane, home := sbxLane(t)
	facts := sbxBwrapFacts(home)
	s := sbxDelegateSession(t, facts)
	sbxSetParentMode(t, s, facts, lane, sandbox.ModeRestricted) // net on
	if got, want := sandboxPromptLine(s.currentEnv()), "restricted (network on) — fixed for this session"; got != want {
		t.Errorf("sandboxed env line = %q, want %q", got, want)
	}
}

// TestSandboxPromptLineIncludesScratchDir: kata g8q6 — once EnableSandbox has
// provisioned a real kernel wrapper (Wrapper != nil), the environment section
// must also tell the model where its scratch directory is. The model's file
// tools have no other way to discover it: a spawned shell command learns it
// through $TMPDIR/$SERF_SCRATCH_DIR (agent/sandbox/env_floor.go), but the model's
// own write_file/read_file calls never see process environment variables.
// sbxSetParentMode (used by TestSandboxPromptLine above) deliberately builds an
// env with no Wrapper "so a createDelegate floor test can run under a concrete
// parent box" — this test instead exercises the real EnableSandbox path so a
// Wrapper (and its real, on-disk scratch dir) exists.
func TestSandboxPromptLineIncludesScratchDir(t *testing.T) {
	root := t.TempDir()
	host := sandbox.HostFacts{OS: "linux", Home: t.TempDir(), BwrapPath: "/usr/bin/bwrap", BwrapCapable: true, OverlaySupported: true}
	rp, err := sandbox.Resolve(sandbox.SandboxPolicy{Mode: sandbox.ModeWorkspaceWrite}, host, root)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	local := execenv.NewLocalExecutionEnvironment(root)
	t.Cleanup(local.Cleanup)
	if err := local.EnableSandbox(&rp); err != nil {
		t.Fatalf("EnableSandbox: %v", err)
	}

	got := sandboxPromptLine(local)
	if !strings.Contains(got, "workspace-write (network on) — fixed for this session") {
		t.Fatalf("sandbox prompt line lost the mode/network text: %q", got)
	}
	scratch := local.Wrapper.SessionTmp()
	if scratch == "" {
		t.Fatal("EnableSandbox must provision a session scratch dir")
	}
	if !strings.Contains(got, scratch) {
		t.Fatalf("sandbox prompt line must name the scratch directory so the model can find it: got %q, want it to contain %q", got, scratch)
	}
}

func TestSandboxPromptLineReadOnlyDelegateScratchGuidance(t *testing.T) {
	root := t.TempDir()
	net := true
	host := sandbox.HostFacts{OS: "linux", Home: t.TempDir(), BwrapPath: "/usr/bin/bwrap", BwrapCapable: true, OverlaySupported: true}
	rp, err := sandbox.Resolve(sandbox.SandboxPolicy{Mode: sandbox.ModeReadOnly, Network: &net}, host, root)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	local := execenv.NewLocalExecutionEnvironment(root)
	t.Cleanup(local.Cleanup)
	if err := local.EnableSandbox(&rp); err != nil {
		t.Fatalf("EnableSandbox: %v", err)
	}

	got := sandboxPromptLine(local)
	if !strings.Contains(got, "read-only (network on) — fixed for this session") {
		t.Fatalf("read-only prompt line lost the mode/network text: %q", got)
	}
	scratch := local.Wrapper.SessionTmp()
	if scratch == "" {
		t.Fatal("EnableSandbox must provision a session scratch dir")
	}
	if !strings.Contains(got, scratch) {
		t.Fatalf("read-only prompt line must name the scratch directory: got %q, want it to contain %q", got, scratch)
	}
	if !strings.Contains(got, "Read-only delegates may write only inside this scratch directory; all other writes are denied.") {
		t.Fatalf("read-only prompt line must explain its write boundary: %q", got)
	}
}

func TestReadOnlyDelegateDumbModelWritesOnlyToPromptNamedScratch(t *testing.T) {
	const (
		scratchMarker = "Scratch directory (read-write even in this sandbox; also $TMPDIR / $SERF_SCRATCH_DIR for shell commands): "
		guidance      = "Read-only delegates may write only inside this scratch directory; all other writes are denied."
	)

	root := t.TempDir()
	host := sandbox.HostFacts{OS: "linux", Home: t.TempDir(), BwrapPath: "/usr/bin/bwrap", BwrapCapable: true, OverlaySupported: true}
	var chosenPath string
	var sawGuidance bool
	var modelError string
	childClient := llm.NewClient()
	childAdapter := &fakeAdapter{
		name: "openai",
		steps: []func(llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				var prompt strings.Builder
				for _, msg := range req.Messages {
					prompt.WriteString(msg.Text())
					prompt.WriteByte('\n')
				}
				promptText := prompt.String()
				sawGuidance = strings.Contains(promptText, guidance)
				if !sawGuidance {
					// This intentionally literal model has no recovery strategy: without
					// the explicit rule, it tries the obvious relative path in the
					// worktree and the real read-only file tool must reject it.
					chosenPath = "dumb-report.md"
				} else {
					start := strings.Index(promptText, scratchMarker)
					if start < 0 {
						modelError = "prompt had the write-boundary sentence but no scratch path"
						chosenPath = "dumb-report.md"
					} else {
						scratch := promptText[start+len(scratchMarker):]
						if end := strings.Index(scratch, ". "+guidance); end >= 0 {
							scratch = scratch[:end]
						}
						scratch = strings.TrimSpace(strings.SplitN(scratch, "\n", 2)[0])
						if scratch == "" {
							modelError = "prompt named an empty scratch path"
							chosenPath = "dumb-report.md"
						} else {
							chosenPath = filepath.Join(scratch, "dumb-report.md")
						}
					}
				}

				args, _ := json.Marshal(map[string]string{
					"file_path": chosenPath,
					"content":   "the literal delegate wrote this report\n",
				})
				return toolCallResponse(llm.ToolCallData{
					ID:        "write_scratch_report",
					Name:      "write_file",
					Arguments: args,
					Type:      "function",
				})
			},
			func(req llm.Request) llm.Response {
				return communicateWithDefaultOutput("wrote the report")
			},
		},
	}
	childClient.Register(childAdapter)

	parent := newSession(t, withClient(delegateTestClient(func(req llm.Request) llm.Response {
		return communicateWithDefaultOutput("parent done")
	})), withDir(root), withConfig(SessionConfig{
		StateDir:         t.TempDir(),
		MaxSubagentDepth: 1,
		NoProjectPrompts: true,
		testOnly: testConfig{
			skipGitSnapshot: true,
			noSyncJobStore:  true,
			childClientFactory: func() *llm.Client {
				return childClient
			},
			sandboxProber: sandbox.FakeProber{Facts: host},
		},
	}))
	eventsDone := make(chan struct{})
	go func() {
		for range parent.Events() {
		}
		close(eventsDone)
	}()
	t.Cleanup(func() {
		parent.Close()
		<-eventsDone
	})

	net := true
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res := parent.createDelegate(ctx, delegateArgs{
		Task:           "write a tiny report in the only writable location available to you",
		Sandbox:        "read-only",
		SandboxNet:     &net,
		Background:     false,
		BlockTimeoutMS: 5000,
	})
	if res.Err != nil {
		t.Fatalf("createDelegate: %v", res.Err)
	}
	if !sawGuidance {
		t.Fatal("the literal delegate did not find the read-only scratch write rule in its system prompt")
	}
	if modelError != "" {
		t.Fatal(modelError)
	}
	if filepath.Base(chosenPath) != "dumb-report.md" || !filepath.IsAbs(chosenPath) {
		t.Fatalf("literal delegate chose %q, want an absolute scratch report path", chosenPath)
	}
	contents, err := os.ReadFile(chosenPath)
	if err != nil {
		t.Fatalf("read delegate scratch report %q: %v", chosenPath, err)
	}
	if string(contents) != "the literal delegate wrote this report\n" {
		t.Fatalf("scratch report contents = %q", contents)
	}
	if _, err := os.Stat(filepath.Join(root, "dumb-report.md")); !os.IsNotExist(err) {
		t.Fatalf("literal delegate unexpectedly wrote in the read-only worktree: err=%v", err)
	}
	if len(childAdapter.Requests()) < 2 {
		t.Fatalf("child model made %d requests, want a write turn followed by communicate", len(childAdapter.Requests()))
	}
}

// TestCreateDelegate_ResultEchoesSandboxBox: an enforced delegate echoes its box
// (mode + network) in the delegate result so the parent can verify the child's
// actual confinement; an unsandboxed delegate omits the key.
func TestCreateDelegate_ResultEchoesSandboxBox(t *testing.T) {
	lane, home := sbxLane(t)
	facts := sbxBwrapFacts(home)
	c := delegateTestClient(func(req llm.Request) llm.Response { return communicateWithDefaultOutput("done") })
	s := newSession(t, withClient(c), withDir(lane), withConfig(SessionConfig{
		StateDir:         packageFixtureTempDir(t, "sbx-echo-*"),
		MaxSubagentDepth: 1,
		NoProjectPrompts: true,
		testOnly: testConfig{
			skipGitSnapshot:     true,
			minimalSystemPrompt: true,
			noSyncJobStore:      true,
			sandboxProber:       sandbox.FakeProber{Facts: facts},
		},
	}))

	res := s.createDelegate(context.Background(), delegateArgs{
		Task:           "do sandboxed work",
		Sandbox:        "restricted",
		SandboxNet:     boolPtr(false),
		Background:     false,
		BlockTimeoutMS: 5000,
	})
	if res.Err != nil {
		t.Fatalf("createDelegate: %v", res.Err)
	}
	if res.Sandbox == nil || res.Sandbox.Mode != "restricted" || res.Sandbox.Network {
		t.Fatalf("result must echo the enforced box {restricted, net off}, got %+v", res.Sandbox)
	}

	out, err := marshalDelegateResult(res, 30000)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(out, `"sandbox":{"mode":"restricted","network":false}`) {
		t.Errorf("marshaled result must include the sandbox object, got %s", out)
	}

	// An unsandboxed result omits the key entirely.
	res.Sandbox = nil
	out, err = marshalDelegateResult(res, 30000)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(out, `"sandbox"`) {
		t.Errorf("unsandboxed delegate must omit the sandbox key, got %s", out)
	}
}

// TestPrepareSubagentRun_PerDelegateSandboxWithoutIsolationDoesNotMutateParent: a
// delegate may request its own box WITHOUT isolation="worktree" (empty workingDir),
// which shares the parent's working dir. The per-delegate EnableSandbox mutates its
// env IN PLACE, so the clone-before-mutate guard is containment-critical: without it
// EnableSandbox would sandbox (or re-box) the SHARED parent env mid-session. Assert
// the child gets the requested box AND the parent env is untouched.
func TestPrepareSubagentRun_PerDelegateSandboxWithoutIsolationDoesNotMutateParent(t *testing.T) {
	lane, home := sbxLane(t)
	facts := sbxBwrapFacts(home)
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	// Root the OFF parent session at a real worktree so the child's restricted box
	// (resolved at the shared working dir, since workingDir=="") anchors there.
	s := newSession(t, withClient(c), withDir(lane), withConfig(SessionConfig{
		StateDir:         packageFixtureTempDir(t, "sbx-noiso-*"),
		MaxSubagentDepth: 1,
		NoProjectPrompts: true,
		testOnly: testConfig{
			skipGitSnapshot:     true,
			minimalSystemPrompt: true,
			noSyncJobStore:      true,
			sandboxProber:       sandbox.FakeProber{Facts: facts},
		},
	}))

	parentEnv := s.currentEnv()
	parentLocal, ok := parentEnv.(*execenv.LocalExecutionEnvironment)
	if !ok {
		t.Fatal("parent env must be a LocalExecutionEnvironment")
	}
	if parentLocal.Sandbox != nil || parentLocal.Wrapper != nil {
		t.Fatal("parent must start unsandboxed (off)")
	}

	ctx := context.WithValue(context.Background(), ctxDelegationAllowance, 0)
	ctx = context.WithValue(ctx, ctxParentJobID, "job_noiso")
	ctx = context.WithValue(ctx, ctxDelegateSandboxPolicy, &sandbox.SandboxPolicy{Mode: sandbox.ModeRestricted, Network: boolPtr(true)})

	// workingDir == "" : a per-delegate sandbox WITHOUT an isolation lane.
	prepared, err := s.prepareSubagentRun(ctx, "child task", "", "", 0, "", "", nil, nil)
	if err != nil {
		t.Fatalf("prepareSubagentRun: %v", err)
	}
	t.Cleanup(func() {
		releasePreparedTreeSlot(prepared)
		prepared.sub.sess.Close()
	})

	// (a) The child env is enforced with the REQUESTED box.
	childLocal, ok := prepared.sub.sess.currentEnv().(*execenv.LocalExecutionEnvironment)
	if !ok {
		t.Fatal("child env must be a LocalExecutionEnvironment")
	}
	if childLocal.Sandbox == nil || !childLocal.Sandbox.Enforced() || childLocal.Sandbox.Mode != sandbox.ModeRestricted {
		t.Fatalf("child env must be enforced restricted, got %+v", childLocal.Sandbox)
	}

	// (b) The SHARED parent env is UNCHANGED — same instance, still unsandboxed.
	if s.currentEnv() != parentEnv {
		t.Error("parent env identity changed after a no-isolation per-delegate sandbox spawn")
	}
	if parentLocal.Sandbox != nil || parentLocal.Wrapper != nil {
		t.Errorf("per-delegate sandbox mutated the shared parent env: Sandbox=%v Wrapper=%v", parentLocal.Sandbox, parentLocal.Wrapper)
	}
	// The child must be a distinct env instance (clone-before-mutate), not the parent.
	if childLocal == parentLocal {
		t.Error("child env must be a clone of the parent, not the shared parent instance")
	}
}

// TestCreateDelegate_SandboxNetWithoutModeRefusedEarly: setting sandbox_net alone
// under an unsandboxed parent is refused with a legible error (not silently
// dropped), before any IDs are minted.
func TestCreateDelegate_SandboxNetWithoutModeRefusedEarly(t *testing.T) {
	_, home := sbxLane(t)
	facts := sbxBwrapFacts(home)
	s := sbxDelegateSession(t, facts) // parent env is off

	res := s.createDelegate(context.Background(), delegateArgs{Task: "do work", SandboxNet: boolPtr(false)})
	if res.Err == nil {
		t.Fatal("sandbox_net without a mode under an unsandboxed parent must be refused")
	}
	if !strings.Contains(res.Err.Error(), "invalid_request:") || !strings.Contains(res.Err.Error(), "requires a sandbox mode") {
		t.Errorf("refusal must explain sandbox_net requires a sandbox mode, got %v", res.Err)
	}
	if res.DelegateID != "" {
		t.Errorf("refusal must not mint a delegate id, got %q", res.DelegateID)
	}
}

// TestPerDelegateSandbox_CreateResumeRoundTrip: a delegate created with its own
// explicit box (restricted, net off) persists that box, and on RESTORE re-resolves
// the SAME box against its lane — independent of the parent, which by resume time is
// a DIFFERENT (workspace-write) sandbox. The parent's extra writable root never
// leaks into the resumed delegate.
func TestPerDelegateSandbox_CreateResumeRoundTrip(t *testing.T) {
	lane, home := sbxLane(t)
	facts := sbxBwrapFacts(home)
	s := sbxDelegateSession(t, facts) // parent off at create time

	prepared := prepareWithDelegateSandbox(t, s, lane, &sandbox.SandboxPolicy{Mode: sandbox.ModeRestricted, Network: boolPtr(false)})
	if prepared.sandboxSnapshot == nil || prepared.sandboxSnapshot.Mode != "restricted" ||
		prepared.sandboxSnapshot.Network == nil || *prepared.sandboxSnapshot.Network {
		t.Fatalf("create must persist restricted + net off, got %+v", prepared.sandboxSnapshot)
	}
	desc := s.delegateRestoreDescriptor("job_sbx", prepared.sub.id, "child task", encodeRef("", prepared.sub.id), nil, prepared)

	// At resume time the parent is a DIFFERENT, looser sandbox.
	parentExtra := sbxSandboxedParent(t, s, facts, lane)

	childEnv, err := s.restoreDelegateChildEnvironment(desc, "dlg_rt")
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	le := childEnv.(*execenv.LocalExecutionEnvironment)
	if le.Sandbox == nil || le.Sandbox.Mode != sandbox.ModeRestricted {
		t.Fatalf("resumed delegate must keep its OWN restricted box, got %+v", le.Sandbox)
	}
	if le.Sandbox.Network {
		t.Error("resumed delegate must keep its persisted net-off, got net on")
	}
	if slices.Contains(le.Sandbox.FileTool.WriteRoots, parentExtra) {
		t.Errorf("the resume-time parent's extra writable root %q leaked into the resumed delegate: %v", parentExtra, le.Sandbox.FileTool.WriteRoots)
	}
}
