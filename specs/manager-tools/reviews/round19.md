# Round 19 — reconciliation and disposition

Reviewed at commit `8531637bed` (the round-18 revision pass), code base
`2be411cd8f`. Seats — the same three families, a twelfth pass, each
auditing closure of its OWN round-18 findings before fresh attack on the
round-18 revision's own text, all leaves, high effort; all three ran the
reds uncached with `-count=1 -v` (every red failed for exactly the
documented reason, zero disputed, all four characterizations passing —
the thirteenth consecutive clean red audit):

- **R** `codex-jesse-fsck.com/gpt-5.6`
  (dlg_034tiPI9jW8zR2UpEMniXh, run 23:34-23:50): 2 significant + 1
  minor; R18-R-1 **PARTIAL** (the rollback and non-final Close faces).
- **S** `codex-jesse-fsck.com/gpt-6.1-sol-1m`
  (dlg_034tiPJtFxTYbvpyA0tynw, run 23:34-23:46): 2 significant, no
  minors; S18-1 and S18-2 both CLOSED for their reported schedules, with
  nine dynamic probes through the real seams — a retained attention
  first entry read from a REAL index with zero events, and a real
  intervening assistant write emitting a mismatch-held id.
- **T** `lunaroute/deepseek-4.1-flash-background`
  (dlg_034tiPLBm24excxT38H5JM, run 23:34-23:44): **ZERO significant —
  the first seat ever to report none** — + 3 minors; T18-1..T-4 CLOSED,
  T18-5 PARTIAL.

Tally: **3 distinct significant + 2 minor. The count went UP, 2 to 3
(r11-r19: 7, 5, 4, 5, 7, 4, 2, 2, 3) — but the round is not a
regression in kind: every finding, from all three seats, attacks the
SAME root, and the root is the pass's own founding decision.** Every
load-bearing claim was re-verified in-tree by me at the exact hop named,
including the rollback fsync, the deferred stabilization Close, the
compaction events' facet rows, and the close sequence's consumer path.

## The honest shape of this round

Rounds 14 through 19 built a first-record signal gated on durability:
held on retained outcomes, discharged at durability-completion
boundaries, enumerated door by door. Every round since 16 found a new
hole in that design — the population, the retained outcome, the
discharge doors, the frontier collision, and now: the async signal-only
paths still split the serving shapes during the retained window
(R19-R-2 = S19-1, two seats, S's probe read a REAL index naming a
retained attention entry with zero emitted events while the metadata
frontier held the previous execution); the "every durability result"
table still misses the rollback's own successful fsync after a failed
force-synced append (the caller sees only the hard error, no Record)
and the non-final stabilization Close (R19-R-1 + T's N1: the mid-turn
and retirement `EstablishDurability` barriers, the old-writer Close
after a reopen); and the any-door discharge emits a mismatch-held id
before its content ever verified (S19-2 = R19-R-3, S proving it
dynamically with a real intervening assistant write). Six rounds of
holes, one cause: **the signal's post-durability gating contradicts
the frontier's recorded semantics that the served view already
implements — and the contradiction manufactures a new escape every
time a new durability boundary, path, or composition is found.** Both
R and S name the same correction: a recorded-time refresh for every
first recorded entry, independent of durability. The disposition
below takes it to its conclusion: the gating goes, and with it the
entire held-id/discharge/clear-without-emit machinery rounds 16-18
built — every finding this round dissolves under it.

## The findings, verified

- **R19-R-2 = S19-1** (significant, spec 3, two-seat convergence, S's
  dynamic proof through the real attention path and a real index
  read): the recorded revert fixed the four synchronous rows but left
  the signal — the ONLY refresh on the async paths (attention-only
  wakes, whose first entry joins the running execution through
  `PlaceAsync`; compaction checkpoints, whose postwrite events carry
  only context/meta facets, `server/thread_envelope.go:288-289`) —
  durability-gated. A retained async first entry is indexed
  (recorded length published at `agent/session.go:2765-2766`, the
  anchor advanced) while the signal is held: history says the new
  execution, metadata says the previous one, for as long as the
  provider call is held. R's face adds the serving detail: a
  metadata-only read never projects history
  (`server/appwire_read_handlers.go:233-241` assigns
  `LatestExecutionTurnID` only inside `IncludeTurns`), and the
  envelope cannot read the live session under the response cut.
  Verified by me at the facet rows, the anchor path, and the
  read-handlers hop. Disposition 1.
- **R19-R-1 + T-N1** (significant, spec 3, two-seat family): the
  "every result that proves the whole file durable" table still
  omits real results — the successful ROLLBACK fsync after a failed
  force-synced append (`agent/transcript/transcript.go:790-817` →
  `:1000-1030`: the clean rollback truncates and fsyncs, settling
  earlier dirty debt, while the caller receives only the hard error
  and `recordSynced` discards the outcome on error,
  `agent/transcript/record.go:73-77`); the NON-FINAL stabilization
  Close (`agent/session_attention.go:1871-1879`, the deferred
  `_ = reopened.Close()` whose fsync settles a recorded/retained
  first entry — R; the old-writer Close after a reopen at `:1839` —
  T; the same face); the mid-turn and retirement
  `EstablishDurability` barriers (`agent/session_tool_round.go:377`,
  `agent/session_retirement_prepare.go:238` — T). Plus T's loose
  citation: the DOORDURABLE proof is the fsync at
  `transcript.go:781-788`, not the appendBatch span. Verified by me
  at every hop. Disposition 1.
- **S19-2 = R19-R-3** (significant, two-seat convergence, S's dynamic
  proof with a real intervening `appendAssistantTurn`): the any-door
  discharge emits a mismatch-held id before its content ever
  verified — the attention Record commits, the read-back differs, the
  id is held; an unrelated durable write arrives before the retry and
  the global discharge rule emits it; the later typed conflict's
  clear-without-emit is too late. A second fsync proves durability,
  not content equality. R rates it minor, S significant with the
  dynamic proof; the design contradiction is real either way.
  Disposition 1.
- **T-N2** (minor, spec 3, verified by me): the differs-at-read-back
  disposition site at `:600-602` is unreachable — with the adopted
  entry in the file a differing read-back implies a conflicting
  second entry, and the retry's fold read ERRORS at `:237-240`
  (propagated `:1711-1713`) during the read at `:586-589` before the
  pre-record check ever runs: the clear as wired never fires.
  Disposition 1.
- **T-N3** (minor, spec 3): the "emits exactly once" guarantee
  contradicts the CLOSE abandonment in the same paragraph — with
  abandonment the count is zero; the guarantee was at-most-once.
  Disposition 1.

Also verified and NOT findings: S18-1's and S18-2's reported schedules
both closed (S re-drove the retained-input → assistant-reply probe; the
doors table's four doors each correctly stated); the final session
close's consumer path is real (SessionEnd precedes the transcript close,
`agent/session_close.go:260-270`; the event loop drains to channel
closure; `ConsumeEventsLossless` at `cmd/evener/serve.go:279-283`) and
the abandonment statement honest for the FINAL close; the goal row's
journey green verified under production 1s timing; the wrapper sibling
expressible on the user-input path (the ambiguous-sync fixture is a
generic afero wrapper); the stabilization sibling's running-execution
seam real (reopened writers share the OS file's append tail,
`agent/transcript/append_tail.go:15-44`, `placement.go:166-179` — R
verified the seam T doubted); the sampler never takes `attentionMu`
(pinned); the content-MISSING sub-case reconciles without double
emission; no independent double-emission schedule exists among
serialized discharges; spec 1, spec 2, and spec 4 unchanged by the
round-18 pass and READY from every seat that walked them; the fence at
zero findings through its eleventh audit.

## Disposition — one revision pass, then round 20

1. **The signal emits at the RECORDED OUTCOME — the gating and the
   held-id machinery withdrawn wholesale.** The first-record signal
   fires when `Record` reports `rec.Recorded == true` — any door,
   with or without an error — once per execution, read from the
   outcome's fresh-join fact by the outcome handler at every producer
   (the round-15 centralization, unchanged). The mode rows and the
   signal now share the same recorded-time contract: the frontier
   facet advances at the row AND at the signal, both matching the
   index's recorded anchor, on every path — the async attention and
   compaction cases included. The post-durability requirement is
   WITHDRAWN as the wrong invariant — it contradicted the served
   view's recorded semantics and manufactured an escape every round;
   the corrected invariant is post-RECORD (write-then-emit, the row
   set's own rule). Durability is the WRITER's own documented debt —
   the pair helper's adoption contract ("the next fsync settles the
   debt"), Close the terminal settlement — with no manager-visible
   held id, no discharge enumeration, no clear-without-emit class;
   the doors table stays as the writer's durability model (the
   outcome's durability computable from the sync decision, stated
   as the writer's accounting, with the rollback and non-final
   Close faces named). The recorded view is the honest one — the
   file holds the whole line; an OS-crash loss is the writer's
   documented risk for every buffered write, stated as the accepted
   cost, matching today's behavior. The read-back and mismatch
   paths keep TODAY's content-verification behavior — the failed
   read-back removes the unverified turn from memory, the retry
   re-derives through the fold or fails typed at the
   conflicting-content error (`:237-240`) — with NO signal
   interaction. Journey 8's retained/buffered/stop siblings all
   simplify to emit-at-record cases, and the failed-read-back
   sibling is withdrawn with the machinery it pinned.
2. **README + fence-spike**: the round-19 trail (nineteen; the count
   2 to 3; all five findings one root; the withdrawal owned plainly
   — the machinery was the author's own rounds-16-18 work, and the
   record says so); the eleventh fence re-audit note.

Round 20: the same three seats, closure audits of their own round-19
findings plus fresh attack on the rewritten signal chapter — the
emit-at-record contract is the attack surface, first target again.

## Readiness after this round

Spec 1 READY (all three, tenth consecutive). Spec 2 READY (all three,
fourth consecutive). Spec 4 READY (all three). Spec 3 NOT READY (all
three seats). Counts r11-r19: 7, 5, 4, 5, 7, 4, 2, 2, 3. The fence at
zero findings across eleven re-audits. T's round-19 report — zero
significant — marks the first time any seat has found no significant
defect anywhere in the set; specs 1, 2, and 4 have now held READY
from every seat for consecutive rounds, and spec 3's remaining
surface is one mechanism, now being simplified rather than extended.
