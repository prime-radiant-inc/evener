# Immutable Transcript Logging

## Problem

Serf lacks an immutable, append-only record of conversations. The two existing
persistence mechanisms serve different purposes and neither provides a complete
transcript:

1. **Snapshots** (`agent/snapshot.go`): Point-in-time JSON dumps of the session
   state. Mutable (overwritten on each save). After compaction, the snapshot
   contains the compacted history, not the originals. Crashes lose unsaved turns.

2. **Session log** (`agent/session_log.go`): Append-only JSONL of LLM-generated
   summaries. Strategy-specific (only exists with `--strategy session-log` or
   derivatives). Contains 1-2 sentence summaries, not actual messages.

Sub-agent conversations are ephemeral: when a sub-agent closes, its Session is
garbage collected and the conversation is gone.

## Motivation

This is Step 1 toward a "conversation tree" context strategy where:

- Sub-agent transcripts are preserved and addressable
- Any agent in the tree can read any other agent's full transcript
- Compaction stores result + pointer to the original transcript (lossless)
- The model has a native tool to read prior conversations

## Design

### Core: Append-only JSONL transcript per session

Every session with a `stateDir` gets a transcript file:

    <stateDir>/sessions/<sessionID>.transcript.jsonl

The file is append-only. Each line is a self-contained JSON object. The
transcript is the authoritative record of what happened in the conversation.

### File format

Three line types, discriminated by a `kind` field:

**Header** (first line, written at session creation):

```json
{
  "kind": "header",
  "format_version": 1,
  "session_id": "01HXYZ...",
  "parent_session_id": "01HABCD...",
  "parent_tool_call_id": "call_abc123",
  "task": "Implement the auth middleware",
  "created_at": "2026-02-18T10:00:00Z",
  "profile_id": "openai",
  "model": "gpt-5-mini",
  "working_dir": "/Users/jesse/project",
  "depth": 1
}
```

`parent_session_id`, `parent_tool_call_id`, `task`, and `depth` are present
only for sub-agent sessions. `parent_tool_call_id` references the specific
`spawn_agent` tool call in the parent transcript for causal linkage.

**Entry** (one per turn):

```json
{
  "kind": "entry",
  "seq": 42,
  "turn": { "kind": "ASSISTANT", "message": {...}, "timestamp": "...", "usage": {...}, "response_id": "..." }
}
```

The `turn` field is the standard `Turn` struct, serialized as-is. Seq numbers
are monotonically increasing, starting from 0.

**Compaction entries** also use `"kind": "entry"` with turn kinds `CHECKPOINT`
or `SUMMARY` (see Compaction section below).

### TranscriptWriter

```go
type TranscriptWriter struct {
    file *os.File
    mu   sync.Mutex
    seq  int
}
```

- Persistent file handle, opened at session creation
- Each `Append(turn)`: JSON-encode entry, write line, fsync
- Close flushes and closes the file handle
- All operations are no-ops when the writer is nil (no stateDir)

Writes happen outside `s.mu` — the transcript writer has its own mutex. This
keeps I/O off the session's critical path.

### Integration points

1. `appendTurn()` — after appending to `s.history`, call
   `s.transcript.Append(turn)`
2. `appendAssistantTurn()` — same
3. `NewSession()` — create TranscriptWriter, write header
4. `RestoreSession()` — open existing transcript in append mode, count valid
   parsed entries for seq offset
5. `Session.Close()` — close the transcript writer

### Compaction in the transcript

Today, compaction (Layers 3 and 4) creates synthetic turns and injects them
into `s.history` via wholesale replacement. This changes:

**Layer 3 (checkpoint)** produces a deterministic summary turn. This turn is
recorded in the transcript with kind `TurnCheckpoint` via `appendTurn()`.

**Layer 4 (LLM summarization)** makes an LLM call and produces a summary turn.
This turn is recorded in the transcript with kind `TurnSummary` via
`appendTurn()`.

**Layers 1-2 (observation masking, thinking clearing)** are in-place mutations
of existing turns. They are ephemeral context-window optimizations. The
transcript retains the originals; these mutations are not recorded.

By recording compaction turns in the transcript:

- The transcript contains both the original conversation AND the compacted
  views, interleaved at the point they occurred
- Resume becomes: scan the transcript for the last compaction turn, use it plus
  everything after it as the starting history
- The snapshot no longer needs to store conversation history

### Resume from transcript

On `RestoreSession()`:

1. Open the transcript file
2. Parse entries sequentially
3. Track the last compaction turn (CHECKPOINT or SUMMARY)
4. The resume history = [last compaction turn] + [all entries after it]
5. If no compaction occurred, resume history = all entries

This eliminates the need for the snapshot to carry `History []Turn`. The
snapshot becomes a lightweight metadata record: config, env info, turn count.

### Partial write recovery

On restore, if the last line fails to parse (partial write from crash):

1. Truncate the file to the last `\n` boundary
2. Open in append mode
3. Count valid parsed entries for seq offset

This prevents a partial line from corrupting the next appended entry.

### Sub-agent transcripts

Sub-agents inherit `stateDir` from the parent config. Each sub-agent's
`NewSession()` creates its own transcript file with its own session ID.

To record the parent-child relationship, `SessionConfig` gains a
`ParentSessionID` field, set by `spawnAgent()`. The sub-agent's transcript
header includes `parent_session_id` and `parent_tool_call_id`.

Sub-agent transcripts persist after `closeAgent()` — the file is closed but
not deleted.

### Error handling

Transcript write failures emit `EventWarning` but do not interrupt the session.
If the transcript writer cannot be created (e.g., permission error), the session
proceeds without a transcript.

### What does NOT change

- **Session log** (`session_log.go`) — strategy-specific summarization, separate concern
- **How compaction decides when to fire** — same thresholds, same layers
- **Sub-agent lifecycle** — same spawn/wait/close pattern

### What changes

- **Snapshot** — `History []Turn` field becomes unnecessary for resume. The
  snapshot becomes lightweight metadata. (Migration: keep the field but stop
  relying on it for resume when a transcript exists.)
- **Compaction output** — Layer 3/4 synthetic turns flow through `appendTurn()`
  instead of wholesale history replacement, so they appear in the transcript.
- **TurnKind** — new values `TurnCheckpoint` and `TurnSummary` for compaction
  turns.
- **SessionConfig** — new `ParentSessionID` field for sub-agents.

## Testing plan

1. Transcript file created on session start when stateDir is set
2. No transcript created when stateDir is empty (nil writer, no errors)
3. Every `appendTurn` call adds a line to the transcript
4. Every `appendAssistantTurn` call adds a line to the transcript
5. Seq numbers are monotonically increasing
6. Header is valid JSON with correct session metadata
7. Sub-agent sessions create own transcript files with parent_session_id
8. Sub-agent transcript survives after closeAgent()
9. Compaction checkpoint turns appear in transcript with TurnCheckpoint kind
10. Compaction summary turns appear in transcript with TurnSummary kind
11. Original (pre-compaction) turns remain in transcript unchanged
12. RestoreSession opens existing transcript in append mode
13. RestoreSession correctly determines seq offset from existing entries
14. Partial last line is handled on restore (truncate + continue)
15. Concurrent Append calls produce valid JSONL (no interleaved lines)
16. Large entries (1MB tool output) serialize correctly
17. Transcript valid JSONL throughout (each line parses independently)
18. Write failure emits warning, session continues
19. Resume from transcript: correct history reconstructed from last compaction turn
20. Resume from transcript: no compaction = full history restored
