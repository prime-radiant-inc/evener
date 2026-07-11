//go:build serffuzz

package agenttest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/llm"
)

// FuzzAgenttestAdaptersProgram exercises the deterministic adapter and response
// builders used at the LLM boundary. It never contacts a provider.
func FuzzAgenttestAdaptersProgram(f *testing.F) {
	f.Add("model", "reply", true)
	f.Add("", "", false)

	f.Fuzz(func(t *testing.T, model, text string, fallback bool) {
		req := llm.Request{Model: model, Messages: []llm.Message{llm.User(text)}}
		ctx := context.Background()

		fake := &FakeAdapter{
			Provider:          "fake",
			CanFallbackToChat: fallback,
			Steps: []func(llm.Request) llm.Response{
				func(got llm.Request) llm.Response {
					return llm.Response{Message: llm.Assistant(text + "|" + got.Model)}
				},
			},
		}
		if fake.Name() != "fake" {
			t.Fatalf("FakeAdapter.Name = %q", fake.Name())
		}
		first, err := fake.Complete(ctx, req)
		if err != nil || first.Provider != "fake" || first.Model != model || first.Text() != text+"|"+model {
			t.Fatalf("first FakeAdapter response = %#v, %v", first, err)
		}
		second, err := fake.Complete(ctx, req)
		if err != nil || second.Text() != "done" || second.Provider != "fake" || second.Model != model {
			t.Fatalf("fallback FakeAdapter response = %#v, %v", second, err)
		}
		if _, err := fake.Stream(ctx, req); !errors.Is(err, llm.ErrStreamUnsupported) {
			t.Fatalf("FakeAdapter.Stream error = %v, want ErrStreamUnsupported", err)
		}
		if _, err := fake.PlanResponsesContinuation(req); err == nil {
			t.Fatal("FakeAdapter missing planner did not fail")
		}
		fake.PlanResponsesContinuationFunc = func(llm.Request) (llm.ResponsesContinuationPlan, error) {
			return llm.ResponsesContinuationPlan{}, nil
		}
		plan, err := fake.PlanResponsesContinuation(req)
		if err != nil || plan.CanFallbackToChat != fallback {
			t.Fatalf("FakeAdapter plan = %#v, %v", plan, err)
		}
		fake.PlanResponsesContinuationFunc = func(llm.Request) (llm.ResponsesContinuationPlan, error) {
			return llm.ResponsesContinuationPlan{}, errors.New("planner fault")
		}
		if _, err := fake.PlanResponsesContinuation(req); err == nil {
			t.Fatal("FakeAdapter planner fault did not propagate")
		}
		assertAgenttestRequestCopy(t, fake.Requests(), fake.Requests, 2)

		tracking := &ModelTrackingAdapter{
			Provider: "tracked",
			Respond: func(got llm.Request) (llm.Response, error) {
				return llm.Response{Message: llm.Assistant(got.Model + ":" + text)}, nil
			},
		}
		tracked, err := tracking.Complete(ctx, req)
		if err != nil || tracked.Provider != "tracked" || tracked.Model != model || tracked.Text() != model+":"+text {
			t.Fatalf("ModelTrackingAdapter response = %#v, %v", tracked, err)
		}
		if got := tracking.Models(); !reflect.DeepEqual(got, []string{model}) {
			t.Fatalf("ModelTrackingAdapter.Models = %#v", got)
		}
		assertAgenttestRequestCopy(t, tracking.Requests(), tracking.Requests, 1)
		if _, err := tracking.Stream(ctx, req); !errors.Is(err, llm.ErrStreamUnsupported) {
			t.Fatalf("ModelTrackingAdapter.Stream error = %v", err)
		}
		faultTracking := &ModelTrackingAdapter{Provider: "tracked", Respond: func(llm.Request) (llm.Response, error) {
			return llm.Response{}, errors.New("response fault")
		}}
		if _, err := faultTracking.Complete(ctx, req); err == nil {
			t.Fatal("ModelTrackingAdapter response fault did not propagate")
		}

		responderCalls := 0
		scripted := &ScriptedAdapter{
			Provider: "scripted",
			Responder: func(got llm.Request) llm.Response {
				responderCalls++
				return llm.Response{Message: llm.Assistant(got.Model + text)}
			},
		}
		if scripted.Name() != "scripted" {
			t.Fatalf("ScriptedAdapter.Name = %q", scripted.Name())
		}
		scriptedResp, err := scripted.Complete(ctx, req)
		if err != nil || scriptedResp.Provider != "scripted" || scriptedResp.Model != model || scriptedResp.Text() != model+text {
			t.Fatalf("ScriptedAdapter response = %#v, %v", scriptedResp, err)
		}
		scripted.FaultResponder = func(llm.Request) error { return errors.New("scripted fault") }
		if _, err := scripted.Complete(ctx, req); err == nil {
			t.Fatal("ScriptedAdapter fault did not propagate")
		}
		if responderCalls != 1 {
			t.Fatalf("ScriptedAdapter responder calls = %d, want 1", responderCalls)
		}
		assertAgenttestRequestCopy(t, scripted.Requests(), scripted.Requests, 2)
		if _, err := scripted.Stream(ctx, req); !errors.Is(err, llm.ErrStreamUnsupported) {
			t.Fatalf("ScriptedAdapter.Stream error = %v", err)
		}

		if got := EmptyResponse(); got.Text() != "" || len(got.Message.Content) != 0 {
			t.Fatalf("EmptyResponse = %#v", got)
		}
		call := CommunicateCallArgs("call-"+model, map[string]any{
			"message":  "  " + text + "  ",
			"end_turn": fallback,
			"output":   map[string]any{"artifacts": []string{text}},
		})
		assertAgenttestCommunicateCall(t, call, strings.TrimSpace(text), fallback)
		fallbackCall := CommunicateCallArgs("fallback", map[string]any{"message": nil, "output": map[string]any{"message": text}})
		assertAgenttestCommunicateCall(t, fallbackCall, text, true)
		if got := CommunicateCall("simple", text); got.Name != "communicate" || got.ID != "simple" {
			t.Fatalf("CommunicateCall = %#v", got)
		}
		toolResp := ToolCallResponse(call, fallbackCall)
		if toolResp.Message.Role != llm.RoleAssistant || len(toolResp.Message.Content) != 2 || toolResp.Message.Content[0].ToolCall == nil || toolResp.Message.Content[1].ToolCall == nil {
			t.Fatalf("ToolCallResponse = %#v", toolResp)
		}
		communicateResp := CommunicateResponse(fallback, text)
		if len(communicateResp.Message.Content) != 1 || communicateResp.Message.Content[0].ToolCall == nil {
			t.Fatalf("CommunicateResponse = %#v", communicateResp)
		}
		final := FinalResponse(text)
		if len(final.Message.Content) != 1 || final.Message.Content[0].ToolCall == nil {
			t.Fatalf("FinalResponse = %#v", final)
		}

		assertAgenttestFakeEnv(t, text)
	})
}

func assertAgenttestRequestCopy(t *testing.T, requests []llm.Request, fresh func() []llm.Request, want int) {
	t.Helper()
	if len(requests) != want {
		t.Fatalf("recorded requests = %d, want %d", len(requests), want)
	}
	if want == 0 {
		return
	}
	requests[0].Model = "mutated"
	if got := fresh(); got[0].Model == "mutated" {
		t.Fatal("Requests exposed its backing slice")
	}
}

func assertAgenttestCommunicateCall(t *testing.T, call llm.ToolCallData, wantMessage string, wantEndTurn bool) {
	t.Helper()
	if call.Name != "communicate" || call.Type != "function" {
		t.Fatalf("communicate call envelope = %#v", call)
	}
	var args struct {
		Message string         `json:"message"`
		EndTurn bool           `json:"end_turn"`
		Output  map[string]any `json:"output"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		t.Fatalf("communicate arguments: %v", err)
	}
	if args.Message != wantMessage || args.EndTurn != wantEndTurn || args.Output == nil {
		t.Fatalf("communicate args = %#v, want message=%q endTurn=%v output", args, wantMessage, wantEndTurn)
	}
}

func assertAgenttestFakeEnv(t *testing.T, root string) {
	t.Helper()
	env := &FakeEnv{WorkDir: root, GitRoot: root + "/repo"}
	if err := env.Initialize(); err != nil {
		t.Fatalf("FakeEnv.Initialize: %v", err)
	}
	defer env.Cleanup()
	if env.WorkingDirectory() != root || env.Platform() != "test" || env.OSVersion() != "test" {
		t.Fatalf("FakeEnv identity = %q/%q/%q", env.WorkingDirectory(), env.Platform(), env.OSVersion())
	}
	git, err := env.ExecCommand(context.Background(), "git rev-parse --show-toplevel", 0, root, nil)
	if err != nil || git.Stdout != env.GitRoot || git.ExitCode != 0 {
		t.Fatalf("FakeEnv git = %#v, %v", git, err)
	}
	other, err := env.ExecCommand(context.Background(), "not git", 0, root, nil)
	if err != nil || other.ExitCode != 1 {
		t.Fatalf("FakeEnv other command = %#v, %v", other, err)
	}
	if got, err := env.ReadFile("x", nil, nil); err != nil || got != "" {
		t.Fatalf("FakeEnv.ReadFile = %q, %v", got, err)
	}
	if got, err := env.WriteFile("x", "y"); err != nil || got != "" {
		t.Fatalf("FakeEnv.WriteFile = %q, %v", got, err)
	}
	if got, err := env.EditFile("x", "a", "b", false); err != nil || got != "" {
		t.Fatalf("FakeEnv.EditFile = %q, %v", got, err)
	}
	if env.FileExists("x") {
		t.Fatal("FakeEnv.FileExists = true")
	}
	if got, err := env.Glob("*", root); err != nil || got != nil {
		t.Fatalf("FakeEnv.Glob = %#v, %v", got, err)
	}
	if got, err := env.Grep("x", root, "", false, 1, ""); err != nil || got != "" {
		t.Fatalf("FakeEnv.Grep = %q, %v", got, err)
	}
	if got, err := env.ListDirectory(root, 0); err != nil || got != nil {
		t.Fatalf("FakeEnv.ListDirectory = %#v, %v", got, err)
	}
}

// FuzzDenyEnvProgram calls every fake execution-environment method through
// deterministic success and failure inputs. It is a containment harness: all
// outputs are fabricated from Seed and no host process or filesystem is used.
func FuzzDenyEnvProgram(f *testing.F) {
	f.Add(uint64(1), "path", "content")
	f.Add(uint64(77), "", "")

	f.Fuzz(func(t *testing.T, seed uint64, path, content string) {
		ctx := context.Background()
		readErr := agenttestDenyForRemainder(t, seed, 5, 0, "read", path)
		readOK := agenttestDenyForRemainder(t, seed, 5, 1, "read", path)
		if got, err := readErr.ReadFile(path, nil, nil); err == nil || got != "" {
			t.Fatalf("DenyEnv.ReadFile error case = %q, %v", got, err)
		}
		if got, err := readOK.ReadFile(path, nil, nil); err != nil || len(got) > denyMaxBytes {
			t.Fatalf("DenyEnv.ReadFile success case = %q, %v", got, err)
		}

		editErr := agenttestDenyForRemainder(t, seed, 3, 0, "edit", path, content)
		editOK := agenttestDenyForRemainder(t, seed, 3, 1, "edit", path, content)
		if got, err := editErr.EditFile(path, content, "new", false); err == nil || got != "" {
			t.Fatalf("DenyEnv.EditFile error case = %q, %v", got, err)
		}
		if got, err := editOK.EditFile(path, content, "new", true); err != nil || !strings.Contains(got, path) {
			t.Fatalf("DenyEnv.EditFile success case = %q, %v", got, err)
		}

		execErr := agenttestDenyForRemainder(t, seed, 7, 0, "exec", content, path)
		execOK := agenttestDenyForRemainder(t, seed, 7, 1, "exec", content, path)
		if _, err := execErr.ExecCommand(ctx, content, 0, path, nil); err == nil {
			t.Fatal("DenyEnv.ExecCommand error case succeeded")
		}
		result, err := execOK.ExecCommand(ctx, content, 0, path, nil)
		if err != nil || len(result.Stdout) > denyMaxBytes || result.ExitCode < 0 || result.ExitCode > 2 {
			t.Fatalf("DenyEnv.ExecCommand success case = %#v, %v", result, err)
		}
		again, err := execOK.ExecCommand(ctx, content, 0, path, nil)
		if err != nil || again != result {
			t.Fatalf("DenyEnv.ExecCommand replay = %#v, %v; want %#v", again, err, result)
		}

		env := &DenyEnv{WorkDir: path, Seed: seed}
		if err := env.Initialize(); err != nil {
			t.Fatalf("DenyEnv.Initialize: %v", err)
		}
		defer env.Cleanup()
		env.Cleanup()
		if env.WorkingDirectory() != path || env.Platform() != "linux" || env.OSVersion() != "deny-env" {
			t.Fatalf("DenyEnv identity = %q/%q/%q", env.WorkingDirectory(), env.Platform(), env.OSVersion())
		}
		if got, err := env.WriteFile(path, content); err != nil || !strings.Contains(got, path) {
			t.Fatalf("DenyEnv.WriteFile = %q, %v", got, err)
		}
		if env.FileExists(path) != env.FileExists(path) {
			t.Fatal("DenyEnv.FileExists is not deterministic")
		}
		for _, mode := range []string{"", "count", "files_with_matches"} {
			got, err := env.Grep(content, path, "", false, 1, mode)
			if err != nil || len(got) > denyMaxBytes {
				t.Fatalf("DenyEnv.Grep(%q) = %q, %v", mode, got, err)
			}
		}
		grepMatch := agenttestDenyForRemainder(t, seed, 2, 0, "grep", content, path, "files_with_matches")
		grepMiss := agenttestDenyForRemainder(t, seed, 2, 1, "grep", content, path, "files_with_matches")
		if got, err := grepMatch.Grep(content, path, "", false, 1, "files_with_matches"); err != nil || got != path {
			t.Fatalf("DenyEnv.Grep files match = %q, %v", got, err)
		}
		if got, err := grepMiss.Grep(content, path, "", false, 1, "files_with_matches"); err != nil || got != "" {
			t.Fatalf("DenyEnv.Grep files miss = %q, %v", got, err)
		}
		if got := env.boundedText(0); got != "" {
			t.Fatalf("DenyEnv.boundedText(0) = %q", got)
		}
		if matches, err := env.Glob("*", path); err != nil || len(matches) > 3 {
			t.Fatalf("DenyEnv.Glob = %#v, %v", matches, err)
		}
		if entries, err := env.ListDirectory(path, 0); err != nil || len(entries) > 3 {
			t.Fatalf("DenyEnv.ListDirectory = %#v, %v", entries, err)
		}
		var out bytes.Buffer
		handle, err := env.StreamCommand(ctx, content, path, nil, &out)
		if err != nil || handle == nil || out.Len() > denyMaxBytes {
			t.Fatalf("DenyEnv.StreamCommand = %#v, %v", handle, err)
		}
		code, err := handle.Wait()
		if err != nil || code < 0 || code > 2 {
			t.Fatalf("DenyEnv stream wait = %d, %v", code, err)
		}
		handle.Signal()
		if _, err := env.StreamCommand(ctx, content, path, nil, nil); err != nil {
			t.Fatalf("DenyEnv nil-writer stream: %v", err)
		}
	})
}

func agenttestDenyForRemainder(t *testing.T, start, modulus, want uint64, parts ...string) *DenyEnv {
	t.Helper()
	for offset := uint64(0); offset < 8192; offset++ {
		env := &DenyEnv{Seed: start + offset}
		if env.draw(parts...)%modulus == want {
			return env
		}
	}
	t.Fatalf("no DenyEnv seed with draw(%q) %% %d == %d", parts, modulus, want)
	return nil
}

// FuzzFakeClockProgram drives timer, ticker, callback, and sleep lifecycles
// with virtual time only. It uses BlockUntil instead of wall-clock polling.
func FuzzFakeClockProgram(f *testing.F) {
	f.Add(int64(1))
	f.Add(int64(-9))

	f.Fuzz(func(t *testing.T, rawSeconds int64) {
		seconds := rawSeconds % 9
		if seconds < 0 {
			seconds = -seconds
		}
		d := time.Duration(seconds+1) * time.Second
		start := time.Unix(1000, 0).UTC()
		clock := NewFakeClockAt(start)
		if got := NewFakeClock().Now(); got != start {
			t.Fatalf("NewFakeClock starts at %v, want %v", got, start)
		}
		if got := <-clock.After(-d); got != start {
			t.Fatalf("immediate After = %v, want %v", got, start)
		}
		if clock.BlockedCount() != 0 {
			t.Fatal("immediate After left a waiter")
		}

		timer := clock.NewTimer(d)
		if !timer.Reset(d) {
			t.Fatal("Reset of a pending timer = false")
		}
		ticker := clock.NewTicker(d)
		callbackDone := make(chan struct{})
		clock.AfterFunc(d, func() { close(callbackDone) })
		clock.BlockUntil(3)
		clock.Advance(d)
		if got := <-timer.C(); got != clock.Now() {
			t.Fatalf("timer = %v, want %v", got, clock.Now())
		}
		if got := <-ticker.C(); got != clock.Now() {
			t.Fatalf("ticker = %v, want %v", got, clock.Now())
		}
		<-callbackDone
		if timer.Stop() {
			t.Fatal("Stop after a fired timer = true")
		}
		if timer.Reset(d) {
			t.Fatal("Reset after a fired timer = true")
		}
		clock.Advance(d)
		<-timer.C()

		ticker.Reset(d)
		clock.Advance(d)
		<-ticker.C()
		ticker.Stop()
		if clock.BlockedCount() != 0 {
			t.Fatalf("stopped ticker left %d waiters", clock.BlockedCount())
		}
		ticker.Reset(d)
		clock.Advance(d)
		<-ticker.C()
		ticker.Stop()

		mustPanicAgenttest(t, func() { clock.NewTicker(0) })

		sleepClock := NewFakeClock()
		slept := make(chan struct{})
		go func() {
			sleepClock.Sleep(d)
			close(slept)
		}()
		sleepClock.BlockUntil(1)
		sleepClock.Advance(d)
		<-slept
	})
}

func mustPanicAgenttest(t *testing.T, fn func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic")
		}
	}()
	fn()
}
