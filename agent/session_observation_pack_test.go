package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/llm"
)

// obsPackBody builds a deterministic oversized tool-result payload: well over
// the 10 KiB packing threshold, line-oriented, with sentinel first and last
// lines the assertions target by value. Fill lines stay under the excerpt
// line cap so the head and tail excerpts survive verbatim in a packed view.
func obsPackBody() string {
	var b strings.Builder
	b.WriteString("OBS-PACK-HEAD-SENTINEL\n")
	for i := 1; i <= 298; i++ {
		b.WriteString("obs-pack-fill-line-")
		b.WriteString(strconv.Itoa(i))
		b.WriteString("-abcdefghijklmnopqrstuvwxyz0123456789\n")
	}
	b.WriteString("OBS-PACK-TAIL-SENTINEL\n")
	return b.String()
}

// obsPackSession builds a session over the scripted openai adapter whose
// steps are fully under the test's control, with observation packing enabled
// and a StateDir so the durable transcript can be inspected.
func obsPackSession(t *testing.T, steps []func(req llm.Request) llm.Response) (*Session, *fakeAdapter) {
	t.Helper()
	adapter := &fakeAdapter{name: "openai", steps: steps}
	client := llm.NewClient()
	client.Register(adapter)
	sess := newSession(t,
		withClient(client),
		withConfig(SessionConfig{
			StateDir:           newBucket(t),
			ObservationPacking: true,
			MaxSubagentDepth:   1,
			NoProjectPrompts:   true,
			testOnly: testConfig{
				skipGitSnapshot:     true,
				minimalSystemPrompt: true,
				noSyncJobStore:      true,
			},
		}),
	)
	drainSessionEvents(sess)
	return sess, adapter
}

// obsPackCallStep scripts one assistant tool call.
func obsPackCallStep(callID, tool, args string) func(llm.Request) llm.Response {
	return func(llm.Request) llm.Response {
		return llm.Response{Message: llm.Message{
			Role: llm.RoleAssistant,
			Content: []llm.ContentPart{
				{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: callID, Name: tool, Arguments: []byte(args)}},
			},
		}}
	}
}

// obsPackResult returns the string content carried for callID in req.
func obsPackResult(req llm.Request, callID string) (string, bool) {
	for _, msg := range req.Messages {
		for _, part := range msg.Content {
			if part.Kind != llm.ContentToolResult || part.ToolResult == nil || part.ToolResult.ToolCallID != callID {
				continue
			}
			if s, ok := part.ToolResult.Content.(string); ok {
				return s, true
			}
			return "", true
		}
	}
	return "", false
}

func obsPackMustResult(t *testing.T, req llm.Request, callID, label string) string {
	t.Helper()
	content, ok := obsPackResult(req, callID)
	if !ok {
		t.Fatalf("request %s carries no tool result for %s", label, callID)
	}
	return content
}

// obsPackArtifactRef extracts the artifact handle from a packed view.
func obsPackArtifactRef(t *testing.T, view string) string {
	t.Helper()
	match := regexp.MustCompile(`artifact:[0-9a-f]{32}`).FindString(view)
	if match == "" {
		t.Fatalf("packed view carries no artifact handle:\n%s", view)
	}
	return match
}

func obsPackRun(t *testing.T, sess *Session) {
	t.Helper()
	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := sess.ProcessInput(ctx, "work", nil)
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if out != "done" {
		t.Fatalf("ProcessInput output = %q, want %q", out, "done")
	}
}

func TestObservationExcerpts_HeadAndTailLines(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	b.WriteString("head-0\n")
	for i := 1; i <= 28; i++ {
		b.WriteString("fill-")
		b.WriteString(strconv.Itoa(i))
		b.WriteString("\n")
	}
	b.WriteString("tail-29\n")

	head, tail, lines := observationExcerpts(b.String())
	if lines != 30 {
		t.Fatalf("lines = %d, want 30", lines)
	}
	if len(head) != observationPackExcerptLines || len(tail) != observationPackExcerptLines {
		t.Fatalf("excerpt sizes = %d/%d, want %d/%d", len(head), len(tail), observationPackExcerptLines, observationPackExcerptLines)
	}
	if head[0] != "head-0" || head[len(head)-1] != "fill-9" {
		t.Fatalf("head = %q..%q, want head-0..fill-9", head[0], head[len(head)-1])
	}
	if tail[0] != "fill-20" || tail[len(tail)-1] != "tail-29" {
		t.Fatalf("tail = %q..%q, want fill-20..tail-29", tail[0], tail[len(tail)-1])
	}
}

func TestObservationExcerpts_LongLineCappedAndFewLinesDoNotOverlap(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("é", 600) // multibyte: the cut must land on a rune boundary
	head, tail, lines := observationExcerpts("short\n" + long + "\nmid\n" + long + "\n")
	if lines != 4 {
		t.Fatalf("lines = %d, want 4", lines)
	}
	if len(head) != 4 || len(tail) != 0 {
		t.Fatalf("head/tail = %d/%d, want 4/0 for a short line count", len(head), len(tail))
	}
	for _, view := range head {
		if len(view) > observationPackExcerptLineMax+64 {
			t.Fatalf("excerpt line not capped: %d bytes", len(view))
		}
	}
	if !strings.HasSuffix(head[1], "[... line truncated: 1200 bytes ...]") {
		t.Fatalf("long line excerpt lacks its truncation marker: %q", head[1])
	}
	// 12 lines: head 10, tail the remaining 2, no overlap, none omitted.
	var content strings.Builder
	for i := range 12 {
		content.WriteString("l")
		content.WriteString(strconv.Itoa(i))
		content.WriteString("\n")
	}
	head, tail, lines = observationExcerpts(content.String())
	if lines != 12 || len(head) != 10 || len(tail) != 2 {
		t.Fatalf("head/tail/lines = %d/%d/%d, want 10/2/12", len(head), len(tail), lines)
	}
	if head[9] != "l9" || tail[0] != "l10" || tail[1] != "l11" {
		t.Fatalf("excerpt boundary wrong: head ends %q, tail %q", head[9], tail)
	}
}

// TestObservationPack_PacksAfterTwoFullLooks is the mechanism's headline
// contract: a tool result over 10 KiB is archived to the session artifact
// store, the next two requests carry it in full, the third request carries a
// stable handle plus original size and head/tail excerpt lines instead of the
// body, and the transcript keeps the full result.
func TestObservationPack_PacksAfterTwoFullLooks(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	body := obsPackBody()
	if len(body) <= observationPackThresholdBytes {
		t.Fatalf("fixture is %d bytes, want over %d", len(body), observationPackThresholdBytes)
	}
	bigPath := filepath.Join(dir, "big.txt")
	if err := os.WriteFile(bigPath, []byte(body), 0o644); err != nil {
		t.Fatalf("write big fixture: %v", err)
	}
	smallPath := filepath.Join(dir, "small.txt")
	if err := os.WriteFile(smallPath, []byte("small ok"), 0o644); err != nil {
		t.Fatalf("write small fixture: %v", err)
	}

	sess, adapter := obsPackSession(t, []func(req llm.Request) llm.Response{
		obsPackCallStep("call_big", "read_file", `{"file_path": "`+bigPath+`"}`),
		obsPackCallStep("call_small", "read_file", `{"file_path": "`+smallPath+`"}`),
		obsPackCallStep("call_s1", "shell", `{"command": "echo one"}`),
		obsPackCallStep("call_s2", "shell", `{"command": "echo two"}`),
		func(llm.Request) llm.Response { return finalResponse("done") },
	})
	obsPackRun(t, sess)

	reqs := adapter.Requests()
	if len(reqs) != 5 {
		t.Fatalf("scripted adapter saw %d requests, want 5", len(reqs))
	}
	// The first request that carries the big result (the round after the tool
	// ran) defines the full body; the next two requests are the two full
	// looks the mechanism owes the model.
	full := obsPackMustResult(t, reqs[1], "call_big", "1")
	if len(full) <= observationPackThresholdBytes {
		t.Fatalf("full result is %d bytes, want over the threshold", len(full))
	}
	if got := obsPackMustResult(t, reqs[2], "call_big", "2"); got != full {
		t.Fatalf("second full look differs from the first")
	}

	view3 := obsPackMustResult(t, reqs[3], "call_big", "3")
	if view3 == full || strings.Contains(view3, "obs-pack-fill-line-150") {
		t.Fatalf("third request still carries the full body (%d bytes)", len(view3))
	}
	ref := obsPackArtifactRef(t, view3)
	if !strings.Contains(view3, strconv.Itoa(len(full))) {
		t.Fatalf("packed view omits the original size %d:\n%s", len(full), view3)
	}
	if !strings.Contains(view3, "OBS-PACK-HEAD-SENTINEL") || !strings.Contains(view3, "OBS-PACK-TAIL-SENTINEL") {
		t.Fatalf("packed view omits head/tail excerpt lines:\n%s", view3)
	}
	if !strings.Contains(view3, `read_transcript(transcript_ref="`+ref+`")`) {
		t.Fatalf("packed view lacks the retrieval instruction for %s:\n%s", ref, view3)
	}
	if len(view3) >= len(full) {
		t.Fatalf("packed view is %d bytes, not smaller than the %d-byte original", len(view3), len(full))
	}
	// The handle is stable: later requests render the same view.
	view4 := obsPackMustResult(t, reqs[4], "call_big", "4")
	if view4 != view3 {
		t.Fatalf("fourth request rendered a different packed view")
	}

	// The handle retrieves the original on demand through the session's
	// artifact store — the same store read_transcript's artifact path opens.
	reader, err := sess.artifactStore.Open(ref)
	if err != nil {
		t.Fatalf("open archived observation %s: %v", ref, err)
	}
	archived, err := os.ReadFile(reader.Name())
	if err != nil {
		t.Fatalf("read archived observation: %v", err)
	}
	_ = reader.Close()
	if string(archived) != full {
		t.Fatalf("archived content differs from the full result: %d vs %d bytes", len(archived), len(full))
	}

	// The transcript stays complete: the recorded turn keeps the full body.
	transcriptPath := sess.TranscriptPath()
	sess.Close()
	_, entries, _, err := readTranscript(transcriptPath)
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	persisted, ok := findToolResultInEntries(entries, "call_big")
	if !ok {
		t.Fatal("transcript omitted the oversized tool result")
	}
	persistedContent, ok := persisted.Content.(string)
	if !ok || persistedContent != full {
		t.Fatalf("transcript result = %d-byte %T, want the full %d-byte body", len(persistedContent), persisted.Content, len(full))
	}
}

// TestObservationPack_HandleRetrievesOriginal proves the packed view's handle
// round-trips through the real artifact-read machinery: the model calls
// read_transcript with the handle from the packed view, and the next request
// carries the full original content back.
func TestObservationPack_HandleRetrievesOriginal(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	body := obsPackBody()
	bigPath := filepath.Join(dir, "big.txt")
	if err := os.WriteFile(bigPath, []byte(body), 0o644); err != nil {
		t.Fatalf("write big fixture: %v", err)
	}

	sess, adapter := obsPackSession(t, []func(req llm.Request) llm.Response{
		obsPackCallStep("call_big", "read_file", `{"file_path": "`+bigPath+`"}`),
		obsPackCallStep("call_s1", "shell", `{"command": "echo one"}`),
		obsPackCallStep("call_s2", "shell", `{"command": "echo two"}`),
		func(req llm.Request) llm.Response {
			view, ok := obsPackResult(req, "call_big")
			if !ok || view == "" {
				t.Fatalf("round-3 request carries no big result to recover the handle from")
			}
			ref := obsPackArtifactRef(t, view)
			return obsPackCallStep("call_read", "read_transcript", `{"transcript_ref": "`+ref+`"}`)(req)
		},
		func(llm.Request) llm.Response { return finalResponse("done") },
	})
	obsPackRun(t, sess)

	reqs := adapter.Requests()
	if len(reqs) != 5 {
		t.Fatalf("scripted adapter saw %d requests, want 5", len(reqs))
	}
	full := obsPackMustResult(t, reqs[1], "call_big", "1")
	recovered := obsPackMustResult(t, reqs[4], "call_read", "4")
	// Assert on the decoded page envelope rather than its rendering: the
	// tool's output formatting is not this mechanism's contract.
	var envelope struct {
		Page struct {
			TotalBytes    int64  `json:"total_bytes"`
			BytesReturned int64  `json:"bytes_returned"`
			Data          string `json:"data"`
		} `json:"page"`
	}
	if err := json.Unmarshal([]byte(recovered), &envelope); err != nil {
		t.Fatalf("retrieved result is not a page envelope: %v", err)
	}
	if envelope.Page.TotalBytes != int64(len(full)) {
		t.Fatalf("retrieved page reports total_bytes %d, want the original size %d", envelope.Page.TotalBytes, len(full))
	}
	if !strings.Contains(envelope.Page.Data, "OBS-PACK-HEAD-SENTINEL") {
		t.Fatalf("retrieved page omits the original head content")
	}
}

// TestObservationPack_SmallResultsPassThrough pins that results at or under
// the threshold never pack: identical bytes in every request, no archive.
func TestObservationPack_SmallResultsPassThrough(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	smallPath := filepath.Join(dir, "small.txt")
	if err := os.WriteFile(smallPath, []byte("small ok"), 0o644); err != nil {
		t.Fatalf("write small fixture: %v", err)
	}

	sess, adapter := obsPackSession(t, []func(req llm.Request) llm.Response{
		obsPackCallStep("call_small", "read_file", `{"file_path": "`+smallPath+`"}`),
		obsPackCallStep("call_s1", "shell", `{"command": "echo one"}`),
		obsPackCallStep("call_s2", "shell", `{"command": "echo two"}`),
		obsPackCallStep("call_s3", "shell", `{"command": "echo three"}`),
		func(llm.Request) llm.Response { return finalResponse("done") },
	})
	obsPackRun(t, sess)

	reqs := adapter.Requests()
	if len(reqs) != 5 {
		t.Fatalf("scripted adapter saw %d requests, want 5", len(reqs))
	}
	first := obsPackMustResult(t, reqs[1], "call_small", "1")
	if strings.Contains(first, "artifact:") {
		t.Fatalf("small result gained an artifact handle:\n%s", first)
	}
	for i := 2; i < len(reqs); i++ {
		if got := obsPackMustResult(t, reqs[i], "call_small", strconv.Itoa(i)); got != first {
			t.Fatalf("request %d changed the small result", i)
		}
	}
	sess.obsPack.mu.Lock()
	packed := len(sess.obsPack.entries)
	sess.obsPack.mu.Unlock()
	if packed != 0 {
		t.Fatalf("%d results registered for packing, want 0", packed)
	}
}

// TestObservationPack_DisabledLeavesRequestsUnchanged pins the off-by-default
// contract: with the flag unset every request carries the oversized result in
// full and nothing is archived.
func TestObservationPack_DisabledLeavesRequestsUnchanged(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	body := obsPackBody()
	bigPath := filepath.Join(dir, "big.txt")
	if err := os.WriteFile(bigPath, []byte(body), 0o644); err != nil {
		t.Fatalf("write big fixture: %v", err)
	}

	adapter := &fakeAdapter{name: "openai", steps: []func(req llm.Request) llm.Response{
		obsPackCallStep("call_big", "read_file", `{"file_path": "`+bigPath+`"}`),
		obsPackCallStep("call_s1", "shell", `{"command": "echo one"}`),
		obsPackCallStep("call_s2", "shell", `{"command": "echo two"}`),
		obsPackCallStep("call_s3", "shell", `{"command": "echo three"}`),
		func(llm.Request) llm.Response { return finalResponse("done") },
	}}
	client := llm.NewClient()
	client.Register(adapter)
	sess := newSession(t,
		withClient(client),
		withConfig(SessionConfig{
			MaxSubagentDepth: 1,
			NoProjectPrompts: true,
			testOnly: testConfig{
				skipGitSnapshot:     true,
				minimalSystemPrompt: true,
				noSyncJobStore:      true,
			},
		}),
	)
	drainSessionEvents(sess)
	obsPackRun(t, sess)

	reqs := adapter.Requests()
	if len(reqs) != 5 {
		t.Fatalf("scripted adapter saw %d requests, want 5", len(reqs))
	}
	first := obsPackMustResult(t, reqs[1], "call_big", "1")
	if len(first) <= observationPackThresholdBytes {
		t.Fatalf("fixture result is %d bytes, want over the threshold", len(first))
	}
	for i := 2; i < len(reqs); i++ {
		if got := obsPackMustResult(t, reqs[i], "call_big", strconv.Itoa(i)); got != first {
			t.Fatalf("request %d changed the oversized result with packing disabled", i)
		}
	}
	sess.obsPack.mu.Lock()
	packed := len(sess.obsPack.entries)
	sess.obsPack.mu.Unlock()
	if packed != 0 {
		t.Fatalf("%d results registered for packing with the flag off, want 0", packed)
	}
}

// TestObservationPack_ArchiveFailureStaysFull pins the degradation contract:
// when the artifact store cannot archive a result, it replays in full on every
// request and is marked unpackable rather than half-packed.
func TestObservationPack_ArchiveFailureStaysFull(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	body := obsPackBody()
	bigPath := filepath.Join(dir, "big.txt")
	if err := os.WriteFile(bigPath, []byte(body), 0o644); err != nil {
		t.Fatalf("write big fixture: %v", err)
	}

	store := &fakeArtifactStore{ref: "artifact:abc", putErr: errObsPackTestArchive}
	adapter := &fakeAdapter{name: "openai", steps: []func(req llm.Request) llm.Response{
		obsPackCallStep("call_big", "read_file", `{"file_path": "`+bigPath+`"}`),
		obsPackCallStep("call_s1", "shell", `{"command": "echo one"}`),
		obsPackCallStep("call_s2", "shell", `{"command": "echo two"}`),
		obsPackCallStep("call_s3", "shell", `{"command": "echo three"}`),
		func(llm.Request) llm.Response { return finalResponse("done") },
	}}
	client := llm.NewClient()
	client.Register(adapter)
	sess := newSession(t,
		withClient(client),
		withConfig(SessionConfig{
			ObservationPacking: true,
			artifactStore:      store,
			MaxSubagentDepth:   1,
			NoProjectPrompts:   true,
			testOnly: testConfig{
				skipGitSnapshot:     true,
				minimalSystemPrompt: true,
				noSyncJobStore:      true,
			},
		}),
	)
	drainSessionEvents(sess)
	obsPackRun(t, sess)

	reqs := adapter.Requests()
	if len(reqs) != 5 {
		t.Fatalf("scripted adapter saw %d requests, want 5", len(reqs))
	}
	first := obsPackMustResult(t, reqs[1], "call_big", "1")
	for i := 2; i < len(reqs); i++ {
		if got := obsPackMustResult(t, reqs[i], "call_big", strconv.Itoa(i)); got != first {
			t.Fatalf("request %d packed a result whose archive failed", i)
		}
	}
	if len(store.puts) != 1 {
		t.Fatalf("archive attempted %d times, want exactly one attempt", len(store.puts))
	}
	sess.obsPack.mu.Lock()
	entry := sess.obsPack.entries["call_big"]
	sess.obsPack.mu.Unlock()
	if entry == nil || !entry.unpackable {
		t.Fatalf("result not marked unpackable after archive failure: %+v", entry)
	}
}

// TestObservationPack_SnapshotRoundTrip pins that the flag rides the
// toSnapshot/configFromSnapshot converter pair, so child sessions built from
// the parent's snapshot and sessions restored from meta.json inherit it.
func TestObservationPack_SnapshotRoundTrip(t *testing.T) {
	t.Parallel()
	in := SessionConfig{ObservationPacking: true}
	out := configFromSnapshot(in.toSnapshot().Clone())
	if !out.ObservationPacking {
		t.Fatal("ObservationPacking did not survive the snapshot round trip: delegates and restored sessions would silently run unpacked")
	}
	off := SessionConfig{}
	if configFromSnapshot(off.toSnapshot()).ObservationPacking {
		t.Fatal("default-off flag turned on by the snapshot round trip")
	}
}

var errObsPackTestArchive = errors.New("archive unavailable (test)")
