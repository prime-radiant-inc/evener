//go:build serffuzz

package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/internal/goal"
	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/schema"
	taskpkg "primeradiant.com/serf/agent/task"
	"primeradiant.com/serf/llm"
)

func FuzzResponsesContinuationEligibility(f *testing.F) {
	f.Add([]byte{0})
	f.Add([]byte{1, 1, 0, 0})
	f.Add([]byte{1, 1, 2, 1, 1})
	f.Add([]byte{1, 0, 4, 2, 3, 5})

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 64 {
			data = data[:64]
		}
		next := func(i int) byte {
			if len(data) == 0 {
				return 0
			}
			return data[i%len(data)]
		}

		history := make([]schema.Turn, 0, 10)
		if next(0)&1 != 0 {
			history = append(history, schema.Turn{Kind: schema.TurnCheckpoint})
		}
		anchor := schema.Turn{
			Kind:                            schema.TurnAssistant,
			Message:                         llm.Assistant("anchor"),
			Timestamp:                       time.Unix(int64(next(1)), 0).UTC(),
			ResponseID:                      "resp",
			ResponseIDHash:                  "hash",
			ResponseEndpoint:                "endpoint",
			ResponseStorageScopeFingerprint: "scope",
			ResponseRequestFingerprint:      "request",
			ResponseContextMarker:           responseContextMarkerV1,
		}
		callID := strings.TrimSpace(string(data))
		if callID == "" {
			callID = "call"
		}
		anchor.Message.Content = append(anchor.Message.Content, llm.ContentPart{
			Kind:     llm.ContentToolCall,
			ToolCall: &llm.ToolCallData{ID: callID, Name: "tool"},
		})
		if next(2)&1 != 0 {
			anchor.ResponseIDHash = ""
		}
		history = append(history, anchor)

		for i := 3; i < len(data) && len(history) < 10; i++ {
			switch next(i) % 6 {
			case 0:
				history = append(history, schema.Turn{Kind: schema.TurnUserInput, Message: llm.User(string(data[:i]))})
			case 1:
				history = append(history, schema.Turn{Kind: schema.TurnToolResults, Message: llm.ToolResultNamed(callID, "tool", "ok", false)})
			case 2:
				history = append(history, schema.Turn{Kind: schema.TurnToolResults, Message: llm.ToolResultNamed("orphan", "tool", "bad", true)})
			case 3:
				history = append(history, schema.Turn{Kind: schema.TurnSteering, Message: llm.User("steer")})
			case 4:
				history = append(history, schema.Turn{Kind: schema.TurnUserInput, Message: llm.Message{Content: []llm.ContentPart{{Kind: llm.ContentImage}}}})
			case 5:
				history = append(history, schema.Turn{Kind: schema.TurnSummary})
			}
		}

		cfg := SessionConfig{SystemPromptAsUser: next(3)&4 != 0}
		candidate, decision := selectResponsesContinuationAnchorCandidate(cfg, history)
		candidate2, decision2 := selectResponsesContinuationAnchorCandidate(cfg, append([]schema.Turn(nil), history...))
		if decision != decision2 || candidate.TurnIndex != candidate2.TurnIndex || len(candidate.Delta) != len(candidate2.Delta) {
			t.Fatalf("continuation selection is not deterministic: (%+v,%+v) vs (%+v,%+v)", candidate, decision, candidate2, decision2)
		}
		if decision.HistoryMode == llm.HistoryModeResponsesDelta {
			if decision.Reason != "continuation_anchor_candidate" || candidate.TurnIndex < 0 || candidate.TurnIndex >= len(history) || len(candidate.Delta) == 0 {
				t.Fatalf("invalid delta decision: candidate=%+v decision=%+v history=%d", candidate, decision, len(history))
			}
		}

		reservation := reserveResponsesContinuationHistoryBase(history)
		if !responsesContinuationHistoryBaseStillCurrent(reservation, history) {
			t.Fatal("fresh history reservation must still be current")
		}
		changed := append([]schema.Turn(nil), history...)
		changed = append(changed, schema.Turn{Kind: schema.TurnUserInput})
		if responsesContinuationHistoryBaseStillCurrent(reservation, changed) {
			t.Fatal("reservation must reject a changed history length")
		}
	})
}

func FuzzSessionMetadataHelpers(f *testing.F) {
	emptyStateDir := f.TempDir()
	f.Add("  Fix flaky tests!!!  ", "gpt-5", "GPT-5", byte(0))
	f.Add("\"quoted title\"", " claude ", "CLAUDE", byte(1))
	f.Add(strings.Repeat("界", 100), "", "other", byte(2))

	f.Fuzz(func(t *testing.T, text, model, alternate string, mode byte) {
		if len(text) > 16<<10 {
			text = text[:16<<10]
		}
		if len(model) > 256 {
			model = model[:256]
		}
		if len(alternate) > 256 {
			alternate = alternate[:256]
		}
		gotName := sanitizeSessionName(text)
		if gotName != sanitizeSessionName(gotName) {
			t.Fatalf("session-name sanitizer is not idempotent: %q", gotName)
		}
		if utf8.RuneCountInString(gotName) > sessionNameMaxRunes || gotName != strings.TrimSpace(gotName) {
			t.Fatalf("invalid sanitized session name %q", gotName)
		}
		trimmed := trimForSessionNamer(text)
		if !utf8.ValidString(text) && utf8.ValidString(trimmed) {
			// Invalid input may become valid when truncation drops the bad suffix; no
			// stronger UTF-8 relation is valid here.
		} else if utf8.ValidString(text) && !utf8.ValidString(trimmed) {
			t.Fatal("session-namer trimming corrupted valid UTF-8")
		}
		if !strings.Contains(sessionNamerUserPrompt(string(mode), text), trimForSessionNamer(text)) {
			t.Fatal("namer prompt omitted the bounded source text")
		}
		if schema := sessionNameSchema(); schema["type"] != "object" {
			t.Fatalf("session-name schema type=%v", schema["type"])
		}
		if *ptrString(text) != text || sessionNameSourceLabel(string(mode)) != normalizeSessionNameSource(string(mode)) {
			t.Fatal("session-name source helpers disagree")
		}
		if _, err := nameSession(context.Background(), nil, nil, string(mode), text, nil); err == nil {
			t.Fatal("session namer accepted a nil client")
		}

		models := []llm.ModelInfo{{ID: alternate, DisplayName: "alternate"}, {ID: model, DisplayName: "exact"}}
		info, ok := liveModelInfoFor(models, model)
		if strings.TrimSpace(model) == "" {
			if ok {
				t.Fatalf("empty model unexpectedly matched %+v", info)
			}
		} else if !ok || info.ID != model {
			t.Fatalf("exact model match lost: got=(%+v,%v), model=%q", info, ok, model)
		}
		if info, ok := liveModelInfoFor([]llm.ModelInfo{{ID: " normalized "}}, "normalized"); !ok || info.ID != " normalized " {
			t.Fatalf("trimmed exact model match lost: (%+v,%v)", info, ok)
		}
		if resolveLiveModelProfile(context.Background(), nil, nil) != nil {
			t.Fatal("nil live-model profile must remain nil")
		}

		if got, err := LoadSessionHistoricalJobRecords(emptyStateDir, "absent"); err != nil || len(got) != 0 {
			t.Fatalf("absent historical jobs=(%v,%v), want empty", got, err)
		}

		recs := make([]*jobstore.JobRecord, 0, detailedStatusTerminalJobsLimit+4)
		recs = append(recs, &jobstore.JobRecord{JobID: "running", Type: jobstore.JobShell, Status: jobstore.StatusRunning})
		for i := 0; i < detailedStatusTerminalJobsLimit+3; i++ {
			recs = append(recs, &jobstore.JobRecord{JobID: string(rune(i + 1)), Type: jobstore.JobDelegate, Status: jobstore.StatusCompleted, Reason: text})
		}
		kept := detailedStatusJobRecords(recs)
		if len(kept) != detailedStatusTerminalJobsLimit+1 || kept[0].JobID != "running" {
			t.Fatalf("terminal retention mismatch: kept=%d first=%q", len(kept), kept[0].JobID)
		}
		projected := projectJobStatusInfos(kept)
		if len(projected) != len(kept) || projected[0].Status != string(jobstore.StatusRunning) {
			t.Fatalf("job projection mismatch: records=%d projected=%+v", len(kept), projected)
		}

		warning := warningHookMessage(&events.WarningData{Message: text})
		if warning != text || warningHookMessage((*events.WarningData)(nil)) != "<nil>" {
			t.Fatalf("warning rendering mismatch: %q", warning)
		}
		reminder := formatCurrentTaskSteering(taskpkg.Task{ID: int(mode), Description: text, Prompt: alternate})
		if !strings.HasPrefix(reminder, "<SYSTEM-REMINDER>") || !strings.HasSuffix(reminder, "</SYSTEM-REMINDER>") {
			t.Fatalf("task reminder envelope missing: %q", reminder)
		}
		if taskReminderAllDone() == "" || taskReminderNudge() == "" {
			t.Fatal("static task reminders must be non-empty")
		}
		eventCh := make(chan events.SessionEvent)
		if got := (&Session{events: eventCh}).Events(); got != eventCh {
			t.Fatal("Events returned a different channel")
		}
	})
}

func FuzzSessionGoalCompactState(f *testing.F) {
	f.Add(-1, uint8(0), "preserve this")
	f.Add(0, uint8(1), "")
	f.Add(1000, uint8(2), "new instructions")

	f.Fuzz(func(t *testing.T, configured int, kindByte uint8, instructions string) {
		if len(instructions) > 4096 {
			instructions = instructions[:4096]
		}
		kind := EntryKind(kindByte % 4)
		cap := goalRoundCap(configured, kind)
		if kind == EntryContinuation && (configured < 0 || configured > goal.GoalTurnMaxRounds) && cap != goal.GoalTurnMaxRounds {
			t.Fatalf("continuation round cap=%d, want %d for configured=%d", cap, goal.GoalTurnMaxRounds, configured)
		}
		if kind != EntryContinuation && cap != configured {
			t.Fatalf("non-continuation round cap=%d, want configured=%d", cap, configured)
		}

		s := &Session{}
		s.setPinnedNote(instructions)
		if got := s.PinnedNote(); got != instructions {
			t.Fatalf("pinned note=%q, want %q", got, instructions)
		}
		s.clearPinnedNote()
		if got := s.PinnedNote(); got != "" {
			t.Fatalf("clearPinnedNote left %q", got)
		}

		if err := s.requestForceCompact(instructions); err != nil {
			t.Fatalf("first force request failed: %v", err)
		}
		if err := s.requestForceCompact("second"); err == nil {
			t.Fatal("second force request must be rejected until the first is consumed")
		}
		got, ok := s.takeForceRequest()
		if !ok || got != instructions {
			t.Fatalf("force request consume=(%q,%v), want (%q,true)", got, ok, instructions)
		}
		if got, ok := s.takeForceRequest(); ok || got != "" {
			t.Fatalf("consumed force request repeated as (%q,%v)", got, ok)
		}

		if goalRootShutdown(context.Background(), errors.New("other")) {
			t.Fatal("non-cancellation error classified as root shutdown")
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if goalRootShutdown(ctx, context.Canceled) {
			t.Fatal("ordinary canceled context classified as queued root shutdown")
		}

		goalSession := &Session{}
		kicks := 0
		goalSession.SetKickFunc(func(prompt string) {
			if strings.TrimSpace(prompt) == "" {
				t.Fatal("goal kick prompt is empty")
			}
			kicks++
		})
		started, err := goalSession.SetGoal(context.Background(), instructions)
		if strings.TrimSpace(instructions) == "" {
			if err == nil || started || kicks != 0 {
				t.Fatalf("empty goal result=(%v,%v,kicks=%d)", started, err, kicks)
			}
		} else {
			if err != nil || !started || kicks != 1 {
				t.Fatalf("non-empty goal result=(%v,%v,kicks=%d)", started, err, kicks)
			}
			status, iterations, ok := goalSession.GoalStatus()
			if !ok || status != "active" || iterations != 0 || len(goalSession.goalCompactionSteering()) != 1 {
				t.Fatalf("active goal projection=(%q,%d,%v)", status, iterations, ok)
			}
			if prompt, ok := goalSession.currentGoalContinuation(); !ok || strings.TrimSpace(prompt) == "" {
				t.Fatalf("active continuation=(%q,%v)", prompt, ok)
			}
			goalSession.ClearGoal()
			if _, _, ok := goalSession.GoalStatus(); ok {
				t.Fatal("cleared goal remained visible")
			}
		}
	})
}
