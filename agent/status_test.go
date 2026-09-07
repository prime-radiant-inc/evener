package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/agent/skill"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

func TestDetailedStatusUsesNamespacedSkillCatalogKey(t *testing.T) {
	s := newTestSession(t)
	s.skills = map[string]skill.SkillMeta{
		"plugin:simplify": {Name: "simplify", Description: "rewrite", SkillFile: writeSkillBodyFile(t, "body")},
	}
	got := s.DetailedStatus()
	if len(got.Skills) != 1 || got.Skills[0].Name != "plugin:simplify" {
		t.Fatalf("skills = %+v", got.Skills)
	}
	if got.Skills[0].Dir != "" || got.Skills[0].SkillFile != "" {
		t.Fatalf("skills exposed filesystem metadata: %+v", got.Skills[0])
	}
}

func TestSession_DetailedStatus_DelegatesMatchControllerFoldAfterReopen(t *testing.T) {
	fixture := newColdStableDelegateFixtureConfigured(t, "", func(descriptor *delegatestore.Descriptor) {
		descriptor.Description = "stable status description"
		descriptor.ParentWatchGranted = true
		descriptor.DelegationAllowance = 2
	})
	want, _, err := LoadSessionDelegateStatus(context.Background(), fixture.stateDir, fixture.meta.ID)
	if err != nil {
		t.Fatalf("cold stable status: %v", err)
	}
	reopened, err := restoreDelegateResourceBootstrapSession(fixture.client, fixture.profile, fixture.workspace, fixture.meta, fixture.stateDir)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	defer reopened.Close()
	gotStatus := reopened.DetailedStatus()
	if len(want) != 1 || len(gotStatus.Delegates) != 1 {
		t.Fatalf("cold/reopened stable delegates = %d/%d, want 1/1", len(want), len(gotStatus.Delegates))
	}
	if !reflect.DeepEqual(want, gotStatus.Delegates) {
		t.Fatalf("stable status differs from reopened fold:\ncold=%+v\nreopened=%+v", want, gotStatus.Delegates)
	}
	got := gotStatus.Delegates[0]
	if got.DelegateID != fixture.delegateID || got.ChildSessionID != fixture.childID || got.OwnerSessionID != fixture.meta.ID || got.Type != "delegate" || got.Phase != "idle" || got.ProjectionRevision == 0 {
		t.Fatalf("stable delegate status = %+v", got)
	}
	if got.Description != "stable status description" || !got.ParentWatchGranted || got.DelegationAllowance != 2 {
		t.Fatalf("descriptor fidelity = %+v", got)
	}
}

func TestStableDelegateAttention_RestoreAndColdRead(t *testing.T) {
	tests := []struct {
		name              string
		pending           bool
		owed              bool
		journalAttention  bool
		lifecycle         string
		transcriptFailure string
		wantAttention     bool
		wantColdError     bool
	}{
		{name: "stale false with pending attention", pending: true, wantAttention: true},
		{name: "stale true without pending attention", journalAttention: true},
		{name: "closed delegate is ineligible", journalAttention: true, lifecycle: "closed", transcriptFailure: "missing"},
		{name: "stopping delegate is ineligible", journalAttention: true, lifecycle: "stopping", transcriptFailure: "missing"},
		{name: "permanently fenced delegate is ineligible", journalAttention: true, lifecycle: "fenced", transcriptFailure: "missing"},
		{name: "eligible missing transcript is an error", transcriptFailure: "missing", wantColdError: true},
		{name: "eligible unreadable transcript is an error", transcriptFailure: "unreadable", wantColdError: true},
		{name: "owed generation is admitted before boolean repair", owed: true, journalAttention: true},
	}

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newColdStableDelegateFixture(t, "")
			targetDelegateID := fixture.delegateID
			targetChildID := fixture.childID
			if tt.lifecycle == "fenced" {
				targetDelegateID = "dlg_permanently_fenced"
				targetChildID = "permanentlyfenced"
			}
			childPath := transcriptPath(fixture.stateDir, targetChildID)
			attentionID := fmt.Sprintf("watch:restore-cold-read:%02d", i)

			if tt.pending || tt.owed {
				if appended, err := appendColdDelegateNotificationDurablyWithOpen(
					childPath, targetChildID, attentionID, "restore and cold-read attention",
					time.Unix(1_700_000_500, 0).UTC(), transcript.OpenWriterForSession,
				); err != nil || !appended {
					t.Fatalf("append attention = appended:%t err:%v", appended, err)
				}
			}
			if tt.owed {
				writer, _, err := transcript.OpenWriterForSession(childPath, targetChildID)
				if err != nil {
					t.Fatalf("open owed attention transcript: %v", err)
				}
				if err := writer.AppendDurable(delegateAttentionResolutionTurnForGeneration(attentionID, delegateAttentionConsumed, 1)); err != nil {
					_ = writer.Close()
					t.Fatalf("append owed resolution: %v", err)
				}
				if err := writer.Close(); err != nil {
					t.Fatalf("close owed attention transcript: %v", err)
				}
			}

			if tt.journalAttention || tt.lifecycle != "" {
				store, err := delegatestore.Open(delegateResourceStorePath(fixture.stateDir, fixture.meta.ID))
				if err != nil {
					t.Fatalf("open delegate store: %v", err)
				}
				events, err := store.Load()
				if err != nil {
					_ = store.Close()
					t.Fatalf("load delegate store: %v", err)
				}
				state, err := delegatestore.Fold(events)
				if err != nil {
					_ = store.Close()
					t.Fatalf("fold delegate store: %v", err)
				}
				appendEvents := make([]delegatestore.Event, 0, 3)
				if tt.lifecycle == "fenced" {
					descriptor := state[fixture.delegateID].Descriptor
					descriptor.ChildSessionID = targetChildID
					descriptor.TranscriptRef = encodeRef("", targetChildID)
					descriptor.ParentDelegateID = fixture.delegateID
					appendEvents = append(appendEvents, delegatestore.Event{
						Kind:       delegatestore.EventDelegateCreated,
						DelegateID: targetDelegateID,
						Created:    &delegatestore.DelegateCreated{Descriptor: descriptor},
					})
				}
				if tt.journalAttention {
					appendEvents = append(appendEvents, delegatestore.Event{
						Kind:       delegatestore.EventDelegateAttentionChanged,
						DelegateID: targetDelegateID,
						AttentionChanged: &delegatestore.DelegateAttentionChanged{
							NeedsAttention: true,
						},
					})
				}
				switch tt.lifecycle {
				case "closed":
					appendEvents = append(appendEvents, delegatestore.Event{
						Kind:               delegatestore.EventDelegateResumabilityClosed,
						DelegateID:         fixture.delegateID,
						ResumabilityClosed: &delegatestore.ResumabilityClosed{Reason: "test closed"},
					})
				case "stopping":
					appendEvents = append(appendEvents, delegatestore.Event{
						Kind:                 delegatestore.EventDelegateSubtreeStopRequested,
						DelegateID:           fixture.delegateID,
						SubtreeStopRequested: &delegatestore.SubtreeStopRequested{TargetDelegateID: fixture.delegateID},
					})
				case "fenced":
					appendEvents = append(appendEvents, delegatestore.Event{
						Kind:               delegatestore.EventDelegateResumabilityClosed,
						DelegateID:         fixture.delegateID,
						ResumabilityClosed: &delegatestore.ResumabilityClosed{Reason: "test permanent fence"},
					})
				}
				if _, _, err := store.AppendBatch(state, appendEvents); err != nil {
					_ = store.Close()
					t.Fatalf("append delegate setup: %v", err)
				}
				if err := store.Close(); err != nil {
					t.Fatalf("close delegate store: %v", err)
				}
			}

			switch tt.transcriptFailure {
			case "missing":
				if err := os.Remove(childPath); err != nil && !os.IsNotExist(err) {
					t.Fatalf("remove child transcript: %v", err)
				}
			case "unreadable":
				if err := os.WriteFile(childPath, []byte("not a transcript\n"), 0o644); err != nil {
					t.Fatalf("corrupt child transcript: %v", err)
				}
			}

			cold, _, coldErr := LoadSessionDelegateStatus(context.Background(), fixture.stateDir, fixture.meta.ID)
			if tt.wantColdError {
				if coldErr == nil {
					t.Fatal("cold delegate status accepted an eligible missing/unreadable transcript")
				}
				return
			}
			if coldErr != nil {
				t.Fatalf("cold delegate status: %v", coldErr)
			}
			coldIndex := slices.IndexFunc(cold, func(row DelegateStatusInfo) bool { return row.DelegateID == targetDelegateID })
			if coldIndex < 0 || cold[coldIndex].NeedsAttention != tt.wantAttention {
				t.Fatalf("cold delegate status = %+v, want needs_attention=%t", cold, tt.wantAttention)
			}
			journalEvents, err := delegatestore.ReadEvents(delegateResourceStorePath(fixture.stateDir, fixture.meta.ID))
			if err != nil {
				t.Fatalf("read journal after cold status: %v", err)
			}
			journalState, err := delegatestore.Fold(journalEvents)
			if err != nil {
				t.Fatalf("fold journal after cold status: %v", err)
			}
			wantJournalAttention := tt.journalAttention && tt.lifecycle == ""
			if got := journalState[targetDelegateID].NeedsAttention; got != wantJournalAttention {
				t.Fatalf("cold status wrote journal needs_attention=%t, want unchanged %t", got, wantJournalAttention)
			}

			var release chan struct{}
			var launchObserved chan error
			if tt.owed {
				release = make(chan struct{})
				launchObserved = make(chan error, 1)
				fixture.adapter.steps = []func(llm.Request) llm.Response{func(llm.Request) llm.Response {
					events, err := delegatestore.ReadEvents(delegateResourceStorePath(fixture.stateDir, fixture.meta.ID))
					if err == nil {
						falseIndex, runIndex := -1, -1
						for i, event := range events {
							if event.Kind == delegatestore.EventDelegateAttentionChanged && event.AttentionChanged != nil && !event.AttentionChanged.NeedsAttention {
								falseIndex = i
							}
							if event.Kind == delegatestore.EventDelegateRunStarted && event.RunStarted != nil && event.RunStarted.Generation == 1 {
								runIndex = i
							}
						}
						if falseIndex < 0 || runIndex < 0 || falseIndex >= runIndex {
							err = fmt.Errorf("owed launch journal order = false attention at %d, generation 1 start at %d", falseIndex, runIndex)
						}
					}
					launchObserved <- err
					<-release
					return communicateResponse(true, "owed attention restored")
				}}
			}
			restored, err := restoreDelegateResourceBootstrapSession(fixture.client, fixture.profile, fixture.workspace, fixture.meta, fixture.stateDir)
			if err != nil {
				if release != nil {
					close(release)
				}
				t.Fatalf("restore delegate status: %v", err)
			}
			defer func() {
				if release != nil {
					close(release)
				}
				restored.Close()
			}()
			if tt.owed {
				if err := <-launchObserved; err != nil {
					t.Fatalf("owed launch observed before attention repair: %v", err)
				}
			}

			restored.delegateController.mu.Lock()
			aggregate := restored.delegateController.durable[targetDelegateID]
			wakeIDs := restored.delegateController.attentionWakeIDs[targetDelegateID]
			_, wakePublished := wakeIDs[attentionID]
			wakeCount := len(wakeIDs)
			restored.delegateController.mu.Unlock()
			if aggregate == nil || aggregate.NeedsAttention != tt.wantAttention {
				t.Fatalf("restored aggregate = %+v, want needs_attention=%t", aggregate, tt.wantAttention)
			}
			wantWake := tt.pending && !tt.owed
			wantWakeCount := 0
			if wantWake {
				wantWakeCount = 1
			}
			if wakePublished != wantWake || wakeCount != wantWakeCount {
				t.Fatalf("restored unresolved attention = %#v, want attention=%t count=%d", wakeIDs, wantWake, wantWakeCount)
			}
			if tt.owed && aggregate.Generation != 1 {
				t.Fatalf("owed generation = %d, want 1", aggregate.Generation)
			}
		})
	}
}

func TestSession_DetailedStatus_CoreTools(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	c := llm.NewClient()
	f := &fakeAdapter{
		name:  "openai",
		steps: []func(req llm.Request) llm.Response{},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	ds := sess.DetailedStatus()

	// Should have core tools.
	if len(ds.Tools) == 0 {
		t.Fatal("expected at least one tool")
	}

	// All tools from a vanilla session should be "core".
	for _, tool := range ds.Tools {
		if tool.Source != "core" {
			t.Errorf("tool %q has source %q, want core", tool.Name, tool.Source)
		}
	}

	// Verify some known core tools are present.
	toolNames := map[string]bool{}
	for _, tool := range ds.Tools {
		toolNames[tool.Name] = true
	}
	for _, name := range []string{"shell", "read_file", "write_file", "edit_file"} {
		if !toolNames[name] {
			t.Errorf("missing core tool %q", name)
		}
	}
}

func TestSession_DetailedStatus_CustomTool(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	c := llm.NewClient()
	f := &fakeAdapter{
		name:  "openai",
		steps: []func(req llm.Request) llm.Response{},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	// Register a custom tool after session init.
	sess.RegisterTool("my_custom_tool", "A custom tool", map[string]any{
		"type": "object", "properties": map[string]any{},
	}, func(ctx context.Context, args any) (any, error) {
		return "ok", nil
	})

	ds := sess.DetailedStatus()

	found := false
	for _, tool := range ds.Tools {
		if tool.Name == "my_custom_tool" {
			if tool.Source != "custom" {
				t.Errorf("custom tool source = %q, want custom", tool.Source)
			}
			found = true
		}
	}
	if !found {
		t.Error("custom tool not found in DetailedStatus")
	}
}

func TestSession_DetailedStatus_Skills(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	// Create a skill directory.
	skillDir := filepath.Join(dir, "skills", "my-skill")
	os.MkdirAll(skillDir, 0o755)
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(`---
name: my-skill
description: A test skill
---
# My Skill
`), 0o644)

	c := llm.NewClient()
	f := &fakeAdapter{
		name:  "openai",
		steps: []func(req llm.Request) llm.Response{},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	ds := sess.DetailedStatus()

	found := false
	for _, skill := range ds.Skills {
		if skill.Name == "my-skill" {
			found = true
			if skill.Description != "A test skill" {
				t.Errorf("skill description = %q, want %q", skill.Description, "A test skill")
			}
		}
	}
	if !found {
		t.Error("skill my-skill not found in DetailedStatus")
	}
}

func TestSession_DetailedStatus_EmptySections(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	c := llm.NewClient()
	f := &fakeAdapter{
		name:  "openai",
		steps: []func(req llm.Request) llm.Response{},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	ds := sess.DetailedStatus()

	// No MCP servers in vanilla session.
	if len(ds.MCP) != 0 {
		t.Errorf("expected no MCP servers, got %d", len(ds.MCP))
	}
	// No plugins in a vanilla session.
	if ds.Plugins == nil {
		t.Fatal("expected an explicit empty plugin slice")
	}
	if len(ds.Plugins) != 0 {
		t.Errorf("expected no plugins, got %d", len(ds.Plugins))
	}
	// No jobs.
	if len(ds.Jobs) != 0 {
		t.Errorf("expected no jobs, got %d", len(ds.Jobs))
	}
	// Core agents are always present.
	foundDefault := false
	foundExplorer := false
	foundSubagent := false
	for _, name := range ds.Agents {
		if name == "default" {
			foundDefault = true
		}
		if name == "explorer" {
			foundExplorer = true
		}
		if name == "subagent" {
			foundSubagent = true
		}
	}
	if !foundDefault {
		t.Errorf("expected core 'default' agent in %v", ds.Agents)
	}
	if !foundExplorer {
		t.Errorf("expected core 'explorer' agent in %v", ds.Agents)
	}
	if !foundSubagent {
		t.Errorf("expected core 'subagent' agent in %v", ds.Agents)
	}
}

func TestSession_DetailedStatus_ConfiguredWorkflowPlugin(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	c := llm.NewClient()
	f := &fakeAdapter{
		name:  "openai",
		steps: []func(req llm.Request) llm.Response{},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5"), execenv.NewLocalExecutionEnvironment(dir), coordinatorWorkflowSessionConfig(t, SessionConfig{}))
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	ds := sess.DetailedStatus()

	if len(ds.Plugins) != 1 {
		t.Fatalf("expected 1 coordinator workflow plugin, got %d", len(ds.Plugins))
	}
	if ds.Plugins[0].Name != coordinatorWorkflowPluginName {
		t.Fatalf("plugin name = %q, want %q", ds.Plugins[0].Name, coordinatorWorkflowPluginName)
	}

	foundReviewer := slices.Contains(ds.Agents, "reviewer")
	if !foundReviewer {
		t.Fatalf("expected configured coordinator workflow reviewer agent in %v", ds.Agents)
	}
}

func TestSession_DetailedStatus_Jobs(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	c := llm.NewClient()
	f := &fakeAdapter{name: "openai"}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	exitCode := 7
	startedAt := time.Now().UTC()
	endedAt := startedAt.Add(time.Second)
	const jobID = "job_status_projection"
	const intent = "Running the test suite to find the failure"
	if err := sess.jobManager.store.Append(jobstore.Event{
		Kind:             jobstore.EventJobStarted,
		TS:               startedAt,
		JobID:            jobID,
		Type:             jobstore.JobShell,
		Command:          "go test ./...",
		Intent:           intent,
		OwnerSessionID:   sess.ID(),
		VisibleToSession: sess.ID(),
		StartedAt:        &startedAt,
	}); err != nil {
		t.Fatalf("append started event: %v", err)
	}
	if err := sess.jobManager.store.Append(jobstore.Event{
		Kind:        jobstore.EventJobFinished,
		TS:          endedAt,
		JobID:       jobID,
		Status:      jobstore.StatusFailed,
		Reason:      "exit_nonzero",
		ExitCode:    &exitCode,
		EndedAt:     &endedAt,
		OutputBytes: 128,
	}); err != nil {
		t.Fatalf("append finished event: %v", err)
	}

	ds := sess.DetailedStatus()

	if len(ds.Jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(ds.Jobs))
	}
	job := ds.Jobs[0]
	if job.JobID != jobID || job.JobType != string(jobstore.JobShell) || job.Status != string(jobstore.StatusFailed) ||
		job.Reason != "exit_nonzero" || job.TranscriptRef != shellTranscriptRef(jobID) ||
		job.OutputBytes != 128 || job.ExitCode == nil || *job.ExitCode != exitCode {
		t.Fatalf("job status = %+v", job)
	}
	if job.Intent != intent {
		t.Fatalf("job intent = %q, want %q", job.Intent, intent)
	}
}

func TestDetailedStatusJobRecords_OmitsLegacyDelegateActivations(t *testing.T) {
	records := detailedStatusJobRecords([]*jobstore.JobRecord{{
		JobID:  "job_exhausted",
		Type:   jobstore.JobType(delegateResourceType),
		Status: jobstore.StatusExhausted,
		Reason: "tool_round_budget_exhausted",
	}})
	if len(records) != 0 {
		t.Fatalf("legacy delegate activation records = %+v, want none", records)
	}
}

func TestSession_DetailedStatus_JobsKeepsActiveAndBoundsTerminal(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	c := llm.NewClient()
	f := &fakeAdapter{name: "openai"}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	base := time.Now().UTC()
	runningStartedAt := base.Add(-time.Hour)
	const runningJobID = "job_running_old"

	// Build every event up front and append it as one batch so the store fsyncs
	// once instead of ~105 times. AppendBatch assigns contiguous Seq in slice
	// order, identical to sequential Append, so Fold/sort order is unchanged.
	events := []jobstore.Event{{
		Kind:             jobstore.EventJobStarted,
		TS:               runningStartedAt,
		JobID:            runningJobID,
		Type:             jobstore.JobShell,
		Status:           jobstore.StatusRunning,
		OwnerSessionID:   sess.ID(),
		VisibleToSession: sess.ID(),
		StartedAt:        &runningStartedAt,
	}}

	for i := range detailedStatusTerminalJobsLimit + 2 {
		startedAt := base.Add(time.Duration(i) * time.Second)
		endedAt := startedAt.Add(time.Second)
		jobID := fmt.Sprintf("job_terminal_%02d", i)
		events = append(events, jobstore.Event{
			Kind:             jobstore.EventJobStarted,
			TS:               startedAt,
			JobID:            jobID,
			Type:             jobstore.JobShell,
			Status:           jobstore.StatusRunning,
			OwnerSessionID:   sess.ID(),
			VisibleToSession: sess.ID(),
			StartedAt:        &startedAt,
		}, jobstore.Event{
			Kind:        jobstore.EventJobFinished,
			TS:          endedAt,
			JobID:       jobID,
			Status:      jobstore.StatusCompleted,
			Reason:      "exit_zero",
			EndedAt:     &endedAt,
			OutputBytes: int64(i),
		})
	}

	if err := sess.jobManager.store.AppendBatch(events); err != nil {
		t.Fatalf("append job events: %v", err)
	}

	ds := sess.DetailedStatus()
	seen := map[string]JobStatusInfo{}
	terminal := 0
	for _, job := range ds.Jobs {
		seen[job.JobID] = job
		if jobstore.Status(job.Status).IsTerminal() {
			terminal++
		}
	}

	if _, ok := seen[runningJobID]; !ok {
		t.Fatalf("active job %q missing from DetailedStatus jobs: %+v", runningJobID, ds.Jobs)
	}
	if terminal != detailedStatusTerminalJobsLimit {
		t.Fatalf("terminal jobs = %d, want %d", terminal, detailedStatusTerminalJobsLimit)
	}
	if _, ok := seen["job_terminal_00"]; ok {
		t.Fatalf("oldest terminal job should be excluded from bounded DetailedStatus jobs: %+v", ds.Jobs)
	}
	if _, ok := seen[fmt.Sprintf("job_terminal_%02d", detailedStatusTerminalJobsLimit+1)]; !ok {
		t.Fatalf("newest terminal job missing from DetailedStatus jobs: %+v", ds.Jobs)
	}
}

func TestSession_DetailedStatus_ToolsSorted(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	c := llm.NewClient()
	f := &fakeAdapter{
		name:  "openai",
		steps: []func(req llm.Request) llm.Response{},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	ds := sess.DetailedStatus()

	names := make([]string, len(ds.Tools))
	for i, tool := range ds.Tools {
		names[i] = tool.Name
	}
	if !sort.StringsAreSorted(names) {
		t.Errorf("tools not sorted: %v", names)
	}
}

// TestDetailedStatus_HookEvents_ExcludesDeadHooks verifies that /status's supported
// hook count reflects only hooks that can actually run: a hook whose handler type is
// unsupported (http) or whose matcher is an invalid regex is dispatch-time dead, so
// it must not be counted as a supported active hook. The legacy Hooks map (registered
// hooks per event) still counts them (Fix 4).
func TestDetailedStatus_HookEvents_ExcludesDeadHooks(t *testing.T) {
	t.Parallel()
	pluginDir := t.TempDir()
	metaDir := filepath.Join(pluginDir, ".claude-plugin")
	os.MkdirAll(metaDir, 0o755)
	os.WriteFile(filepath.Join(metaDir, "plugin.json"),
		[]byte(`{"name": "dead-hook-test"}`), 0o644)
	hooksDir := filepath.Join(pluginDir, "hooks")
	os.MkdirAll(hooksDir, 0o755)
	// PreToolUse: ONLY an http handler (never executes → dispatch-time dead).
	// PostToolUse: a command handler with an invalid-regex matcher (skipped at dispatch).
	os.WriteFile(filepath.Join(hooksDir, "hooks.json"), []byte(`{
		"hooks": {
			"PreToolUse":  [{"matcher": "*", "hooks": [{"type": "http", "url": "http://example"}]}],
			"PostToolUse": [{"matcher": "(", "hooks": [{"type": "command", "command": "echo x", "timeout": 5}]}]
		}
	}`), 0o644)

	dir := t.TempDir()
	c := llm.NewClient()
	f := &fakeAdapter{name: "openai", steps: []func(req llm.Request) llm.Response{}}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5"),
		execenv.NewLocalExecutionEnvironment(dir),
		SessionConfig{PluginDirs: []string{pluginDir}})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	ds := sess.DetailedStatus()

	// Neither dead hook may surface as a supported active hook.
	for _, he := range ds.HookEvents {
		if !he.Supported {
			continue
		}
		if he.Event == plugin.HookPreToolUse {
			t.Errorf("PreToolUse has only an http (unsupported-type) handler; it must not be a supported active hook (got Count=%d)", he.Count)
		}
		if he.Event == plugin.HookPostToolUse {
			t.Errorf("PostToolUse's only handler has an invalid matcher; it must not be a supported active hook (got Count=%d)", he.Count)
		}
	}
}

// TestDetailedStatus_HookEvents verifies that DetailedStatus.HookEvents lists
// supported hook events with their tier and count, and lists recognized-but-
// unsupported events with Supported=false, Count=0, Tier="reserved-placeholder".
// The legacy Hooks map is preserved for backward compatibility.
func TestDetailedStatus_HookEvents(t *testing.T) {
	t.Parallel()
	// Build a plugin dir with PreToolUse (supported) and "Setup" (recognized but
	// not fired by evener — reserved-placeholder).
	pluginDir := t.TempDir()
	metaDir := filepath.Join(pluginDir, ".claude-plugin")
	os.MkdirAll(metaDir, 0o755)
	os.WriteFile(filepath.Join(metaDir, "plugin.json"),
		[]byte(`{"name": "hook-diag-test"}`), 0o644)
	hooksDir := filepath.Join(pluginDir, "hooks")
	os.MkdirAll(hooksDir, 0o755)
	os.WriteFile(filepath.Join(hooksDir, "hooks.json"), []byte(`{
		"hooks": {
			"PreToolUse": [{"matcher": "*", "hooks": [{"type": "command", "command": "echo ok", "timeout": 5}]}],
			"Setup":      [{"matcher": "*", "hooks": [{"type": "command", "command": "echo setup", "timeout": 5}]}]
		}
	}`), 0o644)

	dir := t.TempDir()
	c := llm.NewClient()
	f := &fakeAdapter{name: "openai", steps: []func(req llm.Request) llm.Response{}}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5"),
		execenv.NewLocalExecutionEnvironment(dir),
		SessionConfig{PluginDirs: []string{pluginDir}})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	ds := sess.DetailedStatus()

	// HookEvents should include PreToolUse as supported/claude-compatible-subset.
	var foundSupported, foundUnsupported bool
	for _, he := range ds.HookEvents {
		switch he.Event {
		case plugin.HookPreToolUse:
			if he.Count < 1 {
				t.Errorf("HookEvents PreToolUse count = %d, want ≥ 1", he.Count)
			}
			if !he.Supported {
				t.Errorf("PreToolUse: Supported = false, want true")
			}
			if he.Tier != "claude-compatible-subset" {
				t.Errorf("PreToolUse: Tier = %q, want claude-compatible-subset", he.Tier)
			}
			if he.Count < 1 {
				t.Errorf("PreToolUse: Count = %d, want ≥ 1", he.Count)
			}
			foundSupported = true
		case "Setup":
			if he.Supported {
				t.Errorf("Setup: Supported = true, want false")
			}
			if he.Tier != "reserved-placeholder" {
				t.Errorf("Setup: Tier = %q, want reserved-placeholder", he.Tier)
			}
			if he.Count != 0 {
				t.Errorf("Setup: Count = %d, want 0", he.Count)
			}
			foundUnsupported = true
		}
	}
	if !foundSupported {
		t.Error("HookEvents missing PreToolUse (supported)")
	}
	if !foundUnsupported {
		t.Error("HookEvents missing Setup (unsupported/reserved-placeholder)")
	}
}

// TestLoadSessionDelegateStatus_OversizedDelegateJournalLineDegradesWithDiagnosticInsteadOfFailing
// asserts an oversized delegates.jsonl line does not hard-fail the
// chat/transcript view for every session sharing that root -- live or
// historical -- on a single corrupt line: the posture is "loud but
// CONTAINED". LoadSessionDelegateStatus must not fail, and must carry a
// diagnosed error (with file + line info) rather than propagating
// ErrLineTooLong unclassified or swallowing it silently.
func TestLoadSessionDelegateStatus_OversizedDelegateJournalLineDegradesWithDiagnosticInsteadOfFailing(t *testing.T) {
	stateDir := t.TempDir()
	rootID := "oversizedelegateroot"
	writePastStableDelegates(t, stateDir, rootID, pastStableDescriptor(rootID, "child1", "a task long enough to exceed a tiny test line cap"))
	savePastActivityMeta(t, stateDir, rootID, "Root")

	// Inject a small MaxLineBytes so this test's fixture doesn't need an
	// actual 128 MiB line to trip delegatestore's package default -- same
	// established pattern as
	// TestLoadSessionJobActivityTree_PathologicalLineErrorsLoudlyNotSilently,
	// this test is about the CONTAINMENT property, not re-proving the cap
	// fires (delegatestore's own tests already do that).
	original := scanDelegateJournal
	scanDelegateJournal = func(ctx context.Context, path string, fromOffset int64, limits delegatestore.ScanLimits) ([]delegatestore.Event, int64, delegatestore.ReadDiagnostics, error) {
		limits.MaxLineBytes = 20
		return original(ctx, path, fromOffset, limits)
	}
	defer func() { scanDelegateJournal = original }()

	status, diagnostics, err := LoadSessionDelegateStatus(context.Background(), stateDir, rootID)
	if err != nil {
		t.Fatalf("LoadSessionDelegateStatus: %v, want nil error -- an oversized delegate journal line must degrade, not fail the whole ThreadRead RPC this feeds", err)
	}
	if len(status) != 0 {
		t.Fatalf("status = %+v, want empty (nothing is safely decodable once a line in the shared journal exceeds the cap)", status)
	}
	found := false
	for _, d := range diagnostics {
		if strings.Contains(d, "delegates.jsonl") && strings.Contains(d, "line") {
			found = true
		}
	}
	if !found {
		t.Fatalf("diagnostics = %v, want one identifying the oversized delegates.jsonl line (file + line info), visible rather than silently dropped", diagnostics)
	}
}

func TestSessionOwnsDelegateCancellationDuringJournalFold(t *testing.T) {
	stateDir := t.TempDir()
	ownerID := "02wMz5Txv1C3Hut0M8GCeC"
	childID := "02wMz5Txv1C3Hut0M8GCeD"
	writePastStableDelegates(t, stateDir, ownerID, pastStableDescriptor(ownerID, childID, "inspect ownership"))
	savePastActivityMeta(t, stateDir, ownerID, "Owner")
	synctest.Test(t, func(t *testing.T) {
		started, release := make(chan struct{}), make(chan struct{})
		original := scanDelegateJournal
		scanDelegateJournal = func(ctx context.Context, path string, fromOffset int64, limits delegatestore.ScanLimits) ([]delegatestore.Event, int64, delegatestore.ReadDiagnostics, error) {
			close(started)
			<-release
			return original(ctx, path, fromOffset, limits)
		}
		defer func() { scanDelegateJournal = original }()
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { _, err := SessionOwnsDelegate(ctx, stateDir, ownerID, childID); done <- err }()
		<-started
		cancel()
		synctest.Wait()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Errorf("ownership error=%v, want cancellation", err)
			}
		default:
			t.Error("ownership caller still waits for the journal fold after cancellation")
		}
		close(release)
		synctest.Wait()
		owned, err := SessionOwnsDelegate(context.Background(), stateDir, ownerID, childID)
		if err != nil || !owned {
			t.Errorf("shared fold lost healthy caller's result: owned=%v, err=%v", owned, err)
		}
	})
}
