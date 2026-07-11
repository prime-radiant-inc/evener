//go:build serffuzz

package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/plugin"
	"primeradiant.com/serf/agent/provider"
	"primeradiant.com/serf/agent/sandbox"
	"primeradiant.com/serf/agent/schema"
)

// FuzzDelegateCreationRestoreConfigProgram exercises the durable configuration
// contract shared by delegate creation and crash restore. It runs real Session,
// subagent, transcript, and job-store code below a scripted LLM boundary, then
// checks that the immutable descriptor is accepted or rejected consistently by
// both its pure gate and the restore preflight.
//
// The harness has no live provider, network, shell, or git dependency. jdrNewSession
// supplies a ScriptedAdapter, FakeClock, test-owned state/work directories, and a
// LocalExecutionEnvironment with EnvPolicyNone. The only non-local environment
// exercised below is agenttest.DenyEnv, used solely to prove the restore rejection
// for a changed working directory; no tool handler is ever run.
//
// Semantic oracles:
//   - invalid creation options never mint durable jobs or delegates;
//   - a successful configured create persists the child linkage, model/agent
//     configuration, result schema, and caller provenance before returning;
//   - every descriptor mutation has one stable, machine-readable restore reason;
//   - profile, environment, sandbox, and tool-policy restoration reject malformed
//     inputs before a child runtime is reconstructed; and
//   - resumed descriptors preserve the prior configuration without aliasing it.

type dcrcReader struct {
	data []byte
	pos  int
}

func (r *dcrcReader) byte() byte {
	if r.pos >= len(r.data) {
		return 0
	}
	b := r.data[r.pos]
	r.pos++
	return b
}

func (r *dcrcReader) choose(n int) int {
	if n <= 0 {
		return 0
	}
	return int(r.byte()) % n
}

func dcrcValidRecord(s *Session, workDir string) *jobstore.JobRecord {
	childID := "child_config"
	jobID := "job_config"
	ref := encodeRef("", childID)
	return &jobstore.JobRecord{
		JobID:            jobID,
		Type:             jobstore.JobDelegate,
		Status:           jobstore.StatusStopped,
		Reason:           "runtime_lost",
		OwnerSessionID:   s.ID(),
		VisibleToSession: s.ID(),
		TranscriptRef:    ref,
		DelegateRestore: &jobstore.DelegateRestoreDescriptor{
			Version:           1,
			ChildSessionID:    childID,
			TranscriptRef:     ref,
			ParentSessionID:   s.ID(),
			ParentJobID:       jobID,
			OwnerSessionID:    s.ID(),
			VisibleSessionID:  s.ID(),
			Task:              "restore configuration",
			ResolvedProfileID: "openai",
			ResolvedModel:     "gpt-5.2",
			WorkingDir:        workDir,
			LocalEnvPolicy:    "none",
		},
	}
}

func dcrcCloneRecord(in *jobstore.JobRecord) *jobstore.JobRecord {
	if in == nil {
		return nil
	}
	out := *in
	if in.DelegateRestore != nil {
		desc := *in.DelegateRestore
		desc.FrozenToolNames = append([]string(nil), desc.FrozenToolNames...)
		desc.FrozenSkillNames = append([]string(nil), desc.FrozenSkillNames...)
		desc.FrozenSkillBodies = append([]string(nil), desc.FrozenSkillBodies...)
		desc.ExplicitToolGrants = append([]string(nil), desc.ExplicitToolGrants...)
		desc.Sandbox = cloneSandboxSnapshot(desc.Sandbox)
		out.DelegateRestore = &desc
	}
	return &out
}

func dcrcAssertReason(t *testing.T, got, want string) {
	t.Helper()
	if got != want {
		t.Fatalf("restore reason=%q, want %q", got, want)
	}
}

func dcrcSameStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func dcrcExerciseRestoreGate(t *testing.T, s *Session, workDir string) {
	t.Helper()
	base := dcrcValidRecord(s, workDir)
	cases := []struct {
		name  string
		edit  func(*jobstore.JobRecord)
		state bool
		want  string
	}{
		{"nil_record", func(*jobstore.JobRecord) {}, true, notResumableMissingDelegateResumeMetadata},
		{"not_delegate", func(r *jobstore.JobRecord) { r.Type = jobstore.JobShell }, true, notResumableMissingDelegateResumeMetadata},
		{"missing_descriptor", func(r *jobstore.JobRecord) { r.DelegateRestore = nil }, true, notResumableMissingDelegateResumeMetadata},
		{"missing_child", func(r *jobstore.JobRecord) { r.DelegateRestore.ChildSessionID = " " }, true, notResumableParentLinkageUnavailable},
		{"wrong_transcript", func(r *jobstore.JobRecord) { r.DelegateRestore.TranscriptRef = "local:other" }, true, notResumableParentLinkageUnavailable},
		{"wrong_parent", func(r *jobstore.JobRecord) { r.DelegateRestore.ParentSessionID = "other" }, true, notResumableParentLinkageUnavailable},
		{"wrong_job", func(r *jobstore.JobRecord) { r.DelegateRestore.ParentJobID = "job_other" }, true, notResumableParentLinkageUnavailable},
		{"wrong_owner", func(r *jobstore.JobRecord) { r.DelegateRestore.OwnerSessionID = "other" }, true, notResumableParentLinkageUnavailable},
		{"wrong_visible", func(r *jobstore.JobRecord) { r.DelegateRestore.VisibleSessionID = "other" }, true, notResumableParentLinkageUnavailable},
		{"invalid_ref", func(r *jobstore.JobRecord) { r.TranscriptRef = "not-a-ref" }, true, notResumableParentLinkageUnavailable},
		{"bad_policy", func(r *jobstore.JobRecord) { r.DelegateRestore.LocalEnvPolicy = "bogus" }, true, notResumableParentLinkageUnavailable},
		{"blank_workdir", func(r *jobstore.JobRecord) { r.DelegateRestore.WorkingDir = " " }, true, notResumableParentLinkageUnavailable},
		{"bad_sandbox", func(r *jobstore.JobRecord) { r.DelegateRestore.Sandbox = &jobstore.SandboxSnapshot{Mode: "bogus"} }, true, notResumableSandboxUnsatisfiable},
		{"skill_without_body", func(r *jobstore.JobRecord) { r.DelegateRestore.FrozenSkillNames = []string{"skill"} }, true, notResumableCorruptChildSessionMeta},
		{"no_state_dir", func(*jobstore.JobRecord) {}, false, notResumableMissingChildSessionMeta},
		{"valid", func(*jobstore.JobRecord) {}, true, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "nil_record" {
				dcrcAssertReason(t, validateDelegateRestoreState(nil, s.ID(), tc.state), tc.want)
				return
			}
			rec := dcrcCloneRecord(base)
			tc.edit(rec)
			dcrcAssertReason(t, validateDelegateRestoreState(rec, s.ID(), tc.state), tc.want)
		})
	}

	for _, tc := range []struct {
		reason string
		want   string
	}{
		{notResumableWorktreeDisposed, "isolation worktree was disposed"},
		{notResumableWorkingDirMissing, "working directory no longer exists"},
		{"other", "target_not_resumable:other"},
	} {
		if got := notResumableSendError(tc.reason); !strings.Contains(got.Error(), tc.want) {
			t.Fatalf("notResumableSendError(%q)=%q, want substring %q", tc.reason, got, tc.want)
		}
	}

	if !isRuntimeLostDelegate(base) || isRuntimeLostDelegate(nil) {
		t.Fatal("runtime-lost delegate classification is inconsistent")
	}
	for _, tc := range []struct {
		status jobstore.Status
		want   SubagentStatus
	}{
		{jobstore.StatusCompleted, SubagentCompleted},
		{jobstore.StatusCancelled, SubagentCancelled},
		{jobstore.StatusFailed, SubagentFailed},
		{jobstore.StatusRunning, SubagentFailed},
	} {
		if got := subagentStatusFromJobStatus(tc.status); got != tc.want {
			t.Fatalf("subagentStatusFromJobStatus(%q)=%q, want %q", tc.status, got, tc.want)
		}
	}
}

func dcrcExerciseProfileAndEnvironment(t *testing.T, s *Session, workDir string) {
	t.Helper()
	desc := dcrcValidRecord(s, workDir).DelegateRestore
	if _, err := (*Session)(nil).resolveDelegateRestoreProfile(schemaSessionMetaZero(), desc); err == nil {
		t.Fatal("nil session restored a profile")
	}
	if _, err := s.resolveDelegateRestoreProfile(schemaSessionMetaZero(), nil); err == nil {
		t.Fatal("nil descriptor restored a profile")
	}
	missingModel := *desc
	missingModel.ResolvedModel = ""
	if _, err := s.resolveDelegateRestoreProfile(schemaSessionMetaZero(), &missingModel); err == nil {
		t.Fatal("descriptor without model restored a profile")
	}
	resolved, err := s.resolveDelegateRestoreProfile(schemaSessionMetaZero(), desc)
	if err != nil || resolved.ID() != "openai" || resolved.Model() != "gpt-5.2" {
		t.Fatalf("same-provider restored profile=%v err=%v", resolved, err)
	}

	s.resolveProfile = func(string) (*provider.Profile, error) { return nil, errors.New("resolver failure") }
	if _, err := s.resolveDelegateRestoreProfile(schemaSessionMetaZero(), desc); err == nil {
		t.Fatal("resolver failure was not returned")
	}
	s.resolveProfile = func(string) (*provider.Profile, error) { return nil, nil }
	if _, err := s.resolveDelegateRestoreProfile(schemaSessionMetaZero(), desc); err == nil {
		t.Fatal("nil resolver profile was accepted")
	}
	s.resolveProfile = func(string) (*provider.Profile, error) { return provider.NewOpenAIProfile("gpt-restore"), nil }
	resolved, err = s.resolveDelegateRestoreProfile(schemaSessionMetaZero(), desc)
	if err != nil || resolved.Model() != "gpt-restore" {
		t.Fatalf("resolver profile=%v err=%v", resolved, err)
	}
	s.resolveProfile = nil

	if _, err := (*Session)(nil).restoreDelegateChildEnvironment(desc, "dlg_config"); err == nil {
		t.Fatal("nil session restored an environment")
	}
	noEnv := &Session{}
	if _, err := noEnv.restoreDelegateChildEnvironment(desc, "dlg_config"); err == nil {
		t.Fatal("session without environment restored an environment")
	}
	badPolicy := *desc
	badPolicy.LocalEnvPolicy = "bad"
	if _, err := s.restoreDelegateChildEnvironment(&badPolicy, "dlg_config"); err == nil {
		t.Fatal("invalid restored policy was accepted")
	}
	badDir := *desc
	badDir.WorkingDir = " "
	if _, err := s.restoreDelegateChildEnvironment(&badDir, "dlg_config"); err == nil {
		t.Fatal("blank restored directory was accepted")
	}
	childEnv, err := s.restoreDelegateChildEnvironment(desc, "dlg_config")
	if err != nil || childEnv == nil || childEnv.WorkingDirectory() != workDir {
		t.Fatalf("local restored environment=%v err=%v", childEnv, err)
	}

	denied := &Session{env: &agenttest.DenyEnv{WorkDir: workDir}}
	if got, err := denied.restoreDelegateChildEnvironment(desc, "dlg_config"); err != nil || got != denied.currentEnv() {
		t.Fatalf("same-directory DenyEnv restore=%v err=%v", got, err)
	}
	differentDir := *desc
	differentDir.WorkingDir = workDir + "/other"
	if _, err := denied.restoreDelegateChildEnvironment(&differentDir, "dlg_config"); err == nil {
		t.Fatal("DenyEnv accepted a changed restored directory")
	}

	if policy, reason := s.resolveRestoredDelegateSandbox(nil, workDir); policy != nil || reason != "" {
		t.Fatalf("nil sandbox restore=(%v,%q), want nil/empty", policy, reason)
	}
	badSandbox := *desc
	badSandbox.Sandbox = &jobstore.SandboxSnapshot{Mode: "bad"}
	if policy, reason := s.resolveRestoredDelegateSandbox(&badSandbox, workDir); policy != nil || reason != notResumableSandboxUnsatisfiable {
		t.Fatalf("invalid sandbox restore=(%v,%q)", policy, reason)
	}
}

// schemaSessionMetaZero avoids coupling this config target to a particular
// persisted meta shape: profile resolution intentionally depends only on the
// parent profile and descriptor.
func schemaSessionMetaZero() schema.SessionMeta { return schema.SessionMeta{} }

func dcrcExerciseRestorePreconditions(t *testing.T, s *Session, workDir string) {
	t.Helper()
	base := dcrcValidRecord(s, workDir)
	resumable := true
	preflight := &delegateRestorePreflight{}
	for _, tc := range []struct {
		name string
		rec  *jobstore.JobRecord
		id   string
	}{
		{"nil_session", base, base.DelegateRestore.ChildSessionID},
		{"nil_preflight", base, base.DelegateRestore.ChildSessionID},
		{"not_resumable", &jobstore.JobRecord{Type: jobstore.JobDelegate, Status: jobstore.StatusCompleted}, "child"},
		{"missing_descriptor", &jobstore.JobRecord{Type: jobstore.JobDelegate, Status: jobstore.StatusCompleted, Resumable: &resumable}, "child"},
		{"wrong_child", &jobstore.JobRecord{Type: jobstore.JobDelegate, Status: jobstore.StatusCompleted, Resumable: &resumable, DelegateRestore: &jobstore.DelegateRestoreDescriptor{ChildSessionID: "other"}}, "child"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var err error
			if tc.name == "nil_session" {
				_, err = (*Session)(nil).restoreTerminalDelegateChild(tc.rec, tc.id, preflight)
			} else if tc.name == "nil_preflight" {
				_, err = s.restoreTerminalDelegateChild(tc.rec, tc.id, nil)
			} else {
				_, err = s.restoreTerminalDelegateChild(tc.rec, tc.id, preflight)
			}
			if err == nil {
				t.Fatalf("%s restore precondition unexpectedly succeeded", tc.name)
			}
		})
	}

	for _, tc := range []struct {
		desc *jobstore.DelegateRestoreDescriptor
		want []string
	}{
		{nil, nil},
		{&jobstore.DelegateRestoreDescriptor{FrozenToolNames: []string{"*"}}, nil},
		{&jobstore.DelegateRestoreDescriptor{FrozenToolNames: []string{"read_file", "read_file", " "}, ExplicitToolGrants: []string{"task_list", "*"}}, []string{"read_file", "task_list"}},
	} {
		got := restoredDelegateRequiredTools(tc.desc)
		if !dcrcSameStrings(got, tc.want) {
			t.Fatalf("restored required tools=%v, want %v", got, tc.want)
		}
	}
	if err := validateRestoredDelegateRequiredToolNames(map[string]bool{"read_file": true}, []string{"missing", "also_missing"}); err == nil || !strings.Contains(err.Error(), "also_missing, missing") {
		t.Fatalf("missing required tools error=%v", err)
	}
}

func dcrcExerciseRestoreHelpers(t *testing.T, s *Session, rec *jobstore.JobRecord, workDir string) {
	t.Helper()
	desc := rec.DelegateRestore
	for _, name := range []string{"all", "none", "core_only", "default", "bogus"} {
		got, ok := delegateRestoreLocalEnvPolicy(&jobstore.DelegateRestoreDescriptor{LocalEnvPolicy: name})
		if (name == "bogus") == ok {
			t.Fatalf("local policy %q parsed=%v policy=%v", name, ok, got)
		}
	}
	for _, dir := range []string{"", " ", workDir} {
		got, ok := delegateRestoreWorkingDir(&jobstore.DelegateRestoreDescriptor{WorkingDir: dir})
		if (strings.TrimSpace(dir) != "") != ok || (ok && got != workDir) {
			t.Fatalf("working directory %q parsed as %q/%v", dir, got, ok)
		}
	}

	if got := delegateResultSchemaMap(nil); got != nil {
		t.Fatalf("nil result schema=%#v", got)
	}
	if got := delegateResultSchemaMap(map[string]any{}); got != nil {
		t.Fatalf("empty map result schema=%#v", got)
	}
	if got := delegateResultSchemaMap(struct {
		Type string `json:"type"`
	}{Type: "object"}); got == nil || got["type"] != "object" {
		t.Fatalf("struct result schema=%#v", got)
	}
	if got := delegateResultSchemaMap(map[string]any{"bad": func() {}}); got == nil || got["bad"] == nil {
		t.Fatalf("map result schema lost shallow fallback=%#v", got)
	}
	if got := delegateResultSchemaMap(func() {}); got != nil {
		t.Fatalf("unmarshalable result schema=%#v", got)
	}
	if got := cloneDelegateResultSchema(map[string]any{}); got != nil {
		t.Fatalf("empty clone=%#v", got)
	}
	if got, ok := cloneDelegateResultSchema(map[string]any{"type": "object"}).(map[string]any); !ok || got["type"] != "object" {
		t.Fatalf("result schema clone=%#v", got)
	}
	if got := delegateResultSchema(nil); got != nil {
		t.Fatalf("nil record schema=%#v", got)
	}
	if got := delegateResultSchema(rec); got == nil {
		t.Fatal("durable record lost its result schema")
	}

	if got := restoredDelegateAllowedTools(nil); got != nil {
		t.Fatalf("nil allowed tools=%v", got)
	}
	allowed := restoredDelegateAllowedTools(&jobstore.DelegateRestoreDescriptor{
		FrozenToolNames:    []string{"read_file", "read_file"},
		ExplicitToolGrants: []string{"task_list", "read_file"},
	})
	if !dcrcSameStrings(allowed, []string{"read_file", "read_file", "task_list"}) {
		t.Fatalf("allowed restored tools=%v", allowed)
	}
	if err := validateRestoredDelegateTools(nil, &jobstore.DelegateRestoreDescriptor{FrozenToolNames: []string{"read_file"}}); err == nil {
		t.Fatal("nil restored child registry was accepted")
	}
	if err := s.validateRestoredDelegateRequiredTools(&jobstore.DelegateRestoreDescriptor{FrozenToolNames: []string{"read_file"}}); err != nil {
		t.Fatalf("available required tool rejected: %v", err)
	}
	if err := s.validateRestoredDelegateRequiredTools(&jobstore.DelegateRestoreDescriptor{FrozenToolNames: []string{"delegate"}, DelegationAllowance: 0}); err == nil {
		t.Fatal("leaf delegate retained a root-only required tool")
	}
	if restoredParentInstallWatch(s, &jobstore.DelegateRestoreDescriptor{}) != nil || restoredParentClearWatch(s, &jobstore.DelegateRestoreDescriptor{}) != nil {
		t.Fatal("ungrounded parent watch callbacks were installed")
	}
	watchDesc := &jobstore.DelegateRestoreDescriptor{ParentWatchGranted: true}
	if restoredParentInstallWatch(s, watchDesc) == nil || restoredParentClearWatch(s, watchDesc) == nil {
		t.Fatal("granted parent watch callbacks were not restored")
	}

	assessment := s.assessDelegateResumability(rec, delegateResumabilityPreflight)
	if !assessment.Resumable || assessment.Preflight == nil {
		t.Fatalf("restore helper preflight=%+v", assessment)
	}
	childID := desc.ChildSessionID
	badTools := dcrcCloneRecord(rec)
	badTools.DelegateRestore.FrozenToolNames = []string{"not_registered"}
	if sub, err := s.restoreTerminalDelegateChildClaimed(badTools, childID, assessment.Preflight); sub != nil || err == nil {
		t.Fatalf("unavailable restored tool sub=%v err=%v", sub, err)
	}
	badSkills := dcrcCloneRecord(rec)
	badSkills.DelegateRestore.FrozenSkillNames = []string{"missing-body"}
	if sub, err := s.restoreTerminalDelegateChildClaimed(badSkills, childID, assessment.Preflight); sub != nil || err == nil {
		t.Fatalf("mismatched restored skills sub=%v err=%v", sub, err)
	}
	badEnv := dcrcCloneRecord(rec)
	badEnv.DelegateRestore.LocalEnvPolicy = "bogus"
	if sub, err := s.restoreTerminalDelegateChildClaimed(badEnv, childID, assessment.Preflight); sub != nil || err == nil {
		t.Fatalf("invalid restored environment sub=%v err=%v", sub, err)
	}

	// Use the injected fake prober so the sandbox preflight can exercise the
	// refusal path without invoking the host bwrap/uname probes.
	s.cfg.testOnly.sandboxProber = sandbox.FakeProber{Facts: sandbox.HostFacts{OS: "linux", Home: workDir, BwrapCapable: false}}
	if facts := s.sandboxHostFacts(); facts.OS != "linux" || facts.BwrapCapable {
		t.Fatalf("fake sandbox facts=%+v", facts)
	}
	sandboxed := *desc
	sandboxed.Sandbox = &jobstore.SandboxSnapshot{Mode: "restricted"}
	if policy, reason := s.resolveRestoredDelegateSandbox(&sandboxed, workDir); policy != nil || reason != notResumableSandboxUnsatisfiable {
		t.Fatalf("unenforceable restored sandbox=(%v,%q)", policy, reason)
	}
}

func dcrcAssertConfiguredCreate(t *testing.T, r *dcrcReader) {
	t.Helper()
	s, _, workDir := jdrNewSession(t)
	s.pluginAgents = map[string]plugin.Agent{
		"reviewer": {
			Name:         "reviewer",
			Description:  "deterministic fuzz reviewer",
			Model:        "inherit",
			Tools:        []string{"read_file"},
			SystemPrompt: "Review the scoped task.",
			PluginName:   "fuzz",
		},
	}

	invalid := []delegateArgs{
		{Task: " "},
		{Task: "invalid isolation", Isolation: "container"},
		{Task: "invalid sandbox", Sandbox: "not-a-sandbox"},
		{Task: "off sandbox net", Sandbox: "off", SandboxNet: dcrcBool(false)},
		{Task: "invalid grant", DelegationAllowance: 1},
		{Task: "unknown plugin", AgentType: "missing"},
	}
	for _, args := range invalid {
		res := s.createDelegate(context.Background(), args)
		if res.Err == nil || res.Status != jobstore.StatusFailed || res.Reason != "start_failed" {
			t.Fatalf("invalid create args=%+v result=%+v", args, res)
		}
		jdrAssertEmptyStore(t, s)
	}

	usePlugin := r.choose(2) == 1
	task := []string{"review durable configuration", "record delegate snapshot", "restore a retained child"}[r.choose(3)]
	resultSchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"message": map[string]any{"type": "string"},
		},
		"required": []string{"message"},
	}
	args := delegateArgs{
		Task:                task,
		Model:               "gpt-5.3",
		ReasoningEffort:     []string{"low", "high"}[r.choose(2)],
		Background:          false,
		BlockTimeoutMS:      500,
		DelegationAllowance: 0,
		WatchParent:         r.choose(2) == 1,
		ResultSchema:        resultSchema,
	}
	if usePlugin {
		args.AgentType = "reviewer"
	}
	ctx := context.WithValue(context.Background(), ctxToolCallID, "call_dcrc")
	ctx = context.WithValue(ctx, ctxToolItemID, "item_dcrc")
	res := s.createDelegate(ctx, args)
	if res.Err != nil || res.Status != jobstore.StatusCompleted || res.JobID == "" || res.DelegateID == "" {
		t.Fatalf("configured create=%+v", res)
	}
	rec := loadShellRecord(t, s.jobManager, res.JobID)
	desc := rec.DelegateRestore
	if desc == nil {
		t.Fatal("configured create did not persist a restore descriptor")
	}
	if desc.ParentSessionID != s.ID() || desc.ParentJobID != res.JobID || desc.OwnerSessionID != s.ID() || desc.VisibleSessionID != s.ID() || desc.Task != task {
		t.Fatalf("configured descriptor linkage=%+v", desc)
	}
	if desc.OriginToolCallID != "call_dcrc" || desc.OriginItemID != "item_dcrc" || desc.RequestedModel != args.Model || desc.ReasoningEffort != args.ReasoningEffort || desc.ResolvedProfileID != "openai" || desc.ResolvedModel != args.Model {
		t.Fatalf("configured descriptor launch snapshot=%+v", desc)
	}
	if desc.WorkingDir != workDir || desc.LocalEnvPolicy != "none" || desc.DelegationAllowance != 0 || desc.ParentWatchGranted != args.WatchParent {
		t.Fatalf("configured descriptor environment=%+v", desc)
	}
	if usePlugin {
		if desc.AgentType != "reviewer" || desc.AgentName != "reviewer" || !hasString(desc.FrozenToolNames, "read_file") || !hasString(desc.FrozenToolNames, "task_list") {
			t.Fatalf("plugin descriptor=%+v", desc)
		}
	} else if desc.AgentType != "" || desc.AgentName != "subagent" {
		t.Fatalf("default descriptor agent snapshot=%+v, want empty type and subagent name", desc)
	}
	if got := delegateResultSchemaMap(desc.ResultSchema); got == nil || got["type"] != "object" {
		t.Fatalf("configured result schema=%#v", desc.ResultSchema)
	}
	resultSchema["type"] = "mutated"
	if got := delegateResultSchemaMap(desc.ResultSchema); got == nil || got["type"] != "object" {
		t.Fatalf("descriptor aliased caller schema=%#v", desc.ResultSchema)
	}

	plain := s.delegateRestoreDescriptor("job_plain", "child_missing", "plain", encodeRef("", "child_missing"), nil, nil)
	if plain.ResolvedProfileID != "" || plain.ResolvedModel != "" {
		t.Fatalf("untracked child descriptor unexpectedly resolved profile=%+v", plain)
	}
	resumed := s.resumedDelegateRestoreDescriptor("job_resumed", desc.ChildSessionID, desc.TranscriptRef, map[string]any{"type": "string"}, desc)
	if resumed.Version != desc.Version || resumed.ParentJobID != "job_resumed" || resumed.ResolvedModel != desc.ResolvedModel || delegateResultSchemaMap(resumed.ResultSchema)["type"] != "string" {
		t.Fatalf("resumed descriptor=%+v", resumed)
	}
	if resumed.Sandbox != nil && resumed.Sandbox == desc.Sandbox {
		t.Fatal("resumed descriptor sandbox aliases previous descriptor")
	}

	dcrcExerciseRestoreGate(t, s, workDir)
	dcrcExerciseProfileAndEnvironment(t, s, workDir)
	dcrcExerciseRestorePreconditions(t, s, workDir)
	dcrcExerciseRestoreHelpers(t, s, rec, workDir)

	assessment := s.assessDelegateResumability(rec, delegateResumabilityPreflight)
	if !assessment.Resumable || assessment.Preflight == nil {
		t.Fatalf("fresh durable delegate assessment=%+v", assessment)
	}
	disposed := dcrcCloneRecord(rec)
	disposed.Disposed = true
	if got := s.assessDelegateResumability(disposed, delegateResumabilityPreflight); got.Reason != notResumableWorktreeDisposed {
		t.Fatalf("disposed assessment=%+v", got)
	}
	missingDir := dcrcCloneRecord(rec)
	missingDir.DelegateRestore.WorkingDir = workDir + "/missing"
	if got := s.assessDelegateResumability(missingDir, delegateResumabilityProjection); got.Reason != notResumableWorkingDirMissing {
		t.Fatalf("missing directory assessment=%+v", got)
	}
}

func dcrcBool(v bool) *bool { return &v }

func FuzzDelegateCreationRestoreConfigProgram(f *testing.F) {
	for _, seed := range [][]byte{
		{},
		{0, 0, 0},
		{1, 1, 1},
		{2, 2, 2},
		{255, 3, 1},
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		dcrcAssertConfiguredCreate(t, &dcrcReader{data: data})
	})
}
