# Compaction Transcript Steering Design

## Problem

One logical compaction can create two artifacts: a deterministic checkpoint and an LLM-generated summary. The context manager invokes `OnCompactionTurn` for each artifact. Serf binds that callback to `handleCompactionTurn`, which queues the same transcript-recovery steering message every time. A successful multi-layer compaction therefore queues the message twice.

The message tells the agent to recover pre-compaction context through `read_session_transcript`. Serf should provide that guidance once per compaction operation, not once per artifact.

## Design

### Ownership

The compaction operation owns the transcript-recovery reminder. Compaction artifacts do not own it.

`handleCompactionTurn` will continue to perform artifact-specific work:

- append each checkpoint or summary to the transcript;
- emit each compaction-turn event;
- launch session naming from each eligible artifact; and
- queue the existing task reminder when applicable.

It will no longer queue the transcript-recovery reminder.

### Control Flow

`compactionEmitFunc` already creates operation-scoped state and returns a flush closure. Every production compaction path creates one such scope, runs all applicable layers, and calls the closure once afterward.

The operation scope will record whether the operation produced a checkpoint or summary. At flush time, it will queue the existing transcript-recovery reminder once when:

- the operation produced a history-folding artifact;
- the session is persistent; and
- the session has an ID that `read_session_transcript` can resolve.

A later compaction creates a new operation scope and may queue a new reminder. Observation-only masking and other context-management work that creates no checkpoint or summary will not queue one.

### Error Behavior

If summarization fails after checkpoint creation, the checkpoint still removed earlier context, so the operation will queue one reminder. Existing warning behavior for summary failures and transcript append failures will remain unchanged.

Non-persistent sessions will not receive the reminder because they have no archived transcript to read. Naming failures will not affect reminder delivery.

## Testing

Add a deterministic session-level regression test that uses a scripted LLM provider and exercises the real `Session.Compact` path. The test will force a compaction that creates both checkpoint and summary artifacts, then assert:

- both artifact callbacks still produce their expected events;
- the steering queue contains exactly one transcript-recovery reminder; and
- the reminder still contains the current session reference and documented `read_session_transcript` calls.

Retain the existing focused test for reminder content. Run the focused agent and context-manager tests, then the full default test suite.

## Scope

This change will not:

- add generic steering-queue deduplication;
- change compaction callback semantics;
- rewrite the reminder text;
- suppress reminders from later, distinct compactions; or
- refactor unrelated compaction or task-reminder behavior.
