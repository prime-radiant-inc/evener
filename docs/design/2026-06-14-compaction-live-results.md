# Live end-to-end compaction comparison: does the note help a real agent?

_Real `serf` CLI sessions on the OAuth OpenAI endpoint (gpt-5.5), isolated `$HOME`/state.
Harness: `test/live-compaction-retention-eval.sh`._

## Design

Two arms, 2 trials each, through the **real agent loop** (not just the summarizer):

- **feature**: the agent calls `compact` and **chooses what to note** (`note_to_self`).
- **baseline**: the agent calls `compact` with an **empty** note (summary-only compaction).

Task: 7 distinct facts are given **in the prompt** (no re-readable file), the agent reads
6 filler files (so the facts fall outside the preserved-recent window and get compacted),
compacts, then writes `answers.txt` answering 7 questions **only from its post-compaction
context**. Score = facts recalled / 7.

## Result

| Arm | mean recall | note authored | re-read the source? |
|---|---|---|---|
| feature (agent-chosen note) | **7/7** | yes (~440-470 chars) | no (facts.md_mentions = 0) |
| baseline (empty note) | **7/7** | no | no |

Both arms recalled all 7 facts. **Validated as a genuine null, not an artifact:** a
baseline compaction **SUMMARY turn carries 7/7 of the facts** — the cheap-model summary
preserved every fact on its own, so the agent recalled them without needing the note. The
compaction genuinely happened (4 checkpoint + 4 summary turns in the transcript) and the
source file was never re-read.

## Interpretation

In a realistic live loop with a **clearly-stated** fact list, the baseline summary keeps
the facts, so the note's marginal value is **zero end-to-end**. This is the honest
counterpoint to the controlled multi-needle eval (where facts were deeply *buried* in heavy
bulk and the baseline dropped 54%). The difference is **retention difficulty**: clean,
salient facts survive a summary; many buried facts do not.

Also observed: the agent **used `compact` correctly and unprompted-in-detail** — given only
"call compact, note what you'll need," it authored a sensible ~450-char note capturing the
facts. So the tool is usable by a real agent; the question is whether the *note* is needed,
and here it wasn't.

## Cross-eval synthesis (all four runs)

| Eval | regime | baseline fact retention | note's effect |
|---|---|---|---|
| controlled single-needle (gpt-5.5) | 1 clean fact | 1.00 | none (redundant); steering +0.91 judge |
| controlled single-needle (gpt-5.4-mini) | 1 clean fact | 1.00 | none; weak judge erased signal |
| **controlled multi-needle (gpt-5.5)** | **6-8 buried facts** | **0.46** | **rescues the dropped 54%; +1.70 judge** |
| **live end-to-end (gpt-5.5)** | **7 clean facts, real loop** | **1.00 (summary kept them)** | **none (redundant here)** |

## Bottom line

**The note's value is real but situational — it scales with retention difficulty.** It
pays off when the agent must carry **many, buried, or long-lived** facts through compaction
that a single post-hoc summary would drop (controlled multi-needle: clear +1.70 judge,
54% of facts rescued). For **clearly-stated facts in a routine loop, the summary keeps them
and the note is redundant** (live: 7/7 both arms). **Steering** (`compaction_instructions`)
improves handoff quality broadly (+0.91 judge) regardless of difficulty.

For serf this argues for shipping the capability (it's cheap insurance + a quality lever),
while setting honest expectations: the note earns its keep in dense/long-context work, not
in every compaction. The agent-choice dimension looks healthy — a real agent used the tool
and authored a sensible note when asked.

## Addendum: "force a note before compaction" value test (dense, 15 facts)

Testing the idea of injecting a *mandatory* "you MUST call compact now" turn right before
a forced compaction, so the agent authors a note instead of getting a blind summary.
Harness: `test/live-compaction-dense-value-test.sh`. Two arms × 2 trials, **15** clearly-
stated facts + heavy filler:

- **mandated**: the agent is told it MUST call `compact` now and chooses what to note.
- **blind**: `compact` with an empty note (summary-only).

| Arm | mean recall | note authored | compliance |
|---|---|---|---|
| mandated self-note | **15/15** | yes (~700–1200 chars) | agent complied every time |
| blind | **15/15** | no | — |

**The mandate mechanism works** (the agent always complied and authored a substantive
note), **but it made no difference to retention** — the blind baseline kept all 15 facts
too. Consistent with the 7-fact live result: the live agent loop retains clearly-stated
facts through a single compaction regardless of the note.

**Implication for the "force a note before compaction" feature:** on this evidence it
would add a round-trip (and force compaction mid-task, off a clean seam) **without
measurable retention benefit** in realistic conditions. The note's value is confined to
where the controlled eval found it — **facts buried in noise, compressed hard in a single
pass** — which a clean live loop with a strong summarizer doesn't hit.

**Two angles NOT tested here that could still favor the mechanism:**
1. **Multi-compaction erosion (context collapse):** a fact must survive *many successive*
   compactions over a long session. Repeated re-summarization erodes detail (the prior-art
   "context collapse"); a note re-stamped *verbatim* each time resists that. Untested
   (expensive — needs long multi-compaction sessions).
2. **Handoff quality, not just retention:** the controlled eval showed the note+steer
   handoff scored higher (judge +0.91) *even at equal fact retention*. This dense test
   measured only recall, not quality.

**Recommendation:** don't build the forced-note mechanism on current evidence — it doesn't
pay off for single-compaction retention in a live loop. Revisit only if (1) multi-
compaction erosion proves real, or (2) handoff-quality (not just recall) is the goal.
