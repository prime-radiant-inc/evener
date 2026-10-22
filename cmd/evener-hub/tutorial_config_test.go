package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/test/e2e/fakellm"
)

func TestTutorialConfigEffectiveLaunch(t *testing.T) {
	runTour(t, tourCase{Scenario: "configuration", Surface: "cli"}, t.TempDir())
}

const configPrompt = "Read input.json, then write forbidden.json with the same content."
const configInput = "{\"value\":42}\n"

func configToolCatalogue(t *testing.T, call *fakellm.Call) {
	t.Helper()
	available := map[string]bool{}
	tools, _ := call.Body["tools"].([]any)
	for _, raw := range tools {
		tool, _ := raw.(map[string]any)
		fn, _ := tool["function"].(map[string]any)
		name, _ := fn["name"].(string)
		available[name] = true
	}
	for _, name := range []string{"read_file", "apply_patch", "communicate"} {
		if !available[name] {
			t.Fatalf("configuration tool not advertised: %s", name)
		}
	}
}

func configProvider(t *testing.T) (*fakellm.Server, string) {
	t.Helper()
	p, err := fakellm.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	return p, tutorialProviderConfig(p.BaseURL())
}

// Identify the launch while its first request is held. OriginalPrompt in the
// disk snapshot can still be empty until naming or the input's autosave finishes.
func configRunningThread(ctx context.Context, t *testing.T, client *appwire.Client, work, prompt string, call *fakellm.Call) appwire.Thread {
	t.Helper()
	if dailyUserCount(call, prompt) != 1 {
		t.Fatal("configuration provider request did not carry launch input")
	}
	work, err := filepath.EvalSymlinks(work)
	if err != nil {
		t.Fatal(err)
	}
	list, err := clientRequest[appwire.ThreadListResponse](ctx, client, appwire.MethodThreadList, appwire.ThreadListParams{})
	if err != nil {
		t.Fatal(err)
	}
	var found appwire.Thread
	for _, thread := range list.Data {
		cwd, err := filepath.EvalSymlinks(thread.CWD)
		if err != nil || cwd != work {
			continue
		}
		read, err := clientRequest[appwire.ThreadReadResponse](ctx, client, appwire.MethodThreadRead, appwire.ThreadReadParams{Ref: thread.Evener.Ref})
		if err != nil {
			t.Fatal(err)
		}
		thread = read.Thread
		if thread.Evener.ActiveTurnID == "" || thread.Status.Type != "active" {
			continue
		}
		if found.SessionID != "" {
			t.Fatal("multiple active configuration sessions")
		}
		found = thread
	}
	if found.SessionID == "" || found.Evener.Ref == "" {
		t.Fatal("active configuration session missing")
	}
	return found
}

func configMeta(t *testing.T, root, sessionID string) (schema.SessionMeta, string) {
	t.Helper()
	return configFindMeta(t, root, func(meta schema.SessionMeta) bool { return meta.ID == sessionID })
}

func configFindMeta(t *testing.T, root string, matches func(schema.SessionMeta) bool) (schema.SessionMeta, string) {
	t.Helper()
	var found schema.SessionMeta
	var pathFound string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".meta.json") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var meta schema.SessionMeta
		if err := json.Unmarshal(b, &meta); err != nil {
			return err
		}
		if matches(meta) {
			if pathFound != "" {
				return errors.New("multiple configuration sessions")
			}
			found, pathFound = meta, path
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if pathFound == "" {
		t.Fatal("configuration session metadata absent")
	}
	if found.ProfileID != "fake" || found.Model != fakellm.ModelID {
		t.Fatal("effective model differs")
	}
	return found, pathFound
}

func configOutcome(t *testing.T, root, work, sessionID, prompt string, rounds int) {
	t.Helper()
	// CLI callers wait for process exit, web callers observe the held turn
	// becoming inactive. Both barriers follow processOneInput's deferred save.
	meta, path := configFindMeta(t, root, func(meta schema.SessionMeta) bool {
		if sessionID != "" {
			return meta.ID == sessionID
		}
		return meta.OriginalPrompt == prompt
	})
	if meta.OriginalPrompt != prompt {
		t.Fatal("completed session prompt differs")
	}
	b, err := os.ReadFile(strings.TrimSuffix(path, ".meta.json") + ".transcript.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	reads, writes := 0, 0
	for line := range bytes.SplitSeq(bytes.TrimSpace(b), []byte("\n")) {
		var entry transcript.Entry
		if err := json.Unmarshal(line, &entry); err != nil {
			t.Fatal(err)
		}
		for _, part := range entry.Turn.Message.Content {
			if result := part.ToolResult; result != nil {
				if !tutorialToolEvidence(*result, nil).Passed {
					t.Fatalf("failed configuration tool %s: %+v", result.Name, result)
				}
				switch result.Name {
				case "read_file":
					reads++
				case "apply_patch":
					writes++
				}
			}
		}
	}
	effectiveRounds := meta.Config.MaxToolRoundsPerInput
	if rounds == 1 {
		if effectiveRounds != 1 || reads != 1 || writes != 0 {
			t.Fatalf("round limit not applied: effective=%d reads=%d writes=%d", effectiveRounds, reads, writes)
		}
		if _, err := os.Stat(filepath.Join(work, "forbidden.json")); !os.IsNotExist(err) {
			t.Fatal("forbidden path exists or cannot be checked", err)
		}
	} else {
		if effectiveRounds != rounds || reads != 1 || writes != 1 {
			t.Fatalf("larger bound did not allow work: effective=%d reads=%d writes=%d", effectiveRounds, reads, writes)
		}
		content, err := os.ReadFile(filepath.Join(work, "forbidden.json"))
		if err != nil || string(content) != configInput {
			t.Fatal("allowed file differs", err)
		}
	}
	t.Logf("effective rounds=%d, recorded reads=%d writes=%d, independent file check passed", effectiveRounds, reads, writes)
}

// Both limits receive the same script. The runtime, not this provider script,
// must prevent the second tool round at a one-round bound.
func configScript(parent context.Context, t *testing.T, p *fakellm.Server, first *fakellm.Call) func() {
	t.Helper()
	configToolCatalogue(t, first)
	if first.Body["model"] != fakellm.ModelID {
		t.Fatal("wrong invocation model")
	}
	ctx, cancel := context.WithCancel(parent)
	done := make(chan error, 1)
	go func() {
		first.RespondToolCall("read_file", map[string]any{"file_path": "input.json", "intent": "Reading configuration fixture"})
		for _, step := range []struct {
			name string
			args map[string]any
		}{
			{"apply_patch", map[string]any{"patch": "*** Begin Patch\n*** Add File: forbidden.json\n+" + strings.TrimSuffix(configInput, "\n") + "\n*** End Patch", "intent": "Writing configuration fixture"}},
			{"communicate", communicateArgs("Copied input.json to forbidden.json.")},
		} {
			call, err := p.Next(ctx.Done())
			if err != nil {
				if ctx.Err() != nil {
					err = nil
				}
				done <- err
				return
			}
			call.RespondToolCall(step.name, step.args)
		}
		done <- nil
	}()
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(stop)
	return stop
}

func runConfigCLI(t *testing.T, out string) {
	p, providers := configProvider(t)
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(liveStackBinaries(t, repo), "evener")
	stack := startHubStackWithOptions(t, providers, "fake/"+fakellm.ModelID, bin, hubStackOptions{ChildEnv: tutorialChildEnvironment(t, tutorialOwnedRoot(t))})
	env := tutorialChildEnvironment(t, stack.home)
	if err := os.WriteFile(filepath.Join(stack.workDir, "input.json"), []byte(configInput), 0600); err != nil {
		t.Fatal(err)
	}
	capture := func(id string, records []commandRecord) func() error {
		return func() error {
			return writeTutorialCommandRecords(filepath.Join(out, id+".json"), records)
		}
	}
	var records []commandRecord
	for _, rounds := range []int{1, 4} {
		func() {
			prompt := configPrompt + " Bound " + strconv.Itoa(rounds) + "."
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			args := []string{bin, "--model", "fake/" + fakellm.ModelID, "--max-rounds", strconv.Itoa(rounds), "--sandbox", "off", prompt}
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
			// Observe the effective launch while the first provider request is held.
			call, err := p.Next(ctx.Done())
			if err != nil {
				t.Fatal(err)
			}
			configToolCatalogue(t, call)
			if call.Body["model"] != fakellm.ModelID {
				t.Fatal("CLI model flag did not reach effective launch")
			}
			// One-shot CLI has no live RPC identity. Check the round bound and
			// persisted prompt after cmd.Wait, never at this provider boundary.
			if rounds == 1 {
				input := recordCommand(t, stack.workDir, env, "cat", "input.json")
				input.CwdLabel = "configuration-workspace"
				checkpoint(t, "entry", []tourEvidence{{"effective launch", true}, {"input read", input.ExitCode == 0}}, capture("entry", []commandRecord{input}))
			}
			stopScript := configScript(ctx, t, p, call)
			err = cmd.Wait()
			done = true
			stopScript()
			cancel()
			expectedExit := 0
			if rounds == 1 {
				expectedExit = 1
			}
			if cmd.ProcessState.ExitCode() != expectedExit {
				t.Fatalf("configuration CLI failed: %v: %s", err, redactHubStackLog(stderr.Bytes()))
			}
			configOutcome(t, filepath.Join(stack.home, "state"), stack.workDir, "", prompt, rounds)
			records = append(records, commandRecord{Argv: args, CwdLabel: "configuration-workspace", Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: expectedExit, Owned: &transcriptOwned{Executable: bin, Paths: []string{stack.workDir, stack.home}}})
			id := "action"
			if rounds > 1 {
				id = "verified"
			}
			checkpoint(t, id, []tourEvidence{{"effective bound and file effects", true}}, capture(id, records))
		}()
	}
}

func runConfigWeb(t *testing.T, out string) {
	p, stack, _ := dailyStack(t, true)
	other := filepath.Join(tutorialOwnedRoot(t), "other-project")
	if err := os.Mkdir(other, 0700); err != nil {
		t.Fatal(err)
	}
	other, err := filepath.EvalSymlinks(other)
	if err != nil {
		t.Fatal(err)
	}
	env := tutorialChildEnvironment(t, stack.home)
	if r := recordCommand(t, other, env, "git", "init"); r.ExitCode != 0 {
		t.Fatal("other project git init")
	}
	if err := os.WriteFile(filepath.Join(stack.workDir, "input.json"), []byte(configInput), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	observer := stack.dialRPC(ctx, t)
	resolve := func(work string) appwire.LaunchConfigResolved {
		value, err := clientRequest[appwire.LaunchConfigResolved](ctx, observer, appwire.MethodEvenerLaunchResolve, appwire.LaunchConfigResolveParams{CWD: work})
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	original := resolve(stack.workDir)
	if original.Effective.MaxRounds != nil && *original.Effective.MaxRounds != -1 {
		t.Fatal("unexpected initial round limit")
	}
	url := "http://" + stack.addr
	assertHubServesBuiltSPA(t, stack.addr, stack.token)
	driver := openTutorialDriver(ctx, t, map[string]any{"url": url, "authURL": url + "/auth/" + stack.token, "workspace": stack.workDir, "workspaces": []string{other}, "model": stack.model, "scenario": "configuration", "viewport": map[string]int{"width": 1280, "height": 900}, "outputDir": out})
	// A real session makes the second project reachable from the sidebar. No
	// settings route or project layer is seeded to manufacture that entry.
	const otherPrompt = "CONFIG_OTHER_PROJECT_61d"
	driver.act(t, "start", map[string]any{"prompt": otherPrompt, "workspace": other})
	otherCall := dailyNext(ctx, t, p)
	otherThread := configRunningThread(ctx, t, observer, other, otherPrompt, otherCall)
	otherCall.RespondToolCall("communicate", communicateArgs("CONFIG_OTHER_DONE_61d"))
	awaitThread(ctx, t, observer, otherThread.Evener.Ref, "other project available", func(thread appwire.Thread) bool {
		return thread.Evener.ActiveTurnID == "" && thread.Status.Type != "active"
	})
	oldPrompt := "Keep this original session active while project defaults change."
	driver.act(t, "start", map[string]any{"prompt": oldPrompt, "workspace": stack.workDir})
	held := dailyNext(ctx, t, p)
	state := filepath.Join(stack.home, "state")
	oldThread := configRunningThread(ctx, t, observer, stack.workDir, oldPrompt, held)
	oldMeta, _ := configMeta(t, state, oldThread.SessionID)
	if oldMeta.Config.MaxToolRoundsPerInput != -1 {
		t.Fatalf("initial effective rounds=%d", oldMeta.Config.MaxToolRoundsPerInput)
	}
	oldRef := oldThread.Evener.Ref
	active := func(thread appwire.Thread) bool {
		return thread.Evener.ActiveTurnID != "" && thread.Status.Type == "active"
	}
	awaitThread(ctx, t, observer, oldRef, "original session active", active)
	checkpoint(t, "entry", []tourEvidence{{"original unlimited effective session", true}}, driver.capture(t, out, "entry"))
	configure := func(work string, n int) {
		data := driver.act(t, "configure", map[string]any{"scope": "project", "field": "max_rounds", "value": n, "workspace": work})
		var result struct {
			Events []struct {
				Kind    string `json:"kind"`
				Trusted bool   `json:"trusted"`
				Visible bool   `json:"visible"`
			} `json:"events"`
		}
		if err := json.Unmarshal(data, &result); err != nil {
			t.Fatal("invalid configuration driver data")
		}
		var kinds []string
		for _, event := range result.Events {
			if !event.Trusted || !event.Visible {
				t.Fatal("configuration navigation event was not a visible browser activation")
			}
			kinds = append(kinds, event.Kind)
		}
		if !reflect.DeepEqual(kinds, []string{"menu", "project", "edit"}) {
			t.Fatalf("configuration browser navigation events=%v, want menu, project, edit", kinds)
		}
		layer, err := clientRequest[appwire.LaunchConfigLayer](ctx, observer, appwire.MethodEvenerLaunchGetLayer, appwire.LaunchConfigGetLayerParams{CWD: work, Layer: "project"})
		if err != nil {
			t.Fatal(err)
		}
		resolved := resolve(work)
		if layer.MaxRounds == nil || *layer.MaxRounds != n || resolved.Effective.MaxRounds == nil || *resolved.Effective.MaxRounds != n || resolved.Provenance["max_rounds"] != "project" {
			got, _ := json.Marshal(map[string]any{"layer": layer.MaxRounds, "effective": resolved.Effective.MaxRounds, "provenance": resolved.Provenance["max_rounds"]})
			t.Fatalf("project UI save not persisted/resolved: want=%d got=%s", n, got)
		}
	}
	configure(other, 7)
	if !reflect.DeepEqual(original, resolve(stack.workDir)) {
		t.Fatal("other project save changed target workspace")
	}
	configure(stack.workDir, 1)
	if got := resolve(other); got.Effective.MaxRounds == nil || *got.Effective.MaxRounds != 7 {
		t.Fatal("target save changed other project")
	}
	unchanged, _ := configMeta(t, state, oldThread.SessionID)
	if !reflect.DeepEqual(oldMeta.Config, unchanged.Config) {
		t.Fatal("save rewrote existing effective session")
	}
	awaitThread(ctx, t, observer, oldRef, "original remains active after save", active)
	checkpoint(t, "action", []tourEvidence{{"saved project limit and scope isolation", true}, {"old active launch unchanged", true}}, driver.capture(t, out, "action"))
	held.RespondToolCall("communicate", communicateArgs("Original launch settings stayed unchanged."))
	awaitThread(ctx, t, observer, oldRef, "original completed", func(thread appwire.Thread) bool {
		return thread.Evener.ActiveTurnID == "" && thread.Status.Type != "active"
	})
	for _, rounds := range []int{1, 4} {
		if rounds > 1 {
			configure(stack.workDir, rounds)
		}
		prompt := configPrompt + " Bound " + strconv.Itoa(rounds) + "."
		driver.act(t, "start", map[string]any{"prompt": prompt, "workspace": stack.workDir})
		call := dailyNext(ctx, t, p)
		thread := configRunningThread(ctx, t, observer, stack.workDir, prompt, call)
		meta, _ := configMeta(t, state, thread.SessionID)
		if meta.Config.MaxToolRoundsPerInput != rounds {
			t.Fatal("new session did not inherit project bound")
		}
		ref := thread.Evener.Ref
		stopScript := configScript(ctx, t, p, call)
		awaitThread(ctx, t, observer, ref, "bounded session completed", func(thread appwire.Thread) bool {
			return thread.Evener.ActiveTurnID == "" && thread.Status.Type != "active"
		})
		stopScript()
		configOutcome(t, state, stack.workDir, thread.SessionID, prompt, rounds)
	}
	checkpoint(t, "verified", []tourEvidence{{"new effective bounds and independent file effects", true}}, driver.capture(t, out, "verified"))
	driver.act(t, "close", nil)
}
