//go:build serffuzz

package agent

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/agent/internal/hooks"
	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/internal/mcp"
	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/agent/mcpconfig"
	"primeradiant.com/serf/agent/plugin"
	"primeradiant.com/serf/agent/provider"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/skill"
	taskpkg "primeradiant.com/serf/agent/task"
	"primeradiant.com/serf/llm"
)

func FuzzAgentMiscProgram(f *testing.F) {
	f.Add([]byte("misc"))
	f.Fuzz(func(t *testing.T, data []byte) {
		token := string(data)
		if len(token) > 128 {
			token = token[:128]
		}
		miscCatalogAndReminderProgram(t, token)
		miscDiagnosticProgram(t, token)
		miscContinuationAndCounterProgram(t)
		miscStatusAndPersistenceProgram(t, token)
		miscFaultProgram(t)
	})
}

func miscCatalogAndReminderProgram(t *testing.T, token string) {
	t.Helper()
	agentDef := plugin.Agent{Name: "public", Tools: []string{"a"}, Skills: []string{"b"}, Tasks: []taskpkg.TaskTemplate{{Title: token}}}
	if got := exposedAgentCatalogKey(plugin.Instance{Manifest: plugin.Manifest{Name: coordinatorWorkflowPluginName}}, "raw", agentDef); got != "public" {
		t.Fatalf("coordinator catalog key = %q", got)
	}
	if got := exposedAgentCatalogKey(plugin.Instance{}, "raw", agentDef); got != "raw" {
		t.Fatalf("ordinary catalog key = %q", got)
	}
	original := map[string]plugin.Agent{"public": agentDef}
	cloned := cloneBuiltinAgents(original)
	cloned["public"].Tools[0] = "changed"
	if original["public"].Tools[0] != "a" {
		t.Fatal("built-in agent clone aliases tool storage")
	}
	if agents, err := loadBuiltinAgents(); err != nil || len(agents) == 0 {
		t.Fatalf("load built-in agents = %d, %v", len(agents), err)
	}
	if agents, err := builtinAgents(); err != nil || len(agents) == 0 {
		t.Fatalf("cached built-in agents = %d, %v", len(agents), err)
	}
	if names, err := CoreToolNames(); err != nil || len(names) == 0 {
		t.Fatalf("core tool names = %v, %v", names, err)
	}
	if coreToolNamesHasSchema(nil) {
		t.Fatal("nil registered tool reported a schema")
	}

	store := taskpkg.NewTaskStore(t.TempDir(), "misc")
	created, err := store.Append([]taskpkg.TaskInput{{Type: taskpkg.TaskTypeFix, Description: token, Prompt: "  prompt  ", ReasoningEffort: "high"}, {Type: taskpkg.TaskTypeResearch, Description: "dependent", DependsOn: []int{1}}})
	if err != nil || len(created) != 2 {
		t.Fatalf("append tasks = %#v, %v", created, err)
	}
	if err := store.Update([]taskpkg.TaskUpdate{{ID: 1, Status: taskpkg.TaskInProgress, Notes: token}}); err != nil {
		t.Fatal(err)
	}
	for _, reminder := range []string{taskReminderFull(store), taskReminderForInactivity(store), formatCurrentTaskSteering(created[0]), taskReminderAllDone(), taskReminderNudge()} {
		if !strings.Contains(reminder, "<SYSTEM-REMINDER>") {
			t.Fatalf("invalid task reminder %q", reminder)
		}
	}
	if got := taskReminderFull(taskpkg.NewTaskStore(t.TempDir(), "empty")); got != "" {
		t.Fatalf("empty reminder = %q", got)
	}
	if err := store.Update([]taskpkg.TaskUpdate{{ID: 1, Status: taskpkg.TaskDone}}); err != nil {
		t.Fatal(err)
	}
	if got := taskReminderForInactivity(store); got != "" {
		t.Fatalf("completed task inactivity reminder = %q", got)
	}
}

func miscDiagnosticProgram(t *testing.T, token string) {
	t.Helper()
	for _, data := range []events.EventData{events.WarningData{Message: token}, &events.WarningData{Message: token}, events.ErrorData{Error: token}, &events.ErrorData{Error: token}} {
		kind := events.EventWarning
		switch data.(type) {
		case events.ErrorData, *events.ErrorData:
			kind = events.EventError
		}
		if enrichDiagnosticData(kind, data) == nil {
			t.Fatal("diagnostic enrichment returned nil")
		}
	}
	var nilWarning *events.WarningData
	var nilError *events.ErrorData
	_ = enrichDiagnosticData(events.EventWarning, nilWarning)
	_ = enrichDiagnosticData(events.EventError, nilError)
	if providerCauseFromError(nil, token) != nil || providerCauseFromError(errors.New(token), token) != nil {
		t.Fatal("non-provider error acquired a cause")
	}
	providerErr := llm.NewStreamError(" provider ", token, nil)
	cause := providerCauseFromError(providerErr, "model")
	if cause == nil || cause.Provider != "provider" {
		t.Fatalf("provider cause = %#v", cause)
	}
	if providerCauseFromError(&llm.ConfigurationError{Message: token}, token) != nil {
		t.Fatal("empty provider acquired a cause")
	}
}

func miscContinuationAndCounterProgram(t *testing.T) {
	t.Helper()
	models := []llm.ModelInfo{{ID: "Exact"}, {ID: "Mixed"}}
	for _, model := range []string{"", "Exact", " mixed ", "absent"} {
		_, _ = liveModelInfoFor(models, model)
	}
	if got := resolveLiveModelProfile(context.Background(), nil, provider.NewOpenAIProfile("x")); got == nil {
		t.Fatal("nil client discarded profile")
	}
	client := llm.NewClient()
	client.Register(&miscModelAdapter{ScriptedAdapter: agenttest.ScriptedAdapter{Provider: "openai", Responder: func(llm.Request) llm.Response { return llm.Response{} }}, models: models})
	profile := provider.NewOpenAIProfile("mixed")
	if got := resolveLiveModelProfile(context.Background(), client, profile); got == profile {
		t.Fatal("live model metadata was not applied")
	}
	failing := llm.NewClient()
	failing.Register(&miscModelAdapter{ScriptedAdapter: agenttest.ScriptedAdapter{Provider: "openai", Responder: func(llm.Request) llm.Response { return llm.Response{} }}, err: errors.New("list failed")})
	if got := resolveLiveModelProfile(context.Background(), failing, profile); got != profile {
		t.Fatal("list failure changed profile")
	}
	if got := resolveLiveModelProfile(context.Background(), client, provider.NewOpenAIProfile("absent")); got.Model() != "absent" {
		t.Fatal("missing live model changed profile")
	}

	counter := newTreeCounter(0)
	for i := int64(0); i < counter.limit; i++ {
		if !counter.reserve(slotKindJob) {
			t.Fatalf("reservation %d rejected", i)
		}
	}
	if counter.reserve(slotKindJob) {
		t.Fatal("counter exceeded capacity")
	}
	for counter.n.Load() > 0 {
		counter.releaseKind(slotKindJob)
	}
	if slot, ok := (&Session{}).reserveTreeSlot(slotKindJob); !ok || slot != nil {
		t.Fatalf("unbounded reservation = %#v, %v", slot, ok)
	}
	releasePreparedTreeSlot(nil)

	unsafe := llm.Message{Content: []llm.ContentPart{{Kind: llm.ContentImage}}}
	_ = responsesContinuationMessageHasUnsafeDeltaContent(unsafe)
	anchor := schema.Turn{Kind: schema.TurnAssistant, Message: llm.Message{Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call"}}}}, ResponseID: "id", ResponseIDHash: "hash", ResponseEndpoint: "endpoint", ResponseStorageScopeFingerprint: "scope", ResponseRequestFingerprint: "request", ResponseContextMarker: responseContextMarkerV1}
	toolResult := func(callID string, image bool) schema.Turn {
		part := llm.ContentPart{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: callID}}
		if image {
			part.ToolResult.ImageData = []byte{1}
		}
		return schema.Turn{Kind: schema.TurnToolResults, Message: llm.Message{Content: []llm.ContentPart{part}}}
	}
	for _, history := range [][]schema.Turn{{anchor, {Kind: schema.TurnSteering}}, {anchor, {Kind: schema.TurnUserInput, Message: unsafe}}, {anchor, {Kind: schema.TurnToolResults, Message: llm.Message{Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "ignored"}}}}}, {anchor, toolResult("", false)}, {anchor, toolResult("other", false)}, {anchor, toolResult("call", true)}, {anchor, toolResult("call", false)}} {
		_, _ = selectResponsesContinuationAnchorCandidate(SessionConfig{}, history)
	}
	_, _ = selectResponsesContinuationAnchorCandidate(SessionConfig{SystemPromptAsUser: true}, []schema.Turn{anchor})
	for _, history := range [][]schema.Turn{nil, {{Kind: schema.TurnSummary}}, {{Kind: schema.TurnAssistant, Message: llm.Assistant("x")}}} {
		_, _ = selectResponsesContinuationAnchorCandidate(SessionConfig{}, history)
		reservation := reserveResponsesContinuationHistoryBase(history)
		_ = responsesContinuationHistoryBaseStillCurrent(reservation, history)
	}
}

type miscModelAdapter struct {
	agenttest.ScriptedAdapter
	models []llm.ModelInfo
	err    error
}

func (a *miscModelAdapter) ListModels(context.Context) ([]llm.ModelInfo, error) {
	return a.models, a.err
}

func miscStatusAndPersistenceProgram(t *testing.T, token string) {
	t.Helper()
	s, _ := statusSupportSession(t, provider.NewOpenAIProfile("status"), "openai", nil)
	s.skills = map[string]skill.SkillMeta{"z": {Name: "z"}, "a": {Name: "a"}}
	s.plugins = []plugin.Instance{{Manifest: plugin.Manifest{Name: "p", Version: "1"}, Skills: map[string]skill.SkillMeta{"s": {Name: "s"}}, Agents: map[string]plugin.Agent{"a": {}}, Hooks: map[plugin.HookEvent][]plugin.RegisteredHook{plugin.HookPreToolUse: {{Type: "command", Command: "true"}}}}}
	s.unsupportedPluginHookEvents = map[plugin.HookEvent]bool{plugin.HookEvent("WorktreeCreate"): true}
	runner := hooks.NewRunner(nil, "")
	runner.Add(plugin.HookPreToolUse, plugin.RegisteredHook{Matcher: "*", Type: "command", Command: "true"})
	s.hookRunner = runner
	oldServers := detailedStatusMCPServers
	t.Cleanup(func() { detailedStatusMCPServers = oldServers })
	s.mcpMgr = &mcp.Manager{}
	_ = oldServers(s)
	detailedStatusMCPServers = func(*Session) []mcpconfig.ServerInfo {
		return []mcpconfig.ServerInfo{{Name: "srv", Tools: []string{"mcp_tool"}}}
	}
	s.RegisterTool("mcp_tool", "fixture", map[string]any{"type": "object"}, func(context.Context, any) (any, error) { return nil, nil })
	ds := s.DetailedStatus()
	if len(ds.Skills) != 2 || ds.Skills[0].Name != "a" || len(ds.Plugins) != 1 || len(ds.HookEvents) != 2 {
		t.Fatalf("detailed status fixture = %#v", ds)
	}

	dir := t.TempDir()
	path := filepath.Join(jobsDir(dir, "valid"), "jobs.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := jobstore.OpenNoSync(path)
	if err != nil {
		t.Fatal(err)
	}
	events := []jobstore.Event{
		{Kind: jobstore.EventJobStarted, JobID: "good", Type: jobstore.JobDelegate},
		{Kind: jobstore.EventJobStarted, JobID: "shell", Type: jobstore.JobShell},
	}
	if err := store.AppendBatch(events); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	records, err := LoadSessionHistoricalJobRecords(dir, "valid")
	if err != nil || len(records) != 2 {
		t.Fatalf("historical records = %#v, %v", records, err)
	}
	docsDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(docsDir, "AGENTS.md"), []byte(strings.Repeat(token+"x", projectDocByteBudget+1)), 0o644); err != nil {
		t.Fatal(err)
	}
	docs, truncated := LoadProjectDocs(execenv.NewLocalExecutionEnvironment(docsDir), "AGENTS.md")
	if !truncated || len(docs) != 1 || !strings.Contains(docs[0].Content, projectDocTruncMark) {
		t.Fatalf("truncated docs = %#v, %v", docs, truncated)
	}
}

func miscFaultProgram(t *testing.T) {
	t.Helper()
	sentinel := errors.New("misc fault")
	dir := t.TempDir()
	src := w3sub_writeATIFTranscript(t, dir)
	oldMarshal, oldMkdir, oldWrite := atifMarshalIndent, atifMkdirAll, atifWriteFile
	t.Cleanup(func() { atifMarshalIndent, atifMkdirAll, atifWriteFile = oldMarshal, oldMkdir, oldWrite })
	atifMarshalIndent = func(any, string, string) ([]byte, error) { return nil, sentinel }
	if err := exportATIF(src, filepath.Join(dir, "out.json"), ""); !errors.Is(err, sentinel) {
		t.Fatalf("marshal fault = %v", err)
	}
	atifMarshalIndent = oldMarshal
	atifMkdirAll = func(string, fs.FileMode) error { return sentinel }
	if err := exportATIF(src, filepath.Join(dir, "out", "x.json"), ""); !errors.Is(err, sentinel) {
		t.Fatalf("mkdir fault = %v", err)
	}
	atifMkdirAll = oldMkdir
	atifWriteFile = func(string, []byte, fs.FileMode) error { return sentinel }
	if err := exportATIF(src, filepath.Join(dir, "x.json"), ""); !errors.Is(err, sentinel) {
		t.Fatalf("write fault = %v", err)
	}
	atifWriteFile = oldWrite

	oldFS := builtinAgentsFS
	t.Cleanup(func() { builtinAgentsFS = oldFS })
	builtinAgentsFS = func() fs.FS { return fstest.MapFS{"bad.md": &fstest.MapFile{Data: []byte("not frontmatter")}} }
	if _, err := loadBuiltinAgents(); err == nil {
		t.Fatal("invalid built-in agent succeeded")
	}
	builtinAgentsFS = func() fs.FS {
		return fstest.MapFS{"dir": &fstest.MapFile{Mode: fs.ModeDir}, "skip.txt": &fstest.MapFile{Data: []byte("x")}}
	}
	if got, err := loadBuiltinAgents(); err != nil || len(got) != 0 {
		t.Fatalf("filtered built-ins = %#v, %v", got, err)
	}
	builtinAgentsFS = func() fs.FS { return miscReadDirErrorFS{err: sentinel} }
	if _, err := loadBuiltinAgents(); !errors.Is(err, sentinel) {
		t.Fatalf("built-in readdir fault = %v", err)
	}
	builtinAgentsFS = func() fs.FS { return miscReadFileErrorFS{err: sentinel} }
	if _, err := loadBuiltinAgents(); !errors.Is(err, sentinel) {
		t.Fatalf("built-in read fault = %v", err)
	}
	builtinAgentsFS = oldFS
	oldCacheErr := builtinAgentsCache.err
	builtinAgentsCache.err = sentinel
	if _, err := builtinAgents(); !errors.Is(err, sentinel) {
		t.Fatalf("cached built-in fault = %v", err)
	}
	builtinAgentsCache.err = oldCacheErr

	oldTemp, oldSession, oldHasSchema := coreToolNamesMkdirTemp, coreToolNamesNewSession, coreToolNamesHasSchema
	t.Cleanup(func() {
		coreToolNamesMkdirTemp, coreToolNamesNewSession, coreToolNamesHasSchema = oldTemp, oldSession, oldHasSchema
	})
	coreToolNamesMkdirTemp = func(string, string) (string, error) { return "", sentinel }
	if _, err := CoreToolNames(); !errors.Is(err, sentinel) {
		t.Fatalf("temp fault = %v", err)
	}
	coreToolNamesMkdirTemp = oldTemp
	coreToolNamesNewSession = func(*llm.Client, *provider.Profile, execenv.ExecutionEnvironment, SessionConfig) (*Session, error) {
		return nil, sentinel
	}
	if _, err := CoreToolNames(); !errors.Is(err, sentinel) {
		t.Fatalf("session fault = %v", err)
	}
	coreToolNamesNewSession = oldSession
	coreToolNamesHasSchema = func(*tool.RegisteredTool) bool { return false }
	if names, err := CoreToolNames(); err != nil || len(names) != 0 {
		t.Fatalf("schema filtering = %v, %v", names, err)
	}
	coreToolNamesHasSchema = oldHasSchema

	oldInfo, oldReadDir := workspaceEntryInfo, workspaceReadDir
	t.Cleanup(func() { workspaceEntryInfo, workspaceReadDir = oldInfo, oldReadDir })
	workspaceEntryInfo = func(os.DirEntry) (fs.FileInfo, error) { return nil, sentinel }
	_, _ = walkTree(dir, 10)
	workspaceEntryInfo = oldInfo
	workspaceReadDir = func(string) ([]os.DirEntry, error) { return nil, sentinel }
	_, _ = walkTree(dir, 10)
	workspaceReadDir = oldReadDir

	broken := filepath.Join(dir, "broken")
	if err := os.Symlink(filepath.Join(dir, "missing"), broken); err == nil {
		_, _ = walkTree(dir, 10)
	}

	for _, sessionID := range []string{"missing", "bad"} {
		if sessionID == "bad" {
			path := filepath.Join(jobsDir(dir, sessionID), "jobs.jsonl")
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("not-json\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		_, _ = LoadSessionHistoricalJobRecords(dir, sessionID)
	}
	_ = jobstore.JobRecord{}
	oldStat, oldOpen := historicalJobsStat, historicalJobsOpen
	t.Cleanup(func() { historicalJobsStat, historicalJobsOpen = oldStat, oldOpen })
	validPath := filepath.Join(jobsDir(dir, "valid"), "jobs.jsonl")
	if err := os.MkdirAll(filepath.Dir(validPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(validPath, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	historicalJobsStat = func(string) (fs.FileInfo, error) { return nil, sentinel }
	if _, err := LoadSessionHistoricalJobRecords(dir, "x"); !errors.Is(err, sentinel) {
		t.Fatalf("historical stat fault = %v", err)
	}
	historicalJobsStat = oldStat
	historicalJobsOpen = func(string) (historicalJobStore, error) { return nil, sentinel }
	if _, err := LoadSessionHistoricalJobRecords(dir, "valid"); !errors.Is(err, sentinel) {
		t.Fatalf("historical open fault = %v", err)
	}
	historicalJobsOpen = func(string) (historicalJobStore, error) { return &miscHistoricalStore{loadErr: sentinel}, nil }
	if _, err := LoadSessionHistoricalJobRecords(dir, "valid"); !errors.Is(err, sentinel) {
		t.Fatalf("historical load fault = %v", err)
	}
	historicalJobsOpen = func(string) (historicalJobStore, error) {
		return &miscHistoricalStore{records: map[string]*jobstore.JobRecord{"nil": nil}}, nil
	}
	if got, err := LoadSessionHistoricalJobRecords(dir, "valid"); err != nil || len(got) != 0 {
		t.Fatalf("nil historical record = %#v, %v", got, err)
	}
}

type miscReadDirErrorFS struct{ err error }

func (f miscReadDirErrorFS) Open(string) (fs.File, error) { return nil, f.err }

type miscReadFileErrorFS struct{ err error }

func (f miscReadFileErrorFS) Open(string) (fs.File, error) { return nil, f.err }
func (f miscReadFileErrorFS) ReadDir(string) ([]fs.DirEntry, error) {
	return []fs.DirEntry{miscDirEntry{name: "bad.md"}}, nil
}

type miscDirEntry struct{ name string }

func (e miscDirEntry) Name() string             { return e.name }
func (miscDirEntry) IsDir() bool                { return false }
func (miscDirEntry) Type() fs.FileMode          { return 0 }
func (miscDirEntry) Info() (fs.FileInfo, error) { return nil, errors.New("unused") }

type miscHistoricalStore struct {
	records map[string]*jobstore.JobRecord
	loadErr error
}

func (*miscHistoricalStore) Close() error { return nil }
func (s *miscHistoricalStore) Load() (map[string]*jobstore.JobRecord, error) {
	return s.records, s.loadErr
}
func (s *miscHistoricalStore) LoadOrdered() ([]*jobstore.JobRecord, error) {
	return nil, s.loadErr
}
