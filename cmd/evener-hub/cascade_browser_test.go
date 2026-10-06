//go:build browserguard

package hub

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
	"primeradiant.com/evener/agent/command"
	"primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/agent/sandbox/sandboxtest"
	"primeradiant.com/evener/agent/skill"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubedge"
	"primeradiant.com/evener/test/e2e/fakellm"
)

const cascadeBrowserTripwire = 4 * time.Minute // TRIPWIRE: bounds a broken browser journey, never releases a barrier.
const cascadePagingDelegates = 51              // Exceeds the real public default page size of 50.

// Reverting AgentsTab to openTranscript must fail this journey. Every edge and
// transcript comes from a real delegate tool through the daemon and public hub.
func TestAgentCascadeBrowser(t *testing.T) {
	if testing.Short() {
		t.Skip("browser guard requires Chrome and real daemons")
	}
	if _, err := fs.ReadFile(distFS(), "index.html"); err != nil {
		t.Fatalf("production frontend is required, run make build-web: %v", err)
	}
	if _, err := exec.LookPath("node"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), cascadeBrowserTripwire)
	defer cancel()
	provider, err := fakellm.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(provider.Close)
	stack := startHubStack(t, provider)
	client := stack.dialRPC(ctx, t)
	artifacts, err := os.MkdirTemp(sandboxtest.KeptTempDir(), "cascadeguard-browser-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if t.Failed() || os.Getenv("CASCADEGUARD_KEEP_ARTIFACTS") != "" {
			t.Logf("cascade browser evidence: %s", artifacts)
			return
		}
		if err := os.RemoveAll(artifacts); err != nil {
			t.Error(err)
		}
	})
	skillDir := filepath.Join(stack.workDir, ".agents", "skills", "cascade-source")
	if err := os.MkdirAll(skillDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: cascade-source\ndescription: Source preservation fixture\n---\nCASCADE_SKILL_SENTINEL\n"), 0600); err != nil {
		t.Fatal(err)
	}
	overlapSkillDir := filepath.Join(stack.workDir, ".agents", "skills", "cascade-overlap")
	commandDir := filepath.Join(stack.workDir, ".evener", "commands")
	for _, dir := range []string{overlapSkillDir, commandDir} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	overlapSkillFile := filepath.Join(overlapSkillDir, "SKILL.md")
	if err := os.WriteFile(overlapSkillFile, []byte("---\nname: cascade-overlap\ndescription: Cascade overlap skill fixture\n---\nCASCADE_OVERLAP_SKILL_BODY\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// The daemon scans skills under its symlink-resolved cwd; on macOS the
	// temp root /var is a symlink to /private/var, so compare against the
	// resolved file.
	resolvedOverlapSkillFile, err := filepath.EvalSymlinks(overlapSkillFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(commandDir, "cascade-overlap.md"), []byte("---\ndescription: Cascade overlap command fixture\n---\nCASCADE_OVERLAP_COMMAND_BODY args=[$ARGUMENTS]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"staged.png", "pending-success.png", "pending-failure.png", "mixed-first.png", "mixed-failure.png", "mixed-last.png"} {
		file, err := os.Create(filepath.Join(artifacts, name))
		if err != nil {
			t.Fatal(err)
		}
		input := image.NewRGBA(image.Rect(0, 0, 2, 2))
		input.SetRGBA(0, 0, color.RGBA{R: 255, A: 255})
		input.SetRGBA(1, 1, color.RGBA{G: 255, A: 255})
		encodeErr := png.Encode(file, input)
		closeErr := file.Close()
		if encodeErr != nil || closeErr != nil {
			t.Fatalf("real PNG input: encode %v, close %v", encodeErr, closeErr)
		}
	}

	// Per-role phases tolerate interleaved requests. Tool IDs preserve routing
	// when a completion notice becomes the most recent user-role message.
	scriptErr := make(chan error, 1)
	releaseProvider := make(chan struct{})
	var inputsMu sync.Mutex
	providerInputs := make(map[string]int)
	ready := make(chan struct{})
	go func() {
		phases := make(map[int]int)
		toolOwners := make(map[string]int)
		completed := make(map[int]bool)
		for {
			call, err := provider.Next(ctx.Done())
			if err != nil {
				scriptErr <- err
				return
			}
			role, err := cascadeRequestRole(call, toolOwners)
			if err != nil {
				scriptErr <- err
				return
			}
			input, err := cascadeLatestUserContent(call)
			if err != nil {
				scriptErr <- err
				return
			}
			for _, token := range []string{"CASCADE_BUSY_INPUT", "CASCADE_QUEUE_INPUT", "CASCADE_UNSENT", "CASCADE_NEWER"} {
				if strings.Contains(input, token) {
					inputsMu.Lock()
					providerInputs[token]++
					inputsMu.Unlock()
				}
			}
			mixedInput, mixedDocs, err := cascadeCurrentMixedInput(call)
			if err != nil {
				if captureErr := cascadeRecordMixedProviderCall(artifacts, call, role, mixedInput, mixedDocs); captureErr != nil {
					err = fmt.Errorf("mixed provider parse: %w, capture: %v", err, captureErr)
				}
				scriptErr <- err
				return
			}
			for _, token := range []string{"CASCADE_MIXED_INPUT", "CASCADE_OVERLAP_COMMAND_BODY"} {
				if strings.Contains(mixedInput.text(), token) {
					inputsMu.Lock()
					providerInputs[token]++
					inputsMu.Unlock()
				}
			}
			if strings.Contains(input, "CASCADE_BUSY_INPUT") {
				if role != 0 {
					scriptErr <- fmt.Errorf("source input reached role %d, want root", role)
					return
				}
				if err := os.WriteFile(filepath.Join(artifacts, "provider-held.json"), []byte(`{"role":0,"input":"CASCADE_BUSY_INPUT"}`), 0600); err != nil {
					scriptErr <- err
					return
				}
				select {
				case <-releaseProvider:
					toolOwners[call.ToolCallID()] = role
					call.RespondToolCall("communicate", communicateArgs("CASCADE_BUSY_REPLY"))
				case <-ctx.Done():
					return
				}
				continue
			}
			if strings.Contains(input, "CASCADE_QUEUE_INPUT") {
				if role != 0 {
					scriptErr <- fmt.Errorf("queued source input reached role %d, want root", role)
					return
				}
				toolOwners[call.ToolCallID()] = role
				call.RespondToolCall("communicate", communicateArgs("CASCADE_QUEUE_REPLY"))
				continue
			}
			if strings.Contains(mixedInput.text(), "CASCADE_MIXED_INPUT") {
				if err := cascadeRecordMixedProviderCall(artifacts, call, role, mixedInput, mixedDocs); err != nil {
					scriptErr <- err
					return
				}
				if err := cascadeValidateMixedInput(role, mixedInput, mixedDocs, resolvedOverlapSkillFile); err != nil {
					scriptErr <- err
					return
				}
				if !cascadeMixedAttachmentNoteValid(mixedInput) {
					scriptErr <- fmt.Errorf("mixed source input lost its two ordered persisted first/last images")
					return
				}
				toolOwners[call.ToolCallID()] = role
				call.RespondToolCall("communicate", communicateArgs("CASCADE_MIXED_REPLY"))
				continue
			}
			phase := phases[role]
			phases[role]++
			if role == 0 && phase < cascadePagingDelegates {
				toolOwners[call.ToolCallID()] = role
				call.RespondToolCall("delegate", map[string]any{
					"prompt": cascadeRoleToken(7 + phase), "name": fmt.Sprintf("cascade-page-%d", phase),
					"fork_context": false, "delegation_allowance": 0,
					"intent": "Creating real direct delegates beyond the default page boundary",
				})
				continue
			}
			chainPhase := 0
			if role == 0 {
				chainPhase = cascadePagingDelegates
			}
			if role < 6 && phase == chainPhase {
				toolOwners[call.ToolCallID()] = role
				call.RespondToolCall("delegate", map[string]any{
					"prompt": cascadeRoleToken(role + 1), "name": fmt.Sprintf("cascade-%d", role+1),
					"fork_context": false, "delegation_allowance": 5 - role,
					"intent": "Creating the next real cascade browser scope",
				})
				continue
			}
			text := cascadeRoleToken(role)
			if role <= 6 {
				text += "\n\n" + strings.Repeat(fmt.Sprintf("Scope %d retained transcript paragraph.\n\n", role), 100)
			}
			toolOwners[call.ToolCallID()] = role
			call.RespondToolCall("communicate", communicateArgs(text))
			if !completed[role] {
				completed[role] = true
				if len(completed) == 7+cascadePagingDelegates {
					close(ready)
				}
			}
		}
	}()
	depth := 6
	started, err := client.ThreadStart(ctx, appwire.ThreadStartParams{
		Harness: "evener", CWD: stack.workDir, Model: stack.model,
		Input:           []appwire.InputItem{{Type: "text", Text: cascadeRoleToken(0)}},
		LaunchOverrides: &appwire.LaunchConfigLayer{Sandbox: "off", MaxSubagentDepth: &depth},
	})
	if err != nil {
		t.Fatal(err)
	}
	rootRef := threadRef(started.Thread)
	t.Cleanup(func() {
		cleanupCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		_ = client.ThreadShutdown(cleanupCtx, appwire.ThreadShutdownParams{Ref: rootRef})
	})
	select {
	case <-ready:
	case err := <-scriptErr:
		t.Fatal(err)
	case <-ctx.Done():
		t.Fatal("real six-edge producer did not settle:", ctx.Err())
	}
	refs := []string{rootRef}
	edges := make([]appwire.SessionDelegate, 0, 6)
	var branch appwire.SessionDelegate
	for level := 0; level < 6; level++ {
		owner := refs[level]
		page, err := client.ThreadDelegatesList(ctx, appwire.SessionActivityListParams{Ref: owner, Scope: appwire.SessionActivityScopeSession, Limit: 200})
		if err != nil {
			t.Fatal(err)
		}
		wantRows := 1
		if level == 0 {
			wantRows += cascadePagingDelegates
		}
		if !page.Page.Complete || len(page.Delegates) != wantRows || !page.Context.AncestryKnown || page.Context.SessionID != strings.TrimPrefix(owner, "local:") {
			t.Fatalf("real direct owner %s: %+v", owner, page)
		}
		var edge appwire.SessionDelegate
		for _, candidate := range page.Delegates {
			if candidate.Name == fmt.Sprintf("cascade-%d", level+1) {
				edge = candidate
			}
			if candidate.Name == "cascade-page-0" {
				branch = candidate
			}
		}
		if edge.OwnerRef != owner || edge.RootRef != rootRef || edge.ChildRef == "" || slices.Contains(refs, edge.ChildRef) {
			t.Fatalf("invalid real edge: %+v", edge)
		}
		edges = append(edges, edge)
		refs = append(refs, edge.ChildRef)
	}
	leaf, err := client.ThreadActivityRead(ctx, appwire.SessionActivityReadParams{Ref: refs[6]})
	if err != nil {
		t.Fatal(err)
	}
	if !leaf.Context.AncestryKnown || len(leaf.Context.Ancestors) != 6 || leaf.Context.ParentRef != refs[5] {
		t.Fatalf("real leaf ancestry: %+v", leaf.Context)
	}
	for i, ancestor := range leaf.Context.Ancestors {
		if ancestor.Ref != refs[i] {
			t.Fatalf("ancestor %d = %s, want %s", i, ancestor.Ref, refs[i])
		}
	}
	fixture := map[string]any{
		"url": hubedge.AuthURLFor("http://"+stack.addr, stack.token), "artifactDir": artifacts,
		"rootRef": rootRef, "childRef": refs[1], "grandchildRef": refs[2],
		"childDelegateId": edges[0].DelegateID, "grandchildDelegateId": edges[1].DelegateID,
		"refs": refs, "edges": edges, "branch": branch, "rootDelegateCount": 1 + cascadePagingDelegates,
		"controlPath": filepath.Join(artifacts, "control.jsonl"), "milestonePath": filepath.Join(artifacts, "milestones.jsonl"),
	}
	for _, name := range []string{"control.jsonl", "milestones.jsonl"} {
		if err := os.WriteFile(filepath.Join(artifacts, name), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	if err := watcher.Add(artifacts); err != nil {
		watcher.Close()
		t.Fatal(err)
	}
	controlDone := make(chan error, 1)
	go func() {
		defer watcher.Close()
		controlDone <- cascadeAwaitProviderRelease(ctx, watcher, filepath.Join(artifacts, "control.jsonl"), releaseProvider)
	}()
	defer func() {
		cancel()
		if err := <-controlDone; err != nil {
			t.Error(err)
		}
	}()
	body, err := json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	log, err := os.Create(filepath.Join(artifacts, "driver.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	driver := exec.CommandContext(ctx, "node", "frontend/scripts/cascadeguard/run.mjs")
	driver.Stdin = bytes.NewReader(body)
	driver.Stdout, driver.Stderr = log, log
	driver.WaitDelay = 15 * time.Second
	for _, arg := range driver.Args {
		if strings.Contains(arg, stack.token) {
			t.Fatal("cascade driver exposes the fixture auth token in process arguments")
		}
	}
	if err := driver.Run(); err != nil {
		skillGuardLogDriverTail(t, log.Name())
		select {
		case providerErr := <-scriptErr:
			t.Errorf("scripted provider stopped before the driver failure: %v", providerErr)
		default:
		}
		t.Fatalf("production cascade journey: %v, evidence: %s", err, artifacts)
	}
	milestones, err := os.ReadFile(filepath.Join(artifacts, "milestones.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"root-child", "six-edges", "compact-spine-status", "narrow-independent-scroll", "parent-selection-leaf-scope",
		"peek-tasks-single-escape", "keyboard-branch-reduced-motion", "return-source",
		"reconnect-extent-closed-peek", "pending-image-success", "pending-image-failure",
		"unresolved-storage-source", "queued-source-single-delivery", "live-status-stable-geometry",
		"unrelated-pane-reload", "late-ancestry-stable-geometry", "mobile-saved-cascade", "mobile-agents-transcript",
		"mixed-mounted-image-return", "mixed-held-storage-return",
		"ordinary-reader-width-reflow", "ordinary-reader-width-return", "cascade-reader-width-reflow", "cascade-reader-width-return",
		"Shift-Space-interruption-precedence", "wheel-interruption-precedence", "pill-interruption-precedence", "phone-reader-width-reflow", "phone-reader-width-return",
	} {
		found := false
		for _, line := range strings.Split(string(milestones), "\n") {
			var value struct {
				Milestone string `json:"milestone"`
			}
			if json.Unmarshal([]byte(line), &value) == nil && value.Milestone == required {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing observed milestone %s", required)
		}
	}
	inputsMu.Lock()
	for _, token := range []string{"CASCADE_BUSY_INPUT", "CASCADE_QUEUE_INPUT", "CASCADE_UNSENT", "CASCADE_NEWER", "CASCADE_MIXED_INPUT", "CASCADE_OVERLAP_COMMAND_BODY"} {
		want := 0
		if token == "CASCADE_BUSY_INPUT" || token == "CASCADE_QUEUE_INPUT" || token == "CASCADE_MIXED_INPUT" || token == "CASCADE_OVERLAP_COMMAND_BODY" {
			want = 1
		}
		if providerInputs[token] != want {
			t.Errorf("provider deliveries of %s = %d, want %d", token, providerInputs[token], want)
		}
	}
	inputsMu.Unlock()
	transcript, err := client.ThreadRead(ctx, appwire.ThreadReadParams{Ref: rootRef, IncludeTurns: true, ItemLimit: 40})
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{"CASCADE_BUSY_INPUT", "CASCADE_QUEUE_INPUT", "CASCADE_MIXED_INPUT"} {
		count := 0
		for _, turn := range transcript.Thread.Turns {
			for _, item := range turn.Items {
				if item.Type == "userMessage" && strings.Contains(item.Text, token) {
					count++
				}
			}
		}
		if count != 1 {
			t.Errorf("public source transcript inputs for %s = %d, want 1", token, count)
		}
	}
	select {
	case err := <-scriptErr:
		t.Fatal(err)
	default:
	}
}

func cascadeRoleToken(role int) string { return fmt.Sprintf("CASCADE_ROLE_%d_SENTINEL", role) }

func cascadeRecordMixedProviderCall(artifacts string, call *fakellm.Call, role int, input skillGuardMessage, docs []skillGuardDocument) error {
	messages, _ := call.Body["messages"].([]any)
	start := len(messages)
	for start > 0 {
		message, ok := messages[start-1].(map[string]any)
		if !ok || message["role"] != "user" {
			break
		}
		start--
	}
	body, err := json.Marshal(map[string]any{
		"role": role, "input": input, "skillContexts": docs, "currentUserMessages": messages[start:],
	})
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(artifacts, "provider-mixed.json"), body, 0600)
}

func TestCascadeMixedProducerContract(t *testing.T) {
	const commandFile = "---\ndescription: Cascade overlap command fixture\n---\nCASCADE_OVERLAP_COMMAND_BODY args=[$ARGUMENTS]\n"
	parsedCommand, err := plugin.ParseCommand([]byte(commandFile), "cascade-overlap", "")
	if err != nil {
		t.Fatal(err)
	}
	expanded := command.ExpandArgs(parsedCommand.Body, "")
	if expanded != "CASCADE_OVERLAP_COMMAND_BODY args=[]\n" {
		t.Fatalf("real command producer bytes = %q", expanded)
	}
	const skillFile = "---\nname: cascade-overlap\ndescription: Cascade overlap skill fixture\n---\nCASCADE_OVERLAP_SKILL_BODY\n"
	dir := t.TempDir()
	path := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(path, []byte(skillFile), 0600); err != nil {
		t.Fatal(err)
	}
	descriptor, _, err := skill.Parse([]byte(skillFile), path)
	if err != nil {
		t.Fatal(err)
	}
	descriptor.CatalogName = "cascade-overlap"
	loaded, _, err := skill.Load(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	carrier := skill.Render(loaded).Content
	// appendSelectedCommands adds two newlines in a separate text part, and
	// the multimodal Chat Completions adapter preserves each text part.
	call := &fakellm.Call{Body: map[string]any{"messages": []any{
		map[string]any{"role": "user", "content": "HISTORICAL_NOT_CURRENT"},
		map[string]any{"role": "assistant", "content": "historical boundary"},
		map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "text", "text": "CASCADE_ROLE_0_SENTINEL CASCADE_MIXED_INPUT /cascade-overlap"},
			map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,producer-fixture"}},
			map[string]any{"type": "text", "text": "\n\n" + expanded},
		}},
		map[string]any{"role": "user", "content": carrier},
	}}}
	input, docs, err := cascadeCurrentMixedInput(call)
	if err != nil {
		t.Fatal(err)
	}
	if err := cascadeValidateMixedInput(0, input, docs, path); err != nil {
		t.Fatal(err)
	}
	if err := cascadeRecordMixedProviderCall(dir, call, 0, input, docs); err != nil {
		t.Fatal(err)
	}
	var recorded struct {
		CurrentUserMessages []any `json:"currentUserMessages"`
	}
	body, err := os.ReadFile(filepath.Join(dir, "provider-mixed.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(body, &recorded); err != nil {
		t.Fatal(err)
	}
	want, err := json.Marshal(call.Body["messages"].([]any)[2:])
	if err != nil {
		t.Fatal(err)
	}
	got, err := json.Marshal(recorded.CurrentUserMessages)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("raw current input capture = %s, want %s", got, want)
	}
}

func TestCascadeMixedProducerContractPersistedImages(t *testing.T) {
	// Operative text and path bytes captured by DIAGNOSTIC5, retained in
	// job:job_034Yk1GvDDcIfx2YJ69mZi_pAjA15faowRs. Its two image_url parts
	// precede the persistedAttachmentNote from agent/input_message.go:84-103.
	const prose = "🙂 CASCADE_ROLE_0_SENTINEL CASCADE_MIXED_INPUT (attached image 4: mixed-first.png) /cascade-overlap  /cascade-overlap (attached image 6: mixed-last.png) /cascade-overlap"
	const attachmentDir = "/tmp/evener-sandbox-2733248851/tmp/evener-hub-test-3446257873/tmp/TestAgentCascadeBrowser1100892311/001/state/evener/projects/evener-hub-test-3446257873-tmp-TestAgentCascadeBrowser1100892311-002-3gQFTj0g49/sessions/034ZKsJDaDRpxQ7GVFY67W/attachments"
	const note = "<system-notification>The images attached to this message were saved to disk and can be read again later with the read_file tool:\n" + attachmentDir + "/993d22a404947b27-mixed-first.png\n" + attachmentDir + "/993d22a404947b27-mixed-last.png</system-notification>"
	const skillFile = "/project/.agents/skills/cascade-overlap/SKILL.md"
	parsed, err := plugin.ParseCommand([]byte("---\ndescription: Cascade overlap command fixture\n---\nCASCADE_OVERLAP_COMMAND_BODY args=[$ARGUMENTS]\n"), "cascade-overlap", "")
	if err != nil {
		t.Fatal(err)
	}
	commandText := "\n\n" + command.ExpandArgs(parsed.Body, "")
	carrier := "<skill-context>\n{\"name\":\"cascade-overlap\",\"description\":\"Cascade overlap skill fixture\",\"source\":\"" + skillFile + "\",\"base_directory\":\"/project/.agents/skills/cascade-overlap\",\"instructions\":\"CASCADE_OVERLAP_SKILL_BODY\\n\"}\n</skill-context>"
	call := &fakellm.Call{Body: map[string]any{"messages": []any{
		map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "text", "text": prose},
			map[string]any{"type": "image_url"},
			map[string]any{"type": "image_url"},
			map[string]any{"type": "text", "text": note},
			map[string]any{"type": "text", "text": commandText},
		}},
		map[string]any{"role": "user", "content": carrier},
	}}}
	input, docs, err := cascadeCurrentMixedInput(call)
	if err != nil {
		t.Fatal(err)
	}
	if err := cascadeValidateMixedInput(0, input, docs, skillFile); err != nil {
		t.Fatal(err)
	}
	if !cascadeMixedAttachmentNoteValid(input) {
		t.Fatal("captured persisted-image machinery shape was rejected")
	}
	for _, tc := range []struct {
		name   string
		change func(*skillGuardMessage, *[]skillGuardDocument)
	}{
		{"unrelated inserted text", func(input *skillGuardMessage, _ *[]skillGuardDocument) { input.Content[3].Text = "unrelated prose" }},
		{"unrelated notification", func(input *skillGuardMessage, _ *[]skillGuardDocument) {
			input.Content[3].Text = "<system-notification>unrelated</system-notification>"
		}},
		{"malformed notification", func(input *skillGuardMessage, _ *[]skillGuardDocument) {
			input.Content[3].Text = strings.TrimSuffix(note, "</system-notification>")
		}},
		{"notification trailing whitespace", func(input *skillGuardMessage, _ *[]skillGuardDocument) { input.Content[3].Text += "\n" }},
		{"missing attachment note", func(input *skillGuardMessage, _ *[]skillGuardDocument) {
			input.Content = append(input.Content[:3], input.Content[4:]...)
		}},
		{"missing image part", func(input *skillGuardMessage, _ *[]skillGuardDocument) {
			input.Content = append(input.Content[:1], input.Content[2:]...)
		}},
		{"all image evidence missing", func(input *skillGuardMessage, _ *[]skillGuardDocument) {
			input.Content = []skillGuardLLMPart{input.Content[0], input.Content[4]}
		}},
		{"reversed attachment names", func(input *skillGuardMessage, _ *[]skillGuardDocument) {
			input.Content[3].Text = strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(note, "mixed-first", "temporary"), "mixed-last", "mixed-first"), "temporary", "mixed-last")
		}},
		{"failed middle image retained", func(input *skillGuardMessage, _ *[]skillGuardDocument) {
			input.Content[3].Text = strings.ReplaceAll(note, "mixed-last.png", "mixed-failure.png")
		}},
		{"unrelated saved image", func(input *skillGuardMessage, _ *[]skillGuardDocument) {
			input.Content[3].Text = strings.ReplaceAll(note, "mixed-first.png", "other.png")
		}},
		{"different attachment directories", func(input *skillGuardMessage, _ *[]skillGuardDocument) {
			input.Content[3].Text = strings.ReplaceAll(note, attachmentDir+"/993d22a404947b27-mixed-last.png", "/other/sessions/id/attachments/993d22a404947b27-mixed-last.png")
		}},
		{"malformed digest", func(input *skillGuardMessage, _ *[]skillGuardDocument) {
			input.Content[3].Text = strings.ReplaceAll(note, "993d22a404947b27", "not-a-digest")
		}},
		{"duplicate command", func(input *skillGuardMessage, _ *[]skillGuardDocument) {
			input.Content = append(input.Content, input.Content[4])
		}},
		{"nonempty command args", func(input *skillGuardMessage, _ *[]skillGuardDocument) {
			input.Content[4].Text = "\n\nCASCADE_OVERLAP_COMMAND_BODY args=[prose]\n"
		}},
		{"incorrect command whitespace", func(input *skillGuardMessage, _ *[]skillGuardDocument) {
			input.Content[4].Text = strings.TrimSuffix(commandText, "\n")
		}},
		{"duplicate skill", func(_ *skillGuardMessage, docs *[]skillGuardDocument) { *docs = append(*docs, (*docs)[0]) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			current := input
			current.Content = slices.Clone(input.Content)
			currentDocs := slices.Clone(docs)
			tc.change(&current, &currentDocs)
			if err := cascadeValidateMixedInput(0, current, currentDocs, skillFile); err == nil && cascadeMixedAttachmentNoteValid(current) {
				t.Fatal("invalid persisted-image mixed input was accepted")
			}
		})
	}
}

func TestCascadeCurrentMixedInput(t *testing.T) {
	const prose = "🙂 CASCADE_ROLE_0_SENTINEL CASCADE_MIXED_INPUT /cascade-overlap"
	const command = "\n\nCASCADE_OVERLAP_COMMAND_BODY args=[]\n"
	const carrier = "<skill-context>\n{\"name\":\"cascade-overlap\",\"description\":\"Cascade overlap skill fixture\",\"source\":\"/project/.agents/skills/cascade-overlap/SKILL.md\",\"base_directory\":\"/project/.agents/skills/cascade-overlap\",\"instructions\":\"CASCADE_OVERLAP_SKILL_BODY\\n\"}\n</skill-context>"
	user := func(content any) any { return map[string]any{"role": "user", "content": content} }
	operative := user([]any{
		map[string]any{"type": "text", "text": prose},
		map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,fixture"}},
		map[string]any{"type": "text", "text": command},
	})
	for _, tc := range []struct {
		name     string
		messages []any
		text     string
		docs     int
	}{
		{"separate current prose command and canonical skill", []any{operative, user(carrier)}, prose + command, 1},
		{"historical mixed delivery is not current", []any{operative, user(carrier), map[string]any{"role": "assistant", "content": "done"}, user("CASCADE_BUSY_INPUT")}, "CASCADE_BUSY_INPUT", 0},
		{"assistant boundary ends current input", []any{operative, user(carrier), map[string]any{"role": "assistant", "content": "done"}}, "", 0},
		{"tool boundary ends current input", []any{operative, user(carrier), map[string]any{"role": "tool", "content": "done"}}, "", 0},
		{"historical skill does not validate new input", []any{user(carrier), map[string]any{"role": "assistant", "content": "done"}, operative}, prose + command, 0},
		{"malformed context remains operative", []any{operative, user("<skill-context>{\"name\":\"cascade-overlap\"}</skill-context>")}, "<skill-context>{\"name\":\"cascade-overlap\"}</skill-context>", 0},
		{"context with prose remains operative", []any{operative, user(carrier + " prose")}, carrier + " prose", 0},
		{"context with image remains operative", []any{operative, user([]any{map[string]any{"type": "text", "text": carrier}, map[string]any{"type": "image_url"}})}, carrier, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			call := &fakellm.Call{Body: map[string]any{"messages": tc.messages}}
			input, docs, err := cascadeCurrentMixedInput(call)
			if err != nil {
				t.Fatal(err)
			}
			if input.text() != tc.text || len(docs) != tc.docs {
				t.Fatalf("current operative input = %q, skill documents = %d, want %q and %d", input.text(), len(docs), tc.text, tc.docs)
			}
			if tc.docs == 1 {
				if err := cascadeValidateMixedInput(0, input, docs, "/project/.agents/skills/cascade-overlap/SKILL.md"); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestCascadeValidateMixedInput(t *testing.T) {
	const skillFile = "/project/.agents/skills/cascade-overlap/SKILL.md"
	input := skillGuardMessage{Role: "user", Content: []skillGuardLLMPart{
		{Kind: "text", Text: "CASCADE_ROLE_0_SENTINEL CASCADE_MIXED_INPUT /cascade-overlap"},
		{Kind: "text", Text: "\n\nCASCADE_OVERLAP_COMMAND_BODY args=[]\n"},
	}}
	doc := skillGuardDocument{
		Name: "cascade-overlap", Description: "Cascade overlap skill fixture", Source: skillFile,
		BaseDirectory: filepath.Dir(skillFile), Instructions: "CASCADE_OVERLAP_SKILL_BODY\n",
	}
	if err := cascadeValidateMixedInput(0, input, []skillGuardDocument{doc}, skillFile); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		change func(*int, *skillGuardMessage, *[]skillGuardDocument)
	}{
		{"wrong recipient", func(role *int, _ *skillGuardMessage, _ *[]skillGuardDocument) { *role = 1 }},
		{"nonempty command args", func(_ *int, input *skillGuardMessage, _ *[]skillGuardDocument) {
			input.Content[1].Text = "\n\nCASCADE_OVERLAP_COMMAND_BODY args=[prose]\n"
		}},
		{"combined prose and command", func(_ *int, input *skillGuardMessage, _ *[]skillGuardDocument) {
			input.Content = []skillGuardLLMPart{{Kind: "text", Text: input.text()}}
		}},
		{"missing current skill", func(_ *int, _ *skillGuardMessage, docs *[]skillGuardDocument) { *docs = nil }},
		{"duplicate skill", func(_ *int, _ *skillGuardMessage, docs *[]skillGuardDocument) { *docs = append(*docs, doc) }},
		{"wrong skill name", func(_ *int, _ *skillGuardMessage, docs *[]skillGuardDocument) { (*docs)[0].Name = "cascade-source" }},
		{"wrong skill description", func(_ *int, _ *skillGuardMessage, docs *[]skillGuardDocument) { (*docs)[0].Description = "wrong" }},
		{"wrong skill source", func(_ *int, _ *skillGuardMessage, docs *[]skillGuardDocument) { (*docs)[0].Source = "/other/SKILL.md" }},
		{"wrong skill directory", func(_ *int, _ *skillGuardMessage, docs *[]skillGuardDocument) { (*docs)[0].BaseDirectory = "/other" }},
		{"wrong skill instructions", func(_ *int, _ *skillGuardMessage, docs *[]skillGuardDocument) { (*docs)[0].Instructions = "truncated" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			role, current, docs := 0, input, []skillGuardDocument{doc}
			current.Content = slices.Clone(input.Content)
			tc.change(&role, &current, &docs)
			if err := cascadeValidateMixedInput(role, current, docs, skillFile); err == nil {
				t.Fatal("invalid mixed provider input was accepted")
			}
		})
	}
}

func cascadeCurrentMixedInput(call *fakellm.Call) (skillGuardMessage, []skillGuardDocument, error) {
	// Only trailing user messages belong to this input. Crossing an assistant or
	// tool boundary would count retained historical deliveries again. Complete
	// selected-skill carriers follow the operative prose/command message.
	messages, _ := call.Body["messages"].([]any)
	var docs []skillGuardDocument
	for i := len(messages) - 1; i >= 0; i-- {
		message, ok := messages[i].(map[string]any)
		if !ok || message["role"] != "user" {
			break
		}
		input, err := cascadeWireUserMessage(message)
		if err != nil {
			return input, nil, err
		}
		if input.skillContextOnly() {
			carrier := skillGuardLLMCall{Messages: []skillGuardMessage{input}}
			docs = append(carrier.skillContexts(), docs...)
			continue
		}
		return input, docs, nil
	}
	return skillGuardMessage{}, nil, nil
}

func cascadeValidateMixedInput(role int, input skillGuardMessage, docs []skillGuardDocument, skillFile string) error {
	var texts []string
	images := 0
	for _, part := range input.Content {
		if part.Kind == "text" {
			texts = append(texts, part.Text)
		} else if part.Kind == "image_url" {
			images++
		}
	}
	validShape := len(texts) == 2 && images <= 1 // Existing stateless producer cases.
	if len(texts) == 3 {
		validShape = cascadeMixedAttachmentNoteValid(input)
	}
	if role != 0 || !validShape || !strings.Contains(texts[0], cascadeRoleToken(0)) ||
		!strings.Contains(texts[0], "CASCADE_MIXED_INPUT") || strings.Contains(texts[0], "CASCADE_OVERLAP_COMMAND_BODY") ||
		texts[len(texts)-1] != "\n\nCASCADE_OVERLAP_COMMAND_BODY args=[]\n" {
		return fmt.Errorf("mixed source input lost its owning role, separate prose/command parts or empty command args: role %d, text parts %q", role, texts)
	}
	want := skillGuardDocument{
		Name: "cascade-overlap", Description: "Cascade overlap skill fixture", Source: skillFile,
		BaseDirectory: filepath.Dir(skillFile), Instructions: "CASCADE_OVERLAP_SKILL_BODY\n",
	}
	if len(docs) != 1 || docs[0] != want {
		return fmt.Errorf("mixed source input lost its current canonical skill document: got %+v, want %+v", docs, want)
	}
	return nil
}

func cascadeMixedAttachmentNoteValid(input skillGuardMessage) bool {
	parts := input.Content
	if len(parts) != 5 || parts[0].Kind != "text" || parts[1].Kind != "image_url" ||
		parts[2].Kind != "image_url" || parts[3].Kind != "text" || parts[4].Kind != "text" {
		return false
	}
	const prefix = "<system-notification>The images attached to this message were saved to disk and can be read again later with the read_file tool:\n"
	note, ok := strings.CutPrefix(parts[3].Text, prefix)
	if !ok {
		return false
	}
	note, ok = strings.CutSuffix(note, "</system-notification>")
	if !ok {
		return false
	}
	paths := strings.Split(note, "\n")
	if len(paths) != 2 {
		return false
	}
	dir := filepath.Dir(paths[0])
	if filepath.Base(dir) != "attachments" || filepath.Base(filepath.Dir(filepath.Dir(dir))) != "sessions" {
		return false
	}
	var digest string
	for i, name := range []string{"mixed-first.png", "mixed-last.png"} {
		path := paths[i]
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || filepath.Dir(path) != dir {
			return false
		}
		storedDigest, ok := strings.CutSuffix(filepath.Base(path), "-"+name)
		if !ok || len(storedDigest) != 16 || strings.ToLower(storedDigest) != storedDigest {
			return false
		}
		if _, err := hex.DecodeString(storedDigest); err != nil {
			return false
		}
		// These fixture PNGs have identical pixels/bytes. The failed middle
		// image must not appear, and surviving names must keep input order.
		if i == 0 {
			digest = storedDigest
		} else if storedDigest != digest {
			return false
		}
	}
	return true
}

func cascadeWireUserMessage(message map[string]any) (skillGuardMessage, error) {
	input := skillGuardMessage{Role: "user"}
	if text, ok := message["content"].(string); ok {
		input.Content = []skillGuardLLMPart{{Kind: "text", Text: text}}
		return input, nil
	}
	content, err := json.Marshal(message["content"])
	if err != nil {
		return input, err
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(content, &parts); err != nil {
		return input, err
	}
	for _, part := range parts {
		input.Content = append(input.Content, skillGuardLLMPart{Kind: part.Type, Text: part.Text})
	}
	return input, nil
}

func cascadeLatestUserContent(call *fakellm.Call) (string, error) {
	messages, _ := call.Body["messages"].([]any)
	for i := len(messages) - 1; i >= 0; i-- {
		message, ok := messages[i].(map[string]any)
		if ok && message["role"] == "user" {
			content, err := json.Marshal(message["content"])
			return string(content), err
		}
	}
	return "", nil
}

func cascadeAwaitProviderRelease(ctx context.Context, watcher *fsnotify.Watcher, controlPath string, release chan struct{}) error {
	for {
		body, err := os.ReadFile(controlPath)
		if err != nil {
			return err
		}
		// A notification may precede the final newline of an append. Only complete
		// records can release the real provider call.
		if end := strings.LastIndexByte(string(body), '\n'); end >= 0 {
			for _, line := range strings.Split(string(body[:end]), "\n") {
				var record struct {
					Command string `json:"command"`
				}
				if err := json.Unmarshal([]byte(line), &record); err != nil {
					return fmt.Errorf("cascade control record: %w", err)
				}
				if record.Command == "release-provider" {
					close(release)
					return nil
				}
				return fmt.Errorf("unexpected cascade control command %q", record.Command)
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case err := <-watcher.Errors:
			return err
		case <-watcher.Events:
		}
	}
}

func cascadeRequestRole(call *fakellm.Call, toolOwners map[string]int) (int, error) {
	if role, ok := toolOwners[call.PreviousToolCallID()]; ok {
		return role, nil
	}
	messages, _ := call.Body["messages"].([]any)
	for i := len(messages) - 1; i >= 0; i-- {
		message, ok := messages[i].(map[string]any)
		if !ok || message["role"] != "user" {
			continue
		}
		content, err := json.Marshal(message["content"])
		if err != nil {
			return 0, err
		}
		matches := []int{}
		for role := 0; role < 7+cascadePagingDelegates; role++ {
			if strings.Contains(string(content), cascadeRoleToken(role)) {
				matches = append(matches, role)
			}
		}
		if len(matches) == 1 {
			return matches[0], nil
		}
		if len(matches) > 1 {
			return 0, fmt.Errorf("ambiguous provider user payload, roles %v", matches)
		}
	}
	return 0, fmt.Errorf("provider request has no cascade role sentinel or admitted tool identity")
}
