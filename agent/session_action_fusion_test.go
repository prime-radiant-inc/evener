package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

// Action Fusion tests (SoL-Pi auto-research design, mechanism 1), in the
// ObservationPack house style: a scripted openai adapter drives a real session
// end to end, so every assertion lands on what the LLM boundary actually saw
// (recorded requests) or on the durable transcript. All offline and
// deterministic; the fused commands are real but trivial (cat/echo/exit).

// fusionSession builds a session over the scripted openai adapter whose steps
// are fully under the test's control, with the ActionFusion flag set per the
// caller, its own workspace dir, and a StateDir so the durable transcript can
// be inspected. It returns the session, the adapter, and the workspace dir.
func fusionSession(t *testing.T, fusion bool, steps []func(req llm.Request) llm.Response) (*Session, *fakeAdapter, string) {
	t.Helper()
	adapter := &fakeAdapter{name: "openai", steps: steps}
	client := llm.NewClient()
	client.Register(adapter)
	dir := t.TempDir()
	sess := newSession(t,
		withClient(client),
		withDir(dir),
		withConfig(SessionConfig{
			StateDir:         newBucket(t),
			ActionFusion:     fusion,
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
	return sess, adapter, dir
}

// fusionCallStep scripts one assistant tool call.
func fusionCallStep(callID, toolName, args string) func(llm.Request) llm.Response {
	return func(llm.Request) llm.Response {
		return llm.Response{Message: llm.Message{
			Role: llm.RoleAssistant,
			Content: []llm.ContentPart{
				{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: callID, Name: toolName, Arguments: []byte(args)}},
			},
		}}
	}
}

// fusionResult returns the string content carried for callID in req.
func fusionResult(req llm.Request, callID string) (string, bool) {
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

func fusionMustResult(t *testing.T, req llm.Request, callID, label string) string {
	t.Helper()
	content, ok := fusionResult(req, callID)
	if !ok {
		t.Fatalf("request %s carries no tool result for %s", label, callID)
	}
	return content
}

// fusionResultIsError reports whether the recorded tool result for callID is
// flagged as an error.
func fusionResultIsError(req llm.Request, callID string) bool {
	for _, msg := range req.Messages {
		for _, part := range msg.Content {
			if part.Kind == llm.ContentToolResult && part.ToolResult != nil && part.ToolResult.ToolCallID == callID {
				return part.ToolResult.IsError
			}
		}
	}
	return false
}

// fusionToolDef returns the advertised tool definition with the given name
// from the first recorded request (what the model actually saw).
func fusionToolDef(t *testing.T, req llm.Request, name string) llm.ToolDefinition {
	t.Helper()
	for _, def := range req.Tools {
		if def.Name == name {
			return def
		}
	}
	t.Fatalf("request advertises no tool named %q (have %d tools)", name, len(req.Tools))
	return llm.ToolDefinition{}
}

// fusionRun drives the session to completion with the standard tripwire: a
// scripted in-process adapter, no real I/O; the timeout only fires on a hang.
func fusionRun(t *testing.T, sess *Session) {
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

// fusionTranscript closes the session and returns its durable transcript
// entries, so tests can pin what was faithfully recorded.
func fusionTranscript(t *testing.T, sess *Session) []transcript.Entry {
	t.Helper()
	transcriptPath := sess.TranscriptPath()
	sess.Close()
	_, entries, _, err := readTranscript(transcriptPath)
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	return entries
}

// fusionCallsOf returns every non-result tool call the transcript recorded,
// in order. The final communicate call that ends the turn is a tool call too,
// but it is the harness's result protocol, not model-visible work; the cycle
// counts here are about the work calls (the default test sessions use the
// default result tool name "communicate").
func fusionCallsOf(entries []transcript.Entry) []llm.ToolCallData {
	var calls []llm.ToolCallData
	for _, entry := range entries {
		if entry.Turn.Kind != schema.TurnAssistant {
			continue
		}
		for _, part := range entry.Turn.Message.Content {
			if part.Kind == llm.ContentToolCall && part.ToolCall != nil {
				if part.ToolCall.Name == "communicate" {
					continue
				}
				calls = append(calls, *part.ToolCall)
			}
		}
	}
	return calls
}

// fusionWriteFixture seeds a workspace file the mutation tools can target.
func fusionWriteFixture(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write fixture %s: %v", name, err)
	}
}

func fusionReadFile(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}

// fusionJSONQuote embeds s as a JSON string literal.
func fusionJSONQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// TestActionFusion_FusedEditConsumesOneCall is the mechanism's headline
// contract: with the flag on, an edit_file call carrying run_after applies the
// mutation and runs the command inside the SAME tool call, the observation
// carries both the mutation result and the command output with the command
// echoed and its exit status visible, and the whole edit-then-check cycle
// costs two model requests where the unfused pattern costs three.
func TestActionFusion_FusedEditConsumesOneCall(t *testing.T) {
	t.Parallel()

	// Fused arm: one edit_file call carries the check command.
	sess, adapter, dir := fusionSession(t, true, []func(req llm.Request) llm.Response{
		fusionCallStep("call_edit", "edit_file", `{"file_path": "fusion.txt", "old_string": "alpha", "new_string": "gamma", "run_after": "cat fusion.txt"}`),
		func(llm.Request) llm.Response { return finalResponse("done") },
	})
	fusionWriteFixture(t, dir, "fusion.txt", "alpha\n")
	fusionRun(t, sess)

	reqs := adapter.Requests()
	if len(reqs) != 2 {
		t.Fatalf("fused arm saw %d requests, want 2 (edit+run_after round, then final)", len(reqs))
	}
	// The mutation really applied...
	if got := fusionReadFile(t, dir, "fusion.txt"); got != "gamma\n" {
		t.Fatalf("fused edit did not apply: file = %q", got)
	}
	// ...and the command really ran after it, in the same observation.
	combined := fusionMustResult(t, reqs[1], "call_edit", "2")
	for _, want := range []string{
		"edited fusion.txt: 1 replacement",
		"[run_after] $ cat fusion.txt",
		"gamma",
		"[exit 0]",
	} {
		if !strings.Contains(combined, want) {
			t.Fatalf("fused observation lacks %q:\n%s", want, combined)
		}
	}
	if fusionResultIsError(reqs[1], "call_edit") {
		t.Fatalf("fused edit result flagged as error:\n%s", combined)
	}
	// The fused cycle consumed ONE recorded tool call, and it was the edit.
	entries := fusionTranscript(t, sess)
	calls := fusionCallsOf(entries)
	if len(calls) != 1 || calls[0].Name != "edit_file" {
		t.Fatalf("fused cycle recorded %d tool calls (%v), want exactly one edit_file", len(calls), calls)
	}
	if !strings.Contains(string(calls[0].Arguments), "run_after") {
		t.Fatalf("transcript lost the run_after argument: %s", calls[0].Arguments)
	}
	// The transcript records the combined observation faithfully.
	persisted, ok := findToolResultInEntries(entries, "call_edit")
	if !ok {
		t.Fatal("transcript omitted the fused edit result")
	}
	if persistedContent, ok := persisted.Content.(string); !ok || persistedContent != combined {
		t.Fatalf("transcript result differs from the request-carried observation:\n%q\nvs\n%q", persistedContent, combined)
	}

	// Unfused arm: the same work as a plain edit followed by a shell call.
	sess2, adapter2, dir2 := fusionSession(t, false, []func(req llm.Request) llm.Response{
		fusionCallStep("call_edit", "edit_file", `{"file_path": "fusion.txt", "old_string": "alpha", "new_string": "gamma"}`),
		fusionCallStep("call_shell", "shell", `{"command": "cat fusion.txt"}`),
		func(llm.Request) llm.Response { return finalResponse("done") },
	})
	fusionWriteFixture(t, dir2, "fusion.txt", "alpha\n")
	fusionRun(t, sess2)
	reqs2 := adapter2.Requests()
	if len(reqs2) != 3 {
		t.Fatalf("unfused arm saw %d requests, want 3 (edit, shell, final)", len(reqs2))
	}
	calls2 := fusionCallsOf(fusionTranscript(t, sess2))
	if len(calls2) != 2 {
		t.Fatalf("unfused cycle recorded %d tool calls, want 2 (edit and shell)", len(calls2))
	}
}

// TestActionFusion_FailingCommandStandsWithMutation pins failure composition:
// when the fused command fails after the mutation applied, the mutation result
// stands, the command's failure (output, nonzero exit) appears in the same
// observation, the call is not an error result, and the transcript records
// the fused call and combined observation faithfully.
func TestActionFusion_FailingCommandStandsWithMutation(t *testing.T) {
	t.Parallel()
	sess, adapter, dir := fusionSession(t, true, []func(req llm.Request) llm.Response{
		fusionCallStep("call_edit", "edit_file", `{"file_path": "fusion.txt", "old_string": "alpha", "new_string": "gamma", "run_after": "sh -c 'echo fusion-boom 1>&2; exit 7'"}`),
		func(llm.Request) llm.Response { return finalResponse("done") },
	})
	fusionWriteFixture(t, dir, "fusion.txt", "alpha\n")
	fusionRun(t, sess)

	reqs := adapter.Requests()
	if len(reqs) != 2 {
		t.Fatalf("saw %d requests, want 2", len(reqs))
	}
	if got := fusionReadFile(t, dir, "fusion.txt"); got != "gamma\n" {
		t.Fatalf("mutation did not stand after command failure: file = %q", got)
	}
	combined := fusionMustResult(t, reqs[1], "call_edit", "2")
	for _, want := range []string{
		"edited fusion.txt: 1 replacement", // the mutation result stands...
		"[run_after] $ sh -c 'echo fusion-boom 1>&2; exit 7'",
		"fusion-boom", // ...the command's output appears in the same observation...
		"[exit 7]",    // ...with its nonzero exit status visible.
	} {
		if !strings.Contains(combined, want) {
			t.Fatalf("fused failure observation lacks %q:\n%s", want, combined)
		}
	}
	if fusionResultIsError(reqs[1], "call_edit") {
		t.Fatalf("mutation-succeeded call flagged as error:\n%s", combined)
	}
	// The transcript records the fused call and combined observation faithfully.
	entries := fusionTranscript(t, sess)
	persisted, ok := findToolResultInEntries(entries, "call_edit")
	if !ok {
		t.Fatal("transcript omitted the fused edit result")
	}
	if persistedContent, ok := persisted.Content.(string); !ok || persistedContent != combined {
		t.Fatalf("transcript result differs from the request-carried observation:\n%q\nvs\n%q", persistedContent, combined)
	}
	calls := fusionCallsOf(entries)
	if len(calls) != 1 || calls[0].Name != "edit_file" {
		t.Fatalf("fused failure cycle recorded %d tool calls (%v), want exactly one edit_file", len(calls), calls)
	}
}

// TestActionFusion_MalformedRunAfterRejectedBeforeMutation pins validation
// ordering: a malformed run_after (empty, whitespace-only, or not a string) is
// rejected BEFORE the mutation applies, so a bogus fusion cannot half-apply.
func TestActionFusion_MalformedRunAfterRejectedBeforeMutation(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		args string
	}{
		{"empty", `{"file_path": "fusion.txt", "old_string": "alpha", "new_string": "gamma", "run_after": ""}`},
		{"whitespace", `{"file_path": "fusion.txt", "old_string": "alpha", "new_string": "gamma", "run_after": " \t "}`},
		{"not a string", `{"file_path": "fusion.txt", "old_string": "alpha", "new_string": "gamma", "run_after": 17}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sess, adapter, dir := fusionSession(t, true, []func(req llm.Request) llm.Response{
				fusionCallStep("call_bad", "edit_file", tc.args),
				func(llm.Request) llm.Response { return finalResponse("done") },
			})
			fusionWriteFixture(t, dir, "fusion.txt", "alpha\n")
			fusionRun(t, sess)

			reqs := adapter.Requests()
			if len(reqs) != 2 {
				t.Fatalf("saw %d requests, want 2", len(reqs))
			}
			if got := fusionReadFile(t, dir, "fusion.txt"); got != "alpha\n" {
				t.Fatalf("malformed run_after half-applied the mutation: file = %q", got)
			}
			rejection := fusionMustResult(t, reqs[1], "call_bad", "2")
			if !fusionResultIsError(reqs[1], "call_bad") {
				t.Fatalf("malformed run_after not flagged as an error:\n%s", rejection)
			}
			if !strings.Contains(rejection, "run_after") {
				t.Fatalf("rejection does not name run_after:\n%s", rejection)
			}
		})
	}
}

// TestActionFusion_MissingShellToolRejectedBeforeMutation pins the guard for
// a session whose registry has no shell tool (a restricted role): a fused
// call is rejected whole, before the mutation applies, rather than mutating
// and failing to run the command.
func TestActionFusion_MissingShellToolRejectedBeforeMutation(t *testing.T) {
	t.Parallel()
	sess, adapter, dir := fusionSession(t, true, []func(req llm.Request) llm.Response{
		fusionCallStep("call_edit", "edit_file", `{"file_path": "fusion.txt", "old_string": "alpha", "new_string": "gamma", "run_after": "cat fusion.txt"}`),
		func(llm.Request) llm.Response { return finalResponse("done") },
	})
	fusionWriteFixture(t, dir, "fusion.txt", "alpha\n")
	sess.reg.Remove("shell")
	fusionRun(t, sess)

	reqs := adapter.Requests()
	if len(reqs) != 2 {
		t.Fatalf("saw %d requests, want 2", len(reqs))
	}
	if got := fusionReadFile(t, dir, "fusion.txt"); got != "alpha\n" {
		t.Fatalf("unrunnable run_after half-applied the mutation: file = %q", got)
	}
	if !fusionResultIsError(reqs[1], "call_edit") {
		t.Fatalf("unrunnable run_after not flagged as an error:\n%s", fusionMustResult(t, reqs[1], "call_edit", "2"))
	}
}

// TestActionFusion_WriteFileAndApplyPatchFuse pins that all three
// file-mutation tools fuse, not just edit_file.
func TestActionFusion_WriteFileAndApplyPatchFuse(t *testing.T) {
	t.Parallel()
	patch := "*** Begin Patch\n*** Add File: patched.txt\n+patched-body\n*** End Patch"
	writeArgs := `{"file_path": "fused-new.txt", "content": "written-body\n", "run_after": "cat fused-new.txt"}`
	patchArgs := `{"patch": ` + fusionJSONQuote(patch) + `, "run_after": "cat patched.txt"}`
	sess, adapter, dir := fusionSession(t, true, []func(req llm.Request) llm.Response{
		fusionCallStep("call_write", "write_file", writeArgs),
		fusionCallStep("call_patch", "apply_patch", patchArgs),
		func(llm.Request) llm.Response { return finalResponse("done") },
	})
	fusionRun(t, sess)

	reqs := adapter.Requests()
	if len(reqs) != 3 {
		t.Fatalf("saw %d requests, want 3 (fused write, fused patch, final)", len(reqs))
	}
	writeOut := fusionMustResult(t, reqs[2], "call_write", "3")
	for _, want := range []string{
		"wrote 13 bytes to fused-new.txt",
		"[run_after] $ cat fused-new.txt",
		"written-body",
		"[exit 0]",
	} {
		if !strings.Contains(writeOut, want) {
			t.Fatalf("fused write_file observation lacks %q:\n%s", want, writeOut)
		}
	}
	patchOut := fusionMustResult(t, reqs[2], "call_patch", "3")
	for _, want := range []string{
		"applied patch to:\npatched.txt",
		"[run_after] $ cat patched.txt",
		"patched-body",
		"[exit 0]",
	} {
		if !strings.Contains(patchOut, want) {
			t.Fatalf("fused apply_patch observation lacks %q:\n%s", want, patchOut)
		}
	}
	if got := fusionReadFile(t, dir, "patched.txt"); got != "patched-body\n" {
		t.Fatalf("patched file = %q", got)
	}
	calls := fusionCallsOf(fusionTranscript(t, sess))
	if len(calls) != 2 || calls[0].Name != "write_file" || calls[1].Name != "apply_patch" {
		t.Fatalf("fused cycle recorded %d tool calls (%v), want write_file then apply_patch", len(calls), calls)
	}
}

// TestActionFusion_FlagOffUnfusedBehaviorUntouched pins the off-by-default
// contract: with the flag off, an unfused edit-then-command cycle behaves
// exactly as today (separate observations, no run_after markers), and a stray
// run_after argument follows today's generic unknown-property behavior (the
// repair layer drops it; the plain edit applies; no command runs).
func TestActionFusion_FlagOffUnfusedBehaviorUntouched(t *testing.T) {
	t.Parallel()
	sess, adapter, dir := fusionSession(t, false, []func(req llm.Request) llm.Response{
		fusionCallStep("call_edit", "edit_file", `{"file_path": "fusion.txt", "old_string": "alpha", "new_string": "gamma"}`),
		fusionCallStep("call_shell", "shell", `{"command": "cat fusion.txt"}`),
		func(llm.Request) llm.Response { return finalResponse("done") },
	})
	fusionWriteFixture(t, dir, "fusion.txt", "alpha\n")
	fusionRun(t, sess)

	reqs := adapter.Requests()
	if len(reqs) != 3 {
		t.Fatalf("unfused cycle saw %d requests, want 3", len(reqs))
	}
	editOut := fusionMustResult(t, reqs[2], "call_edit", "3")
	if strings.Contains(editOut, "[run_after]") {
		t.Fatalf("flag-off edit observation grew a run_after section:\n%s", editOut)
	}
	if !strings.Contains(editOut, "edited fusion.txt: 1 replacement") {
		t.Fatalf("flag-off edit observation changed shape:\n%s", editOut)
	}
	shellOut := fusionMustResult(t, reqs[2], "call_shell", "3")
	if !strings.Contains(shellOut, "gamma") || strings.Contains(shellOut, "[run_after]") {
		t.Fatalf("flag-off shell observation changed shape:\n%s", shellOut)
	}

	// A stray run_after with the flag off is just an unknown property: the
	// generic repair layer drops it and the plain edit applies.
	sess2, adapter2, dir2 := fusionSession(t, false, []func(req llm.Request) llm.Response{
		fusionCallStep("call_stray", "edit_file", `{"file_path": "fusion.txt", "old_string": "alpha", "new_string": "gamma", "run_after": "cat fusion.txt"}`),
		func(llm.Request) llm.Response { return finalResponse("done") },
	})
	fusionWriteFixture(t, dir2, "fusion.txt", "alpha\n")
	fusionRun(t, sess2)
	reqs2 := adapter2.Requests()
	if len(reqs2) != 2 {
		t.Fatalf("stray run_after arm saw %d requests, want 2", len(reqs2))
	}
	strayOut := fusionMustResult(t, reqs2[1], "call_stray", "2")
	if strings.Contains(strayOut, "[run_after]") {
		t.Fatalf("flag-off stray run_after fused a command:\n%s", strayOut)
	}
	if got := fusionReadFile(t, dir2, "fusion.txt"); got != "gamma\n" {
		t.Fatalf("flag-off stray run_after changed today's unknown-property behavior: file = %q", got)
	}
}

// TestActionFusion_SchemaExposureFollowsFlag pins both directions of schema
// exposure at the LLM boundary: with the flag off, the three mutation tools'
// advertised schemas are byte-identical to today's (base definition plus the
// shared intent parameter, no run_after); with the flag on, they differ by
// exactly the added run_after property.
func TestActionFusion_SchemaExposureFollowsFlag(t *testing.T) {
	t.Parallel()
	finalOnly := []func(req llm.Request) llm.Response{
		func(llm.Request) llm.Response { return finalResponse("done") },
	}
	off, adapterOff, _ := fusionSession(t, false, finalOnly)
	fusionRun(t, off)
	on, adapterOn, _ := fusionSession(t, true, finalOnly)
	fusionRun(t, on)

	base := map[string]llm.ToolDefinition{
		"edit_file":   tool.WithIntentParameter(tool.DefEditFile()),
		"write_file":  tool.WithIntentParameter(tool.DefWriteFile()),
		"apply_patch": tool.WithIntentParameter(tool.DefApplyPatch()),
	}
	offReqs := adapterOff.Requests()
	onReqs := adapterOn.Requests()
	if len(offReqs) == 0 || len(onReqs) == 0 {
		t.Fatal("no recorded requests to inspect advertised schemas")
	}
	for name, want := range base {
		wantJSON := fusionMustMarshal(t, want, name+" baseline")
		offDef := fusionToolDef(t, offReqs[0], name)
		offJSON := fusionMustMarshal(t, offDef, name+" advertised (flag off)")
		if offJSON != wantJSON {
			t.Fatalf("flag-off %s schema is not byte-identical to today's:\n got: %s\nwant: %s", name, offJSON, wantJSON)
		}
		if fusionRunAfterProp(offDef) != nil {
			t.Fatalf("flag-off %s schema exposes run_after:\n%s", name, offJSON)
		}

		onDef := fusionToolDef(t, onReqs[0], name)
		runAfter := fusionRunAfterProp(onDef)
		if runAfter == nil {
			t.Fatalf("flag-on %s schema does not expose run_after:\n%s", name, fusionMustMarshal(t, onDef, name))
		}
		if typ, _ := runAfter["type"].(string); typ != "string" {
			t.Fatalf("flag-on %s run_after is not a string property: %v", name, runAfter)
		}
		// The flag-on definition differs from the flag-off one by exactly the
		// run_after property: strip it and the bytes match again.
		strippedJSON := fusionMustMarshal(t, fusionWithoutRunAfterProp(t, onDef), name+" stripped (flag on)")
		if strippedJSON != offJSON {
			t.Fatalf("flag-on %s schema differs beyond run_after:\n got: %s\nwant: %s", name, strippedJSON, offJSON)
		}
	}
}

func fusionRunAfterProp(def llm.ToolDefinition) map[string]any {
	props, _ := def.Parameters["properties"].(map[string]any)
	if props == nil {
		return nil
	}
	p, _ := props["run_after"].(map[string]any)
	return p
}

// fusionWithoutRunAfterProp clones def's parameter schema with the run_after
// property removed, so byte comparisons never touch the shared advertised map.
func fusionWithoutRunAfterProp(t *testing.T, def llm.ToolDefinition) llm.ToolDefinition {
	t.Helper()
	cloned := tool.CloneSchemaMap(def.Parameters)
	if cloned == nil {
		t.Fatal("tool definition has no parameters to strip")
	}
	props, _ := cloned["properties"].(map[string]any)
	if props == nil {
		t.Fatal("tool definition has no properties to strip")
	}
	delete(props, "run_after")
	def.Parameters = cloned
	return def
}

func fusionMustMarshal(t *testing.T, v any, label string) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal %s: %v", label, err)
	}
	return string(b)
}

// TestActionFusion_SnapshotRoundTrip pins that the flag rides the
// toSnapshot/configFromSnapshot converter pair, so child sessions built from
// the parent's snapshot and sessions restored from meta.json inherit it, and
// that it defaults to off.
func TestActionFusion_SnapshotRoundTrip(t *testing.T) {
	t.Parallel()
	in := SessionConfig{ActionFusion: true}
	out := configFromSnapshot(in.toSnapshot().Clone())
	if !out.ActionFusion {
		t.Fatal("ActionFusion did not survive the snapshot round trip: delegates and restored sessions would silently run unfused")
	}
	off := SessionConfig{}
	if configFromSnapshot(off.toSnapshot()).ActionFusion {
		t.Fatal("default-off flag turned on by the snapshot round trip")
	}
}
