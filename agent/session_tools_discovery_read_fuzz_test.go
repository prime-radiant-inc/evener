//go:build serffuzz

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/llm"
)

// FuzzTranscriptDiscoveryReadProgram drives the real archived-transcript tool
// surface over a bounded state-home fixture. The fixture contains current and
// sibling project buckets, a parent/child relationship, rich transcript turns,
// and one recoverable corrupt line. It exercises discovery plus markdown,
// outline, raw, and job-transcript reads without consulting a user state
// directory, starting a provider request, or running a process.
func FuzzTranscriptDiscoveryReadProgram(f *testing.F) {
	for _, seed := range [][]byte{
		nil,
		{0},
		{1, 2, 3},
		{1, 2, 3, 4},
		{5, 8, 13, 21},
		{5, 8, 13, 21, 34},
		{255, 0, 255, 0},
		{255, 0, 255, 0, 255, 0},
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, program []byte) {
		stmRunJobTranscriptContracts(t, program)
		first := tdrpRun(t, program)
		second := tdrpRun(t, program)
		if first != second {
			t.Fatalf("transcript tool program was not deterministic:\nfirst=%#v\nsecond=%#v", first, second)
		}
	})
}

type tdrpTrace struct {
	CatalogRefs  string
	QueryRefs    string
	ChildrenRefs string
	Markdown     string
	Outline      string
	Raw          string
	BadReadError bool
}

type tdrpReader struct {
	data []byte
	pos  int
}

func (r *tdrpReader) next() byte {
	if r.pos >= len(r.data) {
		return 0
	}
	b := r.data[r.pos]
	r.pos++
	return b
}

func (r *tdrpReader) token() string {
	parts := []string{"parser", "coverage", "needle", "unicode", "history"}
	return parts[int(r.next())%len(parts)]
}

func (r *tdrpReader) readRange() string {
	ranges := []string{"", "1-3", "last:2", "start:2", "bad-range"}
	return ranges[int(r.next())%len(ranges)]
}

func tdrpRun(t *testing.T, program []byte) tdrpTrace {
	t.Helper()
	r := &tdrpReader{data: program}
	deps, reg, env, currentRef, siblingRef, parentRef, contentNeedle := tdrpFixture(t, r)

	for _, name := range []string{"read_transcript", "read_session_transcript", "find_session_transcripts"} {
		if registered := reg.Get(name); registered == nil || registered.Exec == nil {
			t.Fatalf("transcript tool %q was not registered", name)
		}
	}

	catalogArgs := map[string]any{"limit": float64(3)}
	catalog := tdrpFind(t, deps, catalogArgs)
	tdrpAssertFindEnvelope(t, catalog, 3)
	catalogText := tdrpExecute(t, reg, env, "find_session_transcripts", catalogArgs)
	tdrpAssertTextResult(t, catalogText)

	query := contentNeedle
	queryArgs := map[string]any{
		"query": query,
		"scope": []string{scopeCurrentProject, scopeAllProjects}[int(r.next())%2],
		"limit": float64(4),
	}
	queryResult := tdrpFind(t, deps, queryArgs)
	tdrpAssertFindEnvelope(t, queryResult, 4)
	queryText := tdrpExecute(t, reg, env, "find_session_transcripts", queryArgs)
	tdrpAssertTextResult(t, queryText)

	childrenArgs := map[string]any{"children_of": parentRef, "query": query, "limit": float64(4)}
	children := tdrpFind(t, deps, childrenArgs)
	tdrpAssertFindEnvelope(t, children, 4)
	childrenText := tdrpExecute(t, reg, env, "find_session_transcripts", childrenArgs)
	tdrpAssertTextResult(t, childrenText)

	markdown := tdrpExecute(t, reg, env, "read_session_transcript", map[string]any{
		"transcript_ref": currentRef,
		"format":         formatMarkdown,
		"range":          r.readRange(),
		"expand_turn":    float64(3),
	})
	tdrpAssertReadJSON(t, markdown, currentRef, formatMarkdown)

	outline := tdrpExecute(t, reg, env, "read_session_transcript", map[string]any{
		"transcript_ref": siblingRef,
		"format":         formatOutline,
		"range":          r.readRange(),
	})
	tdrpAssertReadJSON(t, outline, siblingRef, formatOutline)

	raw := tdrpExecute(t, reg, env, "read_transcript", map[string]any{
		"transcript_ref": siblingRef,
		"format":         formatJSONL,
		"range":          r.readRange(),
	})
	tdrpAssertReadJSON(t, raw, siblingRef, formatJSONL)

	bad := tdrpExecute(t, reg, env, "read_session_transcript", map[string]any{
		"transcript_ref": "../outside",
		"format":         formatMarkdown,
	})
	if !bad.IsError {
		t.Fatalf("traversal-shaped transcript selector unexpectedly succeeded: %#v", bad)
	}

	return tdrpTrace{
		CatalogRefs:  tdrpRefs(catalog),
		QueryRefs:    tdrpRefs(queryResult),
		ChildrenRefs: tdrpRefs(children),
		Markdown:     markdown.Output,
		Outline:      outline.Output,
		Raw:          raw.Output,
		BadReadError: bad.IsError,
	}
}

func tdrpFixture(t *testing.T, r *tdrpReader) (*toolDeps, *tool.Registry, *agenttest.DenyEnv, string, string, string, string) {
	t.Helper()
	root := t.TempDir()
	currentDir := filepath.Join(root, "serf", "projects", trenderCurrentProject)
	siblingDir := filepath.Join(root, "serf", "projects", trenderOtherProject)
	for _, dir := range []string{currentDir, siblingDir} {
		if err := os.MkdirAll(filepath.Join(dir, sessionsSubdir), 0o755); err != nil {
			t.Fatalf("make transcript bucket %q: %v", dir, err)
		}
	}

	const (
		currentID = trenderCurrentSession
		parentID  = trenderParentSession
		childID   = trenderLocalSession
		otherID   = trenderRemoteSession
	)
	token := r.token()
	contentNeedle := "content-" + r.token()
	tdrpWriteSession(t, currentDir, tdrpSessionSpec{id: currentID, name: "current " + token, prompt: token, content: contentNeedle, turns: 7})
	tdrpWriteSession(t, currentDir, tdrpSessionSpec{id: parentID, name: "parent " + token, prompt: "parent", content: contentNeedle, turns: 5})
	tdrpWriteSession(t, currentDir, tdrpSessionSpec{id: childID, name: "child " + token, prompt: "child", content: contentNeedle, parentID: parentID, subagent: true, turns: 4})
	tdrpWriteSession(t, siblingDir, tdrpSessionSpec{id: otherID, name: "other " + token, prompt: token, content: contentNeedle, turns: 6})

	deps := &toolDeps{
		stateDir:  currentDir,
		sessionID: currentID,
		currentMeta: func() schema.SessionMeta {
			return schema.SessionMeta{ID: currentID, Name: "current " + token, TurnCount: 99, UpdatedAt: tdrpTime}
		},
	}
	reg := tool.NewRegistry()
	for _, registered := range transcriptTools(deps) {
		if err := reg.Register(registered); err != nil {
			t.Fatalf("register transcript tool %q: %v", registered.Definition.Name, err)
		}
	}
	return deps, reg, &agenttest.DenyEnv{WorkDir: root}, encodeRef("", currentID), encodeRef(trenderOtherProject, otherID), encodeRef("", parentID), contentNeedle
}

var tdrpTime = time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)

type tdrpSessionSpec struct {
	id       string
	name     string
	prompt   string
	content  string
	parentID string
	subagent bool
	turns    int
}

func tdrpWriteSession(t *testing.T, bucket string, spec tdrpSessionSpec) {
	t.Helper()
	path := transcriptPath(bucket, spec.id)
	w, err := transcript.NewWriter(path, transcript.Header{SessionID: spec.id, CreatedAt: tdrpTime, Model: "openai/fuzz", ProfileID: "openai"})
	if err != nil {
		t.Fatalf("open transcript %q: %v", spec.id, err)
	}
	for i, turn := range tdrpTurns(spec.content, spec.turns) {
		turn.Timestamp = tdrpTime.Add(time.Duration(i) * time.Minute)
		if err := w.Append(turn); err != nil {
			t.Fatalf("append transcript %q turn %d: %v", spec.id, i, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close transcript %q: %v", spec.id, err)
	}
	// A corrupt tail is recoverable by the production readers and exercises the
	// skipped-line accounting without exposing the harness to arbitrary host files.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open transcript tail %q: %v", spec.id, err)
	}
	if _, err := f.WriteString("{not-json"); err != nil {
		_ = f.Close()
		t.Fatalf("write transcript tail %q: %v", spec.id, err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close transcript tail %q: %v", spec.id, err)
	}
	meta := schema.SessionMeta{
		ID:              spec.id,
		Name:            spec.name,
		OriginalPrompt:  spec.prompt,
		Model:           "openai/fuzz",
		ProfileID:       "openai",
		ParentSessionID: spec.parentID,
		IsSubagent:      spec.subagent,
		TurnCount:       spec.turns,
		CreatedAt:       tdrpTime,
		UpdatedAt:       tdrpTime,
		EnvInfo: schema.EnvironmentInfo{
			WorkingDir:   "/fixture/project",
			GitOriginURL: "git@example.test:fixture/serf.git",
		},
	}
	if err := schema.SaveSessionMeta(bucket, meta); err != nil {
		t.Fatalf("save transcript meta %q: %v", spec.id, err)
	}
}

func tdrpTurns(token string, n int) []schema.Turn {
	base := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("user " + token)},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("assistant " + token)},
		{Kind: schema.TurnAssistant, Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{
			Kind:     llm.ContentToolCall,
			ToolCall: &llm.ToolCallData{ID: "call1", Name: "shell", Arguments: json.RawMessage(`{"command":"echo ` + token + `"}`)},
		}}}},
		{Kind: schema.TurnToolResults, Message: llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{{
			Kind:       llm.ContentToolResult,
			ToolResult: &llm.ToolResultData{ToolCallID: "call1", Name: "shell", Content: "result " + token},
		}}}},
		{Kind: schema.TurnSteering, Message: llm.User("steer " + token)},
		{Kind: schema.TurnSummary, Message: llm.Assistant("summary " + token)},
		{Kind: schema.TurnSystem, Message: llm.System("system " + token)},
	}
	if n < 1 {
		n = 1
	}
	out := make([]schema.Turn, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, base[i%len(base)])
	}
	return out
}

func tdrpFind(t *testing.T, deps *toolDeps, args map[string]any) findSessionsEnvelope {
	t.Helper()
	v, err := execFindSessionTranscripts(deps, args)
	if err != nil {
		t.Fatalf("find transcript sessions %v: %v", args, err)
	}
	env, ok := v.(findSessionsEnvelope)
	if !ok {
		t.Fatalf("find transcript sessions returned %T", v)
	}
	return env
}

func tdrpExecute(t *testing.T, reg *tool.Registry, env *agenttest.DenyEnv, name string, args map[string]any) tool.ExecResult {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal %s args: %v", name, err)
	}
	res := reg.ExecuteCall(context.Background(), env, llm.ToolCallData{ID: "tdrp-" + name, Name: name, Arguments: raw, Type: "function"})
	if res.ToolName != name || res.CallID == "" {
		t.Fatalf("malformed %s result: %#v", name, res)
	}
	if !utf8.ValidString(res.Output) || !utf8.ValidString(res.FullOutput) {
		t.Fatalf("%s returned invalid UTF-8", name)
	}
	return res
}

func tdrpAssertFindEnvelope(t *testing.T, env findSessionsEnvelope, limit int) {
	t.Helper()
	if len(env.Matches) > limit {
		t.Fatalf("find returned %d matches above limit %d", len(env.Matches), limit)
	}
	seen := map[string]bool{}
	for _, match := range env.Matches {
		if match.TranscriptRef == "" || seen[match.TranscriptRef] {
			t.Fatalf("find returned invalid or duplicate ref: %#v", env.Matches)
		}
		seen[match.TranscriptRef] = true
	}
	if env.Scanned != nil && *env.Scanned < 0 {
		t.Fatalf("find reported a negative scan count: %#v", env)
	}
}

func tdrpAssertTextResult(t *testing.T, res tool.ExecResult) {
	t.Helper()
	if res.IsError || strings.TrimSpace(res.Output) == "" {
		t.Fatalf("find registry wrapper failed: %#v", res)
	}
}

func tdrpAssertReadJSON(t *testing.T, res tool.ExecResult, wantRef, wantFormat string) {
	t.Helper()
	if res.IsError {
		t.Fatalf("read %s failed: %s", wantFormat, res.Output)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(res.Output), &out); err != nil {
		t.Fatalf("read %s returned invalid JSON: %v\n%s", wantFormat, err, res.Output)
	}
	if got, _ := out["transcript_ref"].(string); got != wantRef {
		t.Fatalf("read ref = %q, want %q", got, wantRef)
	}
	if got, _ := out["format"].(string); got != wantFormat {
		t.Fatalf("read format = %q, want %q", got, wantFormat)
	}
}

func tdrpRefs(env findSessionsEnvelope) string {
	refs := make([]string, 0, len(env.Matches))
	for _, match := range env.Matches {
		refs = append(refs, match.TranscriptRef)
	}
	return fmt.Sprint(refs)
}
