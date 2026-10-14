package hub

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/llm"
)

func TestHubHistoryContentSavedOverlapReleasesOwnership(t *testing.T) {
	// Not parallel: holds the saved-read capture seam to overlap real RPCs.
	entries := []schema.Turn{schema.NewTurn(schema.TurnUserInput, llm.User("fixture-user")), schema.NewTurn(schema.TurnAssistant, llm.Assistant("fixture-older"))}
	for range 80 {
		entries = append(entries, schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "fixture-thinking"}}}}))
	}
	entries = append(entries, schema.NewTurn(schema.TurnAssistant, llm.Assistant("fixture-latest")))
	cfg, entry, _ := seedIndexedPastSession(t, entries...)
	cfg.ResumeLocks = hubcore.NewResumeLocks()
	captured, metadataAttempted, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var firstRead, firstMetadata sync.Once
	afterSavedSubscriptionCapture = func(params appwire.ThreadReadParams) {
		if params.IncludeTurns {
			firstRead.Do(func() { close(captured); <-release })
		} else {
			firstMetadata.Do(func() { close(metadataAttempted) })
		}
	}
	t.Cleanup(func() { afterSavedSubscriptionCapture = nil })
	hub, web := newHubRPCTestServerWithWeb(t, cfg)
	t.Cleanup(hub.Close)
	client := dialHubRPC(t, hub)
	t.Cleanup(func() { _ = client.Close() })
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	if _, err := client.Initialize(ctx, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatal(err)
	}
	no := false
	options := appwire.ThreadContentParams{IncludeThinking: &no}
	params := appwire.ThreadReadParams{ThreadContentParams: options, Ref: "local:" + entry.ID, IncludeTurns: true, Subscribe: true, ItemLimit: 1}
	selected := make(chan appwire.ThreadReadResponse, 1)
	results := make(chan error, 2)
	go func() {
		r, err := client.ThreadRead(ctx, params)
		selected <- r
		results <- err
	}()
	select {
	case <-captured:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	go func() {
		_, err := client.ThreadRead(ctx, appwire.ThreadReadParams{Ref: params.Ref, Subscribe: true})
		results <- err
	}()
	select {
	case <-metadataAttempted:
	case <-ctx.Done():
		t.Fatal("metadata capture retained ownership while waiting for selected read")
	}
	close(release)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	r := <-selected
	if len(r.Thread.Turns) != 1 || len(r.Thread.Turns[0].Items) != 1 || r.Thread.Turns[0].Items[0].Type != "agentMessage" || r.OlderCursor == "" {
		t.Fatal("sparse selected read did not fill through the saved transcript")
	}
	web.appRPC.Broadcast(params.Ref, appwire.NotifyHistoryUpdated, appwire.HistoryUpdatedParams{Snapshot: appwire.SnapshotIdentity{Incarnation: "inc", Length: 2}, Items: []appwire.ThreadItem{{ID: "a", Type: "agentMessage"}, {ID: "r", Type: "reasoning"}}})
	select {
	case notification := <-client.Notifications():
		var update appwire.HistoryUpdatedParams
		if notification.Method != appwire.NotifyHistoryUpdated {
			t.Fatalf("unexpected notification %s", notification.Method)
		}
		if err := json.Unmarshal(notification.Params, &update); err != nil {
			t.Fatal(err)
		}
		if len(update.Items) != 1 || update.Snapshot.Incarnation == "inc" {
			t.Fatal("metadata replaced the selected saved subscription")
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}
