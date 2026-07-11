//go:build serffuzz

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/agent/internal/clock"
	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// FuzzStatefulSessionToolsProgram drives the real root-session registry for
// communicate, ask_user, update_goal, and compact_context. It keeps the LLM
// boundary scripted, uses a fake clock, and supplies a DenyEnv, so handlers run
// through the same Session-owned state as production without filesystem,
// process, provider, or network effects.
func FuzzStatefulSessionToolsProgram(f *testing.F) {
	for _, seed := range [][]byte{
		nil,
		{0},
		{1, 2, 3, 4},
		{5, 8, 13, 21, 34},
		{255, 0, 255, 0, 255, 0},
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, program []byte) {
		first := stpRun(t, program)
		second := stpRun(t, program)
		if first != second {
			t.Fatalf("stateful tool program was not deterministic:\nfirst=%#v\nsecond=%#v", first, second)
		}
	})
}

type stpTrace struct {
	Results      string
	Communicated bool
	Output       string
	AskPending   int
	GoalStatus   string
	GoalIters    int
	PinnedNote   string
	ForcePending bool
	Instructions string
}

type statefulToolProgramReader struct {
	data []byte
	pos  int
}

func (r *statefulToolProgramReader) next() byte {
	if r.pos >= len(r.data) {
		return 0
	}
	b := r.data[r.pos]
	r.pos++
	return b
}

func (r *statefulToolProgramReader) bool() bool { return r.next()&1 != 0 }

func (r *statefulToolProgramReader) word() string {
	words := []string{"alpha", "coverage", "decision", "note", "result"}
	return words[int(r.next())%len(words)]
}

func stpRun(t *testing.T, program []byte) stpTrace {
	t.Helper()
	r := &statefulToolProgramReader{data: program}
	s, env := stpNewSession(t, program)
	for _, name := range []string{"communicate", "ask_user", "update_goal", "compact_context"} {
		if registered := s.reg.Get(name); registered == nil || registered.Exec == nil {
			t.Fatalf("stateful tool %q was not registered", name)
		}
	}

	ctx := context.Background()
	var results []string
	stpExercisePinnedContracts(t, s, env, ctx, &results)

	steps := int(r.next()%10) + 1
	for step := 0; step < steps; step++ {
		switch r.next() % 8 {
		case 0:
			res := stpCall(t, s, env, ctx, fmt.Sprintf("communicate-%d", step), "communicate", stpCommunicateArgs(r.word(), r.bool()))
			stpRecord(&results, res)
		case 1:
			res := stpCall(t, s, env, ctx, fmt.Sprintf("ask-%d", step), "ask_user", stpAskArgs(r.word()))
			stpRecord(&results, res)
		case 2:
			before := s.askPendingCount()
			res := stpCall(t, s, env, ctx, fmt.Sprintf("ask-bad-%d", step), "ask_user", stpDuplicateAskArgs())
			if !res.IsError || s.askPendingCount() != before {
				t.Fatalf("invalid ask changed pending state: before=%d after=%d result=%#v", before, s.askPendingCount(), res)
			}
			stpRecord(&results, res)
		case 3:
			started, err := s.SetGoal(ctx, "goal "+r.word())
			if err != nil || started {
				t.Fatalf("SetGoal returned started=%v err=%v without a kick callback", started, err)
			}
			results = append(results, "set-goal")
		case 4:
			status := []string{"complete", "blocked", "invalid"}[int(r.next())%3]
			res := stpCall(t, s, env, ctx, fmt.Sprintf("goal-%d", step), "update_goal", map[string]any{"status": status})
			stpRecord(&results, res)
		case 5:
			note := r.word()
			if r.bool() {
				note = ""
			}
			res := stpCall(t, s, env, ctx, fmt.Sprintf("compact-%d", step), "compact_context", map[string]any{
				"note_to_self":            note,
				"compaction_instructions": r.word(),
			})
			stpRecord(&results, res)
		case 6:
			if instructions, ok := s.takeForceRequest(); ok {
				results = append(results, "consume-compact:"+instructions)
			} else {
				results = append(results, "consume-compact:none")
			}
		case 7:
			name := []string{"communicate", "ask_user", "update_goal", "compact_context"}[int(r.next())%4]
			res := s.reg.ExecuteCall(ctx, env, llm.ToolCallData{ID: "stp-raw", Name: name, Arguments: []byte{r.next(), r.next()}, Type: "function"})
			stpAssertResult(t, name, res)
			stpRecord(&results, res)
		}
	}

	status, iterations, _ := s.GoalStatus()
	s.mu.Lock()
	forcePending := s.forceRequested
	instructions := s.pendingInstructions
	s.mu.Unlock()
	return stpTrace{
		Results:      strings.Join(results, "|"),
		Communicated: s.Communicated(),
		Output:       s.CommunicateOutput(),
		AskPending:   s.askPendingCount(),
		GoalStatus:   status,
		GoalIters:    iterations,
		PinnedNote:   s.PinnedNote(),
		ForcePending: forcePending,
		Instructions: instructions,
	}
}

func stpNewSession(t *testing.T, program []byte) (*Session, *agenttest.DenyEnv) {
	t.Helper()
	env := &agenttest.DenyEnv{WorkDir: t.TempDir(), Seed: stpSeed(program)}
	client := llm.NewClient()
	client.Register(&agenttest.ScriptedAdapter{
		Provider: "openai",
		Responder: func(llm.Request) llm.Response {
			return agenttest.FinalResponse("unused scripted boundary")
		},
	})
	cfg := SessionConfig{
		MaxSubagentDepth: 1,
		NoProjectPrompts: true,
		clock:            agenttest.NewFakeClock(),
	}
	cfg.testOnly = testConfig{
		skipGitSnapshot:     true,
		environmentInfo:     stpEnvironmentInfo,
		minimalSystemPrompt: true,
		noSyncJobStore:      true,
	}
	s, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), env, cfg)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(s.Close)
	return s, env
}

func stpEnvironmentInfo(env execenv.ExecutionEnvironment, clk clock.Clock) schema.EnvironmentInfo {
	return schema.EnvironmentInfo{
		WorkingDir: env.WorkingDirectory(),
		Platform:   "stateful-fuzz",
		OSVersion:  "stateful-fuzz",
		Today:      clk.Now().UTC().Format("2006-01-02"),
	}
}

func stpSeed(data []byte) uint64 {
	var out uint64
	for i, b := range data {
		if i == 8 {
			break
		}
		out |= uint64(b) << (8 * i)
	}
	return out
}

func stpExercisePinnedContracts(t *testing.T, s *Session, env *agenttest.DenyEnv, ctx context.Context, results *[]string) {
	t.Helper()
	s.Steer("queued before terminal communicate")
	first := stpCall(t, s, env, ctx, "communicate-first", "communicate", stpCommunicateArgs("first", true))
	if first.IsError || !s.Communicated() || s.CommunicateOutput() == "" {
		t.Fatalf("first terminal communicate did not establish result state: %#v", first)
	}
	firstOutput := s.CommunicateOutput()
	stpRecord(results, first)

	second := stpCall(t, s, env, ctx, "communicate-second", "communicate", stpCommunicateArgs("second", true))
	if second.IsError || s.CommunicateOutput() != firstOutput {
		t.Fatalf("later terminal communicate replaced first result: first=%q now=%q result=%#v", firstOutput, s.CommunicateOutput(), second)
	}
	stpRecord(results, second)

	nonTerminal := stpCall(t, s, env, ctx, "communicate-progress", "communicate", stpCommunicateArgs("progress", false))
	if nonTerminal.IsError || s.CommunicateOutput() != firstOutput {
		t.Fatalf("non-terminal communicate changed terminal result: %#v", nonTerminal)
	}
	stpRecord(results, nonTerminal)

	pendingBefore := s.askPendingCount()
	validAsk := stpCall(t, s, env, ctx, "ask-valid", "ask_user", stpAskArgs("choice"))
	if validAsk.IsError || s.askPendingCount() != pendingBefore+1 {
		t.Fatalf("valid ask did not add exactly one pending question: before=%d after=%d result=%#v", pendingBefore, s.askPendingCount(), validAsk)
	}
	stpRecord(results, validAsk)
	invalidAsk := stpCall(t, s, env, ctx, "ask-invalid", "ask_user", stpDuplicateAskArgs())
	if !invalidAsk.IsError || s.askPendingCount() != pendingBefore+1 {
		t.Fatalf("semantic-invalid ask changed pending questions: %#v", invalidAsk)
	}
	stpRecord(results, invalidAsk)

	started, err := s.SetGoal(ctx, "finish deterministic coverage")
	if err != nil || started {
		t.Fatalf("SetGoal returned started=%v err=%v without a kick callback", started, err)
	}
	goal := stpCall(t, s, env, ctx, "goal-complete", "update_goal", map[string]any{"status": "complete"})
	status, _, ok := s.GoalStatus()
	if goal.IsError || !ok || status != "complete" {
		t.Fatalf("update_goal did not terminalize active goal: result=%#v status=%q ok=%v", goal, status, ok)
	}
	stpRecord(results, goal)
	noActive := stpCall(t, s, env, ctx, "goal-repeat", "update_goal", map[string]any{"status": "blocked"})
	if noActive.IsError || !strings.Contains(noActive.Output, "No active goal") {
		t.Fatalf("repeat update_goal did not report no active goal: %#v", noActive)
	}
	stpRecord(results, noActive)

	compact := stpCall(t, s, env, ctx, "compact-first", "compact_context", map[string]any{
		"note_to_self":            "retain this evidence",
		"compaction_instructions": "keep protocol details",
	})
	if compact.IsError || s.PinnedNote() != "retain this evidence" {
		t.Fatalf("first compact did not retain note: %#v note=%q", compact, s.PinnedNote())
	}
	stpRecord(results, compact)
	duplicateCompact := stpCall(t, s, env, ctx, "compact-duplicate", "compact_context", map[string]any{
		"note_to_self":            "must not replace",
		"compaction_instructions": "must not replace",
	})
	if !duplicateCompact.IsError || s.PinnedNote() != "retain this evidence" {
		t.Fatalf("duplicate compact clobbered pending note: %#v note=%q", duplicateCompact, s.PinnedNote())
	}
	stpRecord(results, duplicateCompact)
	if instructions, ok := s.takeForceRequest(); !ok || instructions != "keep protocol details" {
		t.Fatalf("force compact request = %q, %v", instructions, ok)
	}
	clear := stpCall(t, s, env, ctx, "compact-clear", "compact_context", map[string]any{"note_to_self": ""})
	if clear.IsError || s.PinnedNote() != "" {
		t.Fatalf("compact clear did not clear note: %#v note=%q", clear, s.PinnedNote())
	}
	stpRecord(results, clear)
}

func stpCommunicateArgs(message string, endTurn bool) map[string]any {
	return map[string]any{
		"message":  message,
		"end_turn": endTurn,
		"output": map[string]any{
			"message":   "structured " + message,
			"data":      map[string]any{"kind": message},
			"artifacts": []any{"artifact-" + message},
		},
	}
}

func stpAskArgs(word string) map[string]any {
	return map[string]any{
		"questions": []any{map[string]any{
			"header":   "Choice",
			"question": "Choose " + word,
			"options": []any{
				map[string]any{"label": "first", "detail": "first option", "recommended": true},
				map[string]any{"label": "second", "detail": "second option"},
			},
		}},
	}
}

func stpDuplicateAskArgs() map[string]any {
	return map[string]any{
		"questions": []any{map[string]any{
			"header":   "Choice",
			"question": "duplicate labels",
			"options": []any{
				map[string]any{"label": "same", "detail": "one"},
				map[string]any{"label": "same", "detail": "two"},
			},
		}},
	}
}

func stpCall(t *testing.T, s *Session, env *agenttest.DenyEnv, ctx context.Context, id, name string, args map[string]any) tool.ExecResult {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal %s args: %v", name, err)
	}
	res := s.reg.ExecuteCall(ctx, env, llm.ToolCallData{ID: id, Name: name, Arguments: raw, Type: "function"})
	stpAssertResult(t, name, res)
	return res
}

func stpAssertResult(t *testing.T, name string, res tool.ExecResult) {
	t.Helper()
	if res.ToolName != name || res.CallID == "" {
		t.Fatalf("malformed %s result: %#v", name, res)
	}
	if !utf8.ValidString(res.Output) || !utf8.ValidString(res.FullOutput) {
		t.Fatalf("%s returned invalid UTF-8", name)
	}
	if len(res.ToolState) > 0 && !json.Valid(res.ToolState) {
		t.Fatalf("%s returned invalid tool state: %q", name, res.ToolState)
	}
}

func stpRecord(results *[]string, res tool.ExecResult) {
	*results = append(*results, fmt.Sprintf("%s:%t:%s", res.ToolName, res.IsError, res.Output))
}
