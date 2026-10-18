package hub

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/test/e2e/fakellm"
)

func TestTutorialExtensionEffects(t *testing.T) {
	runTour(t, tourCase{Scenario: "extensions", Surface: "cli"}, t.TempDir())
}

func TestTutorialExtensionOracleRequiresSkillResult(t *testing.T) {
	for _, tc := range []struct {
		name, resultCallID string
		wantError          bool
	}{
		{"matching_result", "activated", false},
		{"missing_result", "", true},
		{"different_call_result", "other", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			plugin, err := filepath.Abs("../../docs/examples/extensions/hello-plugin")
			if err != nil {
				t.Fatal(err)
			}
			writeJSON := func(name string, value any) {
				t.Helper()
				b, err := json.Marshal(value)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, name), b, 0600); err != nil {
					t.Fatal(err)
				}
			}
			writeJSON("session.meta.json", schema.SessionMeta{
				Config: schema.ConfigSnapshot{PluginDirs: []string{plugin}},
				Skills: &schema.SkillLifecycleSnapshot{Inventory: map[string]schema.SkillInventoryEntry{
					"change-summary": {Ordinary: &schema.OrdinarySkillActivation{
						Identity: schema.SkillContentIdentity{Name: "change-summary", FileDigest: "fixture-digest"}, Route: "model_tool",
					}},
				}},
			})
			turns := []schema.Turn{
				{Hook: &schema.HookInfo{Event: "SessionStart", Matcher: "startup", PluginName: "hello-plugin"}},
				{Message: llm.Message{Content: []llm.ContentPart{
					{ToolCall: &llm.ToolCallData{ID: "activated", Name: "use_skill"}},
					{ToolCall: &llm.ToolCallData{ID: "other", Name: "use_skill"}},
					{ToolCall: &llm.ToolCallData{ID: "echo", Name: "example__echo", Arguments: json.RawMessage(`{"message":"nonce"}`)}},
				}}},
				{SkillState: &schema.SkillTurnState{Outcomes: []schema.SkillActivationOutcome{{
					Identity: schema.SkillContentIdentity{Name: "change-summary"}, Status: "pending", ToolCallID: "activated",
				}}}},
				{Message: llm.Message{Content: []llm.ContentPart{{ToolResult: &llm.ToolResultData{
					ToolCallID: "echo", Name: "example__echo", Content: "nonce",
				}}}}},
			}
			if tc.resultCallID != "" {
				turns = append(turns, schema.Turn{Message: llm.Message{Content: []llm.ContentPart{{ToolResult: &llm.ToolResultData{
					ToolCallID: tc.resultCallID, Name: "use_skill", Content: "skill loaded",
				}}}}})
			}
			var transcriptBytes bytes.Buffer
			for _, turn := range turns {
				if err := json.NewEncoder(&transcriptBytes).Encode(transcript.Entry{Turn: turn}); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(root, "session.transcript.jsonl"), transcriptBytes.Bytes(), 0600); err != nil {
				t.Fatal(err)
			}
			err = checkExtensionOutcome(root, "unchanged", "unchanged", "nonce")
			if tc.wantError && err == nil {
				t.Fatal("accepted activation without its successful skill result")
			}
			if !tc.wantError && err != nil {
				t.Fatal(err)
			}
		})
	}
}

func extensionHash(t *testing.T, root string) string {
	t.Helper()
	h := sha256.New()
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		fmt.Fprintf(h, "%s\x00%s\x00", rel, info.Mode())
		if d.IsDir() {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		fmt.Fprintf(h, "%s\x00%d\x00", rel, len(b))
		h.Write(b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func extensionNonce(t *testing.T) string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

func extensionExpanded(t *testing.T, call *fakellm.Call, nonce, command string) {
	t.Helper()
	available := false
	tools, _ := call.Body["tools"].([]any)
	for _, raw := range tools {
		tool, _ := raw.(map[string]any)
		fn, _ := tool["function"].(map[string]any)
		if fn["name"] == "example__echo" {
			available = true
		}
	}
	if !available {
		t.Fatal("MCP server unavailable, refusing extension checkpoint")
	}
	messages, _ := call.Body["messages"].([]any)
	found := false
	for _, raw := range messages {
		m, _ := raw.(map[string]any)
		if m["role"] != "user" {
			continue
		}
		b, _ := json.Marshal(m["content"])
		if bytes.Contains(b, []byte(nonce)) && !bytes.Contains(b, []byte(command)) {
			found = true
		}
	}
	if !found {
		t.Fatal("command argument did not cross expansion boundary")
	}
}

func extensionOutcome(t *testing.T, root, work, beforeHash, sentNonce string) {
	t.Helper()
	if err := checkExtensionOutcome(root, beforeHash, extensionHash(t, work), sentNonce); err != nil {
		t.Fatal(err)
	}
	t.Log("verified startup hook, skill activation, correlated MCP nonce, unchanged workspace")
}

func checkExtensionOutcome(root, beforeHash, workspaceHash, sentNonce string) error {
	hookFound, skillFound, echoFound := false, false, false
	skillCommitted := false
	pluginConfigured := false
	expectedPlugin, err := filepath.Abs("../../docs/examples/extensions/hello-plugin")
	if err != nil {
		return err
	}
	echoNonce := ""
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(path, ".meta.json") {
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			var meta schema.SessionMeta
			if err := json.Unmarshal(b, &meta); err != nil {
				return err
			}
			for _, dir := range meta.Config.PluginDirs {
				if dir == expectedPlugin {
					pluginConfigured = true
				}
			}
			if meta.Skills != nil {
				if activation := meta.Skills.Inventory["change-summary"].Ordinary; activation != nil && activation.Route == "model_tool" && activation.Identity.FileDigest != "" {
					skillCommitted = true
				}
			}
		}
		if d.IsDir() || !strings.HasSuffix(path, ".transcript.jsonl") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		calls := map[string]string{}
		skillActivations := map[string]bool{}
		skillResults := map[string]bool{}
		for line := range bytes.SplitSeq(bytes.TrimSpace(b), []byte("\n")) {
			var entry transcript.Entry
			if err := json.Unmarshal(line, &entry); err != nil {
				return err
			}
			turn := entry.Turn
			if v := turn.Hook; v != nil && v.Event == "SessionStart" && v.Matcher == "startup" && v.PluginName == "hello-plugin" {
				if v.ExitCode != 0 {
					return fmt.Errorf("startup hook failed with exit %d", v.ExitCode)
				}
				hookFound = true
			}
			if state := turn.SkillState; state != nil {
				for _, outcome := range state.Outcomes {
					if outcome.Identity.Name == "change-summary" && outcome.Status == "pending" && outcome.ToolCallID != "" && calls[outcome.ToolCallID] == "use_skill" {
						skillActivations[outcome.ToolCallID] = true
					}
				}
			}
			for _, part := range turn.Message.Content {
				if c := part.ToolCall; c != nil {
					if c.Name == "example__echo" {
						var args struct {
							Message string `json:"message"`
						}
						if err := json.Unmarshal(c.Arguments, &args); err != nil {
							return err
						}
						if args.Message != sentNonce {
							return errors.New("echo call argument differs from input nonce")
						}
					}
					calls[c.ID] = c.Name
				}
				if r := part.ToolResult; r != nil && (r.Name == "example__echo" || r.Name == "use_skill") {
					if r.ToolCallID == "" || calls[r.ToolCallID] != r.Name || !tutorialToolEvidence(*r, nil).Passed {
						return fmt.Errorf("uncorrelated or failed extension result: %+v", r)
					}
					if r.Name == "example__echo" {
						echoFound = true
						echoNonce = fmt.Sprint(r.Content)
					} else {
						skillResults[r.ToolCallID] = true
					}
				}
			}
		}
		for callID := range skillActivations {
			if skillResults[callID] {
				skillFound = true
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if !hookFound || !skillFound || !skillCommitted || !pluginConfigured || !echoFound {
		return fmt.Errorf("missing records: hook=%v skill=%v committed=%v plugin=%v echo=%v", hookFound, skillFound, skillCommitted, pluginConfigured, echoFound)
	}
	if echoNonce != sentNonce || workspaceHash != beforeHash {
		return errors.New("extension effects disagree")
	}
	return nil
}

func runExtensionsCLI(t *testing.T, out string) {
	p, providers := configProvider(t)
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(liveStackBinaries(t, repo), "evener")
	home := tutorialOwnedRoot(t)
	server := filepath.Join(home, "echo-server")
	build := exec.Command("go", "build", "-o", server, "./docs/examples/extensions/mcp-server")
	build.Dir = repo
	if b, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build MCP: %v: %s", err, b)
	}
	stack := startHubStackWithOptions(t, providers, "fake/"+fakellm.ModelID, bin, hubStackOptions{ChildEnv: tutorialChildEnvironment(t, home)})
	env := append(tutorialChildEnvironment(t, home), "EVENER_EXAMPLE_MCP_SERVER="+server)
	examples := filepath.Join(repo, "docs", "examples", "extensions")
	var records []commandRecord
	for _, copy := range [][2]string{{"skills/change-summary/SKILL.md", "skills/change-summary/SKILL.md"}, {"commands/summarize-change.md", ".evener/commands/summarize-change.md"}, {"mcp.json", ".mcp.json"}} {
		dst := filepath.Join(stack.workDir, copy[1])
		if err := os.MkdirAll(filepath.Dir(dst), 0700); err != nil {
			t.Fatal(err)
		}
		r := recordCommand(t, repo, env, "cp", filepath.Join("docs", "examples", "extensions", copy[0]), dst)
		if r.ExitCode != 0 {
			t.Fatal("copy example failed")
		}
		r.CwdLabel = "evener-checkout"
		r.Owned = &transcriptOwned{Paths: []string{stack.workDir}}
		records = append(records, r)
	}
	before := extensionHash(t, stack.workDir)
	capture := func(id string) func() error {
		return func() error {
			return writeTutorialCommandRecords(filepath.Join(out, id+".json"), records)
		}
	}
	checkpoint(t, "entry", []tourEvidence{{"installed examples and baseline hash", true}}, capture("entry"))
	// Query the CLI's real NDJSON stream as a user can. The full stream remains
	// private, because prompts contain host paths and exceed a fixed screenshot.
	query := filepath.Join(home, "extension-events.py")
	if err := os.WriteFile(query, []byte(`import json, sys
for line in open(sys.argv[1]):
    event = json.loads(line)
    kind, data = event['kind'], event.get('data', {})
    if kind in ('USER_INPUT', 'PLUGIN_LOADED', 'HOOK_END', 'SKILL_ACTIVATED') or (kind in ('TOOL_CALL_START', 'TOOL_CALL_END') and data.get('tool_name') == 'example__echo'):
        fields = ('text', 'name', 'event', 'matcher', 'plugin_name', 'exit_code', 'tool_name', 'call_id', 'arguments_json', 'output', 'error')
        print(kind, json.dumps({k: data[k] for k in fields if k in data}))
`), 0600); err != nil {
		t.Fatal(err)
	}
	sentNonce := extensionNonce(t)
	for index, command := range []string{"/summarize-change", "/hello-plugin:hello"} {
		func() {
			nonce := extensionNonce(t)
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			args := []string{bin, "--verbose", "--model", "fake/" + fakellm.ModelID, "--sandbox", "off", "--plugin-dir", filepath.Join(examples, "hello-plugin"), "--mcp-config", filepath.Join(stack.workDir, ".mcp.json"), command + " " + nonce}
			cmd := exec.CommandContext(ctx, args[0], args[1:]...)
			cmd.Dir = stack.workDir
			cmd.Env = env
			var stdout, stderr bytes.Buffer
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr
			if err := cmd.Start(); err != nil {
				cancel()
				t.Fatal(err)
			}
			done := false
			defer func() {
				if !done {
					_ = cmd.Process.Kill()
					_ = cmd.Wait()
				}
				cancel()
			}()
			first := dailyNext(ctx, t, p)
			extensionExpanded(t, first, nonce, command)
			if index == 0 {
				first.RespondToolCall("use_skill", map[string]any{"skill_name": "change-summary", "intent": "Loading the installed summary skill"})
				next := dailyNext(ctx, t, p)
				next.RespondToolCall("example__echo", map[string]any{"message": sentNonce})
				first = dailyNext(ctx, t, p)
			}
			first.RespondToolCall("communicate", communicateArgs("Extension walkthrough complete. No files changed."))
			err := cmd.Wait()
			done = true
			cancel()
			if err != nil {
				t.Fatalf("extension CLI: %v: %s", err, redactHubStackLog(stderr.Bytes()))
			}
			loaded := false
			for line := range bytes.SplitSeq(stderr.Bytes(), []byte("\n")) {
				var event struct {
					Kind events.EventKind        `json:"kind"`
					Data events.PluginLoadedData `json:"data"`
				}
				if json.Unmarshal(line, &event) == nil && event.Kind == events.EventPluginLoaded && event.Data.Name == "hello-plugin" {
					loaded = true
				}
			}
			if !loaded {
				t.Fatal("plugin load record absent")
			}
			log := filepath.Join(home, fmt.Sprintf("extension-%d.jsonl", index))
			if err := os.WriteFile(log, stderr.Bytes(), 0600); err != nil {
				t.Fatal(err)
			}
			r := recordCommand(t, stack.workDir, env, "python3", query, log)
			if r.ExitCode != 0 {
				t.Fatal("CLI event query failed")
			}
			r.CwdLabel = "extensions-workspace"
			r.Owned = &transcriptOwned{Paths: []string{stack.workDir, home, examples}}
			if index == 0 {
				records = nil
			}
			records = append(records, r)
			if index == 0 {
				extensionOutcome(t, filepath.Join(home, "state"), stack.workDir, before, sentNonce)
				checkpoint(t, "action", []tourEvidence{{"skill hook and MCP execution", true}}, capture("action"))
			}
		}()
	}
	extensionOutcome(t, filepath.Join(home, "state"), stack.workDir, before, sentNonce)
	checkpoint(t, "verified", []tourEvidence{{"plugin command routing and unchanged workspace", true}}, capture("verified"))
}

func runExtensionsWeb(t *testing.T, out string) {
	p, providers := configProvider(t)
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	bin := settingsUIControllerBinary(t, repo)
	home := tutorialOwnedRoot(t)
	server := filepath.Join(home, "echo-server")
	build := exec.Command("go", "build", "-o", server, "./docs/examples/extensions/mcp-server")
	build.Dir = repo
	if b, err := build.CombinedOutput(); err != nil {
		t.Fatalf("MCP build: %v: %s", err, b)
	}
	env := append(tutorialChildEnvironment(t, home), "EVENER_EXAMPLE_MCP_SERVER="+server)
	stack := startHubStackWithOptions(t, providers, "fake/"+fakellm.ModelID, bin, hubStackOptions{ChildEnv: env})
	stack.workDir, err = filepath.EvalSymlinks(stack.workDir)
	if err != nil {
		t.Fatal(err)
	}
	examples := filepath.Join(repo, "docs", "examples", "extensions")
	for _, copy := range [][2]string{{"skills/change-summary/SKILL.md", "skills/change-summary/SKILL.md"}, {"commands/summarize-change.md", ".evener/commands/summarize-change.md"}, {"mcp.json", ".mcp.json"}} {
		dst := filepath.Join(stack.workDir, copy[1])
		if err := os.MkdirAll(filepath.Dir(dst), 0700); err != nil {
			t.Fatal(err)
		}
		if r := recordCommand(t, stack.workDir, env, "cp", filepath.Join(examples, copy[0]), dst); r.ExitCode != 0 {
			t.Fatal("install example")
		}
	}
	config := fmt.Sprintf("plugin_dirs = [%q]\nmcp_configs = [%q]\nsandbox = \"off\"\n", filepath.Join(examples, "hello-plugin"), filepath.Join(stack.workDir, ".mcp.json"))
	if err := os.WriteFile(filepath.Join(home, "config", "evener", "launch.toml"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	before := extensionHash(t, stack.workDir)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	observer := stack.dialRPC(ctx, t)
	url := "http://" + stack.addr
	assertHubServesBuiltSPA(t, stack.addr, stack.token)
	driver := openTutorialDriver(ctx, t, map[string]any{"url": url, "authURL": url + "/auth/" + stack.token, "workspace": stack.workDir, "model": stack.model, "scenario": "extensions", "viewport": map[string]int{"width": 1280, "height": 900}, "outputDir": out})
	checkpoint(t, "entry", []tourEvidence{{"host installation and baseline hash", true}}, driver.capture(t, out, "entry"))
	nonce := extensionNonce(t)
	driver.act(t, "start", map[string]any{"prompt": "/summarize-change " + nonce})
	first := dailyNext(ctx, t, p)
	extensionExpanded(t, first, nonce, "/summarize-change")
	list, err := clientRequest[appwire.ThreadListResponse](ctx, observer, appwire.MethodThreadList, appwire.ThreadListParams{})
	if err != nil {
		t.Fatal(err)
	}
	var ref string
	for _, thread := range list.Data {
		if thread.CWD == stack.workDir {
			ref = thread.Evener.Ref
		}
	}
	if ref == "" {
		t.Fatalf("UI-created workspace not listed: %+v", list.Data)
	}
	first.RespondToolCall("use_skill", map[string]any{"skill_name": "change-summary", "intent": "Loading the installed summary skill"})
	next := dailyNext(ctx, t, p)
	sentNonce := extensionNonce(t)
	next.RespondToolCall("example__echo", map[string]any{"message": sentNonce})
	next = dailyNext(ctx, t, p)
	extensionOutcome(t, filepath.Join(home, "state"), stack.workDir, before, sentNonce)
	checkpoint(t, "action", []tourEvidence{{"skill hook and correlated MCP result", true}}, driver.capture(t, out, "action"))
	next.RespondToolCall("communicate", communicateArgs("The local skill ran and the echo server returned the nonce. No files changed."))
	idle := func(thread appwire.Thread) bool {
		return thread.Evener.ActiveTurnID == "" && thread.Status.Type != "active"
	}
	awaitThread(ctx, t, observer, ref, "extension turn completed", idle)
	pluginNonce := extensionNonce(t)
	driver.act(t, "send", map[string]any{"prompt": "/hello-plugin:hello " + pluginNonce})
	next = dailyNext(ctx, t, p)
	extensionExpanded(t, next, pluginNonce, "/hello-plugin:hello")
	next.RespondToolCall("communicate", communicateArgs("The local plugin command completed. No files changed."))
	awaitThread(ctx, t, observer, ref, "plugin command completed", idle)
	extensionOutcome(t, filepath.Join(home, "state"), stack.workDir, before, sentNonce)
	checkpoint(t, "verified", []tourEvidence{{"plugin command routing and unchanged workspace", true}}, driver.capture(t, out, "verified"))
	driver.act(t, "close", nil)
}

// Invalid host setup must not advertise a working MCP tool. The tour checks
// that same provider catalogue before allowing its action checkpoint.
func TestTutorialExtensionMCPFailures(t *testing.T) {
	for _, kind := range []string{"missing-server", "malformed-config"} {
		t.Run(kind, func(t *testing.T) {
			p, providers := configProvider(t)
			repo, err := filepath.Abs("../..")
			if err != nil {
				t.Fatal(err)
			}
			bin := filepath.Join(liveStackBinaries(t, repo), "evener")
			home := tutorialOwnedRoot(t)
			stack := startHubStackWithOptions(t, providers, "fake/"+fakellm.ModelID, bin, hubStackOptions{ChildEnv: tutorialChildEnvironment(t, home)})
			config := filepath.Join(home, "mcp.json")
			body := fmt.Sprintf(`{"mcpServers":{"example":{"command":%q}}}`, filepath.Join(home, "absent-server"))
			if kind == "malformed-config" {
				body = `{"mcpServers":`
			}
			if err := os.WriteFile(config, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			observed := make(chan int, 1)
			go func() {
				call, err := p.Next(ctx.Done())
				if err != nil {
					observed <- -1
					return
				}
				advertised := false
				tools, _ := call.Body["tools"].([]any)
				for _, raw := range tools {
					tool, _ := raw.(map[string]any)
					fn, _ := tool["function"].(map[string]any)
					if fn["name"] == "example__echo" {
						advertised = true
					}
				}
				call.RespondToolCall("communicate", communicateArgs("Unavailable extension checked."))
				if advertised {
					observed <- 1
				} else {
					observed <- 0
				}
			}()
			cmd := exec.CommandContext(ctx, bin, "--model", "fake/"+fakellm.ModelID, "--mcp-config", config, "Inspect extension availability.")
			cmd.Dir = stack.workDir
			cmd.Env = tutorialChildEnvironment(t, home)
			output, runErr := cmd.CombinedOutput()
			cancel()
			seen := <-observed
			if cmd.ProcessState == nil || (seen == -1 && runErr == nil) {
				t.Fatal("negative check did not exercise a CLI launch")
			}
			if seen == 1 {
				t.Fatal("broken MCP setup advertised echo")
			}
			if ctx.Err() == context.DeadlineExceeded {
				t.Fatalf("CLI did not terminate: %s", redactHubStackLog(output))
			}
			t.Logf("%s: CLI error=%v, echo not advertised", kind, runErr)
		})
	}
}
