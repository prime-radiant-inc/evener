//go:build serffuzz

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/agent/internal/sessionlog"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// FuzzSessionNamerProgram drives the session naming pipeline with an offline
// scripted provider. It deliberately covers both the direct naming contract and
// the session-owned prompt/compaction lifecycle without issuing a provider call.
//
// Oracles:
//   - canonicalization is deterministic and keeps its documented rune bound;
//   - a scripted model's JSON name is applied exactly once with the expected
//     source/model selection; and
//   - prompt and compaction launchers preserve their precedence rules.
func FuzzSessionNamerProgram(f *testing.F) {
	for _, seed := range []struct {
		program   []byte
		text      string
		candidate string
	}{
		{[]byte{0}, "repair the session name fuzzer", "Repair Session Name Fuzzer"},
		{[]byte{1}, "[CONTEXT SUMMARY] parser work complete", `  "Parser Work Complete!!!"  `},
		{[]byte{2}, strings.Repeat("long source ", 700), strings.Repeat("Long Name ", 20)},
		{[]byte{3}, "non-ascii input", "\u66f4\u65b0\u4f1a\u8bdd\u6807\u9898"},
		{[]byte{4}, "punctuation", "...!!!"},
		{[]byte{5}, "", "Empty Source"},
	} {
		f.Add(seed.program, seed.text, seed.candidate)
	}

	f.Fuzz(func(t *testing.T, program []byte, text, candidate string) {
		mode := byte(0)
		if len(program) > 0 {
			mode = program[0]
		}
		text = namerFuzzText(text)
		candidate = namerFuzzText(candidate)

		// The pure normalization helpers define the model-facing contract. Repeat
		// them rather than comparing against a hand-written duplicate algorithm.
		source := "unexpected"
		if mode&1 != 0 {
			source = sessionNameSourceCompaction
		}
		prompt := sessionNamerUserPrompt(source, text)
		if again := sessionNamerUserPrompt(source, text); prompt != again {
			t.Fatalf("session namer prompt was not deterministic")
		}
		clean := sanitizeSessionName(candidate)
		if again := sanitizeSessionName(candidate); clean != again {
			t.Fatalf("session name sanitization was not deterministic")
		}
		if utf8.RuneCountInString(clean) > sessionNameMaxRunes {
			t.Fatalf("sanitized name has %d runes, exceeds %d", utf8.RuneCountInString(clean), sessionNameMaxRunes)
		}
		if clean != "" {
			last, _ := utf8.DecodeLastRuneInString(clean)
			if unicode.IsPunct(last) || unicode.IsSpace(last) {
				t.Fatalf("sanitized name retains trailing punctuation or space: %q", clean)
			}
		}
		if len([]rune(trimForSessionNamer(text))) > 4015 { // 4,000 input runes plus the marker.
			t.Fatalf("trimmed model input exceeded its bound")
		}
		if !strings.Contains(prompt, trimForSessionNamer(text)) {
			t.Fatalf("model prompt omitted normalized source text")
		}

		profile := NewOpenAIProfile("gpt-main")
		if mode&2 != 0 {
			profile = WithCheapModel(profile, "gpt-cheap")
		}
		if sessionNamerEnabled(nil) || sessionNamerModel(nil) != "" || configuredSessionNamerModel(nil) != "" {
			t.Fatal("nil profile unexpectedly enabled the namer")
		}
		if sessionNamerEnabled(NewOpenAIProfile("gpt-main")) {
			t.Fatal("active model unexpectedly enabled automatic naming")
		}
		if !sessionNamerEnabled(WithCheapModel(NewOpenAIProfile("gpt-main"), "gpt-cheap")) {
			t.Fatal("configured cheap model did not enable automatic naming")
		}

		// Exercise public input guards before constructing a valid scripted call.
		if _, err := nameSession(context.Background(), nil, profile, source, text, noNamerSleep); err == nil {
			t.Fatal("nil client was accepted")
		}
		if _, err := nameSession(context.Background(), llm.NewClient(), nil, source, text, noNamerSleep); err == nil {
			t.Fatal("nil profile was accepted")
		}
		if _, err := nameSession(context.Background(), llm.NewClient(), profile, source, " ", noNamerSleep); err == nil {
			t.Fatal("empty source text was accepted")
		}
		if _, err := nameSession(context.Background(), llm.NewClient(), NewOpenAIProfile(""), source, text, noNamerSleep); err == nil {
			t.Fatal("empty model was accepted")
		}
		failureClient := llm.NewClient()
		failureClient.Register(namerFuzzErrorAdapter{})
		if _, err := nameSession(context.Background(), failureClient, profile, source, text, noNamerSleep); err == nil || !strings.Contains(err.Error(), "scripted failure") {
			t.Fatalf("scripted provider error = %v", err)
		}

		// GenerateObject validates the JSON schema before nameSession sanitizes the
		// model output, so successful scripted values must satisfy maxLength first.
		responseName := namerFuzzSchemaName(candidate)
		wantName := sanitizeSessionName(responseName)
		wantNameError := mode%5 == 4
		if wantNameError {
			responseName = "!!!"
		}
		body, err := json.Marshal(map[string]string{"name": responseName})
		if err != nil {
			t.Fatalf("marshal scripted name: %v", err)
		}
		adapter := &fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
			func(llm.Request) llm.Response {
				return llm.Response{Message: llm.Assistant(string(body)), Usage: llm.Usage{TotalTokens: 7}}
			},
		}}
		client := llm.NewClient()
		client.Register(adapter)
		result, err := nameSession(context.Background(), client, profile, source, text, noNamerSleep)
		if wantNameError {
			if err == nil || !strings.Contains(err.Error(), "generated name is empty") {
				t.Fatalf("empty scripted name error = %v", err)
			}
			return
		}
		if err != nil {
			t.Fatalf("nameSession: %v", err)
		}
		if result.Name != wantName || result.Source != normalizeSessionNameSource(source) || result.Usage.TotalTokens != 7 {
			t.Fatalf("name result = %#v, want name=%q source=%q usage=7", result, wantName, normalizeSessionNameSource(source))
		}
		requests := adapter.Requests()
		if len(requests) != 1 {
			t.Fatalf("scripted provider calls = %d, want 1", len(requests))
		}
		wantModel := "gpt-main"
		if mode&2 != 0 {
			wantModel = "gpt-cheap"
		}
		if requests[0].Provider != "openai" || requests[0].Model != wantModel {
			t.Fatalf("namer request = provider=%q model=%q, want openai/%q", requests[0].Provider, requests[0].Model, wantModel)
		}

		namerFuzzSessionLifecycle(t, mode, text, wantName)
	})
}

func namerFuzzText(text string) string {
	if len(text) > 6000 {
		text = text[:6000]
	}
	text = strings.ToValidUTF8(text, "?")
	if strings.TrimSpace(text) == "" {
		return "repair fuzz coverage"
	}
	return text
}

func namerFuzzSchemaName(name string) string {
	runes := []rune(name)
	if len(runes) > sessionNameMaxRunes {
		return string(runes[:sessionNameMaxRunes])
	}
	return name
}

func namerFuzzSessionLifecycle(t *testing.T, mode byte, text, name string) {
	t.Helper()
	dir := t.TempDir()
	profile := WithCheapModel(NewOpenAIProfile("gpt-main"), "gpt-cheap")
	body, err := json.Marshal(map[string]string{"name": name})
	if err != nil {
		t.Fatalf("marshal lifecycle name: %v", err)
	}
	adapter := &fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return llm.Response{Message: llm.Assistant(string(body))} },
	}}
	client := llm.NewClient()
	client.Register(adapter)
	sess, err := NewSession(client, profile, &agenttest.DenyEnv{WorkDir: dir}, SessionConfig{StateDir: dir})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	if err := sess.nameSessionFromText(context.Background(), sessionNameSourcePrompt, text); err != nil {
		t.Fatalf("nameSessionFromText: %v", err)
	}
	meta := sess.Meta()
	if meta.Name != name || meta.NameSource != sessionNameSourcePrompt || meta.NameUpdatedAt.IsZero() {
		t.Fatalf("applied session name meta = %#v", meta)
	}

	compactionCalls := 0
	sess.nameSessionFromTextFunc = func(_ context.Context, source, gotText string) error {
		compactionCalls++
		if source != sessionNameSourceCompaction || strings.TrimSpace(gotText) == "" {
			t.Fatalf("compaction naming input = source=%q text=%q", source, gotText)
		}
		return nil
	}
	turn := schema.NewTurn(schema.TurnSummary, llm.User("[CONTEXT SUMMARY] "+text))
	if mode&8 != 0 {
		sess.mu.Lock()
		sess.naming.value = "Manual Name"
		sess.naming.source = sessionNameSourceUser
		sess.mu.Unlock()
	}
	if err := sess.nameSessionFromCompactionTurn(context.Background(), turn); err != nil {
		t.Fatalf("nameSessionFromCompactionTurn: %v", err)
	}
	if mode&8 != 0 {
		if compactionCalls != 0 || sess.shouldNameFromCompaction() {
			t.Fatalf("manual name was not protected from compaction")
		}
	} else if compactionCalls != 1 || !sess.shouldNameFromCompaction() {
		t.Fatalf("compaction naming calls=%d, want one permitted call", compactionCalls)
	}
	if err := sess.nameSessionFromCompactionTurn(context.Background(), schema.NewTurn(schema.TurnAssistant, llm.Assistant(text))); err != nil {
		t.Fatalf("non-compaction turn: %v", err)
	}
	if mode&8 == 0 && compactionCalls != 1 {
		t.Fatalf("non-compaction turn started a namer")
	}

	// Reset the precedence state so the asynchronous launchers run through their
	// real goroutine paths. WaitGroup joins make this deterministic without sleep
	// or polling.
	sess.mu.Lock()
	sess.naming.value = ""
	sess.naming.source = ""
	sess.naming.set = false
	sess.naming.promptPending = false
	sess.mu.Unlock()
	launcherCalls := 0
	launcherProblem := ""
	sess.nameSessionFromTextFunc = func(_ context.Context, source, gotText string) error {
		launcherCalls++
		if strings.TrimSpace(gotText) == "" {
			launcherProblem = "launcher passed empty naming text"
			return nil
		}
		switch launcherCalls {
		case 1:
			if source != sessionNameSourcePrompt {
				launcherProblem = "initial launcher source = " + source
			}
		case 2, 3:
			if source != sessionNameSourceCompaction {
				launcherProblem = "compaction launcher source = " + source
			}
		default:
			launcherProblem = "unexpected namer launcher call"
		}
		return nil
	}
	sess.launchInitialPromptNamer(context.Background(), text)
	sess.sendersWG.Wait()
	if launcherCalls != 1 {
		t.Fatalf("initial launcher calls = %d, want 1", launcherCalls)
	}
	if launcherProblem != "" {
		t.Fatal(launcherProblem)
	}
	if sess.naming.promptPending {
		t.Fatal("completed initial launcher left promptPending set")
	}
	sess.launchInitialPromptNamer(context.Background(), " ")
	sess.sendersWG.Wait()
	if launcherCalls != 1 {
		t.Fatal("empty initial prompt started a namer")
	}
	sess.launchCompactionNamer(context.Background(), turn)
	sess.sendersWG.Wait()
	if launcherCalls != 2 {
		t.Fatalf("compaction launcher calls = %d, want 2", launcherCalls)
	}
	if launcherProblem != "" {
		t.Fatal(launcherProblem)
	}
	sess.handleCompactionTurn(turn)
	sess.sendersWG.Wait()
	if launcherCalls != 3 {
		t.Fatalf("compaction handler launcher calls = %d, want 3", launcherCalls)
	}
	if launcherProblem != "" {
		t.Fatal(launcherProblem)
	}
	(&Session{}).appendSessionNamerLog(sessionlog.SessionLogEntry{})
}

type namerFuzzErrorAdapter struct{}

func (namerFuzzErrorAdapter) Name() string { return "openai" }

func (namerFuzzErrorAdapter) Complete(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{}, errors.New("scripted failure")
}

func (namerFuzzErrorAdapter) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}
