//go:build browserguard

package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubedge"
	"primeradiant.com/evener/test/e2e/fakellm"
)

// The only scripted product dependency is the external provider. Drivers press
// the shipped client actions, with faults confined to the WebSocket boundary.
func TestRetryContinuationJourney(t *testing.T) {
	requireFreshDist(t)
	if _, err := exec.LookPath("node"); err != nil {
		t.Fatal(err)
	}
	provider, err := fakellm.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(provider.Close)
	stack := startHubStack(t, provider)
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()
	client := stack.dialRPC(ctx, t)
	for _, surface := range []string{"browser", "native"} {
		for _, mode := range []string{"normal", "lost-reply", "refused"} {
			t.Run(surface+"/"+mode, func(t *testing.T) {
				ctx, cancel := context.WithCancel(ctx)
				defer cancel()
				original := "RETRY_ORIGINAL_" + surface + "_" + mode
				started, err := clientRequest[appwire.ThreadStartResponse](ctx, client, appwire.MethodThreadStart, appwire.ThreadStartParams{
					Harness: "evener", CWD: stack.workDir, Model: stack.model,
					Input:           []appwire.InputItem{{Type: "text", Text: original}},
					LaunchOverrides: &appwire.LaunchConfigLayer{Sandbox: "off"},
				})
				if err != nil {
					t.Fatal(err)
				}
				ref := started.Thread.Evener.Ref
				t.Cleanup(func() {
					shutdownCtx, stop := context.WithTimeout(context.Background(), 20*time.Second)
					defer stop()
					if _, err := clientRequest[appwire.EmptyResponse](shutdownCtx, client, appwire.MethodThreadShutdown, appwire.ThreadShutdownParams{Ref: ref}); err != nil {
						t.Error(err)
					}
				})
				first, err := provider.Next(ctx.Done())
				if err != nil {
					t.Fatal(err)
				}
				first.RespondHTTPError(http.StatusBadRequest, "retry_fixture_terminal")
				awaitThread(ctx, t, client, ref, "terminal failed turn", func(thread appwire.Thread) bool { return thread.Evener.ActiveTurnID == "" })
				failed, err := clientRequest[appwire.ThreadReadResponse](ctx, client, appwire.MethodThreadRead, appwire.ThreadReadParams{Ref: ref, IncludeTurns: true})
				if err != nil {
					t.Fatal(err)
				}
				if len(failed.Thread.Turns) != 2 || failed.Thread.Turns[1].Status != "failed" || failed.Thread.Turns[1].Error == nil {
					t.Fatal("expected the system prelude followed by a terminal provider failure")
				}
				continued := make(chan *fakellm.Call, 1)
				responderDone := make(chan struct{})
				defer func() { cancel(); <-responderDone }()
				go func() {
					defer close(responderDone)
					call, err := provider.Next(ctx.Done())
					if err != nil {
						return
					}
					continued <- call
					call.RespondToolCall("communicate", communicateArgs("RETRY_DONE_"+mode))
				}()
				dir := t.TempDir()
				fixture := map[string]any{
					"url":    hubedge.AuthURLFor("http://"+stack.addr, stack.token),
					"origin": "http://" + stack.addr, "token": stack.token,
					"artifactDir": dir, "milestonePath": filepath.Join(dir, "milestones.jsonl"),
					"scenarios": []map[string]string{{"ref": ref, "mode": mode}},
				}
				data, err := json.Marshal(fixture)
				if err != nil {
					t.Fatal(err)
				}
				driver := retryContinuationDriverCommand(ctx, "frontend/scripts/retryguard/run.mjs")
				if surface == "native" {
					fixturePath := filepath.Join(dir, "connection.json")
					if err := os.WriteFile(fixturePath, data, 0600); err != nil {
						t.Fatal(err)
					}
					driver = retryContinuationDriverCommand(ctx, "node_modules/vitest/vitest.mjs", "run", "--maxWorkers=1", "src/ConversationScreen.retryJourney.test.tsx")
					driver.Dir = "../../mobile-native"
					driver.Env = append(os.Environ(), "EVENER_RETRY_JOURNEY="+fixturePath)
				} else {
					driver.Stdin = bytes.NewReader(data)
				}
				output, err := driver.CombinedOutput()
				if err != nil {
					t.Fatalf("%s driver: %v\n%s", surface, err, output)
				}
				t.Logf("%s driver completed: %s", surface, output)
				select {
				case <-responderDone:
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
				select {
				case call := <-continued:
					texts := call.Texts()
					if !slices.Contains(texts, "user: "+original) || !slices.Contains(texts, "user: continue") {
						t.Fatalf("provider did not retain original input and receive continuation: %v", texts)
					}
					if slices.Contains(texts, "user: RETRY_UNSENT_DRAFT") {
						t.Fatal("draft reached provider")
					}
				default:
					t.Fatal("no continuation reached provider")
				}
				awaitThread(ctx, t, client, ref, "continued turn completion", func(thread appwire.Thread) bool { return thread.Evener.ActiveTurnID == "" })
				read, err := clientRequest[appwire.ThreadReadResponse](ctx, client, appwire.MethodThreadRead, appwire.ThreadReadParams{Ref: ref, IncludeTurns: true})
				if err != nil {
					t.Fatal(err)
				}
				counts := map[string]int{}
				for _, turn := range read.Thread.Turns {
					for _, item := range turn.Items {
						if item.Type == "userMessage" {
							counts[item.Text]++
						}
					}
				}
				if counts[original] != 1 || counts["continue"] != 1 || len(counts) != 2 {
					t.Fatalf("authoritative user input counts: %v", counts)
				}
				if len(read.Thread.Turns) != 3 || read.Thread.Turns[1].Status != "failed" || read.Thread.Turns[2].Status != "completed" {
					t.Fatal("expected the system prelude, failed original turn, and completed continuation")
				}
			})
		}
	}
}

func retryContinuationDriverCommand(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "node", args...)
	// Let the driver's lifecycle reap its detached browser and remove its
	// profile before the existing bounded forced-stop policy takes over.
	configureBackgroundDriverCommand(cmd)
	return cmd
}
