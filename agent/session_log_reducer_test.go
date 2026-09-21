package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/research"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

// Evidence-Preserving Reducer tests (SoL-Pi auto-research design, mechanism 4),
// in the ObservationPack house style: a scripted openai adapter drives a real
// session end to end so every assertion lands on what the LLM boundary actually
// saw (recorded requests) or on the durable transcript. The receipt extraction
// is an LLM call at the cheap-model boundary and is always served by the
// scripted adapter — never a live provider. All offline and deterministic; the
// shell commands are real but trivial printf loops.

const logRedCheapModel = "gpt-4.1-nano"

// logRedCommand builds a deterministic build-log fixture command: it prints a
// header, N fill lines (~46 bytes each), and a PASS tail. The command string
// contains "go test", so it matches the declared command set exactly the way
// the oracle's researchTestBuildRe matches (a word-boundary match anywhere in
// the command string).
func logRedCommand(lines int) string {
	return `printf '=== go test ./... ===\n'; for i in $(seq 1 ` + strconv.Itoa(lines) +
		`); do printf 'line-%03d-abcdefghijklmnopqrstuvwxyz0123456789\n' "$i"; done; echo PASS`
}

func logRedShellCallStep(callID, command string) func(llm.Request) llm.Response {
	args, err := json.Marshal(map[string]any{"command": command})
	if err != nil {
		panic(err)
	}
	return obsPackCallStep(callID, "shell", string(args))
}

// logRedSession builds a LogReducer-on session over the scripted openai
// adapter with the cheap model explicitly configured (WithCheapModel), plus
// the caller's extra config (StateDir, ObservationPacking, a fake artifact
// store, or the flag off).
// A variadic dir overrides the shared workspace (the fusion leg needs a
// writable one).
func logRedSession(t *testing.T, cfg SessionConfig, steps []func(req llm.Request) llm.Response, dir ...string) (*Session, *fakeAdapter) {
	t.Helper()
	adapter := &fakeAdapter{name: "openai", steps: steps}
	client := llm.NewClient()
	client.Register(adapter)
	cfg.MaxSubagentDepth = 1
	cfg.NoProjectPrompts = true
	cfg.testOnly = testConfig{
		skipGitSnapshot:     true,
		minimalSystemPrompt: true,
		noSyncJobStore:      true,
	}
	// The auto-namer would draw from the same scripted adapter as the test's
	// steps whenever StateDir is set (the namer launches on the configured
	// cheap model); route it to its own client so its async draw can never
	// steal a step — the same separation the fuzz kit's namerClient exists
	// for.
	namerClient := llm.NewClient()
	namerClient.Register(&fakeAdapter{name: "openai", steps: []func(req llm.Request) llm.Response{
		func(llm.Request) llm.Response {
			return llm.Response{Message: llm.Assistant(`{"name":"log reducer test"}`)}
		},
	}})
	cfg.testOnly.namerClient = namerClient
	opts := []sessionOpt{
		withClient(client),
		withProfile(WithCheapModel(NewOpenAIProfile("gpt-5.2"), logRedCheapModel)),
		withConfig(cfg),
	}
	if len(dir) > 0 && dir[0] != "" {
		opts = append(opts, withDir(dir[0]))
	}
	sess := newSession(t, opts...)
	drainSessionEvents(sess)
	return sess, adapter
}

// logRedExtractionStep scripts one cheap-model receipt-extraction call. It
// asserts the call routed to the configured cheap model and returns payload as
// the model's JSON answer.
func logRedExtractionStep(t *testing.T, payload map[string]any) func(llm.Request) llm.Response {
	t.Helper()
	return func(req llm.Request) llm.Response {
		if req.Model != logRedCheapModel {
			t.Errorf("receipt extraction routed to model %q, want the configured cheap model %q", req.Model, logRedCheapModel)
		}
		b, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal scripted extraction payload: %v", err)
		}
		return llm.Response{Message: llm.Assistant(string(b))}
	}
}

// logRedValidPayload is an honest extraction of the logRedCommand fixture.
func logRedValidPayload(command string) map[string]any {
	return map[string]any{
		"command":     command,
		"exit_status": 0,
		"quoted_lines": []any{
			"=== go test ./... ===",
			"line-001-abcdefghijklmnopqrstuvwxyz0123456789",
			"PASS",
			"[exit 0]",
		},
		"summary": "Simulated go test run: a header, N fill lines, then PASS with exit 0.",
	}
}

// logRedTranscriptResult reads the durable transcript's recorded content for
// callID.
func logRedTranscriptResult(t *testing.T, sess *Session, callID string) string {
	t.Helper()
	transcriptPath := sess.TranscriptPath()
	sess.Close()
	_, entries, _, err := readTranscript(transcriptPath)
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	persisted, ok := findToolResultInEntries(entries, callID)
	if !ok {
		t.Fatalf("transcript omitted the tool result for %s", callID)
	}
	content, ok := persisted.Content.(string)
	if !ok {
		t.Fatalf("transcript result for %s is %T, want string", callID, persisted.Content)
	}
	return content
}

// TestLogReducer_FlagOn_ReplacesBuildTestLogWithVerifiedReceipt is the
// mechanism's headline contract: with the flag on and the cheap model
// configured, a shell command from the declared set producing a log of 4 KiB
// or more is archived, and the observation the model receives is the verified
// receipt — strictly smaller, carrying the receipt schema (command, exit
// status, source hash, quoted lines, summary) plus the archive handle — while
// the durable transcript keeps the full original log.
func TestLogReducer_FlagOn_ReplacesBuildTestLogWithVerifiedReceipt(t *testing.T) {
	t.Parallel()
	command := logRedCommand(120)
	sess, adapter := logRedSession(t, SessionConfig{
		StateDir:   newBucket(t),
		LogReducer: true,
	}, []func(req llm.Request) llm.Response{
		logRedShellCallStep("call_log", command),
		logRedExtractionStep(t, logRedValidPayload(command)),
		obsPackCallStep("call_s2", "shell", `{"command": "echo two"}`),
		func(llm.Request) llm.Response { return finalResponse("done") },
	})
	obsPackRun(t, sess)

	reqs := adapter.Requests()
	if len(reqs) != 4 {
		t.Fatalf("scripted adapter saw %d requests, want 4 (round 1, extraction, round 2, final)", len(reqs))
	}
	// The extraction happened during the tool round, between requests 1 and 2.
	if reqs[1].Model != logRedCheapModel {
		t.Fatalf("request 2 model = %q, want the cheap model %q (extraction must use the side channel)", reqs[1].Model, logRedCheapModel)
	}
	if got, ok := obsPackResult(reqs[0], "call_log"); ok && got != "" {
		t.Fatalf("request 1 already carries a tool result for the not-yet-run call: %d bytes", len(got))
	}

	receipt := obsPackMustResult(t, reqs[2], "call_log", "round 2")
	if !strings.HasPrefix(receipt, logReceiptMarker) {
		t.Fatalf("observation the model receives is not a receipt (missing %q prefix):\n%.400s", logReceiptMarker, receipt)
	}
	if !strings.Contains(receipt, "command: "+command) {
		t.Fatalf("receipt omits the command:\n%s", receipt)
	}
	if !strings.Contains(receipt, "exit status: 0") {
		t.Fatalf("receipt omits the verified exit status:\n%s", receipt)
	}
	for _, line := range []string{"=== go test ./... ===", "line-001-abcdefghijklmnopqrstuvwxyz0123456789", "PASS", "[exit 0]", "Simulated go test run:"} {
		if !strings.Contains(receipt, line) {
			t.Fatalf("receipt omits quoted evidence/summary line %q:\n%s", line, receipt)
		}
	}
	ref := obsPackArtifactRef(t, receipt)
	if !strings.Contains(receipt, `read_transcript(transcript_ref="`+ref+`")`) {
		t.Fatalf("receipt lacks the on-demand retrieval instruction for %s:\n%s", ref, receipt)
	}

	// The full log is retrievable on demand from the archive, byte-identical.
	reader, err := sess.artifactStore.Open(ref)
	if err != nil {
		t.Fatalf("open archived log %s: %v", ref, err)
	}
	archived, err := os.ReadFile(reader.Name())
	if err != nil {
		t.Fatalf("read archived log: %v", err)
	}
	_ = reader.Close()
	original := string(archived)
	if !strings.Contains(original, "line-120-abcdefghijklmnopqrstuvwxyz0123456789") || !strings.Contains(original, "[exit 0]") {
		t.Fatalf("archived log is not the complete shell result: %d bytes", len(original))
	}
	if len(original) < logReducerMinBytes {
		t.Fatalf("fixture log is %d bytes, want at least the %d-byte threshold", len(original), logReducerMinBytes)
	}
	if len(receipt) >= len(original) {
		t.Fatalf("receipt is %d bytes, not strictly smaller than the %d-byte original", len(receipt), len(original))
	}
	// The receipt carries the source hash of the full log it summarizes.
	sum := sha256.Sum256([]byte(original))
	if !strings.Contains(receipt, "sha256:"+hex.EncodeToString(sum[:])) {
		t.Fatalf("receipt omits or misstates the source hash sha256:%s:\n%s", hex.EncodeToString(sum[:]), receipt)
	}

	// The receipt is stable across later requests: history carries it, not the log.
	if got := obsPackMustResult(t, reqs[3], "call_log", "final"); got != receipt {
		t.Fatalf("later request carries a different view of the reduced log")
	}

	// The durable transcript keeps the full original log (evidence-preserving).
	if persisted := logRedTranscriptResult(t, sess, "call_log"); persisted != original {
		t.Fatalf("transcript result is %d bytes, want the full %d-byte original log", len(persisted), len(original))
	}
}

// TestLogReducer_VerifierRejectionsFallBackToOriginalLog pins every
// verifier rejection leg: a receipt missing a required field, a quoted line
// absent from the source, an exit status or command mismatch, or credentials
// in the receipt — each falls back to the ORIGINAL log, which flows to the
// model unchanged and is recorded in the transcript.
func TestLogReducer_VerifierRejectionsFallBackToOriginalLog(t *testing.T) {
	t.Parallel()
	command := logRedCommand(120)
	cases := []struct {
		name    string
		payload func(valid map[string]any) map[string]any
	}{
		{
			name: "missing summary",
			payload: func(valid map[string]any) map[string]any {
				delete(valid, "summary")
				return valid
			},
		},
		{
			name: "missing quoted lines",
			payload: func(valid map[string]any) map[string]any {
				delete(valid, "quoted_lines")
				return valid
			},
		},
		{
			name: "empty quoted lines",
			payload: func(valid map[string]any) map[string]any {
				valid["quoted_lines"] = []any{}
				return valid
			},
		},
		{
			name: "quoted line not in source",
			payload: func(valid map[string]any) map[string]any {
				valid["quoted_lines"] = []any{"THIS LINE IS NOT IN THE LOG"}
				return valid
			},
		},
		{
			name: "exit status mismatch",
			payload: func(valid map[string]any) map[string]any {
				valid["exit_status"] = 1
				return valid
			},
		},
		{
			name: "command mismatch",
			payload: func(valid map[string]any) map[string]any {
				valid["command"] = "go test ./some/other/..."
				return valid
			},
		},
		{
			name: "credentials in receipt",
			payload: func(valid map[string]any) map[string]any {
				valid["summary"] = "leaked a GitHub token ghp_0123456789abcdefghijklmnopqrstuv in the summary"
				return valid
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := &fakeArtifactStore{ref: "artifact:abc"}
			sess, adapter := logRedSession(t, SessionConfig{
				StateDir:      newBucket(t),
				LogReducer:    true,
				artifactStore: store,
			}, []func(req llm.Request) llm.Response{
				logRedShellCallStep("call_log", command),
				logRedExtractionStep(t, tc.payload(logRedValidPayload(command))),
				func(llm.Request) llm.Response { return finalResponse("done") },
			})
			obsPackRun(t, sess)

			reqs := adapter.Requests()
			if len(reqs) != 3 {
				t.Fatalf("scripted adapter saw %d requests, want 3 (round, extraction, final)", len(reqs))
			}
			original := obsPackMustResult(t, reqs[2], "call_log", "final")
			if strings.HasPrefix(original, logReceiptMarker) {
				t.Fatalf("rejected receipt still replaced the log:\n%.300s", original)
			}
			if !strings.Contains(original, "line-120-abcdefghijklmnopqrstuvwxyz0123456789") {
				t.Fatalf("fallback observation is not the full original log: %d bytes", len(original))
			}
			if len(store.puts) != 1 {
				t.Fatalf("archive attempts = %d, want exactly one (the log was archived before verification)", len(store.puts))
			}
			if persisted := logRedTranscriptResult(t, sess, "call_log"); persisted != original {
				t.Fatalf("transcript result differs from the model-facing fallback log: %d vs %d bytes", len(persisted), len(original))
			}
		})
	}
}

// TestLogReducer_CredentialInLogBoundaryAbortsBeforeExtraction pins that a
// suspected credential in the log's receipt-boundary region aborts the
// reduction before anything is archived or any provider request is made.
func TestLogReducer_CredentialInLogBoundaryAbortsBeforeExtraction(t *testing.T) {
	t.Parallel()
	// The credential is assembled at runtime so the command string itself is
	// clean; only the log's first line carries it.
	command := `printf 'warning: env token ghp_%s\n' 0123456789abcdefghijklmnopqrstuv; ` + logRedCommand(120)
	store := &fakeArtifactStore{ref: "artifact:abc"}
	sess, adapter := logRedSession(t, SessionConfig{
		StateDir:      newBucket(t),
		LogReducer:    true,
		artifactStore: store,
	}, []func(req llm.Request) llm.Response{
		logRedShellCallStep("call_log", command),
		func(llm.Request) llm.Response { return finalResponse("done") },
	})
	obsPackRun(t, sess)

	reqs := adapter.Requests()
	if len(reqs) != 2 {
		t.Fatalf("scripted adapter saw %d requests, want 2 (round, final): a credential in the boundary region must abort before any extraction request", len(reqs))
	}
	original := obsPackMustResult(t, reqs[1], "call_log", "final")
	if strings.HasPrefix(original, logReceiptMarker) {
		t.Fatalf("credential-bearing log was still reduced:\n%.300s", original)
	}
	if !strings.Contains(original, "ghp_0123456789abcdefghijklmnopqrstuv") {
		t.Fatalf("fallback observation is not the original log")
	}
	if len(store.puts) != 0 {
		t.Fatalf("archive attempts = %d, want none (abort precedes archiving)", len(store.puts))
	}
}

// TestLogReducer_CredentialInCommandAbortsBeforeExtraction pins the same
// abort when the command string itself carries a credential: the receipt would
// quote the command verbatim, so the whole reduction aborts up front.
func TestLogReducer_CredentialInCommandAbortsBeforeExtraction(t *testing.T) {
	t.Parallel()
	command := `echo "go test start"; printf 'ghp_0123456789abcdefghijklmnopqrstuv' > /dev/null; for i in $(seq 1 120); do printf 'line-%03d-abcdefghijklmnopqrstuvwxyz0123456789\n' "$i"; done; echo PASS`
	store := &fakeArtifactStore{ref: "artifact:abc"}
	sess, adapter := logRedSession(t, SessionConfig{
		LogReducer:    true,
		artifactStore: store,
	}, []func(req llm.Request) llm.Response{
		logRedShellCallStep("call_log", command),
		func(llm.Request) llm.Response { return finalResponse("done") },
	})
	obsPackRun(t, sess)

	reqs := adapter.Requests()
	if len(reqs) != 2 {
		t.Fatalf("scripted adapter saw %d requests, want 2: a credential in the command must abort before any extraction request", len(reqs))
	}
	original := obsPackMustResult(t, reqs[1], "call_log", "final")
	if strings.HasPrefix(original, logReceiptMarker) {
		t.Fatalf("credential-bearing command was still reduced:\n%.300s", original)
	}
	if len(store.puts) != 0 {
		t.Fatalf("archive attempts = %d, want none", len(store.puts))
	}
}

// TestLogReducer_ArchiveFailureFallsBack pins that a failing artifact store
// aborts the reduction: the original log flows unchanged and no extraction
// call is made.
func TestLogReducer_ArchiveFailureFallsBack(t *testing.T) {
	t.Parallel()
	command := logRedCommand(120)
	store := &fakeArtifactStore{ref: "artifact:abc", putErr: errObsPackTestArchive}
	sess, adapter := logRedSession(t, SessionConfig{
		LogReducer:    true,
		artifactStore: store,
	}, []func(req llm.Request) llm.Response{
		logRedShellCallStep("call_log", command),
		func(llm.Request) llm.Response { return finalResponse("done") },
	})
	obsPackRun(t, sess)

	reqs := adapter.Requests()
	if len(reqs) != 2 {
		t.Fatalf("scripted adapter saw %d requests, want 2 (archive failure precedes extraction)", len(reqs))
	}
	original := obsPackMustResult(t, reqs[1], "call_log", "final")
	if strings.HasPrefix(original, logReceiptMarker) || !strings.Contains(original, "line-120-") {
		t.Fatalf("archive failure did not fall back to the original log: %d bytes", len(original))
	}
}

// TestLogReducer_BypassesFileReadsAndSearches pins that file reads and search
// results bypass the reducer entirely, even well over the threshold with the
// flag on and the cheap model configured: only shell commands from the
// declared set are reducer-eligible.
func TestLogReducer_BypassesFileReadsAndSearches(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	body := obsPackBody() // > 10 KiB of line-oriented fixture
	bigPath := filepath.Join(dir, "big.txt")
	if err := os.WriteFile(bigPath, []byte(body), 0o644); err != nil {
		t.Fatalf("write big fixture: %v", err)
	}
	grepPath := filepath.Join(dir, "greps.txt")
	var grepBody strings.Builder
	for i := 1; i <= 120; i++ {
		grepBody.WriteString("grepsent-")
		grepBody.WriteString(strconv.Itoa(i))
		grepBody.WriteString("-abcdefghijklmnopqrstuvwxyz0123456789\n")
	}
	if err := os.WriteFile(grepPath, []byte(grepBody.String()), 0o644); err != nil {
		t.Fatalf("write grep fixture: %v", err)
	}

	sess, adapter := logRedSession(t, SessionConfig{
		LogReducer: true,
	}, []func(req llm.Request) llm.Response{
		obsPackCallStep("call_read", "read_file", `{"file_path": "`+bigPath+`"}`),
		obsPackCallStep("call_grep", "grep", `{"pattern": "grepsent", "path": "`+grepPath+`"}`),
		func(llm.Request) llm.Response { return finalResponse("done") },
	})
	obsPackRun(t, sess)

	reqs := adapter.Requests()
	if len(reqs) != 3 {
		t.Fatalf("scripted adapter saw %d requests, want 3: file reads and searches must not trigger an extraction call", len(reqs))
	}
	read := obsPackMustResult(t, reqs[2], "call_read", "final")
	if strings.HasPrefix(read, logReceiptMarker) {
		t.Fatalf("file read was reduced:\n%.200s", read)
	}
	// read_file wraps the file body in its own rendering; assert the original
	// content rides through rather than byte-identity with the raw file.
	if !strings.Contains(read, "OBS-PACK-HEAD-SENTINEL") || !strings.Contains(read, "OBS-PACK-TAIL-SENTINEL") {
		t.Fatalf("file read result lost the original content: %d bytes", len(read))
	}
	got := obsPackMustResult(t, reqs[2], "call_grep", "final")
	// The grep tool renders matches as "lineno:content" with unpadded
	// numbers; assert on the first match rather than the raw fixture text.
	if strings.HasPrefix(got, logReceiptMarker) || !strings.Contains(got, ":grepsent-1-") {
		t.Fatalf("search result was not passed through: %d bytes", len(got))
	}
}

// TestLogReducer_BypassesUndeclaredAndSmallShellLogs pins the remaining
// eligibility legs at the session boundary: a large shell log from a command
// OUTSIDE the declared set, and a small shell log from a declared command,
// both flow unchanged with no extraction call.
func TestLogReducer_BypassesUndeclaredAndSmallShellLogs(t *testing.T) {
	t.Parallel()
	undeclared := `for i in $(seq 1 120); do printf 'line-%03d-abcdefghijklmnopqrstuvwxyz0123456789\n' "$i"; done; echo PASS`
	small := `printf '=== go test ./... ===\n'; echo PASS`
	sess, adapter := logRedSession(t, SessionConfig{
		LogReducer: true,
	}, []func(req llm.Request) llm.Response{
		logRedShellCallStep("call_undeclared", undeclared),
		logRedShellCallStep("call_small", small),
		func(llm.Request) llm.Response { return finalResponse("done") },
	})
	obsPackRun(t, sess)

	reqs := adapter.Requests()
	if len(reqs) != 3 {
		t.Fatalf("scripted adapter saw %d requests, want 3: neither log is reducer-eligible", len(reqs))
	}
	undeclaredResult := obsPackMustResult(t, reqs[2], "call_undeclared", "final")
	if strings.HasPrefix(undeclaredResult, logReceiptMarker) || !strings.Contains(undeclaredResult, "line-120-") {
		t.Fatalf("undeclared-command log was reduced or truncated: %d bytes", len(undeclaredResult))
	}
	smallResult := obsPackMustResult(t, reqs[2], "call_small", "final")
	if strings.HasPrefix(smallResult, logReceiptMarker) {
		t.Fatalf("sub-threshold log was reduced:\n%s", smallResult)
	}
}

// TestLogReducer_UnconfiguredCheapModelMakesNoCall pins the unconfigured
// ruling: with LogReducer on but no cheap model configured (no
// --fast-cheap-model), no receipt extraction is attempted, no provider call
// is made at all, and the original log is used unchanged.
func TestLogReducer_UnconfiguredCheapModelMakesNoCall(t *testing.T) {
	t.Parallel()
	command := logRedCommand(120)
	adapter := &fakeAdapter{name: "openai", steps: []func(req llm.Request) llm.Response{
		logRedShellCallStep("call_log", command),
		func(llm.Request) llm.Response { return finalResponse("done") },
	}}
	client := llm.NewClient()
	client.Register(adapter)
	cfg := SessionConfig{
		LogReducer:       true,
		MaxSubagentDepth: 1,
		NoProjectPrompts: true,
		AgentsDocPath:    filepath.Join(t.TempDir(), "no-personal-AGENTS.md"),
		testOnly: testConfig{
			skipGitSnapshot:     true,
			minimalSystemPrompt: true,
			noSyncJobStore:      true,
		},
	}
	// NewSession directly (not the test kit's newSession, which would
	// auto-configure a test session-namer cheap model): the production
	// construction with no WithCheapModel is the unconfigured case.
	sess, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), cfg)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { sess.Close() })
	if sess.currentProfile().ConfiguredCheapModel() != "" {
		t.Fatal("test precondition: profile unexpectedly has a configured cheap model")
	}
	drainSessionEvents(sess)
	obsPackRun(t, sess)

	reqs := adapter.Requests()
	if len(reqs) != 2 {
		t.Fatalf("scripted adapter saw %d requests, want 2: an unconfigured cheap model must not attempt extraction", len(reqs))
	}
	original := obsPackMustResult(t, reqs[1], "call_log", "final")
	if strings.HasPrefix(original, logReceiptMarker) || !strings.Contains(original, "line-120-") {
		t.Fatalf("unconfigured cheap model changed the log: %d bytes", len(original))
	}
}

// TestLogReducer_FlagOffLeavesRequestsUnchanged pins the off-by-default
// contract: with the flag unset the reducer is inert even with a cheap model
// configured and an eligible build log — no extraction call, original bytes in
// every request, byte-identical to a session without the mechanism.
func TestLogReducer_FlagOffLeavesRequestsUnchanged(t *testing.T) {
	t.Parallel()
	command := logRedCommand(120)
	sess, adapter := logRedSession(t, SessionConfig{
		LogReducer: false,
	}, []func(req llm.Request) llm.Response{
		logRedShellCallStep("call_log", command),
		obsPackCallStep("call_s2", "shell", `{"command": "echo two"}`),
		obsPackCallStep("call_s3", "shell", `{"command": "echo three"}`),
		func(llm.Request) llm.Response { return finalResponse("done") },
	})
	obsPackRun(t, sess)

	reqs := adapter.Requests()
	if len(reqs) != 4 {
		t.Fatalf("scripted adapter saw %d requests, want 4 (no extraction with the flag off)", len(reqs))
	}
	first := obsPackMustResult(t, reqs[1], "call_log", "2")
	if strings.HasPrefix(first, logReceiptMarker) {
		t.Fatalf("flag-off session reduced a log:\n%.300s", first)
	}
	for i := 2; i < len(reqs); i++ {
		if got := obsPackMustResult(t, reqs[i], "call_log", strconv.Itoa(i+1)); got != first {
			t.Fatalf("request %d changed the log with the reducer off", i+1)
		}
	}
}

// TestLogReducer_ReceiptNeverPacksAndDeclinedLogStillPacks pins the
// ObservationPack composition: a receipt is recognized and never packed, even
// in a flag-on-both session, while an original large log that the reducer
// DECLINED still packs after its two full looks.
func TestLogReducer_ReceiptNeverPacksAndDeclinedLogStillPacks(t *testing.T) {
	t.Parallel()
	// Reduced arm: an honest receipt of a 5.4 KiB log (under the 10 KiB
	// packing threshold anyway) must never register for packing.
	reducedCommand := logRedCommand(120)
	reducedSess, reducedAdapter := logRedSession(t, SessionConfig{
		LogReducer:         true,
		ObservationPacking: true,
	}, []func(req llm.Request) llm.Response{
		logRedShellCallStep("call_log", reducedCommand),
		logRedExtractionStep(t, logRedValidPayload(reducedCommand)),
		obsPackCallStep("call_r1", "shell", `{"command": "echo r1"}`),
		obsPackCallStep("call_r2", "shell", `{"command": "echo r2"}`),
		obsPackCallStep("call_r3", "shell", `{"command": "echo r3"}`),
		func(llm.Request) llm.Response { return finalResponse("done") },
	})
	obsPackRun(t, reducedSess)

	reqs := reducedAdapter.Requests()
	if len(reqs) != 6 {
		t.Fatalf("reduced arm saw %d requests, want 6", len(reqs))
	}
	receipt := obsPackMustResult(t, reqs[2], "call_log", "round 2")
	if !strings.HasPrefix(receipt, logReceiptMarker) {
		t.Fatalf("reduced arm did not produce a receipt:\n%.300s", receipt)
	}
	for i := 3; i < len(reqs); i++ {
		if got := obsPackMustResult(t, reqs[i], "call_log", strconv.Itoa(i+1)); got != receipt {
			t.Fatalf("request %d re-packed or altered the receipt (receipts must never pack)", i+1)
		}
	}
	reducedSess.obsPack.mu.Lock()
	_, packed := reducedSess.obsPack.entries["call_log"]
	reducedSess.obsPack.mu.Unlock()
	if packed {
		t.Fatal("a receipt was registered for observation packing")
	}

	// Declined arm, same flag-on-both configuration: the reducer declines an
	// eligible log (the extraction fails verification), so the original
	// observation flows untouched; and the packer still packs an oversized
	// ORIGINAL — the > 10 KiB file-read result — after its two full looks.
	// (A shell log over the shell tool's 8 KiB ride-whole budget is digested
	// by the shell tool itself before the reducer ever sees it, so the
	// oversized original the packer composes with here is a file read, and
	// the declined shell log stays whole below both thresholds.)
	dir := t.TempDir()
	bigBody := obsPackBody()
	bigPath := filepath.Join(dir, "big.txt")
	if err := os.WriteFile(bigPath, []byte(bigBody), 0o644); err != nil {
		t.Fatalf("write big fixture: %v", err)
	}
	declinedCommand := logRedCommand(120)
	declinedSess, declinedAdapter := logRedSession(t, SessionConfig{
		LogReducer:         true,
		ObservationPacking: true,
	}, []func(req llm.Request) llm.Response{
		logRedShellCallStep("call_big", declinedCommand),
		logRedExtractionStep(t, func(valid map[string]any) map[string]any {
			valid["quoted_lines"] = []any{"THIS LINE IS NOT IN THE LOG"}
			return valid
		}(logRedValidPayload(declinedCommand))),
		obsPackCallStep("call_read", "read_file", `{"file_path": "`+bigPath+`"}`),
		obsPackCallStep("call_d1", "shell", `{"command": "echo d1"}`),
		obsPackCallStep("call_d2", "shell", `{"command": "echo d2"}`),
		obsPackCallStep("call_d3", "shell", `{"command": "echo d3"}`),
		func(llm.Request) llm.Response { return finalResponse("done") },
	})
	obsPackRun(t, declinedSess)

	dreqs := declinedAdapter.Requests()
	if len(dreqs) != 7 {
		t.Fatalf("declined arm saw %d requests, want 7 (round 1, extraction, rounds 2-6)", len(dreqs))
	}
	if dreqs[1].Model != logRedCheapModel {
		t.Fatalf("declined arm request 2 model = %q, want the cheap model (the extraction must run before it can fail)", dreqs[1].Model)
	}
	// The declined shell log keeps its original bytes on every request,
	// never packed (it is under the packing threshold) and never a receipt.
	declined := obsPackMustResult(t, dreqs[2], "call_big", "round 2")
	if strings.HasPrefix(declined, logReceiptMarker) || !strings.Contains(declined, "line-120-") {
		t.Fatalf("declined log was reduced or truncated: %d bytes", len(declined))
	}
	for i := 3; i < len(dreqs); i++ {
		if got := obsPackMustResult(t, dreqs[i], "call_big", strconv.Itoa(i+1)); got != declined {
			t.Fatalf("declined request %d altered the original log", i+1)
		}
	}
	// The oversized ORIGINAL (the > 10 KiB file read) still packs after its
	// two full looks: the fallback path composes with ObservationPack.
	fullRead := obsPackMustResult(t, dreqs[3], "call_read", "round 3")
	if len(fullRead) <= observationPackThresholdBytes {
		t.Fatalf("big read is %d bytes, want over the packing threshold", len(fullRead))
	}
	if got := obsPackMustResult(t, dreqs[4], "call_read", "round 4"); got != fullRead {
		t.Fatal("second full look of the big read differs from the first")
	}
	packedView := obsPackMustResult(t, dreqs[5], "call_read", "round 5")
	if packedView == fullRead || !strings.Contains(packedView, "packed observation") {
		t.Fatalf("the oversized original did not pack after its two full looks:\n%.300s", packedView)
	}
}

// TestLogReducer_FusedRunAfterObservationNotReduced pins the Action Fusion
// interaction ruling: a fused observation (mutation report plus run_after
// command output) is not reducer-eligible — the mutation report must stay
// whole — and no extraction call is made for it.
func TestLogReducer_FusedRunAfterObservationNotReduced(t *testing.T) {
	t.Parallel()
	fusionCommand := `for i in $(seq 1 120); do printf 'line-%03d-abcdefghijklmnopqrstuvwxyz0123456789\n' "$i"; done; echo PASS; echo "go test done"`
	patch := "*** Begin Patch\n*** Add File: fusion-reduce.txt\n+alpha\n*** End Patch\n"
	fusionArgs, err := json.Marshal(map[string]any{"patch": patch, "run_after": fusionCommand})
	if err != nil {
		t.Fatalf("marshal fusion args: %v", err)
	}
	sess, adapter := logRedSession(t, SessionConfig{
		LogReducer:   true,
		ActionFusion: true,
	}, []func(req llm.Request) llm.Response{
		fusionCallStep("call_fused", "apply_patch", string(fusionArgs)),
		func(llm.Request) llm.Response { return finalResponse("done") },
	}, t.TempDir())
	obsPackRun(t, sess)

	reqs := adapter.Requests()
	if len(reqs) != 2 {
		t.Fatalf("scripted adapter saw %d requests, want 2: a fused observation must not trigger an extraction call", len(reqs))
	}
	fused := obsPackMustResult(t, reqs[1], "call_fused", "final")
	if strings.HasPrefix(fused, logReceiptMarker) {
		t.Fatalf("fused observation was reduced:\n%.300s", fused)
	}
	if !strings.Contains(fused, "[run_after] $ "+fusionCommand) || !strings.Contains(fused, "line-120-") {
		t.Fatalf("fused observation lost its run_after section: %d bytes", len(fused))
	}
}

// TestLogReducer_SnapshotRoundTrip pins that the flag rides the
// toSnapshot/configFromSnapshot converter pair, so child sessions built from
// the parent's snapshot and sessions restored from meta.json inherit it, and
// defaults to off.
func TestLogReducer_SnapshotRoundTrip(t *testing.T) {
	t.Parallel()
	in := SessionConfig{LogReducer: true}
	out := configFromSnapshot(in.toSnapshot().Clone())
	if !out.LogReducer {
		t.Fatal("LogReducer did not survive the snapshot round trip: delegates and restored sessions would silently run unreduced")
	}
	off := SessionConfig{}
	if configFromSnapshot(off.toSnapshot()).LogReducer {
		t.Fatal("default-off flag turned on by the snapshot round trip")
	}
}

// TestLogReducer_EligibilityTable pins the pure eligibility predicate: only
// foreground, complete, successful-dispatch shell results from the declared
// command set at or over 4 KiB are candidates.
func TestLogReducer_EligibilityTable(t *testing.T) {
	t.Parallel()
	foreground := func(exit int, jobID string, truncated bool) json.RawMessage {
		state := shellToolResult{
			Type:      "shell",
			Status:    "completed",
			Mode:      string(shellModeForeground),
			ExitCode:  &exit,
			JobID:     jobID,
			Truncated: &truncated,
		}
		b, err := json.Marshal(state)
		if err != nil {
			t.Fatalf("marshal shell state: %v", err)
		}
		return b
	}
	declared := logRedCommand(120)
	cases := []struct {
		name   string
		call   llm.ToolCallData
		res    tool.ExecResult
		wantOK bool
	}{
		{
			name:   "declared foreground big log",
			call:   llm.ToolCallData{ID: "c1", Name: "shell", Arguments: json.RawMessage(`{"command":"` + jsonEsc(declared) + `"}`)},
			res:    tool.ExecResult{ToolName: "shell", CallID: "c1", Output: strings.Repeat("line\n", 1024), ToolState: foreground(0, "", false)},
			wantOK: true,
		},
		{
			name:   "file read bypasses",
			call:   llm.ToolCallData{ID: "c2", Name: "read_file", Arguments: json.RawMessage(`{"file_path":"x"}`)},
			res:    tool.ExecResult{ToolName: "read_file", CallID: "c2", Output: strings.Repeat("line\n", 1024), ToolState: foreground(0, "", false)},
			wantOK: false,
		},
		{
			name:   "search result bypasses",
			call:   llm.ToolCallData{ID: "c3", Name: "grep", Arguments: json.RawMessage(`{"pattern":"x"}`)},
			res:    tool.ExecResult{ToolName: "grep", CallID: "c3", Output: strings.Repeat("line\n", 1024)},
			wantOK: false,
		},
		{
			name:   "undeclared command",
			call:   llm.ToolCallData{ID: "c4", Name: "shell", Arguments: json.RawMessage(`{"command":"cat big.log"}`)},
			res:    tool.ExecResult{ToolName: "shell", CallID: "c4", Output: strings.Repeat("line\n", 1024), ToolState: foreground(0, "", false)},
			wantOK: false,
		},
		{
			name:   "sub-threshold log",
			call:   llm.ToolCallData{ID: "c5", Name: "shell", Arguments: json.RawMessage(`{"command":"go test ./..."}`)},
			res:    tool.ExecResult{ToolName: "shell", CallID: "c5", Output: strings.Repeat("line\n", 102), ToolState: foreground(0, "", false)},
			wantOK: false,
		},
		{
			name:   "dispatch error result",
			call:   llm.ToolCallData{ID: "c6", Name: "shell", Arguments: json.RawMessage(`{"command":"go test ./..."}`)},
			res:    tool.ExecResult{ToolName: "shell", CallID: "c6", Output: strings.Repeat("line\n", 1024), IsError: true, ToolState: foreground(0, "", false)},
			wantOK: false,
		},
		{
			name:   "registry-truncated result",
			call:   llm.ToolCallData{ID: "c7", Name: "shell", Arguments: json.RawMessage(`{"command":"go test ./..."}`)},
			res:    tool.ExecResult{ToolName: "shell", CallID: "c7", Output: strings.Repeat("line\n", 1024), Truncated: true, ToolState: foreground(0, "", false)},
			wantOK: false,
		},
		{
			name:   "windowed job result",
			call:   llm.ToolCallData{ID: "c8", Name: "shell", Arguments: json.RawMessage(`{"command":"go test ./..."}`)},
			res:    tool.ExecResult{ToolName: "shell", CallID: "c8", Output: strings.Repeat("line\n", 1024), ToolState: foreground(0, "job_1", true)},
			wantOK: false,
		},
		{
			name: "background result",
			call: llm.ToolCallData{ID: "c9", Name: "shell", Arguments: json.RawMessage(`{"command":"go test ./..."}`)},
			res: tool.ExecResult{ToolName: "shell", CallID: "c9", Output: strings.Repeat("line\n", 1024), ToolState: func() json.RawMessage {
				state := shellToolResult{Type: "shell", Status: "running", Mode: string(shellModeBackground), JobID: "job_1"}
				b, _ := json.Marshal(state)
				return b
			}()},
			wantOK: false,
		},
		{
			name:   "no structured exit status",
			call:   llm.ToolCallData{ID: "c10", Name: "shell", Arguments: json.RawMessage(`{"command":"go test ./..."}`)},
			res:    tool.ExecResult{ToolName: "shell", CallID: "c10", Output: strings.Repeat("line\n", 1024)},
			wantOK: false,
		},
		{
			name:   "non-zero exit is eligible",
			call:   llm.ToolCallData{ID: "c11", Name: "shell", Arguments: json.RawMessage(`{"command":"go test ./..."}`)},
			res:    tool.ExecResult{ToolName: "shell", CallID: "c11", Output: strings.Repeat("line\n", 1024), ToolState: foreground(1, "", false)},
			wantOK: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			command, exit, ok := logReducerCandidate(tc.call, tc.res)
			if ok != tc.wantOK {
				t.Fatalf("logReducerCandidate ok = %v, want %v", ok, tc.wantOK)
			}
			if ok {
				if command == "" || exit != 0 && exit != 1 {
					t.Fatalf("eligible candidate lost the command or exit status: %q / %d", command, exit)
				}
			}
		})
	}
}

// TestLogReducer_ReduceLogObservationSeam exercises the whole reduction
// pipeline directly on a synthetic call and result, so the session-side gates
// (flag, cheap model, archive, extraction, verification) are observable
// independently of the shell tool.
func TestLogReducer_ReduceLogObservationSeam(t *testing.T) {
	t.Parallel()
	command := logRedCommand(120)
	source := logRedSource(120)
	state := shellToolResult{
		Type:     "shell",
		Status:   "completed",
		Mode:     string(shellModeForeground),
		ExitCode: new(int),
	}
	stateBytes, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal shell state: %v", err)
	}
	call := llm.ToolCallData{ID: "call_log", Name: "shell", Arguments: func() json.RawMessage {
		args, err := json.Marshal(map[string]any{"command": command})
		if err != nil {
			t.Fatalf("marshal call args: %v", err)
		}
		return args
	}()}
	res := tool.ExecResult{ToolName: "shell", CallID: "call_log", Output: source, ToolState: stateBytes}

	// Configured + honest extraction: a verified receipt comes back.
	sess, adapter := logRedSession(t, SessionConfig{LogReducer: true}, []func(req llm.Request) llm.Response{
		logRedExtractionStep(t, logRedValidPayload(command)),
	})
	receipt := sess.reduceLogObservation(context.Background(), call, res)
	if !strings.HasPrefix(receipt, logReceiptMarker) {
		t.Fatalf("seam reduction produced no receipt (gates failed upstream):\n%.200s", receipt)
	}
	if got := adapter.Requests(); len(got) != 1 || got[0].Model != logRedCheapModel {
		t.Fatalf("extraction calls = %d, want exactly one on the cheap model", len(got))
	}

	// Flag off: no call, no receipt.
	offSess, offAdapter := logRedSession(t, SessionConfig{}, []func(req llm.Request) llm.Response{})
	if got := offSess.reduceLogObservation(context.Background(), call, res); got != "" {
		t.Fatalf("flag-off seam produced %d bytes, want none", len(got))
	}
	if got := offAdapter.Requests(); len(got) != 0 {
		t.Fatalf("flag-off seam made %d provider calls, want none", len(got))
	}

	// Unconfigured cheap model: no call, no receipt, no warning.
	unCfg := SessionConfig{LogReducer: true, MaxSubagentDepth: 1, NoProjectPrompts: true,
		testOnly: testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true}}
	unSess, err := NewSession(llm.NewClient(), NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), unCfg)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { unSess.Close() })
	if got := unSess.reduceLogObservation(context.Background(), call, res); got != "" {
		t.Fatalf("unconfigured seam produced %d bytes, want none", len(got))
	}
}

// jsonEsc JSON-escapes s for embedding inside a JSON string literal in a
// raw-message literal.
func jsonEsc(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return strings.Trim(string(b), `"`)
}

// --- deterministic verifier (pure code) ---

// logRedReceiptFixture builds an honest receipt for a synthetic source.
func logRedReceiptFixture(source, command, ref string) *logReceipt {
	sum := sha256.Sum256([]byte(source))
	lines := strings.Split(strings.TrimRight(source, "\n"), "\n")
	return &logReceipt{
		Command:       command,
		ExitStatus:    0,
		SourceHash:    hex.EncodeToString(sum[:]),
		OriginalBytes: len(source),
		QuotedLines:   []string{lines[0], lines[len(lines)-1]},
		Summary:       "Two lines passed, zero failed.",
		ArchiveRef:    ref,
	}
}

func logRedSource(n int) string {
	var b strings.Builder
	b.WriteString("=== go test ./... ===\n")
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "line-%03d-abcdefghijklmnopqrstuvwxyz0123456789\n", i)
	}
	b.WriteString("PASS\n")
	b.WriteString("[exit 0]")
	return b.String()
}

func TestVerifyLogReceiptAcceptsHonestReceipt(t *testing.T) {
	t.Parallel()
	source := logRedSource(120)
	r := logRedReceiptFixture(source, "go test ./...", "artifact:abc")
	if err := verifyLogReceipt(r, source, "go test ./...", 0); err != nil {
		t.Fatalf("honest receipt rejected: %v", err)
	}
}

func TestVerifyLogReceiptRejectionLegs(t *testing.T) {
	t.Parallel()
	const command = "go test ./..."
	source := logRedSource(120)
	base := func() *logReceipt { return logRedReceiptFixture(source, command, "artifact:abc") }
	cases := []struct {
		name string
		mut  func(*logReceipt)
	}{
		{name: "empty command", mut: func(r *logReceipt) { r.Command = "" }},
		{name: "empty summary", mut: func(r *logReceipt) { r.Summary = "" }},
		{name: "no quoted lines", mut: func(r *logReceipt) { r.QuotedLines = nil }},
		{name: "empty quoted line", mut: func(r *logReceipt) { r.QuotedLines = []string{""} }},
		{name: "too many quoted lines", mut: func(r *logReceipt) {
			lines := make([]string, logReducerMaxQuotedLines+1)
			for i := range lines {
				lines[i] = "line-1-abcdefghijklmnopqrstuvwxyz0123456789"
			}
			r.QuotedLines = lines
		}},
		{name: "oversized quoted line", mut: func(r *logReceipt) { r.QuotedLines = []string{strings.Repeat("x", logReducerMaxQuotedLineBytes+1)} }},
		{name: "command mismatch", mut: func(r *logReceipt) { r.Command = "go test ./other" }},
		{name: "exit status mismatch", mut: func(r *logReceipt) { r.ExitStatus = 1 }},
		{name: "source hash mismatch", mut: func(r *logReceipt) { r.SourceHash = strings.Repeat("ab", 32) }},
		{name: "malformed source hash", mut: func(r *logReceipt) { r.SourceHash = "not-a-hash" }},
		{name: "original bytes mismatch", mut: func(r *logReceipt) { r.OriginalBytes = len(source) + 1 }},
		{name: "quoted line not in source", mut: func(r *logReceipt) { r.QuotedLines = []string{"THIS LINE IS NOT IN THE LOG"} }},
		{name: "quoted line is not a whole source line", mut: func(r *logReceipt) { r.QuotedLines = []string{"line-1-abc"} }},
		{name: "empty archive ref", mut: func(r *logReceipt) { r.ArchiveRef = "" }},
		{name: "credential in receipt summary", mut: func(r *logReceipt) {
			r.Summary = "leaked token ghp_0123456789abcdefghijklmnopqrstuv"
		}},
		{name: "credential in quoted line", mut: func(r *logReceipt) {
			r.QuotedLines = []string{"export AWS_KEY=AKIAIOSFODNN7EXAMPLE"}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := base()
			tc.mut(r)
			if err := verifyLogReceipt(r, source, command, 0); err == nil {
				t.Fatal("verifier accepted a dishonest receipt")
			}
		})
	}
}

// TestVerifyLogReceiptRejectsNotSmaller pins the strict size-reduction check
// in isolation: a maximal honest receipt against a log so small the receipt
// renders larger than it is refused (and such logs are also under the 4 KiB
// candidate threshold end to end).
func TestVerifyLogReceiptRejectsNotSmaller(t *testing.T) {
	t.Parallel()
	const command = "go test ./..."
	source := logRedSource(3)
	sum := sha256.Sum256([]byte(source))
	lines := make([]string, logReducerMaxQuotedLines)
	for i := range lines {
		lines[i] = "line-1-abcdefghijklmnopqrstuvwxyz0123456789"
	}
	r := &logReceipt{
		Command:     command,
		ExitStatus:  0,
		SourceHash:  hex.EncodeToString(sum[:]),
		QuotedLines: lines,
		Summary:     "many lines",
		ArchiveRef:  "artifact:abc",
	}
	if err := verifyLogReceipt(r, source, command, 0); err == nil {
		t.Fatal("verifier accepted a receipt larger than its source log")
	}
}

// TestVerifyLogReceiptTrailingWhitespaceTolerated pins the quoted-line
// matching rule: trailing whitespace differences (CRLF, trailing spaces)
// do not fail an otherwise verbatim quote, but any other difference does.
func TestVerifyLogReceiptTrailingWhitespaceTolerated(t *testing.T) {
	t.Parallel()
	const command = "go test ./..."
	var sourceB strings.Builder
	sourceB.WriteString("=== go test ./... ===\r\nok  pkg 1.2s  \n")
	for i := 1; i <= 150; i++ {
		fmt.Fprintf(&sourceB, "fill-%03d-abcdefghijklmnopqrstuvwxyz0123456789\n", i)
	}
	sourceB.WriteString("[exit 0]")
	source := sourceB.String()
	sum := sha256.Sum256([]byte(source))
	r := &logReceipt{
		Command:       command,
		ExitStatus:    0,
		SourceHash:    hex.EncodeToString(sum[:]),
		OriginalBytes: len(source),
		QuotedLines:   []string{"=== go test ./... ===", "ok  pkg 1.2s"},
		Summary:       "one package ok",
		ArchiveRef:    "artifact:abc",
	}
	if err := verifyLogReceipt(r, source, command, 0); err != nil {
		t.Fatalf("trailing-whitespace tolerance missing: %v", err)
	}
}

func TestLogReducerCredentialDetectionPatterns(t *testing.T) {
	t.Parallel()
	hits := []string{
		"aws access key AKIAIOSFODNN7EXAMPLE here",
		"-----BEGIN RSA PRIVATE KEY-----",
		"-----BEGIN OPENSSH PRIVATE KEY-----",
		"token ghp_0123456789abcdefghijklmnopqrstuv leaked",
		"token gho_0123456789abcdefghijklmnopqrstuv leaked",
		"slack xoxb-123456789-abcdefghijkl",
		"google AIzaSyA1234567890_-abcdefghijklmnopqrstuv",
		"openai sk-ant-abc123XYZdef456GHijkl789MN",
		"password: hunter2hunter2",
		"API_TOKEN=\"abcdef12345678\"",
		"Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9",
	}
	for _, s := range hits {
		if logReducerCredentialHit(s) == "" {
			t.Errorf("credential pattern missed %q", s)
		}
	}
	clean := []string{
		"go test ./... ok",
		"PASS",
		"ok  	example.com/pkg	1.2s",
		"make test",
		"AKIA too short to be a key",
		"ghp_ short",
		"--- FAIL: TestSomething (0.00s)",
	}
	for _, s := range clean {
		if hit := logReducerCredentialHit(s); hit != "" {
			t.Errorf("clean log line %q tripped credential pattern %q", s, hit)
		}
	}
}

func TestLogReducerBoundaryCredentialWindow(t *testing.T) {
	t.Parallel()
	// Credential at line 150 of 400: outside the 64-line head/tail boundary.
	var mid strings.Builder
	mid.WriteString("head line\n")
	for i := 2; i < 400; i++ {
		mid.WriteString("line-")
		mid.WriteString(strconv.Itoa(i))
		mid.WriteString("\n")
	}
	mid.WriteString("tail line\n")
	// 399 middle lines; place the credential at index 150.
	lines := strings.Split(mid.String(), "\n")
	lines[150] = "leaked ghp_0123456789abcdefghijklmnopqrstuv"
	source := strings.Join(lines, "\n")
	if hit := logReducerBoundaryCredential(source); hit != "" {
		t.Fatalf("credential deep inside the log tripped the boundary scan: %q", hit)
	}
	// Same credential in the head and tail windows must trip.
	head := "leaked ghp_0123456789abcdefghijklmnopqrstuv\n" + strings.Repeat("line\n", 400)
	if hit := logReducerBoundaryCredential(head); hit == "" {
		t.Fatal("credential in the head boundary was not detected")
	}
	tail := strings.Repeat("line\n", 400) + "leaked ghp_0123456789abcdefghijklmnopqrstuv"
	if hit := logReducerBoundaryCredential(tail); hit == "" {
		t.Fatal("credential in the tail boundary was not detected")
	}
}

// TestLogReducerReceiptMarkerRecognition pins the packer-recognition shape:
// only content beginning with the receipt marker is recognized.
func TestLogReducerReceiptMarkerRecognition(t *testing.T) {
	t.Parallel()
	if !isLogReceiptContent("[log-reduced receipt: original 100 bytes] stuff") {
		t.Fatal("receipt content not recognized")
	}
	if isLogReceiptContent("[exit 0] ordinary log") || isLogReceiptContent("") {
		t.Fatal("ordinary content recognized as a receipt")
	}
}

// TestLogReducerDeclaredSetMatchesOracle pins that the reducer's declared
// command set is the oracle's, so the oracle's log-reducer projection keeps
// ranking this mechanism against exactly the commands it reduces. The
// behavioral half runs the read-only oracle over fixture transcripts whose
// shell commands the reducer itself also judges.
func TestLogReducerDeclaredSetMatchesOracle(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	sessionsDir := filepath.Join(dir, "projects", "proj", "sessions")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	log := strings.Repeat("FAIL line\n", 600) // ~5.4 KiB, over the 4 KiB threshold
	writeOracleFixture(t, sessionsDir, "declared", "go test ./...", log)
	writeOracleFixture(t, sessionsDir, "undeclared", "cat build.log", log)
	rep, err := research.RunOracle(research.OracleOptions{StateBase: dir, Stdout: io.Discard})
	if err != nil {
		t.Fatalf("RunOracle: %v", err)
	}
	if rep.LogVolume.Results != 1 || rep.LogVolume.TotalBytes != len(log) {
		t.Fatalf("oracle log volume = %d results / %d bytes, want 1 / %d (the oracle's declared set disagrees with the reducer's)", rep.LogVolume.Results, rep.LogVolume.TotalBytes, len(log))
	}
	// The reducer's predicate must agree with the oracle's effective filter.
	for _, declared := range []string{"go test ./...", "go build ./...", "go vet ./...", "make", "make test", "make all", "npm test", "npm run build", "npm run test", "pytest -x", "cargo test", "cargo build"} {
		if !logReducerCommandRe.MatchString(declared) {
			t.Errorf("declared command %q does not match the reducer's set", declared)
		}
	}
	for _, undeclared := range []string{"cat build.log", "ls -la", "grep -rn pattern .", "npm run lint", "cargo fmt", "echo go.test", "pytestx"} {
		if logReducerCommandRe.MatchString(undeclared) {
			t.Errorf("undeclared command %q matches the reducer's set", undeclared)
		}
	}
}

// writeOracleFixture writes a minimal valid transcript whose one shell call
// ran command and returned a 4 KiB+ result.
func writeOracleFixture(t *testing.T, sessionsDir, sid, command, resultText string) {
	t.Helper()
	args, err := json.Marshal(map[string]any{"command": command})
	if err != nil {
		t.Fatal(err)
	}
	entries := []transcript.Entry{
		{Kind: "entry", Turn: schema.Turn{
			Kind: schema.TurnAssistant,
			Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
				{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "c_" + sid, Name: "shell", Arguments: args}},
			}},
		}},
		{Kind: "entry", Turn: schema.Turn{
			Kind: schema.TurnToolResults,
			Message: llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{
				{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "c_" + sid, Name: "shell", Content: resultText}},
			}},
		}},
	}
	var b strings.Builder
	b.WriteString(`{"kind":"header","format_version":` + strconv.Itoa(transcript.FormatVersion) + "}\n")
	for i := range entries {
		line, err := json.Marshal(entries[i])
		if err != nil {
			t.Fatal(err)
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	path := filepath.Join(sessionsDir, sid+".transcript.jsonl")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestLogReducerCommandRegexMirrorsOracleLiteral pins the literal sync with
// the oracle's researchTestBuildRe (agent/research/oracle.go). That pattern is
// unexported and agent/research is read-only for this lineage, so the sync is
// pinned by this literal plus the behavioral RunOracle test above.
func TestLogReducerCommandRegexMirrorsOracleLiteral(t *testing.T) {
	t.Parallel()
	const oracleLiteral = `\b(go test|go build|go vet|make(?:\s+\S+)?|npm test|npm run (?:test|build)|pytest|cargo (?:test|build))\b`
	if logReducerCommandRe.String() != oracleLiteral {
		t.Fatalf("logReducerCommandRe = %q, want the oracle's researchTestBuildRe literal %q (agent/research/oracle.go); the two must stay in sync", logReducerCommandRe.String(), oracleLiteral)
	}
}
