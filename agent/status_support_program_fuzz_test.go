//go:build serffuzz

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/agent/internal/clock"
	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/provider"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// FuzzStatusSupportProgram drives small Session-facing observability and
// support APIs through real deterministic fixtures. It uses ScriptedAdapter,
// FakeClock, DenyEnv, and temporary transcript files only; no provider,
// process, network, or real workspace is reached.
//
// Oracles:
//   - status snapshots retain tool provenance, bound terminal jobs, and keep a
//     deterministic sorted presentation;
//   - pull metrics agree with the directly owned Session/context-manager state;
//   - exported ATIF remains valid JSON for valid transcripts and failures stay
//     confined to their supplied paths; and
//   - the Gemini-only web-search request contains the input query, no tools,
//     and a scripted response.
func FuzzStatusSupportProgram(f *testing.F) {
	for _, seed := range [][]byte{
		{},
		{0},
		{1, 2, 3},
		{255, 128, 7, 42},
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		token := statusSupportToken(data)
		statusSupportStatusAndMetrics(t, token)
		statusSupportATIF(t, token)
		statusSupportWebSearch(t, token)
	})
}

func statusSupportStatusAndMetrics(t *testing.T, token string) {
	t.Helper()
	s, clock := statusSupportSession(t, NewOpenAIProfile("gpt-status"), "openai", nil)
	s.RegisterTool("status_custom_"+token, "status fixture", map[string]any{"type": "object"}, func(context.Context, any) (any, error) {
		return "unused", nil
	})

	first := s.DetailedStatus()
	second := s.DetailedStatus()
	if !statusSupportEqualStatus(first, second) {
		t.Fatalf("DetailedStatus was nondeterministic:\nfirst=%#v\nsecond=%#v", first, second)
	}
	if !sort.SliceIsSorted(first.Tools, func(i, j int) bool { return first.Tools[i].Name < first.Tools[j].Name }) {
		t.Fatalf("status tools are not sorted: %#v", first.Tools)
	}
	if !statusSupportHasTool(first.Tools, "status_custom_"+token, "custom") {
		t.Fatalf("custom tool missing from status: %#v", first.Tools)
	}
	if !statusSupportHasTool(first.Tools, "read_file", "core") {
		t.Fatalf("core tool missing from status: %#v", first.Tools)
	}

	records := make([]*jobstore.JobRecord, 0, detailedStatusTerminalJobsLimit+3)
	records = append(records, &jobstore.JobRecord{JobID: "job_running_" + token, Type: jobstore.JobShell, Status: jobstore.StatusRunning})
	for i := 0; i < detailedStatusTerminalJobsLimit+2; i++ {
		records = append(records, &jobstore.JobRecord{
			JobID:         "job_terminal_" + token + "_" + string(rune('a'+i%26)),
			Type:          jobstore.JobDelegate,
			Status:        jobstore.StatusCompleted,
			Reason:        "done",
			TranscriptRef: "local:" + token,
			OutputBytes:   int64(i),
		})
	}
	bounded := detailedStatusJobRecords(records)
	if len(bounded) != detailedStatusTerminalJobsLimit+1 || bounded[0].JobID != "job_running_"+token {
		t.Fatalf("bounded status records = %d first=%#v", len(bounded), bounded)
	}
	info := projectJobStatusInfos(bounded[:2])
	if len(info) != 2 || info[1].JobType != string(jobstore.JobDelegate) || info[1].TranscriptRef != "local:"+token {
		t.Fatalf("projected status = %#v", info)
	}

	s.mu.Lock()
	s.workMillis = int64(len(token) * 17)
	s.state = SessionProcessing
	s.turnStartedAt = clock.Now()
	s.history = append(s.history, schema.NewTurn(schema.TurnUserInput, llm.User("status "+token)))
	s.mu.Unlock()
	if got, want := s.WorkMillisSnapshot(), int64(len(token)*17); got != want {
		t.Fatalf("WorkMillisSnapshot = %d, want %d", got, want)
	}
	if got, want := s.ActiveTurnStartedAtMillis(), clock.Now().UnixMilli(); got != want {
		t.Fatalf("ActiveTurnStartedAtMillis = %d, want %d", got, want)
	}
	s.mu.Lock()
	s.state = SessionIdle
	s.mu.Unlock()
	if got := s.ActiveTurnStartedAtMillis(); got != 0 {
		t.Fatalf("idle ActiveTurnStartedAtMillis = %d, want 0", got)
	}

	usage := llm.Usage{InputTokens: len(token), OutputTokens: len(token) + 1}
	s.contextMgr.SetCumulativeUsage(usage)
	if got := s.CumulativeUsageSnapshot(); !reflect.DeepEqual(got, usage) {
		t.Fatalf("CumulativeUsageSnapshot = %#v, want %#v", got, usage)
	}
	if got := (&Session{}).CumulativeUsageSnapshot(); !reflect.DeepEqual(got, llm.Usage{}) {
		t.Fatalf("nil context manager usage = %#v", got)
	}
	metrics := s.ContextMetrics()
	if metrics.Window <= 0 || metrics.Used < 0 || metrics.Remaining < 0 || metrics.Remaining > metrics.Window {
		t.Fatalf("ContextMetrics = %#v", metrics)
	}
	if got := (&Session{}).ContextMetrics(); got != (ContextMetrics{}) {
		t.Fatalf("nil context manager metrics = %#v", got)
	}

	host := &ctxHost{s: s}
	if host.StateDir() != s.StateDir() || host.ID() != s.ID() || host.Profile() != s.Profile() {
		t.Fatal("ctxHost did not preserve session identity")
	}
	called := false
	if err := host.WithResponseSideEffects(context.Background(), func() { called = true }); err != nil || !called {
		t.Fatalf("ctxHost response side effects = called:%v err:%v", called, err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := host.WithResponseSideEffects(canceled, func() { t.Fatal("canceled response callback ran") }); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled response side effects = %v", err)
	}
	host.Emit(events.EventWarning, events.WarningData{Message: "status fixture " + token})

	permission := &PermissionDeniedError{Tool: "status_" + token, Message: "denied"}
	if !errors.Is(permission, ErrPermissionDenied) || !strings.Contains(permission.Error(), permission.Tool) {
		t.Fatalf("permission error contract = %q", permission)
	}
}

func statusSupportATIF(t *testing.T, token string) {
	t.Helper()
	dir := t.TempDir()
	src := w3sub_writeATIFTranscript(t, dir)
	out := filepath.Join(dir, "atif", token+".json")
	for _, mode := range []string{"", "redacted", "raw-local"} {
		if err := exportATIF(src, out, mode); err != nil {
			t.Fatalf("exportATIF(%q): %v", mode, err)
		}
		data, err := os.ReadFile(out)
		if err != nil || len(data) == 0 || !json.Valid(data) {
			t.Fatalf("ATIF output = %q err=%v", data, err)
		}
	}
	if err := exportATIF(src, out, "invalid-"+token); err == nil {
		t.Fatal("invalid ATIF provider-handle mode succeeded")
	}
	if err := exportATIF(filepath.Join(dir, "missing.jsonl"), out, ""); err == nil {
		t.Fatal("missing transcript export succeeded")
	}
}

func statusSupportWebSearch(t *testing.T, token string) {
	t.Helper()
	query := "status search " + token
	s, _ := statusSupportSession(t, newGeminiProfile("gemini-status"), "google", func(req llm.Request) llm.Response {
		if !req.WebSearch || len(req.Tools) != 0 || req.Provider != "google" || req.Model != "gemini-status" || len(req.Messages) != 1 || !strings.Contains(req.Messages[0].Text(), query) {
			t.Fatalf("web search request = %#v", req)
		}
		return llm.Response{Message: llm.Assistant("result " + token)}
	})
	result, err := s.webSearch(context.Background(), query)
	if err != nil || result != "result "+token {
		t.Fatalf("webSearch = (%#v, %v)", result, err)
	}
}

func statusSupportSession(t *testing.T, profile *provider.Profile, providerName string, responder func(llm.Request) llm.Response) (*Session, *agenttest.FakeClock) {
	t.Helper()
	if responder == nil {
		responder = func(llm.Request) llm.Response { return agenttest.FinalResponse("unused") }
	}
	adapter := &agenttest.ScriptedAdapter{Provider: providerName, Responder: responder}
	client := llm.NewClient()
	client.Register(adapter)
	clock := agenttest.NewFakeClock()
	cfg := SessionConfig{NoProjectPrompts: true, StateDir: t.TempDir(), clock: clock}
	cfg.testOnly = testConfig{
		skipGitSnapshot:     true,
		minimalSystemPrompt: true,
		noSyncJobStore:      true,
		environmentInfo:     statusSupportEnvironmentInfo,
	}
	s, err := NewSession(client, profile, &agenttest.DenyEnv{WorkDir: t.TempDir(), Seed: 17}, cfg)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(s.Close)
	return s, clock
}

func statusSupportEnvironmentInfo(env execenv.ExecutionEnvironment, clk clock.Clock) schema.EnvironmentInfo {
	return schema.EnvironmentInfo{WorkingDir: env.WorkingDirectory(), Platform: "status-fuzz", OSVersion: "status-fuzz", Today: clk.Now().UTC().Format("2006-01-02")}
}

func statusSupportEqualStatus(a, b DetailedStatus) bool {
	return len(a.Tools) == len(b.Tools) && len(a.Jobs) == len(b.Jobs) && len(a.Agents) == len(b.Agents) && strings.Join(a.Agents, "\x00") == strings.Join(b.Agents, "\x00")
}

func statusSupportHasTool(tools []ToolInfo, name, source string) bool {
	for _, tool := range tools {
		if tool.Name == name && tool.Source == source {
			return true
		}
	}
	return false
}

func statusSupportToken(data []byte) string {
	if len(data) == 0 {
		return "seed"
	}
	var b strings.Builder
	for _, value := range data {
		b.WriteByte('a' + value%26)
		if b.Len() == 24 {
			break
		}
	}
	return b.String()
}
