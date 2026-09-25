package hub

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/transcriptindex"
	"primeradiant.com/evener/llm"
)

// seedIndexedPastSession saves a daemonless session whose transcript holds
// entries, and returns the hub config that finds it and the transcript path.
func seedIndexedPastSession(t *testing.T, entries ...schema.Turn) (hubcore.WebConfig, hubcore.PastEntry, string) {
	t.Helper()
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-indexed-0000000000")
	sessionID := "02wMz5Txv5aIxgf9yVdd0N"
	if err := os.MkdirAll(filepath.Join(stateDir, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1700000000, 0).UTC()
	if err := schema.SaveSessionMeta(stateDir, schema.SessionMeta{
		ID: sessionID, ProfileID: "openai", Model: "gpt-5",
		EnvInfo: schema.EnvironmentInfo{WorkingDir: t.TempDir()}, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(stateDir, "sessions", sessionID+".transcript.jsonl")
	writer, err := transcript.NewWriter(path, transcript.Header{SessionID: sessionID, CreatedAt: now, ProfileID: "openai", Model: "gpt-5"})
	if err != nil {
		t.Fatal(err)
	}
	writer.SyncInterval = time.Hour
	for _, entry := range entries {
		if err := writer.Append(entry); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	index := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := index.Rebuild(); err != nil {
		t.Fatal(err)
	}
	entry, ok := index.Find(sessionID)
	if !ok {
		t.Fatal("seeded past session not found")
	}
	return hubcore.WebConfig{Past: index}, entry, path
}

// appendTranscriptEntries appends entries to a closed transcript, as the
// session's process would while the hub is not watching.
func appendTranscriptEntries(t *testing.T, path string, entries ...schema.Turn) {
	t.Helper()
	writer, err := transcript.OpenWriter(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if err := writer.Append(entry); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
}

// execution stamps entry as part of the new-format execution turn turnID.
func execution(turnID string, entry schema.Turn) schema.Turn {
	entry.Format = schema.TurnFormatIdentity
	entry.TurnID = turnID
	entry.TurnKind = schema.TurnSpanExecution
	return entry
}

func completedExecution(turnID string) schema.Turn {
	entry := schema.Turn{Kind: schema.TurnCompletion, Message: llm.Message{Role: llm.RoleUser}}
	entry.Completion = &schema.TurnCompletionInfo{Status: schema.TurnCompleted, CompletedAt: time.Unix(1700000100, 0).UTC(), DurationMS: 100}
	entry.Format = schema.TurnFormatIdentity
	entry.TurnID = turnID
	return entry
}

func toolCallEntry(turnID, callID, name, args string) schema.Turn {
	return execution(turnID, schema.Turn{Kind: schema.TurnAssistant, Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{
		Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: callID, Name: name, Arguments: json.RawMessage(args)},
	}}}})
}

func toolResultsEntry(turnID, callID, name, content string) schema.Turn {
	return execution(turnID, schema.Turn{Kind: schema.TurnToolResults, Message: llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{{
		Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: callID, Name: name, Content: content},
	}}}})
}

func dispatchDaemonlessThreadRead(t *testing.T, cfg hubcore.WebConfig, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
	t.Helper()
	server := newHubAppServer(cfg, appsource.NewRegistry())
	value, err := server.Router().Dispatch(context.Background(), appwire.Request{
		ID: appwire.NewIntID(1), Method: appwire.MethodThreadRead, Params: mustPagingJSON(t, params),
	})
	if err != nil {
		return appwire.ThreadReadResponse{}, err
	}
	response, ok := value.(appwire.ThreadReadResponse)
	if !ok {
		t.Fatalf("thread/read response = %T", value)
	}
	return response, nil
}

func indexLatestWindow(t *testing.T, path string, limit int) transcriptindex.Window {
	t.Helper()
	index, err := transcriptindex.Open(path, transcriptindex.DirFor(path))
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()
	window, err := index.Latest(limit)
	if err != nil {
		t.Fatal(err)
	}
	return window
}

func TestDaemonlessThreadReadIsTheIndexLatestWindow(t *testing.T) {
	cfg, entry, path := seedIndexedPastSession(t,
		execution("turn_m1", schema.NewTurn(schema.TurnUserInput, llm.User("first question"))),
		execution("turn_m1", schema.NewTurn(schema.TurnAssistant, llm.Assistant("first answer"))),
		completedExecution("turn_m1"),
		execution("turn_m2", schema.NewTurn(schema.TurnUserInput, llm.User("second question"))),
		execution("turn_m2", schema.NewTurn(schema.TurnAssistant, llm.Assistant("second answer"))),
		completedExecution("turn_m2"),
	)
	response, err := dispatchDaemonlessThreadRead(t, cfg, appwire.ThreadReadParams{
		Ref: "local:" + entry.Meta.ID, IncludeTurns: true, ItemLimit: 3, RequestGeneration: 7,
	})
	if err != nil {
		t.Fatalf("daemonless thread/read: %v", err)
	}
	window := indexLatestWindow(t, path, 3)
	var got []string
	for _, turn := range response.Thread.Turns {
		for _, item := range turn.Items {
			got = append(got, item.TranscriptKey)
		}
	}
	var want []string
	for _, candidate := range window.Candidates {
		want = append(want, candidate.Item.TranscriptKey)
	}
	if len(got) != 3 || !slices.Equal(got, want) {
		t.Fatalf("daemonless window keys = %v, want the index's latest window %v", got, want)
	}
	if !response.Authoritative || response.Epoch != 0 || response.BootGeneration != appwire.DaemonlessBootGeneration {
		t.Fatalf("daemonless read identity = authoritative %v epoch %d boot %q, want authoritative, epoch 0, %q",
			response.Authoritative, response.Epoch, response.BootGeneration, appwire.DaemonlessBootGeneration)
	}
	if response.Snapshot == nil || *response.Snapshot != (appwire.SnapshotIdentity{Incarnation: window.Incarnation, Length: window.Length}) {
		t.Fatalf("daemonless snapshot = %+v, want the index's {%s %d}", response.Snapshot, window.Incarnation, window.Length)
	}
	if response.RequestGeneration != 7 {
		t.Fatalf("requestGeneration = %d, want the request's 7 echoed", response.RequestGeneration)
	}
	if response.OlderCursor == "" {
		t.Fatal("a window short of the whole history carries no older cursor")
	}
	older, found, err := pastThreadTurnsList(context.Background(), cfg, appwire.ThreadTurnsListParams{Ref: "local:" + entry.Meta.ID, Cursor: response.OlderCursor, ItemLimit: 10})
	if err != nil || !found {
		t.Fatalf("daemonless backfill = (%v, %v)", found, err)
	}
	if !older.Authoritative || older.BootGeneration != appwire.DaemonlessBootGeneration || older.Snapshot == nil || *older.Snapshot != *response.Snapshot {
		t.Fatalf("daemonless backfill identity = %+v, want authoritative, daemonless, snapshot %+v", older, *response.Snapshot)
	}
	if items := flattenTestItems(older.Data); len(items) != 1 || items[0].Text != "first question" {
		t.Fatalf("daemonless backfill items = %+v, want the one item before the window", items)
	}
}

// TestDaemonlessClientHoldingPageWhenToolResultsLands is the spec's daemonless
// held-page boundary test: a client read the page holding a tool call before
// its TOOL_RESULTS was recorded, and a later latest-window read whose window no
// longer covers the call still brings the completed call back, through the
// index's update log.
func TestDaemonlessClientHoldingPageWhenToolResultsLands(t *testing.T) {
	cfg, entry, path := seedIndexedPastSession(t,
		execution("turn_m1", schema.NewTurn(schema.TurnUserInput, llm.User("list it"))),
		toolCallEntry("turn_m1", "call_ls", "shell", `{"cmd":"ls"}`),
	)
	ref := "local:" + entry.Meta.ID
	held, err := dispatchDaemonlessThreadRead(t, cfg, appwire.ThreadReadParams{Ref: ref, IncludeTurns: true, ItemLimit: 10, RequestGeneration: 1})
	if err != nil {
		t.Fatalf("first read: %v", err)
	}
	if held.Snapshot == nil {
		t.Fatal("first read carries no snapshot")
	}
	appendTranscriptEntries(t, path,
		toolResultsEntry("turn_m1", "call_ls", "shell", "a.txt"),
		execution("turn_m1", schema.NewTurn(schema.TurnAssistant, llm.Assistant("one file"))),
		completedExecution("turn_m1"),
	)

	later, err := dispatchDaemonlessThreadRead(t, cfg, appwire.ThreadReadParams{
		Ref: ref, IncludeTurns: true, ItemLimit: 1, RequestGeneration: 2, HeldSnapshot: held.Snapshot,
	})
	if err != nil {
		t.Fatalf("later read: %v", err)
	}
	if items := flattenTestItems(later.Thread.Turns); len(items) != 1 || items[0].Text != "one file" {
		t.Fatalf("later window = %+v, want only the final answer", items)
	}
	if later.Changes == nil {
		t.Fatal("a read holding the earlier snapshot got no changes")
	}
	var call *appwire.ThreadItem
	for i, item := range later.Changes.Items {
		if item.CallID == "call_ls" {
			call = &later.Changes.Items[i]
		}
		if item.Text == "one file" {
			t.Fatalf("changes repeat the window's item %+v", item)
		}
	}
	if call == nil || call.Status != appwire.TurnStatusCompleted || call.Output != "a.txt" {
		t.Fatalf("changes items = %+v, want the completed call_ls", later.Changes.Items)
	}
	for _, turn := range later.Changes.Turns {
		if turn.ID == "turn_m1" {
			t.Fatalf("changes repeat the window's turn %+v", turn)
		}
	}

	// A held snapshot the index can answer for gets changes even when nothing
	// changed: present and empty is a merge, absent is a full replacement.
	unchanged, err := dispatchDaemonlessThreadRead(t, cfg, appwire.ThreadReadParams{
		Ref: ref, IncludeTurns: true, ItemLimit: 1, RequestGeneration: 3, HeldSnapshot: later.Snapshot,
	})
	if err != nil {
		t.Fatalf("read holding the current snapshot: %v", err)
	}
	if unchanged.Changes == nil || len(unchanged.Changes.Items) != 0 || len(unchanged.Changes.Turns) != 0 {
		t.Fatalf("read holding the current snapshot got changes %+v, want present and empty", unchanged.Changes)
	}

	// A held snapshot of another incarnation gets a full replacement, no deltas.
	other := appwire.SnapshotIdentity{Incarnation: "another-incarnation", Length: held.Snapshot.Length}
	replaced, err := dispatchDaemonlessThreadRead(t, cfg, appwire.ThreadReadParams{
		Ref: ref, IncludeTurns: true, ItemLimit: 1, RequestGeneration: 4, HeldSnapshot: &other,
	})
	if err != nil {
		t.Fatalf("read holding another incarnation: %v", err)
	}
	if replaced.Changes != nil {
		t.Fatalf("read holding another incarnation got changes %+v, want none", replaced.Changes)
	}
}

// TestDaemonlessOpenExecutionReadsInProgressWithNoRunningTurn pins the read of
// a turn whose daemon died mid-execution: the turn has no completion, so it is
// still inProgress, but nothing runs it, so the thread names no running turn.
func TestDaemonlessOpenExecutionReadsInProgressWithNoRunningTurn(t *testing.T) {
	cfg, entry, _ := seedIndexedPastSession(t,
		execution("turn_m1", schema.NewTurn(schema.TurnUserInput, llm.User("start"))),
		execution("turn_m1", schema.NewTurn(schema.TurnAssistant, llm.Assistant("working on it"))),
	)
	response, err := dispatchDaemonlessThreadRead(t, cfg, appwire.ThreadReadParams{Ref: "local:" + entry.Meta.ID, IncludeTurns: true})
	if err != nil {
		t.Fatalf("daemonless thread/read: %v", err)
	}
	if len(response.Thread.Turns) != 1 || response.Thread.Turns[0].Status != appwire.TurnStatusInProgress {
		t.Fatalf("turns = %+v, want the one open turn inProgress", response.Thread.Turns)
	}
	if response.Thread.Evener.ActiveTurnID != "" || response.Thread.Status.Type == appwire.ThreadStatusActive {
		t.Fatalf("thread status = %+v activeTurnId %q, want no running turn", response.Thread.Status, response.Thread.Evener.ActiveTurnID)
	}
}

func TestDaemonlessReadErrorCarriesTheDaemonlessGenerationAndNoSnapshot(t *testing.T) {
	cfg, entry, path := seedIndexedPastSession(t, execution("turn_m1", schema.NewTurn(schema.TurnUserInput, llm.User("start"))))
	if err := os.WriteFile(path, []byte(`{"kind":"header","format_version":1}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := dispatchDaemonlessThreadRead(t, cfg, appwire.ThreadReadParams{Ref: "local:" + entry.Meta.ID, IncludeTurns: true})
	wire, ok := errors.AsType[appwire.WireError](err)
	if !ok {
		t.Fatalf("daemonless read error = %T %v, want a WireError", err, err)
	}
	data, ok := wire.Data.(appwire.HistoryReadErrorData)
	if !ok || data.BootGeneration != appwire.DaemonlessBootGeneration || data.Epoch == nil || *data.Epoch != 0 {
		t.Fatalf("daemonless read error data = %#v, want the daemonless generation and epoch 0", wire.Data)
	}
}

func TestEnrichOutputImageNotificationCompletesHistoryAndOverlayItems(t *testing.T) {
	cwd := t.TempDir()
	png := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 'p', 'a', 'y'}
	if err := os.WriteFile(filepath.Join(cwd, "plot.png"), png, 0o644); err != nil {
		t.Fatal(err)
	}
	sessionID := "01HISTORYIMG"
	write := appwire.ThreadItem{
		Type: "commandExecution", ID: "item_write", ToolName: "write_file", CallID: "call_write",
		ArgumentsJSON: `{"file_path":"plot.png"}`, Output: "wrote", Status: appwire.TurnStatusCompleted,
	}
	wantURL := "/doc/image?session=" + sessionID + "&path=plot.png"

	history := enrichOutputImageNotification(sessionID, cwd, map[string]string{}, *appwire.NotificationMessage(appwire.NotifyHistoryUpdated, appwire.HistoryUpdatedParams{
		ThreadID: sessionID, Items: []appwire.ThreadItem{{Type: "agentMessage", ID: "item_text", Text: "hi"}, write},
	}).Notification)
	var updated appwire.HistoryUpdatedParams
	if err := json.Unmarshal(history.Params, &updated); err != nil {
		t.Fatal(err)
	}
	if imgs := updated.Items[1].OutputImages; len(imgs) != 1 || imgs[0].URL != wantURL {
		t.Fatalf("history/updated write_file images = %+v, want the file-backed %s", imgs, wantURL)
	}
	if updated.Items[0].Text != "hi" {
		t.Fatalf("history/updated other item = %+v, want it untouched", updated.Items[0])
	}

	overlay := enrichOutputImageNotification(sessionID, cwd, map[string]string{}, *appwire.NotificationMessage(appwire.NotifyOverlayUpserted, appwire.OverlayUpsertedParams{
		ThreadID: sessionID, Item: appwire.OverlayItem{Key: "tool:k", Kind: appwire.OverlayTool, Item: write},
	}).Notification)
	var upserted appwire.OverlayUpsertedParams
	if err := json.Unmarshal(overlay.Params, &upserted); err != nil {
		t.Fatal(err)
	}
	if imgs := upserted.Item.Item.OutputImages; len(imgs) != 1 || imgs[0].URL != wantURL {
		t.Fatalf("overlay/upserted write_file images = %+v, want the file-backed %s", imgs, wantURL)
	}
	if upserted.Item.Key != "tool:k" || upserted.Item.Kind != appwire.OverlayTool {
		t.Fatalf("overlay/upserted envelope = %+v, want it kept", upserted.Item)
	}
}

// TestDaemonlessSubagentPreviewReadsTheIndexNotTheWholeTranscript pins the
// preview of a finished subagent: its newest items come from the index's
// latest window, never from projecting the whole saved transcript.
func TestDaemonlessSubagentPreviewReadsTheIndexNotTheWholeTranscript(t *testing.T) {
	cfg, entry, _ := seedIndexedPastSession(t,
		execution("turn_m1", schema.NewTurn(schema.TurnUserInput, llm.User("one"))),
		execution("turn_m1", schema.NewTurn(schema.TurnAssistant, llm.Assistant("two"))),
		completedExecution("turn_m1"),
		execution("turn_m2", schema.NewTurn(schema.TurnUserInput, llm.User("three"))),
		execution("turn_m2", schema.NewTurn(schema.TurnAssistant, llm.Assistant("four"))),
		completedExecution("turn_m2"),
	)
	forbidWholeTranscriptProjection(t)

	server := newHubAppServer(cfg, appsource.NewRegistry())
	value, err := server.Router().Dispatch(context.Background(), appwire.Request{
		ID: appwire.NewIntID(1), Method: appwire.MethodEvenerSubagentPreview,
		Params: mustPagingJSON(t, appwire.EvenerSubagentPreviewParams{Ref: "local:" + entry.Meta.ID, Limit: 2}),
	})
	if err != nil {
		t.Fatalf("subagent preview: %v", err)
	}
	preview, ok := value.(appwire.EvenerSubagentPreviewResponse)
	if !ok {
		t.Fatalf("subagent preview response = %T", value)
	}
	if len(preview.Items) != 2 || preview.Items[0].Text != "three" || preview.Items[1].Text != "four" || !preview.Truncated {
		t.Fatalf("preview = %+v, want the newest two items, truncated", preview)
	}
}

// TestDaemonlessReadRoutesAPastedImage pins a pasted image on a daemonless
// read: the item carries the sha route this hub serves the image's bytes on.
func TestDaemonlessReadRoutesAPastedImage(t *testing.T) {
	png := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 'p', 'a', 's', 't', 'e'}
	cfg, entry, _ := seedIndexedPastSession(t,
		execution("turn_m1", schema.NewTurn(schema.TurnUserInput, llm.Message{Role: llm.RoleUser, Content: []llm.ContentPart{
			{Kind: llm.ContentText, Text: "look at this"},
			{Kind: llm.ContentImage, Image: &llm.ImageData{Data: png, MediaType: "image/png"}},
		}})),
	)
	response, err := dispatchDaemonlessThreadRead(t, cfg, appwire.ThreadReadParams{Ref: "local:" + entry.Meta.ID, IncludeTurns: true})
	if err != nil {
		t.Fatalf("daemonless thread/read: %v", err)
	}
	items := flattenTestItems(response.Thread.Turns)
	want := "/s/" + entry.Meta.ID + "/images/" + imageSha(png)
	if len(items) != 1 || len(items[0].Images) != 1 || items[0].Images[0].URL != want {
		t.Fatalf("items = %+v, want the pasted image routed at %s", items, want)
	}
}
