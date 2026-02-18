# Immutable Transcript Logging — Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add an append-only JSONL transcript log as a core Session feature, recording every turn (including compaction) for crash-resilient, lossless conversation history.

**Architecture:** A `TranscriptWriter` struct with a persistent file handle and its own mutex writes one JSON line per turn to `<stateDir>/sessions/<sessionID>.transcript.jsonl`. Compaction layers 3 and 4 append their synthetic turns to the transcript via `appendTurn()`. Resume reads the transcript to reconstruct history. Sub-agents create their own transcript files with parent-child linkage.

**Tech Stack:** Go stdlib (`encoding/json`, `os`, `sync`, `bufio`), existing `agent` package types

**Design doc:** `docs/plans/2026-02-18-immutable-transcript-design.md`

---

### Task 1: Add TurnCheckpoint and TurnSummary to TurnKind

**Files:**
- Modify: `agent/turns.go:11-17`
- Test: `agent/turns_test.go`

**Step 1: Write the failing test**

In `agent/turns_test.go`, add a test that asserts the new `TurnKind` constants exist and have distinct string values:

```go
func TestTurnKind_CheckpointAndSummary(t *testing.T) {
	if TurnCheckpoint != "CHECKPOINT" {
		t.Fatalf("TurnCheckpoint = %q, want CHECKPOINT", TurnCheckpoint)
	}
	if TurnSummary != "SUMMARY" {
		t.Fatalf("TurnSummary = %q, want SUMMARY", TurnSummary)
	}
}
```

**Step 2: Run test to verify it fails**

Run: `cd /Users/jesse/prime-radiant/serf/.worktrees/rlm-context && go test ./agent/ -run TestTurnKind_CheckpointAndSummary -v`
Expected: FAIL — `TurnCheckpoint` undefined

**Step 3: Write minimal implementation**

Add to the `const` block in `agent/turns.go`:

```go
TurnCheckpoint  TurnKind = "CHECKPOINT" // Deterministic checkpoint from compaction Layer 3.
TurnSummary     TurnKind = "SUMMARY"    // LLM-generated summary from compaction Layer 4.
```

**Step 4: Run test to verify it passes**

Run: `cd /Users/jesse/prime-radiant/serf/.worktrees/rlm-context && go test ./agent/ -run TestTurnKind_CheckpointAndSummary -v`
Expected: PASS

**Step 5: Run all existing tests to check for regressions**

Run: `cd /Users/jesse/prime-radiant/serf/.worktrees/rlm-context && go test ./agent/ -short -count=1`
Expected: All pass (adding constants is additive)

**Step 6: Commit**

```bash
git add agent/turns.go agent/turns_test.go
git commit -m "feat: add TurnCheckpoint and TurnSummary turn kinds"
```

---

### Task 2: TranscriptWriter — core type with Append and Close

This is the core data structure. It does not integrate with Session yet.

**Files:**
- Create: `agent/transcript.go`
- Create: `agent/transcript_test.go`

**Step 1: Write failing tests for TranscriptWriter**

Create `agent/transcript_test.go`. Tests to write (in this order):

1. `TestTranscriptWriter_CreatesFileAndWritesHeader` — `NewTranscriptWriter(dir, header)` creates the file and writes the header as the first line. Parse line 1 and verify `kind == "header"` and fields match.

2. `TestTranscriptWriter_AppendWritesEntries` — After creating a writer, call `Append(turn)` twice. Read the file and verify lines 2 and 3 are valid `TranscriptEntry` JSON with seq 0 and 1, and the embedded Turn data matches.

3. `TestTranscriptWriter_SeqMonotonicallyIncreasing` — Append 10 turns, read all entries, verify seq goes 0,1,2,...,9.

4. `TestTranscriptWriter_CloseClosesFile` — After `Close()`, the file should be readable and complete. A second `Close()` should not panic (idempotent).

5. `TestTranscriptWriter_NilWriterSafe` — Calling `Append` and `Close` on a nil `*TranscriptWriter` should not panic.

6. `TestTranscriptWriter_ConcurrentAppend` — 10 goroutines each append 10 turns. Verify total line count = header + 100 entries, all valid JSONL.

7. `TestTranscriptWriter_ValidJSONL` — Append turns with various content (text, tool calls, tool results, thinking). Read every line, verify each parses as valid JSON independently.

**Important test pattern:** Use `t.TempDir()` for all file operations. Use `bufio.Scanner` to read lines. Use `json.Unmarshal` to parse each line. The test helper `readTranscriptLines(t, path) []string` will be useful.

**Step 2: Run tests to verify they fail**

Run: `cd /Users/jesse/prime-radiant/serf/.worktrees/rlm-context && go test ./agent/ -run TestTranscriptWriter -v`
Expected: FAIL — types not defined

**Step 3: Implement TranscriptWriter**

Create `agent/transcript.go` with these types and functions:

```go
package agent

// TranscriptHeader is the first line of a transcript JSONL file.
type TranscriptHeader struct {
	Kind             string    `json:"kind"`              // Always "header"
	FormatVersion    int       `json:"format_version"`    // Currently 1
	SessionID        string    `json:"session_id"`
	ParentSessionID  string    `json:"parent_session_id,omitempty"`
	ParentToolCallID string    `json:"parent_tool_call_id,omitempty"`
	Task             string    `json:"task,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
	ProfileID        string    `json:"profile_id"`
	Model            string    `json:"model"`
	WorkingDir       string    `json:"working_dir,omitempty"`
	Depth            int       `json:"depth,omitempty"`
}

// TranscriptEntry is a single turn in the transcript JSONL file.
type TranscriptEntry struct {
	Kind string `json:"kind"` // Always "entry"
	Seq  int    `json:"seq"`
	Turn Turn   `json:"turn"`
}

// TranscriptWriter appends turns to an immutable JSONL transcript file.
type TranscriptWriter struct {
	file *os.File
	mu   sync.Mutex
	seq  int
}
```

Implement:
- `NewTranscriptWriter(path string, header TranscriptHeader) (*TranscriptWriter, error)` — creates dirs, creates file, writes header line, fsyncs. On any failure, returns nil + error. The header line is `json.Marshal(header)` + `\n`.
- `(*TranscriptWriter) Append(turn Turn)` — if receiver is nil, no-op. Lock mu, assign seq, increment seq, marshal `TranscriptEntry{Kind: "entry", Seq: seq, Turn: turn}`, write line + `\n`, fsync. Returns error.
- `(*TranscriptWriter) Close() error` — if receiver is nil or file is nil, no-op. Sync and close. Use `sync.Once` for idempotency.

**Step 4: Run tests to verify they pass**

Run: `cd /Users/jesse/prime-radiant/serf/.worktrees/rlm-context && go test ./agent/ -run TestTranscriptWriter -v`
Expected: All PASS

**Step 5: Run all tests**

Run: `cd /Users/jesse/prime-radiant/serf/.worktrees/rlm-context && go test ./agent/ -short -count=1`
Expected: All pass

**Step 6: Commit**

```bash
git add agent/transcript.go agent/transcript_test.go
git commit -m "feat: add TranscriptWriter with append-only JSONL format"
```

---

### Task 3: TranscriptReader — restore from transcript

Needed for `RestoreSession` and for future `read_transcript` tool.

**Files:**
- Modify: `agent/transcript.go`
- Modify: `agent/transcript_test.go`

**Step 1: Write failing tests**

Add to `agent/transcript_test.go`:

1. `TestReadTranscript_ReturnsHeaderAndEntries` — Write a transcript with header + 5 entries. Call `ReadTranscript(path)`. Verify header fields and 5 entries with correct seq/turns.

2. `TestReadTranscript_PartialLastLine` — Write a transcript, then append a partial JSON line (no trailing newline, truncated). Call `ReadTranscript(path)`. Verify it returns all complete entries and ignores the partial line.

3. `TestReadTranscript_EmptyFile` — Create an empty file. `ReadTranscript` should return an error (no header).

4. `TestReadTranscript_HeaderOnly` — Write just a header, no entries. `ReadTranscript` returns the header and empty entries slice.

5. `TestResumeHistoryFromTranscript_NoCompaction` — Write header + 5 conversation turns (USER_INPUT, ASSISTANT, TOOL_RESULTS, ASSISTANT, USER_INPUT). Call `ResumeHistory(entries)`. Verify it returns all 5 turns.

6. `TestResumeHistoryFromTranscript_WithCheckpoint` — Write header + 10 turns where entry 7 is `TurnCheckpoint`. Call `ResumeHistory(entries)`. Verify it returns [checkpoint turn, entries 8, 9, 10] — i.e., the checkpoint plus everything after it.

7. `TestResumeHistoryFromTranscript_WithSummary` — Same but entry 7 is `TurnSummary`. Same result.

**Step 2: Run tests to verify they fail**

Run: `cd /Users/jesse/prime-radiant/serf/.worktrees/rlm-context && go test ./agent/ -run "TestReadTranscript|TestResumeHistory" -v`
Expected: FAIL

**Step 3: Implement**

Add to `agent/transcript.go`:

```go
// ReadTranscript reads a transcript JSONL file, returning the header and all valid entries.
// Partial/corrupt lines at the end are silently skipped (crash recovery).
func ReadTranscript(path string) (TranscriptHeader, []TranscriptEntry, error)
```

Implementation: open file, `bufio.Scanner`, parse line 1 as `TranscriptHeader` (error if missing or corrupt), parse remaining lines as `TranscriptEntry` (skip corrupt lines at the end only — a corrupt line in the middle should also be skipped to handle truncated writes).

```go
// ResumeHistory extracts the history needed for session resume from transcript entries.
// If a compaction turn (CHECKPOINT or SUMMARY) exists, returns [last compaction turn, ...subsequent turns].
// Otherwise returns all turns.
func ResumeHistory(entries []TranscriptEntry) []Turn
```

Implementation: scan entries backward for the last `TurnCheckpoint` or `TurnSummary`. Return that turn + all entries after it. If none found, return all turns.

**Step 4: Run tests**

Run: `cd /Users/jesse/prime-radiant/serf/.worktrees/rlm-context && go test ./agent/ -run "TestReadTranscript|TestResumeHistory" -v`
Expected: PASS

**Step 5: Run all tests**

Run: `cd /Users/jesse/prime-radiant/serf/.worktrees/rlm-context && go test ./agent/ -short -count=1`
Expected: All pass

**Step 6: Commit**

```bash
git add agent/transcript.go agent/transcript_test.go
git commit -m "feat: add ReadTranscript and ResumeHistory for transcript-based resume"
```

---

### Task 4: OpenTranscriptWriter — resume appending to existing transcript

For `RestoreSession()` to append new turns to an existing transcript file.

**Files:**
- Modify: `agent/transcript.go`
- Modify: `agent/transcript_test.go`

**Step 1: Write failing tests**

1. `TestOpenTranscriptWriter_AppendsToExisting` — Create a transcript with header + 5 entries. Call `OpenTranscriptWriter(path)`. Returns a writer with seq starting at 5. Append 3 more turns. Read the file — should have header + 8 entries with seq 0-7.

2. `TestOpenTranscriptWriter_TruncatesPartialLine` — Create a transcript with header + 3 entries, then append a partial JSON line. Call `OpenTranscriptWriter(path)`. The partial line should be truncated. Append 1 more turn. Read — should have header + 4 entries with seq 0-3.

3. `TestOpenTranscriptWriter_HeaderOnlyFile` — Write just a header. Open returns writer with seq 0.

**Step 2: Run tests, verify fail**

**Step 3: Implement**

```go
// OpenTranscriptWriter opens an existing transcript file for appending.
// Counts valid entries to determine the next seq number.
// Truncates any partial last line for crash recovery.
func OpenTranscriptWriter(path string) (*TranscriptWriter, error)
```

Implementation:
1. Read the file to count valid entries (parse each line, count `"kind":"entry"` lines)
2. Check if last byte is `\n`. If not, truncate to the last `\n` boundary.
3. Open in append mode (`O_WRONLY|O_APPEND`)
4. Return `&TranscriptWriter{file: f, seq: entryCount}`

**Step 4-6: Run tests, run all tests, commit**

```bash
git add agent/transcript.go agent/transcript_test.go
git commit -m "feat: add OpenTranscriptWriter for resuming transcript append"
```

---

### Task 5: Wire TranscriptWriter into Session

Connect the writer to `NewSession`, `appendTurn`, `appendAssistantTurn`, and `Close`.

**Files:**
- Modify: `agent/session.go:111-171` (Session struct — add `transcript *TranscriptWriter` field)
- Modify: `agent/session.go:200-322` (NewSession — create writer)
- Modify: `agent/session.go:327-448` (RestoreSession — open existing writer)
- Modify: `agent/session.go:547-595` (Close — close writer)
- Modify: `agent/session.go:683-701` (appendTurn, appendAssistantTurn — append to transcript)
- Test: `agent/transcript_test.go` (new integration tests)

**Step 1: Write failing tests**

Add to `agent/transcript_test.go`:

1. `TestSession_TranscriptCreatedOnNewSession` — Create a session with `StateDir` set. Verify `<stateDir>/sessions/<id>.transcript.jsonl` exists. Read it and verify header has correct session ID, profile, model.

2. `TestSession_TranscriptRecordsTurns` — Create session with StateDir, process one input (use `snapshotFakeAdapter`). Close session. Read transcript. Verify it has header + at least 2 entries (USER_INPUT, ASSISTANT). Verify seq numbers are 0, 1.

3. `TestSession_TranscriptRecordsToolResults` — Use a `fakeAdapter` that returns a tool call. Verify the transcript has entries for USER_INPUT, ASSISTANT (with tool call), TOOL_RESULTS, ASSISTANT (final response).

4. `TestSession_NoTranscriptWithoutStateDir` — Create session with empty StateDir. No transcript file should exist anywhere. No errors.

5. `TestSession_TranscriptSurvivesClose` — Create session, process input, close. File still exists and is readable.

Use the existing `fakeAdapter`, `snapshotFakeAdapter`, `communicateCall`, and `toolCallResponse` test helpers from `session_test.go`.

**Step 2: Run tests, verify fail**

**Step 3: Implement**

1. Add `transcript *TranscriptWriter` field to `Session` struct.

2. In `NewSession()`, after the session ID is assigned and before the first event emission, if `stateDir != ""`:
   ```go
   hdr := TranscriptHeader{
       Kind:          "header",
       FormatVersion: 1,
       SessionID:     s.id,
       CreatedAt:     time.Now().UTC(),
       ProfileID:     profile.ID(),
       Model:         profile.Model(),
       WorkingDir:    ei.WorkingDir,
   }
   if cfg.ParentSessionID != "" {
       hdr.ParentSessionID = cfg.ParentSessionID
   }
   path := filepath.Join(s.stateDir, sessionsSubdir, s.id+".transcript.jsonl")
   tw, err := NewTranscriptWriter(path, hdr)
   if err != nil {
       // Non-fatal: emit warning, proceed without transcript
       s.emit(EventWarning, WarningData{Message: fmt.Sprintf("transcript create failed: %v", err)})
   }
   s.transcript = tw
   ```

3. In `RestoreSession()`, if `stateDir != ""`:
   ```go
   path := filepath.Join(s.stateDir, sessionsSubdir, s.id+".transcript.jsonl")
   tw, err := OpenTranscriptWriter(path)
   if err != nil {
       // Might not exist yet (old session without transcript). Create new.
       hdr := TranscriptHeader{...same as above...}
       tw, err = NewTranscriptWriter(path, hdr)
       if err != nil {
           s.emit(EventWarning, WarningData{Message: fmt.Sprintf("transcript open failed: %v", err)})
       }
   }
   s.transcript = tw
   ```

4. In `appendTurn()`, after `s.history = append(...)` but still inside the lock, capture the turn. Then after `s.mu.Unlock()` (or restructure to unlock first, then write transcript):
   ```go
   func (s *Session) appendTurn(kind TurnKind, m llm.Message) {
       t := NewTurn(kind, m)
       s.mu.Lock()
       s.history = append(s.history, t)
       s.mu.Unlock()
       if err := s.transcript.Append(t); err != nil {
           s.emit(EventWarning, WarningData{Message: fmt.Sprintf("transcript write failed: %v", err)})
       }
   }
   ```
   Note: this changes `appendTurn` to construct the Turn before locking (to avoid holding `s.mu` during I/O). The Turn is immutable once created, so this is safe.

5. In `appendAssistantTurn()`, same pattern:
   ```go
   func (s *Session) appendAssistantTurn(resp llm.Response) {
       t := Turn{
           Kind:       TurnAssistant,
           Message:    resp.Message,
           Timestamp:  time.Now().UTC(),
           Usage:      resp.Usage,
           ResponseID: resp.ID,
       }
       s.mu.Lock()
       s.history = append(s.history, t)
       s.mu.Unlock()
       if err := s.transcript.Append(t); err != nil {
           s.emit(EventWarning, WarningData{Message: fmt.Sprintf("transcript write failed: %v", err)})
       }
   }
   ```

6. In `Close()`, inside `closeOnce.Do`, after closing MCP but before setting `s.state = SessionClosed`:
   ```go
   if s.transcript != nil {
       _ = s.transcript.Close()
   }
   ```

**Step 4-6: Run tests, run all tests, commit**

```bash
git add agent/session.go agent/transcript.go agent/transcript_test.go
git commit -m "feat: wire TranscriptWriter into Session lifecycle"
```

---

### Task 6: Sub-agent transcript linkage

Add `ParentSessionID` and `ParentToolCallID` to `SessionConfig`, set them in `spawnAgent()`.

**Files:**
- Modify: `agent/session.go:29-87` (SessionConfig — add fields)
- Modify: `agent/subagents.go:41-99` (spawnAgent — set parent fields)
- Modify: `agent/session.go:200-322` (NewSession — pass parent fields to header)
- Test: `agent/transcript_test.go`

**Step 1: Write failing tests**

1. `TestSubagent_TranscriptHasParentSessionID` — Create a parent session with StateDir. Use a `fakeAdapter` that returns a `spawn_agent` tool call, then after the agent runs, a final response. After the session completes, find the sub-agent's transcript file in the sessions dir (any `.transcript.jsonl` file whose header has a `parent_session_id`). Verify the parent_session_id matches the parent session's ID.

Note: this is a more complex integration test. You may need to set `MaxSubagentDepth: 1` and use the existing sub-agent test patterns. The sub-agent's adapter needs to return something simple so it completes quickly.

**Step 2: Run tests, verify fail**

**Step 3: Implement**

1. Add to `SessionConfig`:
   ```go
   ParentSessionID  string `json:"parent_session_id,omitempty"`
   ParentToolCallID string `json:"parent_tool_call_id,omitempty"`
   SubagentTask     string `json:"subagent_task,omitempty"`
   ```

2. In `spawnAgent()`, before `NewSession`:
   ```go
   subCfg.ParentSessionID = s.id
   subCfg.ParentToolCallID = "" // We don't have the tool call ID here; set it if available
   subCfg.SubagentTask = task
   ```
   Note: The tool call ID is available in the `calls` slice in `processOneInput` but not in `spawnAgent`. For v1, `ParentSessionID` is sufficient. The tool call ID can be threaded through later.

3. In `NewSession()`, when building the `TranscriptHeader`:
   ```go
   hdr.ParentSessionID = cfg.ParentSessionID
   hdr.ParentToolCallID = cfg.ParentToolCallID
   hdr.Task = cfg.SubagentTask
   hdr.Depth = 0 // set by depth field below
   ```
   And after `subSess.depth = depth + 1`, the depth is on the Session, not config. Thread it: add a `depth` parameter to `NewTranscriptWriter` header construction, or set the header depth from `s.depth` after creating the session. Simplest: set `hdr.Depth` in `spawnAgent` after `subSess.depth = depth + 1` by passing depth into the config or by adding a method to update the header. For simplicity, just set the depth on the config too:
   ```go
   subCfg.ParentSessionID = s.id
   subCfg.SubagentTask = task
   // depth will be set on the sub-session after creation
   ```
   And in NewSession, set `hdr.Depth = s.depth` (which is 0 for root sessions). Actually, depth isn't set until after NewSession returns in spawnAgent. So either:
   - Add a `Depth int` to SessionConfig and set it before NewSession
   - Or accept that root sessions have depth 0 in the header, and sub-agents also have depth 0 in the header (since depth is set after construction)

   Simplest correct approach: the `depth` field is already going to be 0 in the header for sub-agents because `spawnAgent` sets `subSess.depth = depth + 1` after `NewSession`. To fix this, just add `Depth int` to SessionConfig and pass it:
   ```go
   subCfg.Depth = depth + 1
   ```
   Then in NewSession: `s.depth = cfg.Depth` and `hdr.Depth = cfg.Depth`.

**Step 4-6: Run tests, run all tests, commit**

```bash
git add agent/session.go agent/subagents.go agent/transcript.go agent/transcript_test.go
git commit -m "feat: add parent-child linkage to sub-agent transcripts"
```

---

### Task 7: Compaction turns flow through appendTurn

Make Layer 3 (checkpoint) and Layer 4 (summarizeWithLLM) append their synthetic turns to the transcript via the session's `appendTurn` method instead of wholesale history replacement.

**Files:**
- Modify: `agent/context_manager.go:118-227` (MaybeCompact)
- Modify: `agent/context_manager.go:435-592` (checkpoint)
- Modify: `agent/context_manager.go:632-708` (summarizeWithLLM)
- Test: `agent/transcript_test.go`

**This is the most architecturally significant change.** Currently, MaybeCompact is called by strategies with a `*[]Turn` that it modifies in-place. The session copies history out, calls strategy, then writes back. The compaction functions (`checkpoint`, `summarizeWithLLM`) return new `[]Turn` slices.

The change: when compaction produces a checkpoint or summary turn, it needs to be appended to the transcript. But `checkpoint()` and `summarizeWithLLM()` don't have access to the Session or TranscriptWriter.

**Approach:** Add a callback parameter to MaybeCompact (or to the strategy interface) that the session provides, which appends compaction turns to the transcript. Or: have MaybeCompact return the compaction turns it created, and the caller (session) appends them to the transcript.

The cleanest approach is: MaybeCompact returns a `[]Turn` of compaction turns created (empty if none). The caller in `processOneInput` then appends them to the transcript.

But actually, the simplest change: after MaybeCompact modifies `histCopy`, the session already writes `s.history = histCopy`. At this point, inspect the new history for checkpoint/summary turns that weren't in the old history, and append them to the transcript.

Even simpler: change `checkpoint()` and `summarizeWithLLM()` to use the new `TurnCheckpoint`/`TurnSummary` kinds. Then in `processOneInput`, after `s.history = histCopy`, scan for any turns with these kinds that need to be transcribed.

Actually, the simplest correct approach:
1. Change `checkpoint()` to create its turn with `TurnCheckpoint` instead of `TurnUserInput`
2. Change `summarizeWithLLM()` to create its turn with `TurnSummary` instead of `TurnUserInput`
3. Add a `CompactionTurns []Turn` field on a return struct from MaybeCompact, or just have MaybeCompact accept a callback
4. After MaybeCompact runs in processOneInput, append any new compaction turns to the transcript

Wait — the system prompt builder and LLM request builder convert TurnKind to message roles. `TurnCheckpoint` and `TurnSummary` would need to be handled in the message construction loop in `processOneInput` (line 929-950). They should be treated as user-role messages (same as today's `TurnUserInput` for checkpoints).

**Step 1: Write failing tests**

1. `TestSession_TranscriptRecordsCheckpointTurns` — Create a session with a tiny context window (use `baseProfile` with `contextWindow: 500`). Use a `fakeAdapter` that returns big tool calls to fill the context. Process input. The compaction should fire. Read the transcript and verify there's at least one entry with `turn.kind == "CHECKPOINT"`.

2. `TestCheckpoint_UsesTurnCheckpointKind` — Call `checkpoint()` directly. Verify the first turn in the result has `Kind == TurnCheckpoint` (not `TurnUserInput`).

3. `TestSummarizeWithLLM_UsesTurnSummaryKind` — Call `summarizeWithLLM()` directly. Verify the first turn in the result has `Kind == TurnSummary`.

**Step 2: Run tests, verify fail**

**Step 3: Implement**

1. In `checkpoint()` (context_manager.go:586):
   Change `checkpointTurn := NewTurn(TurnUserInput, llm.User(b.String()))` to:
   ```go
   checkpointTurn := NewTurn(TurnCheckpoint, llm.User(b.String()))
   ```

2. In `summarizeWithLLM()` (context_manager.go:702):
   Change `summaryTurn := NewTurn(TurnUserInput, llm.User(summaryText))` to:
   ```go
   summaryTurn := NewTurn(TurnSummary, llm.User(summaryText))
   ```

3. In `processOneInput()` message construction loop (session.go:929-950), add handling for the new kinds. Currently the loop handles `TurnSteering` and `TurnToolResults` specially, and falls through to `history = append(history, t.Message)` for everything else. `TurnCheckpoint` and `TurnSummary` carry user-role messages, so the default case already handles them correctly. **No change needed in the message loop.**

4. In `processOneInput()`, after the strategy `ManageContext` call writes back to `s.history` (line 921-922), append any compaction turns to the transcript:
   ```go
   // After: s.history = histCopy
   // Append compaction turns to transcript.
   for _, t := range histCopy {
       if t.Kind == TurnCheckpoint || t.Kind == TurnSummary {
           if err := s.transcript.Append(t); err != nil {
               s.emit(EventWarning, WarningData{Message: fmt.Sprintf("transcript compaction write: %v", err)})
           }
       }
   }
   ```
   Wait — this would append ALL checkpoint/summary turns in history on every iteration, not just new ones. We need to only append the ones that were just created.

   Better approach: track whether compaction happened by comparing history lengths or using a flag. Or: have MaybeCompact return the compaction turns.

   **Revised approach:** Add a return value to MaybeCompact:
   ```go
   func (cm *ContextManager) MaybeCompact(...) []Turn
   ```
   Where the returned slice contains any compaction turns created (0, 1, or rarely 2 if both checkpoint and summary fire). The caller appends these to the transcript.

   Inside MaybeCompact:
   - After checkpoint fires, append the checkpoint turn to the return slice
   - After summarize fires, append the summary turn to the return slice

   This is clean because it keeps transcript awareness out of MaybeCompact and lets the session decide what to do with compaction turns.

   But MaybeCompact is called by strategies, not directly by the session. The strategies call `cm.MaybeCompact()`. The session calls `s.strategy.ManageContext()`. So the return value needs to propagate through the strategy interface.

   Actually, looking at the code more carefully: `MaybeCompact` is called directly by strategies (e.g., `CompactStrategy.ManageContext` calls `cs.cm.MaybeCompact`). The strategy's `ManageContext` returns `error`.

   Simplest approach: give MaybeCompact a callback for compaction turns:
   ```go
   func (cm *ContextManager) MaybeCompact(
       ctx context.Context,
       history *[]Turn,
       sysPromptChars int,
       emitFn func(EventKind, any),
       onCompactionTurn func(Turn),  // NEW: called for each compaction turn created
   )
   ```

   In the session's processOneInput, pass a callback that appends to the transcript:
   ```go
   onCompactionTurn := func(t Turn) {
       if err := s.transcript.Append(t); err != nil {
           s.emit(EventWarning, WarningData{...})
       }
   }
   ```

   And thread this through the strategy interface. The `ContextStrategy.ManageContext` signature would need updating, or the callback can be stored on the ContextManager.

   Actually, simplest of all: since the session already has access to `s.transcript`, and it knows `histCopy` before and after ManageContext — just diff them. After ManageContext:
   ```go
   // Detect compaction turns added by context management.
   for _, t := range histCopy {
       if t.Kind == TurnCheckpoint || t.Kind == TurnSummary {
           // Check this turn wasn't already in the transcript by checking the old history.
           // Since compaction replaces old history, these turns only exist in the new histCopy
           // if they were just created (or from a previous compaction that's still in recent turns).
           ...
       }
   }
   ```
   This is fragile. The callback approach is better.

   **Final decision:** Add the callback to MaybeCompact. Thread it through ContextStrategy.ManageContext. Strategies that don't use MaybeCompact can ignore it (pass nil). This is a slightly larger change but architecturally clean.

   Updated `ContextStrategy` interface:
   ```go
   ManageContext(ctx context.Context, history *[]Turn, sysPromptChars int, emitFn func(EventKind, any)) error
   ```
   Stays the same. Instead, store the callback on ContextManager:
   ```go
   type ContextManager struct {
       ...
       onCompactionTurn func(Turn) // set by session before ManageContext
   }
   ```
   Set it in processOneInput before calling ManageContext:
   ```go
   s.contextMgr.onCompactionTurn = func(t Turn) {
       if err := s.transcript.Append(t); err != nil {
           s.emit(EventWarning, WarningData{...})
       }
   }
   ```
   And in MaybeCompact, after creating a checkpoint/summary turn, call the callback.

   This avoids changing any interfaces.

**Step 4-6: Run tests, run all tests, commit**

This task will require updating some existing compaction tests that assert the checkpoint turn is `TurnUserInput` — they'll now need to accept `TurnCheckpoint`/`TurnSummary`. Check:
- `TestCheckpoint_CreatesValidMessage` — asserts `result[0].Kind != TurnUserInput` — **needs update**
- Any test that creates a manual checkpoint turn with `TurnUserInput` prefix `[CONTEXT CHECKPOINT]` — those are simulating existing checkpoints and should still use `TurnUserInput` to test the "previous checkpoint" extraction logic

```bash
git add agent/context_manager.go agent/session.go agent/transcript.go agent/transcript_test.go
git commit -m "feat: compaction turns flow through transcript via appendTurn callback"
```

---

### Task 8: Existing test fixes and full regression pass

After Task 7, some existing tests will break because checkpoint/summary turns changed from `TurnUserInput` to `TurnCheckpoint`/`TurnSummary`. Also, the `processOneInput` message loop needs to handle these new kinds as user-role messages.

**Files:**
- Modify: `agent/context_manager_test.go` (update assertions)
- Modify: `agent/session.go` (message loop if needed)
- Possibly modify: `agent/context_manager.go` (checkpoint extraction logic)

**Step 1: Run all tests, catalog failures**

Run: `cd /Users/jesse/prime-radiant/serf/.worktrees/rlm-context && go test ./agent/ -short -count=1 2>&1 | head -100`

**Step 2: Fix each failure**

Key things to check:
- `TestCheckpoint_CreatesValidMessage`: update to expect `TurnCheckpoint`
- `processOneInput` message loop: `TurnCheckpoint` and `TurnSummary` carry user-role messages. The default case `history = append(history, t.Message)` should handle them. But verify.
- Checkpoint extraction of "Original task": the code scans for `TurnUserInput` turns. With the new kinds, a previous checkpoint would be `TurnCheckpoint`, not `TurnUserInput`. The extraction logic in `checkpoint()` needs to check for `TurnCheckpoint` too (for the "repeated checkpoint" case).
- The `[CONTEXT CHECKPOINT]` prefix check in `checkpoint()` currently keys on the text content, not the TurnKind. Since TurnCheckpoint now identifies these, the text-based check can be simplified, but for backward compatibility with old snapshots that have `TurnUserInput` checkpoints, keep both checks.

**Step 3: Run all tests, verify all pass**

Run: `cd /Users/jesse/prime-radiant/serf/.worktrees/rlm-context && go test ./agent/ -short -count=1`
Expected: All pass

**Step 4: Commit**

```bash
git add agent/context_manager.go agent/context_manager_test.go agent/session.go
git commit -m "fix: update tests and message loop for TurnCheckpoint/TurnSummary kinds"
```

---

### Task 9: Integration test — full session with compaction and transcript

End-to-end test verifying the complete flow.

**Files:**
- Add tests to: `agent/transcript_test.go`

**Step 1: Write integration test**

`TestSession_TranscriptFullLifecycle`:
1. Create a session with tiny context window, StateDir, fakeAdapter that returns tool calls with big output
2. Process input that triggers multiple tool rounds (filling context)
3. Compaction fires (checkpoint or summary)
4. Close session
5. Read the transcript file
6. Verify: header is correct, all conversation turns are present, compaction turn(s) are present with correct kind, seq numbers are monotonic, original tool output turns have their full content (not masked), compaction turn appears at the right point in the sequence

**Step 2: Run test, verify pass**

**Step 3: Commit**

```bash
git add agent/transcript_test.go
git commit -m "test: add full lifecycle integration test for transcript logging"
```

---

### Task 10: Sub-agent transcript persistence test

**Files:**
- Add tests to: `agent/transcript_test.go`

**Step 1: Write test**

`TestSubagent_TranscriptPersistsAfterClose`:
1. Create a parent session with StateDir
2. Use fakeAdapter that: first returns spawn_agent tool call, then wait tool call, then close_agent tool call, then "done"
3. Sub-agent adapter returns simple response
4. After parent session closes, find all `.transcript.jsonl` files in the sessions dir
5. Verify there are 2 files (parent + sub-agent)
6. Verify the sub-agent's transcript header has `parent_session_id` matching parent's ID
7. Verify the sub-agent's transcript has conversation turns from its execution

**Step 2: Run test, verify pass**

**Step 3: Commit**

```bash
git add agent/transcript_test.go
git commit -m "test: verify sub-agent transcript persistence and parent linkage"
```

---

### Task 11: Final cleanup and full test pass

**Files:**
- Review all modified files

**Step 1: Run full test suite**

Run: `cd /Users/jesse/prime-radiant/serf/.worktrees/rlm-context && go test ./... -short -count=1`
Expected: All pass

**Step 2: Run go vet and check for issues**

Run: `cd /Users/jesse/prime-radiant/serf/.worktrees/rlm-context && go vet ./...`
Expected: Clean

**Step 3: Review changes**

Run: `cd /Users/jesse/prime-radiant/serf/.worktrees/rlm-context && git diff main --stat`
Review the file list and ensure no unintended changes.

**Step 4: Commit any cleanup**

```bash
git add -A
git commit -m "chore: cleanup after transcript logging implementation"
```
