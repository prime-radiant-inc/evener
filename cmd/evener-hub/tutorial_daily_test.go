package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/test/e2e/fakellm"
)

// The independent oracle rejects a missing or repeated effect even when a
// provider has reported success. Nonces are data, not model-facing prose.
func TestTutorialDailyQueueEffects(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		body  string
		valid bool
	}{
		{"once", `{"nonce":"a91e","receipts":["a91e"]}`, true},
		{"missing", `{"nonce":"a91e","receipts":[]}`, false},
		{"duplicate", `{"nonce":"a91e","receipts":["a91e","a91e"]}`, false},
		{"wrong nonce", `{"nonce":"b92f","receipts":["b92f"]}`, false},
		{"absent", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "queued.json")
			if tc.body != "" {
				if err := os.WriteFile(path, []byte(tc.body), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := dailyEffectOracle(path, "a91e"); (err == nil) != tc.valid {
				t.Fatalf("valid=%v, error=%v", tc.valid, err)
			}
		})
	}
}

type dailyEffect struct {
	Nonce    string   `json:"nonce"`
	Receipts []string `json:"receipts"`
}

func dailyEffectOracle(path, nonce string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var effect dailyEffect
	if err := json.Unmarshal(b, &effect); err != nil {
		return err
	}
	if effect.Nonce != nonce || len(effect.Receipts) != 1 || effect.Receipts[0] != nonce {
		return errors.New("incorrect effect receipt")
	}
	return nil
}

// All provider discovery remains inside the owned HTTP boundary, including
// the otherwise keyless Ollama probe.
func dailyStack(t *testing.T, web bool) (*fakellm.Server, hubStack, string) {
	t.Helper()
	provider, err := fakellm.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(provider.Close)
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	var bin string
	if web {
		bin = settingsUIControllerBinary(t, repo)
	} else {
		bin = filepath.Join(liveStackBinaries(t, repo), "evener")
	}
	config := tutorialProviderConfig(provider.BaseURL())
	stack := startHubStackWithOptions(t, config, "fake/"+fakellm.ModelID, bin, hubStackOptions{ChildEnv: tutorialChildEnvironment(t, tutorialOwnedRoot(t))})
	stack.workDir, err = filepath.EvalSymlinks(stack.workDir)
	if err != nil {
		t.Fatal(err)
	}
	if r := recordCommand(t, stack.workDir, tutorialChildEnvironment(t, stack.home), "git", "init"); r.ExitCode != 0 {
		t.Fatal("workspace git init")
	}
	return provider, stack, bin
}

func dailyNext(ctx context.Context, t *testing.T, provider *fakellm.Server) *fakellm.Call {
	t.Helper()
	call, err := provider.Next(ctx.Done())
	if err != nil {
		t.Fatal(err)
	}
	if call.Body["model"] != fakellm.ModelID {
		t.Fatal("unexpected provider model")
	}
	return call
}

// Count only user-role data in this request, not assistant echoes or tool output.
func dailyUserCount(call *fakellm.Call, nonce string) int {
	count := 0
	messages, _ := call.Body["messages"].([]any)
	for _, raw := range messages {
		message, _ := raw.(map[string]any)
		if message["role"] != "user" {
			continue
		}
		if text, ok := message["content"].(string); ok {
			count += strings.Count(text, nonce)
		}
		parts, _ := message["content"].([]any)
		for _, raw := range parts {
			part, _ := raw.(map[string]any)
			if part["type"] == "text" {
				text, _ := part["text"].(string)
				count += strings.Count(text, nonce)
			}
		}
	}
	return count
}

func dailyWrite(ctx context.Context, t *testing.T, provider *fakellm.Server, call *fakellm.Call, name, nonce string) *fakellm.Call {
	t.Helper()
	// Append rather than overwrite: a duplicate execution leaves two receipts
	// and is rejected by the independent file oracle.
	command := fmt.Sprintf(`python3 -c 'import json,pathlib; p=pathlib.Path(%q); d=json.loads(p.read_text()) if p.exists() else {"nonce":%q,"receipts":[]}; d["receipts"].append(%q); p.write_text(json.dumps(d))'`, name, nonce, nonce)
	id := call.ToolCallID()
	call.RespondToolCall(receiptCommandTool(t, call), map[string]any{"command": command, "mode": "foreground", "intent": "Recording the requested daily-work effect"})
	next := dailyNext(ctx, t, provider)
	requireReceiptTool(t, next, id)
	return next
}

func dailyWorkspaceMatches(work, workspace string) (bool, error) {
	cwd, err := filepath.EvalSymlinks(work)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return cwd == workspace, nil
}

func TestTutorialDailyWorkspaceMatches(t *testing.T) {
	root := t.TempDir()
	workspace, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, path     string
		match, wantErr bool
	}{
		{"owned", root, true, false},
		{"unrelated", t.TempDir(), false, false},
		{"missing", filepath.Join(root, "missing"), false, false},
		{"invalid parent", filepath.Join(file, "child"), false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			match, err := dailyWorkspaceMatches(tc.path, workspace)
			if match != tc.match || (err != nil) != tc.wantErr {
				t.Fatalf("match=%v error=%v, want match=%v error=%v", match, err, tc.match, tc.wantErr)
			}
		})
	}
}

func dailyTranscript(t *testing.T, root, workspace string) (transcript.Header, []transcript.Entry) {
	t.Helper()
	var header transcript.Header
	var entries []transcript.Entry
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".transcript.jsonl") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		lines := bytes.Split(bytes.TrimSpace(b), []byte("\n"))
		h, err := transcript.DecodeHeader(lines[0])
		if err != nil {
			return err
		}
		matches, err := dailyWorkspaceMatches(h.WorkingDir, workspace)
		if err != nil {
			return err
		}
		if !matches {
			return nil
		}
		if header.SessionID != "" {
			return errors.New("multiple owned sessions")
		}
		if h.ProfileID != "fake" || h.Model != fakellm.ModelID {
			return errors.New("unexpected persisted model")
		}
		header = h
		for _, line := range lines[1:] {
			var entry transcript.Entry
			if err := json.Unmarshal(line, &entry); err != nil {
				return err
			}
			entries = append(entries, entry)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if header.SessionID == "" {
		t.Fatal("owned session missing")
	}
	return header, entries
}

func dailyDelivery(t *testing.T, entries []transcript.Entry, nonce string, kind schema.TurnKind, turnID string) int {
	t.Helper()
	count := 0
	for _, entry := range entries {
		turn := entry.Turn
		if turn.Kind != kind || turn.Message.Role != llm.RoleUser {
			continue
		}
		for _, part := range turn.Message.Content {
			n := strings.Count(part.Text, nonce)
			if n > 0 && turnID != "" && turn.TurnID != turnID {
				t.Fatalf("delivery belongs to %q, want %q", turn.TurnID, turnID)
			}
			count += n
		}
	}
	return count
}

func dailyCheckTools(t *testing.T, root string) {
	t.Helper()
	results := receiptToolResults(t, root)
	if len(results) == 0 {
		t.Fatal("no persisted tools")
	}
	for _, result := range results {
		var exit *int
		zero := 0
		if result.Name == "shell" || result.Name == "exec_command" {
			exit = &zero
		}
		if !tutorialToolEvidence(result, exit).Passed {
			t.Fatal("failed daily tool", result.Name)
		}
	}
}

func runDailyWeb(t *testing.T, out string) {
	provider, stack, _ := dailyStack(t, true)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	observer := stack.dialRPC(ctx, t)
	models, err := clientRequest[appwire.ModelListResponse](ctx, observer, appwire.MethodModelList, appwire.ModelListParams{Harness: "evener", CWD: stack.workDir})
	if err != nil {
		t.Fatal(err)
	}
	if len(models.Data) != 1 || models.Data[0].Provider != "fake" || models.Data[0].Model != fakellm.ModelID || len(models.Diagnostics) != 0 {
		t.Fatal("unisolated model catalogue")
	}
	url := "http://" + stack.addr
	assertHubServesBuiltSPA(t, stack.addr, stack.token)
	driver := openTutorialDriver(ctx, t, map[string]any{"url": url, "authURL": url + "/auth/" + stack.token, "workspace": stack.workDir, "model": stack.model, "scenario": "daily", "viewport": map[string]int{"width": 1280, "height": 900}, "outputDir": out})
	const opening = "d4a10f26"
	const steer = "e5b21a37"
	const queued = "f6c32b48"
	const continuation = "a7d43c59"
	driver.act(t, "start", map[string]any{"prompt": "Read the workspace and wait for my next instruction. Reference: " + opening})
	call := dailyNext(ctx, t, provider)
	if dailyUserCount(call, opening) != 1 {
		t.Fatal("opening user input")
	}
	// The held HTTP request guarantees launch has reached the real session.
	list, err := clientRequest[appwire.ThreadListResponse](ctx, observer, appwire.MethodThreadList, appwire.ThreadListParams{})
	if err != nil {
		t.Fatal(err)
	}
	var ref string
	for _, thread := range list.Data {
		cwd, err := filepath.EvalSymlinks(thread.CWD)
		if err == nil && cwd == stack.workDir {
			if ref != "" {
				t.Fatal("multiple workspace sessions")
			}
			ref = thread.Evener.Ref
		}
	}
	if ref == "" {
		t.Fatal("workspace session missing")
	}
	first := awaitActiveTurn(ctx, t, observer, ref, "")
	checkpoint(t, "entry", []tourEvidence{{"held active turn", first != ""}}, driver.capture(t, out, "entry"))
	driver.act(t, "steer", map[string]any{"prompt": "Record one receipt in steered.json for nonce " + steer})
	driver.act(t, "send", map[string]any{"prompt": "Next, record one receipt in queued.json for nonce " + queued})
	var thread appwire.Thread
	awaitThread(ctx, t, observer, ref, "queued input", func(observed appwire.Thread) bool { thread = observed; return thread.Evener.Queue.Depth == 1 })
	if thread.Evener.Queue.Depth != 1 {
		t.Fatal("queue depth")
	}
	if thread.Evener.ActiveTurnID != first {
		t.Fatal("queue changed active turn")
	}
	id := call.ToolCallID()
	call.RespondToolCall("read_file", map[string]any{"file_path": filepath.Base(stack.readableFile), "intent": "Reading before the next tool boundary"})
	call = dailyNext(ctx, t, provider)
	requireReceiptTool(t, call, id)
	if dailyUserCount(call, steer) != 1 || dailyUserCount(call, queued) != 0 {
		t.Fatal("steer/queue boundary")
	}
	call = dailyWrite(ctx, t, provider, call, "steered.json", steer)
	call.RespondToolCall("communicate", communicateArgs("Recorded the steering receipt. The queued task follows."))
	call = dailyNext(ctx, t, provider)
	if dailyUserCount(call, queued) != 1 {
		t.Fatal("queued user input")
	}
	second := awaitActiveTurn(ctx, t, observer, ref, first)
	call = dailyWrite(ctx, t, provider, call, "queued.json", queued)
	_, entries := dailyTranscript(t, filepath.Join(stack.home, "state"), stack.workDir)
	queuedDeliveryCount := dailyDelivery(t, entries, queued, schema.TurnUserInput, second)
	if queuedDeliveryCount != 1 {
		t.Fatal("delivery count")
	}
	if dailyDelivery(t, entries, steer, schema.TurnSteering, first) != 1 {
		t.Fatal("steering delivery count")
	}
	for name, nonce := range map[string]string{"steered.json": steer, "queued.json": queued} {
		if err := dailyEffectOracle(filepath.Join(stack.workDir, name), nonce); err != nil {
			t.Fatal(err)
		}
	}
	dailyCheckTools(t, filepath.Join(stack.home, "state"))
	checkpoint(t, "action", []tourEvidence{{"later turn", second != first}, {"unique queued receipt", queuedDeliveryCount == 1}}, driver.capture(t, out, "action"))
	// Keep this request held while Stop cancels its HTTP context. A late tool
	// response must never reach the executor, even after continuation.
	cancelledTool := receiptCommandTool(t, call)
	driver.act(t, "stop", nil)
	select {
	case <-call.Cancelled():
	case <-ctx.Done():
		t.Fatal("held request not cancelled")
	}
	call.RespondToolCall(cancelledTool, map[string]any{"command": `python3 -c 'from pathlib import Path; Path("cancelled.json").write_text("{}")'`, "mode": "foreground", "intent": "Recording a cancelled effect"})
	awaitThread(ctx, t, observer, ref, "interrupted turn ended", func(thread appwire.Thread) bool { return thread.Evener.ActiveTurnID == "" })
	driver.act(t, "send", map[string]any{"prompt": "Continue here. Record one receipt in continued.json for nonce " + continuation})
	call = dailyNext(ctx, t, provider)
	third := awaitActiveTurn(ctx, t, observer, ref, second)
	if third == first {
		t.Fatal("continuation reused first turn")
	}
	if dailyUserCount(call, continuation) != 1 || dailyUserCount(call, queued) != 1 {
		t.Fatal("continuation input or duplicate queued history")
	}
	call = dailyWrite(ctx, t, provider, call, "continued.json", continuation)
	call.RespondToolCall("communicate", communicateArgs("Continued in this session and wrote continued.json. Review the three receipt files."))
	awaitThread(ctx, t, observer, ref, "continuation complete", func(thread appwire.Thread) bool { return thread.Evener.ActiveTurnID == "" })
	cancelledPath := filepath.Join(stack.workDir, "cancelled.json")
	if _, err := os.Stat(cancelledPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("cancelled effect exists")
	}
	for name, nonce := range map[string]string{"steered.json": steer, "queued.json": queued, "continued.json": continuation} {
		if err := dailyEffectOracle(filepath.Join(stack.workDir, name), nonce); err != nil {
			t.Fatal(err)
		}
	}
	_, entries = dailyTranscript(t, filepath.Join(stack.home, "state"), stack.workDir)
	if dailyDelivery(t, entries, queued, schema.TurnUserInput, second) != 1 || dailyDelivery(t, entries, continuation, schema.TurnUserInput, third) != 1 {
		t.Fatal("final persisted deliveries")
	}
	dailyCheckTools(t, filepath.Join(stack.home, "state"))
	checkpoint(t, "verified", []tourEvidence{{"cancelled request and absent effect", true}, {"continued effect and unique deliveries", true}}, driver.capture(t, out, "verified"))
	driver.act(t, "close", nil)
}

func runDailyCLI(t *testing.T, out string) {
	provider, stack, bin := dailyStack(t, false)
	env := tutorialChildEnvironment(t, stack.home)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	capture := func(id string, records ...commandRecord) func() error {
		return func() error {
			for i := range records {
				records[i].CwdLabel = "daily-workspace"
				records[i].Owned = &transcriptOwned{Executable: bin, Paths: []string{stack.workDir, stack.home}}
			}
			return writeTutorialCommandRecords(filepath.Join(out, id+".json"), records)
		}
	}
	run := func(extra []string, nonce, name string, entry bool) commandRecord {
		args := append([]string{bin, "--model", "fake/" + fakellm.ModelID, "--timeout", "2m", "--max-rounds", "8", "--sandbox", "off"}, extra...)
		cmd := exec.CommandContext(ctx, args[0], args[1:]...)
		cmd.Dir = stack.workDir
		cmd.Env = env
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		waited := false
		defer func() {
			if !waited {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
			}
		}()
		call := dailyNext(ctx, t, provider)
		if dailyUserCount(call, nonce) != 1 {
			t.Fatal("CLI user input")
		}
		if entry {
			status := recordCommand(t, stack.workDir, env, "git", "status", "--short")
			checkpoint(t, "entry", []tourEvidence{{"held CLI provider call", true}, {"workspace status", status.ExitCode == 0}}, capture("entry", status))
		}
		call = dailyWrite(ctx, t, provider, call, name, nonce)
		call.RespondToolCall("communicate", communicateArgs("Wrote "+name+". Review its receipt before continuing."))
		err := cmd.Wait()
		waited = true
		if err != nil {
			t.Fatalf("CLI failed: %v: %s", err, redactHubStackLog(stderr.Bytes()))
		}
		if err := dailyEffectOracle(filepath.Join(stack.workDir, name), nonce); err != nil {
			t.Fatal(err)
		}
		return commandRecord{Argv: args, Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: 0}
	}
	const firstNonce = "b8e54d60"
	const nextNonce = "c9f65e71"
	initial := run([]string{"Write initial.json with nonce " + firstNonce + " and a receipts array containing that nonce once."}, firstNonce, "initial.json", true)
	header, entries := dailyTranscript(t, filepath.Join(stack.home, "state"), stack.workDir)
	if dailyDelivery(t, entries, firstNonce, schema.TurnUserInput, "") != 1 {
		t.Fatal("initial persisted input")
	}
	listing := recordCommand(t, stack.workDir, env, bin, "--list-sessions")
	if listing.ExitCode != 0 {
		t.Fatal("list-sessions exit")
	}
	checkpoint(t, "action", []tourEvidence{{"owned saved session", header.SessionID != ""}}, capture("action", initial, listing))
	resumed := run([]string{"--resume", header.SessionID, "Continue by writing continued.json with nonce " + nextNonce + " and a receipts array containing that nonce once."}, nextNonce, "continued.json", false)
	after, entries := dailyTranscript(t, filepath.Join(stack.home, "state"), stack.workDir)
	if after.SessionID != header.SessionID {
		t.Fatal("resume changed session identity")
	}
	if dailyDelivery(t, entries, firstNonce, schema.TurnUserInput, "") != 1 || dailyDelivery(t, entries, nextNonce, schema.TurnUserInput, "") != 1 {
		t.Fatal("resumed input history")
	}
	for name, nonce := range map[string]string{"initial.json": firstNonce, "continued.json": nextNonce} {
		if err := dailyEffectOracle(filepath.Join(stack.workDir, name), nonce); err != nil {
			t.Fatal(err)
		}
	}
	dailyCheckTools(t, filepath.Join(stack.home, "state"))
	check := recordCommand(t, stack.workDir, env, "python3", "-c", `import json; print(json.dumps({p:json.load(open(p)) for p in ["initial.json","continued.json"]},sort_keys=True))`)
	if check.ExitCode != 0 {
		t.Fatal("final file read")
	}
	checkpoint(t, "verified", []tourEvidence{{"same session resumed", after.SessionID == header.SessionID}}, capture("verified", resumed, check))
}
