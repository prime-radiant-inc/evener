//go:build serffuzz

package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/agent/internal/clock"
	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/internal/worktree"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// FuzzSessionRestoreCloseStatusProgram drives the restore, managed-worktree
// close, and status surfaces through test-owned state. The worktree portion
// uses the package's scripted Git boundary plus structural .git fixtures, so
// no shell or Git process can run; the ordinary session uses a scripted LLM,
// DenyEnv, and FakeClock. Its oracles protect re-entry ownership, close-time
// unlock/disposal, persisted runtime settings, and status projection bounds.
func FuzzSessionRestoreCloseStatusProgram(f *testing.F) {
	for _, seed := range [][]byte{
		{0, 0, 0},   // empty re-entry, unchanged disposal, gpt-5.3
		{1, 1, 1},   // missing worktree, changed disposal, gpt-4.1
		{2, 0, 2},   // non-managed re-entry
		{3, 1, 3},   // managed unlocked re-entry
		{4, 0, 4},   // managed own-lock adoption
		{5, 1, 5},   // foreign-lock refusal
		{6, 0, 6},   // unregistered-worktree refusal
		{7, 1, 7},   // porcelain failure refusal
		{8, 2, 8},   // non-local restore, missing-sidecar disposal
		{9, 3, 9},   // lock-state failure, unverifiable disposal
		{10, 4, 10}, // relock failure, foreign-lock preservation
		{11, 5, 11}, // restore without fallback root, unlock failure
		{3, 6, 12},  // init lock-state failure
		{4, 7, 13},  // init relock failure
		{5, 8, 14},  // missing lane at close, foreign init lock
		{6, 9, 15},  // unlocked close residue, tracked-init no-op
		{7, 10, 16}, // branch cleanup failure
		{4, 11, 17}, // pre-remove close hook
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		mode := srspByte(data, 0) % 12
		srspRestoreWorktree(t, mode)
		srspCloseWorktree(t, srspByte(data, 1)%12)
		srspInitWorktree(t, srspByte(data, 2)%8)
		srspOwnedIsolationLanes(t)
		srspRuntimeAndStatus(t, srspByte(data, 2))
	})
}

func srspByte(data []byte, index int) byte {
	if index >= len(data) {
		return 0
	}
	return data[index]
}

func srspRestoreWorktree(t *testing.T, mode byte) {
	t.Helper()
	h := newScriptedWorktreeSession(t)
	if mode == 0 {
		h.s.resumeWorktreeReentry(schema.SessionMeta{})
		if got := h.s.currentEnv().WorkingDirectory(); got != h.root {
			t.Fatalf("empty worktree restore changed root to %q, want %q", got, h.root)
		}
		return
	}
	if mode == 1 {
		ghost := filepath.Join(h.root, "missing-lane")
		h.s.resumeWorktreeReentry(schema.SessionMeta{WorktreePath: ghost, WorktreeManaged: true, WorktreeRestoreRoot: h.root})
		if got := h.s.currentEnv().WorkingDirectory(); got != h.root {
			t.Fatalf("missing worktree restore landed at %q, want %q", got, h.root)
		}
		return
	}
	if mode == 8 {
		h.s.mu.Lock()
		h.s.env = &timeoutEnv{wd: h.root}
		h.s.mu.Unlock()
		h.s.resumeWorktreeReentry(schema.SessionMeta{WorktreePath: h.root})
		if _, ok := h.s.currentEnv().(*timeoutEnv); !ok {
			t.Fatal("non-local restore replaced the execution environment")
		}
		return
	}

	out, err := h.exec(map[string]any{"operation": "create", "name": "srsp-reentry"})
	if err != nil {
		t.Fatalf("create scripted worktree: %v", err)
	}
	path, ok := out["path"].(string)
	if !ok || path == "" {
		t.Fatalf("create path = %#v", out["path"])
	}
	entry := h.git.entry(path)
	if entry == nil {
		t.Fatalf("created worktree %q missing from scripted Git", path)
	}
	local, ok := h.s.currentEnv().(*execenv.LocalExecutionEnvironment)
	if !ok {
		t.Fatal("scripted worktree session did not use LocalExecutionEnvironment")
	}
	h.s.mu.Lock()
	h.s.env = local.WithWorkingDirectory(h.root)
	h.s.worktreeCurrentPath = ""
	h.s.worktreeCurrentManaged = false
	h.s.worktreeRestoreEnv = nil
	h.s.mu.Unlock()

	meta := schema.SessionMeta{ID: h.s.ID(), WorktreePath: path, WorktreeRestoreRoot: h.root}
	wantReentry := true
	switch mode {
	case 2:
		entry.lockReason = ""
	case 3:
		meta.WorktreeManaged = true
		entry.lockReason = ""
	case 4:
		meta.WorktreeManaged = true
		entry.lockReason = worktree.FormatSessionMarker(h.s.ID())
	case 5:
		meta.WorktreeManaged = true
		entry.lockReason = "serf:another-session"
		wantReentry = false
	case 6:
		meta.WorktreeManaged = true
		delete(h.git.entries, filepath.Clean(path))
		wantReentry = false
	case 7:
		meta.WorktreeManaged = true
		h.s.cfg.testOnly.worktreeGitRunner = func(context.Context, execenv.ExecutionEnvironment) worktree.GitRunner {
			return func(...string) (string, error) { return "", errors.New("srsp: porcelain unavailable") }
		}
		wantReentry = false
	case 9:
		meta.WorktreeManaged = true
		calls := 0
		h.s.cfg.testOnly.worktreeGitRunner = func(context.Context, execenv.ExecutionEnvironment) worktree.GitRunner {
			return func(args ...string) (string, error) {
				if scriptedArgs(args, "worktree", "list", "--porcelain") {
					calls++
					if calls == 2 {
						return "", errors.New("srsp: lock state unavailable")
					}
				}
				return h.git.run(args...)
			}
		}
		wantReentry = false
	case 10:
		meta.WorktreeManaged = true
		entry.lockReason = ""
		faults := &worktreeFaults{git: h.git, lockErr: map[string]error{filepath.Clean(path): errors.New("srsp: lock denied")}}
		h.s.cfg.testOnly.worktreeGitRunner = func(context.Context, execenv.ExecutionEnvironment) worktree.GitRunner { return faults.run }
		wantReentry = false
	case 11:
		meta.WorktreeManaged = true
		meta.WorktreeRestoreRoot = ""
		entry.lockReason = "serf:another-session"
		wantReentry = false
	}

	h.s.resumeWorktreeReentry(meta)
	if !wantReentry {
		if got := h.s.currentEnv().WorkingDirectory(); got != h.root {
			t.Fatalf("refused worktree restore landed at %q, want restore root %q", got, h.root)
		}
		return
	}
	if got := h.s.currentEnv().WorkingDirectory(); got != path {
		t.Fatalf("re-entered working directory = %q, want %q", got, path)
	}
	h.s.mu.Lock()
	current, managed := h.s.worktreeCurrentPath, h.s.worktreeCurrentManaged
	h.s.mu.Unlock()
	if current != path || managed != meta.WorktreeManaged {
		t.Fatalf("restored occupancy = (%q,%t), want (%q,%t)", current, managed, path, meta.WorktreeManaged)
	}
	if meta.WorktreeManaged {
		if got, want := entry.lockReason, worktree.FormatSessionMarker(h.s.ID()); got != want {
			t.Fatalf("managed re-entry lock = %q, want %q", got, want)
		}
	} else if entry.lockReason != "" {
		t.Fatalf("non-managed re-entry changed lock to %q", entry.lockReason)
	}
}

func srspCloseWorktree(t *testing.T, mode byte) {
	t.Helper()
	h := newScriptedWorktreeSession(t)
	out, err := h.exec(map[string]any{"operation": "create", "name": "srsp-close"})
	if err != nil {
		t.Fatalf("create own worktree: %v", err)
	}
	path := out["path"].(string)
	entry := h.git.entry(path)
	if entry == nil {
		t.Fatalf("own worktree %q missing from scripted Git", path)
	}
	local := h.s.currentEnv().(*execenv.LocalExecutionEnvironment)
	entry.lockReason = ""
	h.s.mu.Lock()
	h.s.env = local.WithWorkingDirectory(path)
	h.s.worktreeCurrentPath = ""
	h.s.worktreeCurrentManaged = false
	h.s.worktreeRestoreEnv = nil
	h.s.mu.Unlock()
	h.s.applyInitInsideWorktreeLock(true)
	if got, want := entry.lockReason, worktree.FormatSessionMarker(h.s.ID()); got != want {
		t.Fatalf("init occupancy lock = %q, want %q", got, want)
	}
	h.s.Close()
	if h.s.State() != SessionClosed {
		t.Fatalf("closed worktree session state = %q, want closed", h.s.State())
	}
	if entry.lockReason != "" {
		t.Fatalf("close did not unlock own managed worktree: %q", entry.lockReason)
	}

	h2, faults := newWorktreeFaultSession(t)
	lane, branch, _, _, _, err := h2.s.createDelegateWorktree(context.Background(), "srsp-delegate")
	if err != nil {
		t.Fatalf("create delegate worktree: %v", err)
	}
	if branch != "srsp-delegate" {
		t.Fatalf("delegate branch = %q, want srsp-delegate", branch)
	}
	laneEntry := h2.git.entry(lane)
	if laneEntry == nil {
		t.Fatalf("delegate lane %q missing from scripted Git", lane)
	}
	if mode == 1 || (mode >= 4 && mode <= 7) {
		laneEntry.head = "srsp-changed-head"
	}
	switch mode {
	case 2:
		if err := worktree.DeleteSidecar(h2.metaDir(), "srsp-delegate"); err != nil {
			t.Fatalf("delete delegate sidecar: %v", err)
		}
	case 3:
		faults.listErr = errors.New("srsp: list unavailable")
	case 4:
		laneEntry.lockReason = "serf:another-session"
	case 5:
		faults.unlockErr[filepath.Clean(lane)] = errors.New("srsp: unlock denied")
	case 6:
		faults.statusErr[filepath.Clean(lane)] = errors.New("srsp: status unavailable")
	case 7:
		faults.removeErr[filepath.Clean(lane)] = errors.New("srsp: late dirty write")
	case 8:
		if err := os.Remove(filepath.Join(lane, ".git")); err != nil {
			t.Fatalf("remove delegate git pointer: %v", err)
		}
	case 9:
		laneEntry.lockReason = ""
	case 10:
		faults.deleteErr["srsp-delegate"] = errors.New("srsp: branch delete denied")
	case 11:
		h2.s.worktreeDisposeBeforeRemove = func(got string) {
			if got != lane {
				t.Fatalf("dispose hook path = %q, want %q", got, lane)
			}
		}
	}
	note, kept := h2.s.disposeOneDelegateLane(h2.s.currentEnv().(*execenv.LocalExecutionEnvironment), isolationLane{
		delegateID: "srsp-delegate",
		path:       lane,
	})
	if mode == 1 {
		if !kept || !strings.Contains(note, "srsp-delegate") {
			t.Fatalf("changed lane disposal = (%q,%t), want kept delegate note", note, kept)
		}
		if h2.git.entry(lane) == nil || laneEntry.lockReason != "" {
			t.Fatalf("changed lane was not kept unlocked: entry=%v lock=%q", h2.git.entry(lane) != nil, laneEntry.lockReason)
		}
		return
	}
	if mode == 2 || mode == 3 || mode == 4 || mode == 5 {
		if kept || note != "" || h2.git.entry(lane) == nil {
			t.Fatalf("declined lane disposal = (%q,%t, present=%t)", note, kept, h2.git.entry(lane) != nil)
		}
		return
	}
	if mode == 6 || mode == 7 {
		if !kept || h2.git.entry(lane) == nil {
			t.Fatalf("preserving lane disposal = (%q,%t, present=%t)", note, kept, h2.git.entry(lane) != nil)
		}
		return
	}
	if mode == 8 {
		if kept || note != "" || h2.git.entry(lane) == nil {
			t.Fatalf("missing lane disposal = (%q,%t, present=%t)", note, kept, h2.git.entry(lane) != nil)
		}
		return
	}
	if kept || note != "" {
		t.Fatalf("unchanged lane disposal = (%q,%t), want removed", note, kept)
	}
	if h2.git.entry(lane) != nil {
		t.Fatalf("removed lane %q is still registered", lane)
	}
	if _, err := os.Stat(filepath.Join(lane, ".git")); !os.IsNotExist(err) {
		t.Fatalf("removed lane .git stat error = %v, want not-exist", err)
	}
}

func srspInitWorktree(t *testing.T, mode byte) {
	t.Helper()
	h, faults := newWorktreeFaultSession(t)
	if mode == 0 {
		h.s.applyInitInsideWorktreeLock(false)
		return
	}
	if mode == 1 {
		h.s.cfg.spawn.parentSessionID = "parent"
		h.s.applyInitInsideWorktreeLock(true)
		return
	}
	path := scriptedCreate(t, h, "srsp-init")
	entry := h.git.entry(path)
	entry.lockReason = ""
	local := h.s.currentEnv().(*execenv.LocalExecutionEnvironment)
	h.s.mu.Lock()
	h.s.env = local.WithWorkingDirectory(path)
	h.s.worktreeCurrentPath = ""
	h.s.worktreeCurrentManaged = false
	h.s.mu.Unlock()
	switch mode {
	case 2:
		h.s.mu.Lock()
		h.s.env = &timeoutEnv{wd: path}
		h.s.mu.Unlock()
	case 3:
		faults.listErr = errors.New("srsp: lock inspection unavailable")
	case 4:
		faults.lockErr[filepath.Clean(path)] = errors.New("srsp: lock denied")
	case 5:
		entry.lockReason = worktree.FormatSessionMarker(h.s.id)
	case 6:
		entry.lockReason = "serf:another-session"
	case 7:
		h.s.worktreeCurrentPath = path
	}
	h.s.applyInitInsideWorktreeLock(true)
	if mode == 5 && h.s.worktreeCurrentPath != path {
		t.Fatalf("own init lock was not adopted: %q, want %q", h.s.worktreeCurrentPath, path)
	}
}

func srspOwnedIsolationLanes(t *testing.T) {
	t.Helper()
	recs := map[string]*jobstore.JobRecord{
		"nil":             {},
		"other-isolation": {DelegateID: "other", DelegateRestore: &jobstore.DelegateRestoreDescriptor{Isolation: "shared", ParentSessionID: "session", WorkingDir: "/other"}},
		"foreign-parent":  {DelegateID: "foreign", DelegateRestore: &jobstore.DelegateRestoreDescriptor{Isolation: "worktree", ParentSessionID: "other", WorkingDir: "/foreign"}},
		"empty-path":      {DelegateID: "empty", DelegateRestore: &jobstore.DelegateRestoreDescriptor{Isolation: "worktree", ParentSessionID: "session", WorkingDir: "  "}},
		"empty-id":        {DelegateRestore: &jobstore.DelegateRestoreDescriptor{Isolation: "worktree", ParentSessionID: "session", WorkingDir: "/empty-id"}},
		"lane":            {DelegateID: "lane", DelegateRestore: &jobstore.DelegateRestoreDescriptor{Isolation: "worktree", ParentSessionID: "session", WorkingDir: " /lane "}},
		"duplicate":       {DelegateID: "lane", DelegateRestore: &jobstore.DelegateRestoreDescriptor{Isolation: "worktree", ParentSessionID: "session", WorkingDir: "/duplicate"}},
	}
	lanes := ownedIsolationLanes(recs, "session")
	if len(lanes) != 1 || lanes[0].delegateID != "lane" || (lanes[0].path != "/lane" && lanes[0].path != "/duplicate") {
		t.Fatalf("owned isolation lanes = %+v", lanes)
	}
}

func srspRuntimeAndStatus(t *testing.T, draw byte) {
	t.Helper()
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	stateDir := filepath.Join(root, "state")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	clk := agenttest.NewFakeClock()
	sess, err := NewSession(srspClient(), NewOpenAIProfile("gpt-5.2"), &agenttest.DenyEnv{WorkDir: workspace, Seed: uint64(draw)}, SessionConfig{
		StateDir:         stateDir,
		NoProjectPrompts: true,
		clock:            clk,
		testOnly: testConfig{
			skipGitSnapshot: true,
			noSyncJobStore:  true,
			environmentInfo: srspEnvironmentInfo,
		},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	model := []string{"gpt-5.3", "gpt-4.1"}[int(draw)&1]
	effort := []string{"high", "off", "none"}[int(draw)%3]
	timeout := 1000 + int(draw)
	name := "  srsp session  "
	sess.SetModel(model)
	sess.SetReasoningEffort(effort)
	sess.SetTimeout(timeout)
	sess.Rename(name)
	sess.RegisterTool("srsp_custom", "status fixture", map[string]any{"type": "object"}, func(context.Context, any) (any, error) {
		return "unused", nil
	})

	status := sess.DetailedStatus()
	if !srspToolSource(status.Tools, "srsp_custom", "custom") || !srspToolsSorted(status.Tools) {
		t.Fatalf("status tools lost custom/sort invariant: %+v", status.Tools)
	}
	meta := sess.Meta()
	if meta.Model != model || meta.Config.ReasoningEffort != llm.NormalizeReasoningEffort(effort) || meta.Config.DefaultCommandTimeoutMS != timeout || meta.Name != strings.TrimSpace(name) {
		t.Fatalf("runtime meta = %+v", meta)
	}

	req := &llm.Request{Model: model}
	sess.applyModelRequestMetadata(sess.Profile(), req)
	if req.SessionID != sess.ID() || req.ThreadID != sess.ID() || req.ClientMetadata == nil {
		t.Fatalf("request metadata missing session identity: %+v", req)
	}
	if strings.HasPrefix(model, "gpt-") && (req.PromptCacheKey == "" || req.PromptCacheRetention != "24h") {
		t.Fatalf("openai prompt-cache metadata = %+v", req)
	}

	records := []*jobstore.JobRecord{{JobID: "active", Type: jobstore.JobShell, Status: jobstore.StatusRunning}}
	for i := 0; i < detailedStatusTerminalJobsLimit+2; i++ {
		records = append(records, &jobstore.JobRecord{JobID: "terminal-" + string(rune('a'+i)), Type: jobstore.JobDelegate, Status: jobstore.StatusCompleted, OutputBytes: int64(i)})
	}
	bounded := detailedStatusJobRecords(records)
	if len(bounded) != detailedStatusTerminalJobsLimit+1 || bounded[0].JobID != "active" {
		t.Fatalf("bounded status records = %d first=%+v", len(bounded), bounded[0])
	}
	projected := projectJobStatusInfos(bounded)
	if len(projected) != len(bounded) || projected[0].Status != string(jobstore.StatusRunning) {
		t.Fatalf("status projection = %+v", projected)
	}

	sess.Close()
	before := sess.Meta()
	sess.SetModel("gpt-5.4")
	sess.SetTimeout(timeout + 1)
	sess.SetReasoningEffort("low")
	sess.Rename("after close")
	after := sess.Meta()
	if after.Model != before.Model || after.Config.DefaultCommandTimeoutMS != before.Config.DefaultCommandTimeoutMS || after.Config.ReasoningEffort != before.Config.ReasoningEffort || after.Name != before.Name {
		t.Fatalf("closed setters mutated meta: before=%+v after=%+v", before, after)
	}
}

func srspClient() *llm.Client {
	client := llm.NewClient()
	client.Register(&agenttest.ScriptedAdapter{
		Provider: "openai",
		Responder: func(llm.Request) llm.Response {
			return llm.Response{Message: llm.Assistant("srsp")}
		},
	})
	return client
}

func srspEnvironmentInfo(env execenv.ExecutionEnvironment, clk clock.Clock) schema.EnvironmentInfo {
	return schema.EnvironmentInfo{
		WorkingDir: env.WorkingDirectory(),
		Platform:   "srsp",
		OSVersion:  "deny-env",
		Today:      clk.Now().UTC().Format("2006-01-02"),
	}
}

func srspToolSource(tools []ToolInfo, name, source string) bool {
	for _, tool := range tools {
		if tool.Name == name && tool.Source == source {
			return true
		}
	}
	return false
}

func srspToolsSorted(tools []ToolInfo) bool {
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name)
	}
	return sort.StringsAreSorted(names)
}
