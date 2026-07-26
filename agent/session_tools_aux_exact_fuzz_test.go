//go:build serffuzz

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v5"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/goal"
	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/agent/internal/tool/repair"
	"primeradiant.com/serf/agent/provenance"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/skill"
	taskpkg "primeradiant.com/serf/agent/task"
	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/llm"
)

// FuzzSessionToolsAuxExact replays finite, deterministic edge states for the
// auxiliary session tools. Filesystem fixtures live below t.TempDir and every
// registry dependency is an in-memory recorder.
func FuzzSessionToolsAuxExact(f *testing.F) {
	f.Add(byte(0))
	f.Fuzz(func(t *testing.T, _ byte) {
		auxRepairExact(t)
		auxAskExact(t)
		auxCommunicateExact(t)
		auxGoalTaskExact(t)
		auxFindExact(t)
		auxWebFetchExact(t)
	})
}

type auxHTTPDoer func(*http.Request) (*http.Response, error)

func (f auxHTTPDoer) Do(r *http.Request) (*http.Response, error) { return f(r) }

func auxWebFetchExact(t *testing.T) {
	s, _, _ := stmNewSession(t, []byte("web-exact"))
	if s.webFetchClient() == nil {
		t.Fatal("default web client is nil")
	}
	response := func() *http.Response {
		return &http.Response{StatusCode: 200, Status: "200 OK", Header: http.Header{"Content-Type": []string{"text/html"}}, Body: io.NopCloser(strings.NewReader("<p>body</p>"))}
	}
	s.httpClient = auxHTTPDoer(func(*http.Request) (*http.Response, error) { return response(), nil })
	base := webFetchRuntime{
		cachePath:  func(string) string { return t.TempDir() },
		mkdirAll:   os.MkdirAll,
		writeFile:  os.WriteFile,
		toMarkdown: htmlToMarkdown,
		newRequest: http.NewRequestWithContext,
	}
	for _, mutate := range []func(*webFetchRuntime){
		func(rt *webFetchRuntime) {
			rt.mkdirAll = func(string, os.FileMode) error { return errors.New("mkdir") }
		},
		func(rt *webFetchRuntime) {
			rt.writeFile = func(string, []byte, os.FileMode) error { return errors.New("raw write") }
		},
		func(rt *webFetchRuntime) {
			rt.toMarkdown = func(string) (string, error) { return "", errors.New("markdown") }
		},
		func(rt *webFetchRuntime) {
			calls := 0
			rt.writeFile = func(string, []byte, os.FileMode) error {
				calls++
				if calls == 2 {
					return errors.New("markdown write")
				}
				return nil
			}
		},
		func(rt *webFetchRuntime) {
			rt.newRequest = func(context.Context, string, string, io.Reader) (*http.Request, error) {
				return nil, errors.New("request")
			}
		},
	} {
		rt := base
		mutate(&rt)
		_, _ = s.webFetchWithRuntime(context.Background(), "https://fixture.invalid/page", "question", rt)
	}
}

func auxRepairExact(t *testing.T) {
	call := llm.ToolCallData{Arguments: json.RawMessage(`{"x":1}`)}
	if got := prepareToolCall(call, nil, []string{"known"}, "missing"); got.Call.ID == "" || got.PrevalErr == "" {
		t.Fatalf("unknown repair = %#v", got)
	}
	if got := offendingField(errors.New("plain")); got != "" {
		t.Fatalf("plain offending field = %q", got)
	}
	ve := &jsonschema.ValidationError{InstanceLocation: "/outer/field"}
	if got := offendingField(ve); got != "field" {
		t.Fatalf("nested offending field = %q", got)
	}
	_ = changeStrings([]repair.Change{{Kind: repair.ChangeKind("fixture"), Field: "x", Detail: "y"}})
}

func auxAskExact(t *testing.T) {
	if _, err := parseAskQuestions(map[string]any{"questions": []any{map[string]any{"options": []any{
		map[string]any{"label": "a", "recommended": true}, map[string]any{"label": "b", "recommended": true},
	}}}}); err == nil {
		t.Fatal("multiple recommendations accepted")
	}
	s, _, _ := stmNewSession(t, []byte("ask-exact"))
	deps := newToolDeps(s)
	reg := tool.NewRegistry()
	registerAskTool(reg, s, deps)
	h := reg.Get("ask_user").Exec
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _ = h(ctx, nil, map[string]any{})
	s.cfg.NonInteractive = true
	if _, err := h(context.Background(), nil, map[string]any{}); err == nil {
		t.Fatal("noninteractive ask accepted")
	}

	badJSON := llm.ToolCallData{ID: "bad-json", Name: "ask_user", Arguments: json.RawMessage(`{`)}
	badSemantic := llm.ToolCallData{ID: "bad-sem", Name: "ask_user", Arguments: json.RawMessage(`{"questions":[{"options":[{"label":"x"},{"label":"x"}]}]}`)}
	nonAsk := llm.ToolCallData{ID: "other", Name: "communicate", Arguments: json.RawMessage(`{}`)}
	history := []schema.Turn{
		stmAssistantTurn(nonAsk, badJSON, badSemantic),
		stmToolResultsTurn(stmToolResult("bad-json", "ask_user", false), stmToolResult("bad-sem", "ask_user", false)),
	}
	if got, ask := deriveRestoredAskPending(history); !ask || len(got) != 0 {
		t.Fatalf("invalid restored asks = %#v, %v", got, ask)
	}
	if got := questionsFromAskCalls([]schema.Turn{{Kind: schema.TurnUserInput}}, 1, map[string]bool{"x": true}); got != nil {
		t.Fatalf("missing assistant questions = %#v", got)
	}
}

func auxCommunicateExact(t *testing.T) {
	var nilSession *Session
	nilSession.deliverWatchCommunicateCallback("ignored")
	s, _, _ := stmNewSession(t, []byte("callback-reject"))
	s.setActiveEntryKind(EntryWatchDelivery)
	s.deliverWatchCommunicateCallback("no route")
	s.cfg.spawn.parentSteerDelivered = func(string, *provenance.Causal, string) bool { return false }
	s.deliverWatchCommunicateCallback("rejected")
	if usesDefaultCommunicateOutputEnvelope(llm.ToolDefinition{Parameters: map[string]any{"properties": map[string]any{
		"output": map[string]any{"properties": map[string]any{"message": 1, "data": 1, "artifacts": 1}, "required": []any{"message", "data"}},
	}}}) {
		t.Fatal("incomplete communicate requirements accepted")
	}
	if communicateSchemaContains([]string{"x"}, "y") {
		t.Fatal("schema contains absent value")
	}
	if got := canonicalNodeOutputTextWithMarshal(nil, func(any) ([]byte, error) { return nil, errors.New("marshal") }); got != "{}" {
		t.Fatalf("unmarshalable canonical output = %q", got)
	}

	root := t.TempDir()
	meta := skill.SkillMeta{Name: "fixture", Dir: root, SkillFile: filepath.Join(root, "SKILL.md")}
	reg := tool.NewRegistry()
	_ = reg.Register(tool.RegisteredTool{Tool: llm.Tool{Definition: tool.DefUseSkill()}, Exec: func(context.Context, execenv.ExecutionEnvironment, map[string]any) (any, error) { return nil, nil }})
	deps := &toolDeps{skill: func(name string) (skill.SkillMeta, bool) { return meta, name == "fixture" }, emit: func(events.EventKind, events.EventData) {}}
	registerSkillTool(reg, deps)
	h := reg.Get("use_skill").Exec
	if _, err := h(context.Background(), nil, map[string]any{"skill_name": "missing"}); err == nil {
		t.Fatal("missing skill accepted")
	}
	if _, err := h(context.Background(), nil, map[string]any{"skill_name": "fixture"}); err == nil {
		t.Fatal("skill without SKILL.md loaded")
	}
	if err := os.WriteFile(filepath.Join(root, "SKILL.md"), []byte("---\nname: fixture\ndescription: fixture\n---\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := h(context.Background(), nil, map[string]any{"skill_name": "fixture"}); err != nil || !strings.Contains(got.(string), "body") {
		t.Fatalf("skill load = %#v, %v", got, err)
	}
}

func auxGoalTaskExact(t *testing.T) {
	s, _, _ := stmNewSession(t, []byte("goal-task-exact"))
	goalHandler := s.reg.Get("update_goal").Exec
	if _, err := goalHandler(context.Background(), nil, map[string]any{"status": "invalid"}); err == nil {
		t.Fatal("invalid goal status accepted")
	}

	reg := tool.NewRegistry()
	registerTaskTools(reg, newToolDeps(s))
	h := reg.Get("task_list").Exec
	for _, args := range []map[string]any{
		{"action": "append", "tasks": []any{"bad"}},
		{"action": "update", "updates": []any{"bad"}},
		{"action": "unknown"},
	} {
		if _, err := h(context.Background(), nil, args); err == nil {
			t.Fatalf("invalid task args accepted: %#v", args)
		}
	}
	if got := formatTaskUpdates([]taskpkg.TaskUpdate{{ID: 7}}); got != "7" {
		t.Fatalf("statusless update = %q", got)
	}
	_ = formatTaskList([]taskpkg.Task{{ID: 1, Type: taskpkg.TaskTypeImplement, Description: "x", Status: taskpkg.TaskDone, DependsOn: []int{2}, ReasoningEffort: "high", Notes: []string{"n"}}})
	_ = goalStateView(goal.Snapshot{})
	if taskListAllDone([]taskpkg.Task{{Status: taskpkg.TaskOpen}}) || !taskListAllDone([]taskpkg.Task{{Status: taskpkg.TaskDone}}) {
		t.Fatal("task completion classifier mismatch")
	}
}

func auxFindExact(t *testing.T) {
	zero, over := 0, findLimitMax+1
	if clampFindLimit(&zero) != findLimitDefault || clampFindLimit(&over) != findLimitMax {
		t.Fatal("find limit clamp mismatch")
	}
	trunc := true
	text := formatSessionFindings(findSessionsEnvelope{ScopeApplied: scopeAllProjects, Scanned: &zero, ScanTruncated: &trunc, Matches: []sessionRecord{
		{TranscriptRef: "local:a", Kind: kindRoot, Title: "a", UpdatedAt: time.Unix(0, 0), ApproxTurns: 1},
		{TranscriptRef: "proj:b", Kind: kindFork, Title: "b", UpdatedAt: time.Unix(1, 0), ApproxTurns: 2, Project: "p", IsCurrent: true, ParentRef: "local:a", Snippets: []snippet{{Seq: 1, Role: "user", Snippet: "x"}}},
	}})
	if !strings.Contains(text, "2 matches") || !strings.Contains(text, "scan truncated") {
		t.Fatalf("formatted findings = %q", text)
	}
	_ = formatSessionFindings(findSessionsEnvelope{ScopeApplied: scopeCurrentProject})
	if got := formatFindSessionResult("raw"); got != "raw" {
		t.Fatalf("raw find result = %#v", got)
	}

	root := t.TempDir()
	good := filepath.Join(root, "good")
	bad := filepath.Join(root, "bad")
	if err := os.MkdirAll(good, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bad, []byte("not-dir"), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = collectCandidates([]string{bad}, good)
	if buckets, scope := findBuckets(good, scopeAllProjects); len(buckets) != 1 || scope != scopeCurrentProject {
		t.Fatalf("flat buckets = %#v %q", buckets, scope)
	}
	// The bucket dir name must be a VALID project id (identifier.ValidateProjectID,
	// enforced by validLocalBucketDir since the identifier refactor): a readable
	// portion plus a 10-character base62 suffix. A bare stand-in like "current"
	// is rejected by design and would degrade the scope probe to nil buckets.
	nested := filepath.Join(root, "home", "serf", "projects", "current-abcdefghij")
	if buckets, scope := findBucketsWithEnumerate(nested, scopeAllProjects, func(string) ([]string, error) { return nil, errors.New("enumerate") }); len(buckets) != 1 || scope != scopeCurrentProject {
		t.Fatalf("failed enumeration = %#v %q", buckets, scope)
	}

	metas := []findCandidate{
		{meta: schema.SessionMeta{ID: "current", UpdatedAt: time.Unix(3, 0)}},
		{meta: schema.SessionMeta{ID: "old", UpdatedAt: time.Unix(1, 0)}},
		{meta: schema.SessionMeta{ID: "new", UpdatedAt: time.Unix(2, 0)}},
		{meta: schema.SessionMeta{ID: "tie", UpdatedAt: time.Unix(2, 0)}},
	}
	sortCandidatesNewestFirst(metas, "current")
	if metas[len(metas)-1].meta.ID != "current" {
		t.Fatalf("current not last: %#v", metas)
	}
	_ = recordsUpTo(metas, func(findCandidate) []snippet { return nil }, "current", 0, nil)
	_ = recordsUpTo([]findCandidate{{meta: schema.SessionMeta{ID: "missing"}, bucketDir: good}}, func(findCandidate) []snippet { return nil }, "", 1, nil)

	m := schema.SessionMeta{ID: "id", Name: "name", OriginalPrompt: "prompt", Model: "model", ProfileID: "profile", ParentSessionID: "parent", EnvInfo: schema.EnvironmentInfo{WorkingDir: "/work/project"}}
	for _, needle := range []string{"id", "name", "prompt", "model", "profile", "parent", "project", "absent"} {
		_ = metaMatches(m, needle)
	}
	_ = sessionKind(schema.SessionMeta{ParentSessionID: "p"})
	_ = projectName(schema.SessionMeta{})
	_ = projectName(schema.SessionMeta{EnvInfo: schema.EnvironmentInfo{WorkingDir: "/a/project"}})
	for _, kind := range []schema.TurnKind{schema.TurnUserInput, schema.TurnAssistant, schema.TurnToolResults, schema.TurnTool, schema.TurnSteering, schema.TurnSummary, schema.TurnCheckpoint, schema.TurnSystem, schema.TurnKind("CUSTOM")} {
		_ = turnRoleLabel(kind)
	}
	turn := schema.Turn{Message: llm.Message{Content: []llm.ContentPart{
		{Kind: llm.ContentText}, {Kind: llm.ContentThinking}, {Kind: llm.ContentToolCall}, {Kind: llm.ContentToolResult},
		{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "think"}},
		{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{Name: "call", Arguments: json.RawMessage(`{}`)}},
		{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{Content: "result"}},
	}}}
	_ = rawEntryText(turn)
	_ = makeSnippet("short text", "absent", 3)
	_ = makeSnippet("0123456789 needle abcdefghij", "needle", 8)
	_ = makeSnippet("needle", "needle", 1)

	path := transcriptPath(good, "session")
	w, err := transcript.NewWriter(path, transcript.Header{SessionID: "session", CreatedAt: time.Unix(0, 0)})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Append(schema.NewTurn(schema.TurnUserInput, llm.User("find needle"))); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := schema.SaveSessionMeta(good, schema.SessionMeta{ID: "session", UpdatedAt: time.Unix(1, 0)}); err != nil {
		t.Fatal(err)
	}
	if err := schema.SaveSessionMeta(good, schema.SessionMeta{ID: "missing", UpdatedAt: time.Unix(2, 0)}); err != nil {
		t.Fatal(err)
	}
	c := findCandidate{meta: schema.SessionMeta{ID: "session"}, bucketDir: good}
	scanned, truncated := maxContentScan, false
	_, _ = matchCandidate(c, "missing", "missing", &scanned, &truncated)
	scanned, truncated = 0, false
	_, _ = matchCandidate(c, "needle", "needle", &scanned, &truncated)
	scanned, truncated = 0, false
	_, _ = matchCandidate(c, "absent", "absent", &scanned, &truncated)
	scanned, truncated = 0, false
	_, _ = matchCandidate(findCandidate{meta: schema.SessionMeta{ID: "needle"}}, "needle", "needle", &scanned, &truncated)
	_, _ = contentSnippets(good, "session", "absent", "absent")
	_, _ = contentSnippets(good, "missing", "x", "x")
	_, _ = execFindAcrossSessions(&toolDeps{stateDir: good}, "query", scopeCurrentProject, 0)
	_, _ = execFindAcrossSessions(&toolDeps{stateDir: good}, "query", scopeCurrentProject, 1)

	deps := &toolDeps{stateDir: good, sessionID: "session"}
	registered := findSessionTranscriptsTool(deps)
	if _, err := registered.Exec(context.Background(), nil, map[string]any{"children_of": "local:bad/path"}); err == nil {
		t.Fatal("bad child ref accepted")
	}
}
