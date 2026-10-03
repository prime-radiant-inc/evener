//go:build browserguard

package hub

import (
	"bytes"
	"context"
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
	"primeradiant.com/evener/agent/sandbox/sandboxtest"
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
	for _, name := range []string{"staged.png", "pending-success.png", "pending-failure.png"} {
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
		t.Fatalf("production cascade journey: %v, evidence: %s", err, artifacts)
	}
	milestones, err := os.ReadFile(filepath.Join(artifacts, "milestones.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"root-child", "six-edges", "narrow-independent-scroll", "parent-selection-leaf-scope",
		"peek-tasks-single-escape", "keyboard-branch-reduced-motion", "return-source",
		"reconnect-extent-closed-peek", "pending-image-success", "pending-image-failure",
		"unresolved-storage-source", "queued-source-single-delivery", "live-status-stable-geometry",
		"unrelated-pane-reload", "late-ancestry-stable-geometry", "mobile-saved-cascade", "mobile-agents-transcript",
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
	for _, token := range []string{"CASCADE_BUSY_INPUT", "CASCADE_QUEUE_INPUT", "CASCADE_UNSENT", "CASCADE_NEWER"} {
		want := 0
		if token == "CASCADE_BUSY_INPUT" || token == "CASCADE_QUEUE_INPUT" {
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
	for _, token := range []string{"CASCADE_BUSY_INPUT", "CASCADE_QUEUE_INPUT"} {
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
