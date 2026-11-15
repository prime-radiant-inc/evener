package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/worktree"
	"primeradiant.com/evener/agent/schema"
)

// The real-git counterpart of the scripted child-lane restore: a session with
// a persisted managed lane and its own session marker resumes into it with the
// lane's work intact.
func TestRestoreIdle_PersistedLaneSessionResumesWithWork(t *testing.T) {
	r := newWorktreeRepo(t)
	res, err := r.create(t, map[string]any{"name": "child-native-lane"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	lane := res["path"].(string)
	childID := r.s.delegateController.newDelegateID()
	// A child's own lane carries the child's session marker, not a delegate
	// marker.
	wtGit(t, r.mainRoot, "worktree", "unlock", lane)
	wtGit(t, r.mainRoot, "worktree", "lock", "--reason", worktree.FormatSessionMarker(childID), lane)
	kept := filepath.Join(lane, "keep-me.txt")
	if err := os.WriteFile(kept, []byte("persisted child work\n"), 0o644); err != nil {
		t.Fatalf("write lane work: %v", err)
	}
	base := r.s.Meta()
	saveColdRestorableChild(t, r.stateDir, base, childID, r.s.id, "child native lane", lane, 1)
	descriptor := delegatestore.Descriptor{
		ChildSessionID:    childID,
		TranscriptRef:     encodeRef("", childID),
		OwnerSessionID:    r.s.id,
		VisibleSessionID:  r.s.id,
		Task:              "child native lane",
		AgentType:         "default",
		ResolvedProfileID: "openai",
		ResolvedModel:     "gpt-5.2",
		ToolNameCeiling:   []string{"communicate", "write_file"},
		WorkingDir:        r.mainRoot,
		LocalEnvPolicy:    localEnvPolicyName(r.s.currentEnv()),
		Config:            base.Config.Clone(),
		Resumable:         true,
	}

	sub, restored, err := (delegateRuntime{owner: r.s}).restoreIdle(delegateStartCommit{
		lease:      delegateLease{delegateID: childID, generation: 1},
		ctx:        context.Background(),
		descriptor: descriptor,
	})
	if err != nil {
		t.Fatalf("restoreIdle: %v", err)
	}
	if !restored || sub == nil || sub.sess == nil {
		t.Fatalf("restoreIdle = sub=%v restored=%t, want a restored child", sub, restored)
	}
	t.Cleanup(sub.sess.discardRestoredCandidate)
	if got := sub.sess.currentEnv().WorkingDirectory(); got != lane {
		t.Fatalf("restored child cwd = %q, want %s", got, lane)
	}
	if got, err := os.ReadFile(kept); err != nil || string(got) != "persisted child work\n" {
		t.Fatalf("restored child work = %q err=%v", got, err)
	}
	if _, locked, reason := r.laneLocked(t, lane); !locked || reason != worktree.FormatSessionMarker(childID) {
		t.Fatalf("restored child lock = locked=%t reason=%q, want its session marker", locked, reason)
	}
}

func TestRestoreDelegateEnvironmentFor_WorktreePathGetsOwnedClone(t *testing.T) {
	t.Parallel()
	sr := newScriptedLaneRepo(t)
	res, err := sr.wt().create(t, map[string]any{"name": "child-lane"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	lane := res["path"].(string)
	parent := newSession(t, withDir(sr.mainRoot))
	meta := sr.wt().s.Meta()
	if meta.WorktreePath != lane {
		t.Fatalf("child meta path = %q, want %s", meta.WorktreePath, lane)
	}
	meta.ParentSessionID = "01PARENTSESSION00000000010"
	descriptor := delegatestore.Descriptor{
		ChildSessionID: meta.ID,
		WorkingDir:     sr.mainRoot,
		LocalEnvPolicy: localEnvPolicyName(parent.currentEnv()),
	}

	env, wantDir, ownsFresh, err := parent.restoreDelegateEnvironmentFor(descriptor, meta, nil)
	if err != nil {
		t.Fatalf("restoreDelegateEnvironmentFor: %v", err)
	}
	if wantDir != sr.mainRoot {
		t.Fatalf("restore directory = %q, want %s", wantDir, sr.mainRoot)
	}
	if env == parent.currentEnv() {
		t.Fatal("a child with a persisted worktree reused the parent's environment")
	}
	if env.WorkingDirectory() != sr.mainRoot {
		t.Fatalf("child env cwd = %q, want %q", env.WorkingDirectory(), sr.mainRoot)
	}
	if !ownsFresh {
		t.Fatal("a child with a persisted worktree did not get an owned environment")
	}
	t.Cleanup(func() { endEnvironmentScratch(env) })
}

// Removing the blanket child refusal must not let a child enter a lane its
// parent owns: the delegate marker classifies Foreign, so restore lands at the
// child's own restore root and the parent's lane keeps its lock.
func TestResumeWorktreeReentry_ChildInParentOwnedLaneLandsAtRestoreRoot(t *testing.T) {
	t.Parallel()
	sr := newScriptedLaneRepo(t)
	res, err := sr.wt().create(t, map[string]any{"name": "parent-lane"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	lane := res["path"].(string)
	parentID := "01PARENTSESSION00000000012"
	parentMarker := worktree.FormatDelegateMarker("dlg_01PARENTOWNEDLANE0000000", parentID)
	sr.setLaneLock(t, lane, parentMarker)
	meta := schema.SessionMeta{
		ID:                  "01CHILDINPARENTOWNEDLANE01",
		ParentSessionID:     parentID,
		IsSubagent:          true,
		WorktreePath:        lane,
		WorktreeManaged:     true,
		WorktreeRestoreRoot: sr.mainRoot,
	}
	cfg := sr.restoreConfig()
	cfg.spawn.parentSessionID = parentID

	sess, err := sr.restoreSessionOn(execenv.NewLocalExecutionEnvironment(sr.mainRoot), meta, cfg)
	if err != nil {
		t.Fatalf("restore child: %v", err)
	}
	t.Cleanup(sess.Close)
	if got := sess.currentEnv().WorkingDirectory(); got != sr.mainRoot {
		t.Fatalf("restored child cwd = %q, want its restore root %s", got, sr.mainRoot)
	}
	if got := sess.Meta().WorktreePath; got != "" {
		t.Fatalf("restored child recorded occupancy of the parent's lane %q, want none", got)
	}
	if !anyContainsAll(warningMessages(sess), parentMarker) {
		t.Fatalf("restored child warnings do not name the parent's lock marker %q", parentMarker)
	}
	if _, locked, reason := sr.laneLocked(t, lane); !locked || reason != parentMarker {
		t.Fatalf("parent lane lock = locked=%t reason=%q, want %q", locked, reason, parentMarker)
	}
}

func TestChildCreatedLaneCreatePauseRestorePreservesWork(t *testing.T) {
	sr := newScriptedLaneRepo(t)
	r := sr.wt()
	r.s.cfg.spawn.parentSessionID = "01PARENTSESSION00000000011"
	res, err := r.create(t, map[string]any{"name": "child-lane"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	lane := res["path"].(string)
	kept := filepath.Join(lane, "keep-me.txt")
	if err := os.WriteFile(kept, []byte("work preserved\n"), 0o644); err != nil {
		t.Fatalf("write lane work: %v", err)
	}
	meta := r.s.Meta()
	if !meta.IsSubagent || meta.WorktreePath != lane {
		t.Fatalf("child meta = subagent=%t path=%q, want subagent at %s", meta.IsSubagent, meta.WorktreePath, lane)
	}
	// The completed create tool round autosaves the lane fields; persist them
	// through that same path before the pause.
	if err := r.s.saveMeta(); err != nil {
		t.Fatalf("autosave child meta: %v", err)
	}
	registered := r.s.reg.RegisteredNames()
	toolCeiling := make([]string, 0, len(registered))
	for name := range registered {
		toolCeiling = append(toolCeiling, name)
	}
	if err := r.s.releaseChildRuntimeForRetirement(context.Background()); err != nil {
		t.Fatalf("pause child runtime: %v", err)
	}

	// delegate_send reads back the durable meta the create autosave wrote, then
	// restores from the child's persisted transcript. The fixture has no
	// transcript yet, so seed only that half.
	saved, err := schema.LoadSessionMeta(sr.stateDir, meta.ID)
	if err != nil {
		t.Fatalf("load saved child meta: %v", err)
	}
	if saved.WorktreePath != lane || !saved.WorktreeManaged {
		t.Fatalf("saved child meta = path %q managed=%t, want %s managed", saved.WorktreePath, saved.WorktreeManaged, lane)
	}
	writeColdRestorableTranscript(t, sr.stateDir, meta.ID, meta.ParentSessionID, "child native lane", sr.mainRoot, 1)
	parentCfg := worktreeTestSessionConfig()
	parentCfg.StateDir = sr.stateDir
	parentCfg.testOnly.worktreeGitRunner = func(context.Context, execenv.ExecutionEnvironment) worktree.GitRunner {
		return sr.git.run
	}
	parent := newSession(t, withDir(sr.mainRoot), withConfig(parentCfg))
	descriptor := delegatestore.Descriptor{
		ChildSessionID:    meta.ID,
		TranscriptRef:     encodeRef("", meta.ID),
		OwnerSessionID:    parent.id,
		ResolvedProfileID: "openai",
		ResolvedModel:     "gpt-5.2",
		WorkingDir:        sr.mainRoot,
		LocalEnvPolicy:    localEnvPolicyName(parent.currentEnv()),
		ToolNameCeiling:   toolCeiling,
		Config:            meta.Config.Clone(),
		Resumable:         true,
	}
	sub, restored, err := (delegateRuntime{owner: parent}).restoreIdle(delegateStartCommit{
		lease:      delegateLease{delegateID: meta.ID, generation: 1},
		ctx:        context.Background(),
		descriptor: descriptor,
	})
	if err != nil {
		t.Fatalf("restoreIdle: %v", err)
	}
	if !restored || sub == nil || sub.sess == nil {
		t.Fatalf("restoreIdle = sub=%v restored=%t, want a restored child", sub, restored)
	}
	t.Cleanup(sub.sess.discardRestoredCandidate)
	if got := sub.sess.currentEnv().WorkingDirectory(); got != lane {
		t.Fatalf("restored child cwd = %q, want %s (meta path %q, warnings %v)", got, lane, sub.sess.Meta().WorktreePath, warningMessages(sub.sess))
	}
	if got, err := os.ReadFile(kept); err != nil || string(got) != "work preserved\n" {
		t.Fatalf("preserved lane work = %q err=%v", got, err)
	}
	if _, locked, reason := sr.laneLocked(t, lane); !locked || reason != worktree.FormatSessionMarker(meta.ID) {
		t.Fatalf("restored child lock = locked=%t reason=%q, want its session marker", locked, reason)
	}
}
