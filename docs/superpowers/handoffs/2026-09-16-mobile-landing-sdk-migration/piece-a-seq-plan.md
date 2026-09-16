# Piece A rebuilt on Entry.Seq (Jesse ruled 2026-09-15: close #1281, small PRs)

Premise: agent/transcript/transcript.go Entry.Seq is a durable, monotonic per-line id written at append and read back by Open (on main). No process-local pair identity, no replay copies, no reader exclusions.

## A1 — transcript writer owns durability (target ~300-400 lines)
- Append/AppendDurable return the Seq they spent.
- AppendBatch([]Turn) (firstSeq, error): encode all lines, one write, one fsync, rollback to startOffset on any failure; all-or-nothing.
- Retained-entry contract stays inside the writer: a whole line that landed but did not sync poisons the writer (two causes: partial line permanent; retained whole line cleared by EstablishDurability) — carried from #1281 r16 (5590daa5f). ErrEntryRetained never crosses to the session: a durable append returns nil when the entry is a record and reports the sync failure through a writer-level channel (LastWarning / callback) that the session surfaces once. Session helpers collapse to `if err != nil { not recorded }`; the environment producer keeps EstablishDurability.
- Oracle: #1281's agent/transcript/writer_failures_test.go and the retained-write caller tests (behaviour, not the helper names).

## A2 — fold record + resume by Seq (target ~600-900 lines incl. tests, most deletions)
- History turns carry their Seq (json:"-"), seeded on restore from the entry they came from; #1200 closes.
- A fold writes ONE record {fold_id, retained_seqs, layers} plus its checkpoint/summary markers and CONTEXT_COMPACTION records via AppendBatch, then announces (write-then-announce, one order live and on disk). Steering/strategy turns a fold injects are ordinary appends listed by Seq in the record.
- ResumeHistory re-slices the original entries by Seq after the last anchored fold record; markers/records are their own standalone turns as today.
- Delete: PairID, MintTurn, newTurnPair, holdPairMinted, logPairPersistedLocked, persistedAppendLog, pairsStillInHistory, ContextReplay, ContextReplayMergedTail, CompactionFoldID, foldRun, mergedTail, and every reader exclusion (apptranscript ×7, appprojector, doctor, fork, delegate_fork_context, atif, transcript/failures).
- Oracle: #1281's session_fold_publication_test.go, session_compaction_replay_transcript_test.go, session_compaction_durability_test.go, hook_turn tests — re-targeted to the record; the appprojector/TUI live-vs-reload parity tests with a REAL two-layer fold read through the writer.
- TurnRoundTimings: not here; lands with its producer in piece B.

## A3 — independent fixes carried from #1281 (small, parallel)
- appprojector: EventEnvironment (and compactionAnnouncement) save/restore reservedTurnIDIsStable; one bookkeepingTurn helper for both.
- skill reload: reminder idempotent by PublicationID on retry after a failed saveMeta; handoff removal/restore under metaSaveMu with the revision guard (as admitCompactedSkillReloads).
- schema.TurnKind.IsPresentationalRecord() replacing the 19 copied exclusion lists.
Source commits on claude/replay-durable-records: 577cbf7ee, 5590daa5f, ba30cf0a5, 90a97ad33 (cherry-pick by content).

Order: A3 and A1 in parallel now; A2 after A1 merges.

## Correction (2026-09-16, pre-flight by the piece A implementer)
The A2 "Delete:" list above assumed #1281's replay/PairID design was on main. It never merged. On main (563100a7d, incl. A1 e7d6265a3) the fold re-appends the persisted forms of the pair log (`persistedAppendLog` + `rewriteTail`) after the CHECKPOINT/SUMMARY markers and `ResumeHistory` returns `[last marker, …everything after]` (`retainedFrom`). Revised A2 scope: add `schema.Turn.Seq` (json:"-", seeded on restore); fold writes one `{fold_id, retained_seqs, layers}` record + markers via `AppendBatch`; `ResumeHistory` re-slices by Seq; DELETE `persistedAppendLog` / `logPairPersistedLocked` / `persistedAppendLogBase` / `rewriteTail` (the only replaced machinery). PairID / replay / foldRun / reader-exclusion deletions are no-ops. Closes #1200 as before.
