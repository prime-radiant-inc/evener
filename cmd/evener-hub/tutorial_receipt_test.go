package hub

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
	"primeradiant.com/evener/test/e2e/fakellm"
)

func TestTutorialReceiptOracle(t *testing.T) {
	source := receiptBaseline(t)
	defer assertReceiptSource(t, source)
	work := t.TempDir()
	copyReceipt(t, work, source)
	baselineExit := receiptCheck(t, work).ExitCode
	// An agent's final message cannot repair the file.
	if err := os.WriteFile(filepath.Join(work, "final.txt"), []byte("All fixed"), 0600); err != nil {
		t.Fatal(err)
	}
	if receiptCheck(t, work).ExitCode != 1 {
		t.Fatal("final prose changed checker result")
	}
	repairReceipt(t, work)
	repairedExit := receiptCheck(t, work).ExitCode
	if baselineExit != 1 || repairedExit != 0 {
		t.Fatal("unexpected checker exit codes")
	}
	if err := receiptOracle(t, work, source); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "check.py"), []byte("raise SystemExit(0)\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if receiptCheck(t, work).ExitCode != 0 {
		t.Fatal("tamper fixture did not exit zero")
	}
	if err := receiptOracle(t, work, source); err == nil {
		t.Fatal("modified checker accepted")
	}
}

type transcriptOwned struct {
	Executable string   `json:"executable"`
	Paths      []string `json:"paths"`
}

type commandRecord struct {
	Argv     []string         `json:"argv"`
	CwdLabel string           `json:"cwdLabel"` //nolint:tagliatelle // Matches the JavaScript tutorial command-record wire field cwdLabel.
	Stdout   string           `json:"stdout"`
	Stderr   string           `json:"stderr"`
	ExitCode int              `json:"exitCode"` //nolint:tagliatelle // Matches the JavaScript tutorial command-record wire field exitCode.
	Owned    *transcriptOwned `json:"owned,omitempty"`
}

func receiptBaseline(t *testing.T) map[string][]byte {
	t.Helper()
	result := map[string][]byte{}
	for _, name := range []string{"README.md", "check.py", "receipt.py"} {
		b, err := os.ReadFile(filepath.Join("..", "..", "docs", "examples", "first-task", name))
		if err != nil {
			t.Fatal(err)
		}
		result[name] = b
	}
	return result
}
func assertReceiptSource(t *testing.T, baseline map[string][]byte) {
	t.Helper()
	for name, b := range receiptBaseline(t) {
		if sha256.Sum256(b) != sha256.Sum256(baseline[name]) {
			t.Errorf("source fixture changed: %s", name)
		}
	}
}
func copyReceipt(t *testing.T, work string, baseline map[string][]byte) {
	t.Helper()
	for name, b := range baseline {
		if err := os.WriteFile(filepath.Join(work, name), b, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

const brokenReceipt = "return sum(price_cents for price_cents, quantity in items)"
const fixedReceipt = "return sum(price_cents * quantity for price_cents, quantity in items)"

func repairReceipt(t *testing.T, work string) {
	b, err := os.ReadFile(filepath.Join(work, "receipt.py"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "receipt.py"), []byte(strings.Replace(string(b), brokenReceipt, fixedReceipt, 1)), 0600); err != nil {
		t.Fatal(err)
	}
}
func recordCommand(t *testing.T, work string, env []string, argv ...string) commandRecord {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = work
	cmd.Env = env
	var out, errout bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errout
	err := cmd.Run()
	exit := 0
	if err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Fatal(err)
		}
		exit = ee.ExitCode()
	}
	return commandRecord{Argv: argv, CwdLabel: "receipt-workspace", Stdout: out.String(), Stderr: errout.String(), ExitCode: exit}
}
func receiptCheck(t *testing.T, work string) commandRecord {
	return recordCommand(t, work, tutorialChildEnvironment(t, t.TempDir()), "python3", "check.py")
}
func receiptOracle(t *testing.T, work string, baseline map[string][]byte) error {
	for _, name := range []string{"README.md", "check.py"} {
		b, err := os.ReadFile(filepath.Join(work, name))
		if err != nil {
			return err
		}
		if !bytes.Equal(b, baseline[name]) {
			return fmt.Errorf("protected fixture changed: %s", name)
		}
	}
	r := recordCommand(t, work, tutorialChildEnvironment(t, t.TempDir()), "python3", "-c", `import json; from receipt import total_cents; print(json.dumps([total_cents(x) for x in [[], [(250,3)], [(125,2),(80,3)], [(999,0),(50,2)]]]))`)
	var actual []int
	if r.ExitCode != 0 {
		return fmt.Errorf("oracle execution failed: %d", r.ExitCode)
	}
	if err := json.Unmarshal([]byte(r.Stdout), &actual); err != nil {
		return err
	}
	if !reflect.DeepEqual(actual, []int{0, 750, 490, 100}) {
		return fmt.Errorf("incorrect amounts: %v", actual)
	}
	if receiptCheck(t, work).ExitCode != 0 {
		return errors.New("checker failed")
	}
	return nil
}

const receiptPrompt = `Fix total_cents in receipt.py so each item's price is multiplied by its
quantity. Work only in this directory. Read README.md and check.py first.
Run python3 check.py to reproduce the bug, then make the smallest repair
in receipt.py and rerun the check. Do not change check.py or README.md.
Do not commit. Report what changed and the actual check result.`

func runReceiptCLI(t *testing.T, candidateDir string) {
	source := receiptBaseline(t)
	defer assertReceiptSource(t, source)
	provider, err := fakellm.New()
	if err != nil {
		t.Fatal(err)
	}
	defer provider.Close()
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(liveStackBinaries(t, repo), "evener")
	root := tutorialOwnedRoot(t)
	env := tutorialChildEnvironment(t, root)
	stack := startHubStackWithOptions(t, tutorialProviderConfig(provider.BaseURL()), "fake/"+fakellm.ModelID, bin, hubStackOptions{ChildEnv: env})
	env = tutorialChildEnvironment(t, stack.home)
	prepareReceiptWorkspace(t, stack.workDir, env, source)
	capture := func(id string, records []commandRecord) func() error {
		return func() error {
			return writeTutorialCommandRecords(filepath.Join(candidateDir, id+".json"), records)
		}
	}
	pre := receiptCheck(t, stack.workDir)
	checkpoint(t, "entry", []tourEvidence{{"baseline exit 1", pre.ExitCode == 1}}, capture("entry", []commandRecord{pre}))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	args := []string{bin, "--model", "fake/" + fakellm.ModelID, "--timeout", "2m", "--max-rounds", "8", "--sandbox", "off", receiptPrompt}
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
	var changed []byte
	var diff commandRecord
	toolIDs, commandIDs := driveReceipt(ctx, t, provider, func(editID string) {
		changed, err = os.ReadFile(filepath.Join(stack.workDir, "receipt.py"))
		if err != nil {
			t.Fatal(err)
		}
		diff = recordCommand(t, stack.workDir, env, "git", "diff", "--", "receipt.py")
	})
	err = cmd.Wait()
	waited = true
	exit := 0
	if err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Fatal(err)
		}
		exit = ee.ExitCode()
	}
	invocation := commandRecord{Argv: args, CwdLabel: "receipt-workspace", Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: exit, Owned: &transcriptOwned{Executable: bin, Paths: []string{stack.workDir, stack.home}}}
	if exit != 0 {
		t.Fatalf("CLI exit %d: %s", exit, redactHubStackLog(stderr.Bytes()))
	}
	if err := receiptOracle(t, stack.workDir, source); err != nil {
		t.Fatal(err)
	}
	results := receiptToolResults(t, filepath.Join(stack.home, "state"))
	var runtimeEvidence []tourEvidence
	for _, id := range toolIDs {
		result, ok := results[id]
		runtimeEvidence = append(runtimeEvidence, tourEvidence{"recorded tool " + id, ok}, tutorialToolEvidence(result, nil))
	}
	for i, id := range commandIDs {
		expected := 1 - i
		runtimeEvidence = append(runtimeEvidence, tutorialToolEvidence(results[id], &expected))
	}
	if err := captureCheckpoint(runtimeEvidence, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	checkpoint(t, "action", []tourEvidence{{"edit result", bytes.Equal(changed, []byte(strings.Replace(string(source["receipt.py"]), brokenReceipt, fixedReceipt, 1)))}, {"diff exit", diff.ExitCode == 0}}, capture("action", []commandRecord{diff}))
	verified := receiptCheck(t, stack.workDir)
	checkpoint(t, "verified", []tourEvidence{{"independent checker", verified.ExitCode == 0}}, capture("verified", []commandRecord{invocation, diff, verified}))
}
func requireReceiptTool(t *testing.T, call *fakellm.Call, id string) {
	t.Helper()
	messages, _ := call.Body["messages"].([]any)
	for _, raw := range messages {
		m, _ := raw.(map[string]any)
		if m["role"] == "tool" && m["tool_call_id"] == id {
			return
		}
	}
	t.Fatalf("tool result missing for %s", id)
}

func tutorialToolEvidence(result llm.ToolResultData, expectedExit *int) tourEvidence {
	passed := !result.IsError && !result.PrevalOnly
	if expectedExit != nil {
		var state struct {
			ExitCode *int `json:"exit_code"`
			TimedOut bool `json:"timed_out"`
		}
		passed = passed && json.Unmarshal(result.ToolState, &state) == nil && state.ExitCode != nil && *state.ExitCode == *expectedExit && !state.TimedOut
	}
	return tourEvidence{Name: result.Name, Passed: passed}
}
func TestTutorialFailedToolBlocksCapture(t *testing.T) {
	source := receiptBaseline(t)
	work := t.TempDir()
	copyReceipt(t, work, source)
	repairReceipt(t, work)
	if err := receiptOracle(t, work, source); err != nil {
		t.Fatal(err)
	}
	for _, result := range []llm.ToolResultData{{Name: "unknown", IsError: true, PrevalOnly: true}, {Name: "shell", IsError: true}, {Name: "shell", ToolState: json.RawMessage(`{"exit_code":1}`)}} {
		zero := 0
		called := false
		err := captureCheckpoint([]tourEvidence{tutorialToolEvidence(result, &zero), {Name: "independent checker", Passed: true}}, func() error { called = true; return nil })
		if called || err == nil {
			t.Fatal("failed tool authorized capture despite checker passing")
		}
	}
}
func receiptToolResults(t *testing.T, root string) map[string]llm.ToolResultData {
	t.Helper()
	results := map[string]llm.ToolResultData{}
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
		var header transcript.Header
		if err := json.Unmarshal(lines[0], &header); err != nil {
			return err
		}
		if header.ProfileID != "fake" || header.Model != fakellm.ModelID {
			return fmt.Errorf("unexpected persisted provider/model: %q/%q", header.ProfileID, header.Model)
		}
		for _, line := range lines {
			var entry transcript.Entry
			if err := json.Unmarshal(line, &entry); err != nil {
				return err
			}
			for _, part := range entry.Turn.Message.Content {
				if part.ToolResult != nil {
					results[part.ToolResult.ToolCallID] = *part.ToolResult
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return results
}

func receiptCommandTool(t *testing.T, call *fakellm.Call) string {
	t.Helper()
	tools, _ := call.Body["tools"].([]any)
	for _, raw := range tools {
		tool, _ := raw.(map[string]any)
		fn, _ := tool["function"].(map[string]any)
		name, _ := fn["name"].(string)
		if name == "shell" || name == "exec_command" {
			return name
		}
	}
	t.Fatal("provider did not advertise a command tool")
	return ""
}

func driveReceipt(ctx context.Context, t *testing.T, provider *fakellm.Server, onEdit func(string)) ([]string, []string) {
	next := func() *fakellm.Call {
		c, err := provider.Next(ctx.Done())
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	call := next()
	if call.Body["model"] != fakellm.ModelID {
		t.Fatal("unexpected launched model identity")
	}
	commandTool := receiptCommandTool(t, call)
	var toolIDs []string
	var commandIDs []string
	for _, name := range []string{"README.md", "check.py", "receipt.py"} {
		id := call.ToolCallID()
		toolIDs = append(toolIDs, id)
		call.RespondToolCall("read_file", map[string]any{"file_path": name, "intent": "Reading receipt fixture"})
		call = next()
		requireReceiptTool(t, call, id)
	}
	id := call.ToolCallID()
	commandIDs = append(commandIDs, id)
	toolIDs = append(toolIDs, id)
	call.RespondToolCall(commandTool, map[string]any{"command": "python3 check.py", "mode": "foreground", "intent": "Reproducing receipt failure"})
	call = next()
	requireReceiptTool(t, call, id)
	id = call.ToolCallID()
	toolIDs = append(toolIDs, id)
	call.RespondToolCall("edit_file", map[string]any{"file_path": "receipt.py", "old_string": brokenReceipt, "new_string": fixedReceipt, "intent": "Fixing quantity multiplication"})
	call = next()
	requireReceiptTool(t, call, id)
	onEdit(id)
	id = call.ToolCallID()
	commandIDs = append(commandIDs, id)
	toolIDs = append(toolIDs, id)
	call.RespondToolCall(commandTool, map[string]any{"command": "python3 check.py", "mode": "foreground", "intent": "Checking repair"})
	call = next()
	requireReceiptTool(t, call, id)
	toolIDs = append(toolIDs, call.ToolCallID())
	call.RespondToolCall("communicate", communicateArgs("Updated receipt.py to multiply prices by quantities. The checker exited zero."))
	return toolIDs, commandIDs
}

func runReceiptWeb(t *testing.T, candidateDir string) {
	source := receiptBaseline(t)
	defer assertReceiptSource(t, source)
	provider, err := fakellm.New()
	if err != nil {
		t.Fatal(err)
	}
	defer provider.Close()
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	bin := settingsUIControllerBinary(t, repo)
	root := tutorialOwnedRoot(t)
	// Ollama is keyless and otherwise probes an ambient localhost service.
	// Redirect its discovery to the owned fake HTTP server, then hide its models.
	providerConfig := tutorialProviderConfig(provider.BaseURL())
	if _, err := registry.ParseConfig([]byte(providerConfig)); err != nil {
		t.Fatal(err)
	}
	stack := startHubStackWithOptions(t, providerConfig, "fake/"+fakellm.ModelID, bin, hubStackOptions{ChildEnv: tutorialChildEnvironment(t, root)})
	stack.workDir, err = filepath.EvalSymlinks(stack.workDir)
	if err != nil {
		t.Fatal(err)
	}
	prepareReceiptWorkspace(t, stack.workDir, tutorialChildEnvironment(t, stack.home), source)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	url := "http://" + stack.addr
	assertHubServesBuiltSPA(t, stack.addr, stack.token)
	observer := stack.dialRPC(ctx, t)
	models, modelErr := clientRequest[appwire.ModelListResponse](ctx, observer, appwire.MethodModelList, appwire.ModelListParams{Harness: "evener", CWD: stack.workDir})
	if modelErr != nil {
		t.Fatal(modelErr)
	}
	if len(models.Data) != 1 || models.Data[0].Provider != "fake" || models.Data[0].Model != fakellm.ModelID || len(models.Diagnostics) != 0 {
		t.Fatal("isolated model catalogue must contain only fake model with no diagnostics")
	}
	driver := openTutorialDriver(ctx, t, map[string]any{"url": url, "authURL": url + "/auth/" + stack.token, "workspace": stack.workDir, "model": stack.model, "viewport": map[string]int{"width": 1280, "height": 900}, "outputDir": candidateDir})
	pre := receiptCheck(t, stack.workDir)
	checkpoint(t, "entry", []tourEvidence{{"baseline checker", pre.ExitCode == 1}}, driver.capture(t, candidateDir, "entry"))
	driver.act(t, "start", map[string]any{"prompt": receiptPrompt})
	var ref string
	for ref == "" {
		list, err := clientRequest[appwire.ThreadListResponse](ctx, observer, appwire.MethodThreadList, appwire.ThreadListParams{})
		if err != nil {
			t.Fatal(err)
		}
		for _, thread := range list.Data {
			canonical, err := filepath.EvalSymlinks(thread.CWD)
			owned, ownErr := filepath.EvalSymlinks(stack.workDir)
			if err == nil && ownErr == nil && canonical == owned {
				if ref != "" {
					t.Fatal("multiple workspace sessions")
				}
				if thread.ModelProvider != fakellm.ModelID {
					t.Fatalf("launched model identity: %q", thread.ModelProvider)
				}
				ref = thread.Evener.Ref
				if ref == "" {
					t.Fatal("UI-created session has no qualified reference")
				}
			}
		}
		if ref == "" {
			select {
			case <-ctx.Done():
				t.Fatal("UI-created workspace session not found")
			case <-time.After(50 * time.Millisecond):
			}
		}
	}
	toolIDs, commandIDs := driveReceipt(ctx, t, provider, func(editID string) {
		changed, err := os.ReadFile(filepath.Join(stack.workDir, "receipt.py"))
		if err != nil {
			t.Fatal(err)
		}
		results := receiptToolResults(t, filepath.Join(stack.home, "state"))
		result, ok := results[editID]
		checkpoint(t, "action", []tourEvidence{{"actual edit", bytes.Equal(changed, []byte(strings.Replace(string(source["receipt.py"]), brokenReceipt, fixedReceipt, 1)))}, {"persisted edit", ok}, tutorialToolEvidence(result, nil)}, driver.capture(t, candidateDir, "action"))
	})
	awaitThread(ctx, t, observer, ref, "receipt turn completed", func(thread appwire.Thread) bool {
		return thread.Evener.ActiveTurnID == "" && thread.Status.Type != "active"
	})
	results := receiptToolResults(t, filepath.Join(stack.home, "state"))
	var evidence []tourEvidence
	for _, id := range toolIDs {
		result, ok := results[id]
		evidence = append(evidence, tourEvidence{"persisted tool", ok}, tutorialToolEvidence(result, nil))
	}
	for i, id := range commandIDs {
		expected := 1 - i
		evidence = append(evidence, tutorialToolEvidence(results[id], &expected))
	}
	if err := receiptOracle(t, stack.workDir, source); err != nil {
		t.Fatal(err)
	}
	checker, err := os.ReadFile(filepath.Join(stack.workDir, "check.py"))
	if err != nil {
		t.Fatal(err)
	}
	checkerExit := receiptCheck(t, stack.workDir).ExitCode
	checkerHash, baselineCheckerHash := sha256.Sum256(checker), sha256.Sum256(source["check.py"])
	evidence = append(evidence, tourEvidence{"checker exit", checkerExit == 0}, tourEvidence{"checker hash", checkerHash == baselineCheckerHash})
	checkpoint(t, "verified", evidence, driver.capture(t, candidateDir, "verified"))
	driver.act(t, "close", nil)
}

func TestTutorialBrokenReceiptBlocksFinalCapture(t *testing.T) {
	source := receiptBaseline(t)
	work := t.TempDir()
	copyReceipt(t, work, source)
	captured := false
	err := captureCheckpoint([]tourEvidence{{"receipt oracle", receiptOracle(t, work, source) == nil}}, func() error { captured = true; return nil })
	if err == nil || captured {
		t.Fatal("broken receipt authorized final capture")
	}
}

func prepareReceiptWorkspace(t *testing.T, work string, env []string, source map[string][]byte) {
	copyReceipt(t, work, source)
	for _, args := range [][]string{{"git", "init"}, {"git", "add", "README.md", "receipt.py", "check.py"}, {"git", "-c", "user.name=Tutorial", "-c", "user.email=tutorial@example.invalid", "commit", "-m", "Add first-task fixture"}} {
		if r := recordCommand(t, work, env, args...); r.ExitCode != 0 {
			t.Fatalf("git setup: %s", r.Stderr)
		}
	}
}
