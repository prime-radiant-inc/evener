# Issue #3999: document `reload_skills` in the `compact_context` table

**Status:** Revised after PAR round 1; scope unchanged, wording corrected against the
reviewers' code citations.

## Scope and intent

The `### The `compact_context` tool` section of `docs/design/context.md` predates
the `reload_skills` parameter. Its parameter table lists only `note_to_self` and
`compaction_instructions`, and the paragraph below it says an empty
`note_to_self` with no `compaction_instructions` schedules no compaction. That
scheduling rule is no longer complete: a present `reload_skills` selection —
even an explicit empty array — still schedules one.

This is a documentation-only fix in `docs/design/context.md`. The parameter
table gains a `reload_skills` row, and the scheduling paragraph states the
explicit-empty-array versus absent/null distinction and the resulting
scheduling rule. No product code, tests, or other documentation pages change.

## Exact implemented behavior (source of truth)

- The tool definition has three properties: required `note_to_self` (string),
  optional `compaction_instructions` (string), and optional `reload_skills`
  (array of strings) — `agent/session_tools_compact.go:26-51`.
- `reload_skills` names exact canonical skills from the session's
  successful-activation inventory, in reload order. Presence distinguishes an
  absent selection (omitted or `null`) from a valid one, and a valid empty array
  means reload none. At the parser level a malformed value or an unknown name is
  `invalid` and authorizes no reload — `agent/skill_reload_selection.go:13-39`;
  on the tool path a malformed value is repaired or rejected before the handler,
  so only an unknown name reaches that state (next-but-one bullet).
- The scheduling decision is `note == "" && instructions == "" &&
  selection.State == "absent"`: only that combination clears the note without
  requesting a compaction. The clear branch also retires a pending automatic
  operation tied to the note; a pending forced operation survives. Every other
  call that passes argument validation is a compaction request. An accepted
  request mints a forced operation and arms the round-tail trigger —
  `agent/session_skill_compaction.go:165-228`. A request is rejected, changing
  nothing, while a forced operation is already pending or any compaction
  operation is still being delivered — `agent/session_skill_compaction.go:169-179`.
  The tool returns a prediction only for an accepted request —
  `agent/session_tools_compact.go:83-92`. A call rejected by argument validation
  never reaches the handler and schedules nothing — `agent/internal/tool/registry.go:798`.
- A selected skill reloads only when the compaction publishes and the next model
  request consumes the handoff receipt — `agent/session_skill_reload.go:168-233`.
  A valid empty selection takes the valid branch with no names, so it reloads
  nothing; an absent or invalid selection takes the reminder branch
  (`agent/session_skill_reload.go:230-310`). The reminder difference belongs to
  `docs/skills.md` and is not restated in the compaction design page.
- A malformed `reload_skills` value is not a handler-visible `invalid`
  selection on the real session path: pre-dispatch repair coerces a scalar to a
  one-element array (`agent/internal/tool/repair/repair.go:100-155`) or the
  schema gate rejects it (`agent/internal/tool/registry.go:798`). The
  `invalid_selection` branch is reachable through the elicitation parser, not a
  `compact_context` call. The table therefore describes only array-of-string
  values and does not call a malformed value an invalid selection.
- The shared client mirrors the same order in `onlyClearsNote`:
  `appwire-client/typescript/housekeepingSteps.ts:60-73`.

## Proposed document text

Replace the two-row table with:

| Parameter | Required | Description |
|-----------|----------|-------------|
| `note_to_self` | yes | Note handed back to the agent verbatim right after the compaction, then cleared. Empty string clears a pending note. |
| `compaction_instructions` | no | Steers what the summary keeps vs. drops. |
| `reload_skills` | no | Optional array of strings naming skills to reload after a compaction that publishes. Absent or `null` makes no selection; an explicit empty array reloads none; any present selection requests the compaction. Order, duplicates, and unknown names: see [Reload selection at compaction](../skills.md#reload-selection-at-compaction). |

Replace the scheduling paragraph with wording equivalent to:

> `compact_context` schedules no compaction only when `note_to_self` is empty,
> `compaction_instructions` is empty or absent, and `reload_skills` is absent
> or `null`; that call clears the note. Any other call that passes argument
> validation requests a compaction. An accepted request queues a
> force-compaction at the tool-round tail and the tool returns a prediction
> (not past-tense confirmation, since the compaction hasn't happened yet). A
> request rejected because a forced compaction is already pending or any
> compaction operation is still being delivered changes nothing.

The final wording must keep the section's existing statement that the tool
returns a prediction rather than past-tense confirmation, and must not claim
behavior the implementation lacks.

## Non-goals

- No changes to product code or tests.
- No changes to `docs/skills.md`, which already documents the reload selection.
- No rewrite of the adjacent `### Agent-force path` section: line 121 says the
  tool calls `requestForceCompact(instructions)`, while
  `agent/session_tools_compact.go:80` now calls `deps.requestSkillCompaction`.
  That drift is outside this issue, will be reported in the PR, and will be
  filed as an issue.
- No change to the model-facing tool description at
  `agent/session_tools_compact.go:34-35`, which says an empty note with no
  `compaction_instructions` skips the compaction and is now false when a
  `reload_skills` selection is present. That is product code and outside a
  documentation-only change; it will be reported and filed as an issue.
- No labels, no full local suite, no unrelated cleanup.

## Verification plan

1. Re-read `agent/session_tools_compact.go`, `agent/skill_reload_selection.go`,
   `agent/session_skill_compaction.go`, and
   `appwire-client/typescript/housekeepingSteps.ts` against the new wording.
2. Run `make docs-check`, which validates the publication manifest, local links
   and fragments, published assets, and the VitePress build.
3. Review the frozen spec and the diff with two independent competing PAR
   reviewers, run simplify-code, commit, and run roborev branch review.
4. Fetch `origin/main` and merge it if it is not an ancestor before the final
   push. Push and open the PR; the parent owns the CI wait and merge.
