# Issue #4181: authority fidelity in opt-in compaction

## Scope and acceptance

[Issue #4181](https://github.com/prime-radiant-inc/evener/issues/4181) extends the
#4173 permissions-and-holds contract to every opt-in context-management strategy.
Permissions, approvals, holds and stops must reach a summarizer in their source
words and attribution. A summarizer must quote them rather than infer, add,
paraphrase or silently remove them. A previous compaction is evidence only for
what it quotes, not authority for its own cautions or conclusions.

This lane starts from `dc6c93e0810857db3359983854c8510e0b4a3709` and merges
fetched `origin/main` before this spec. Allowed changes are contextmgr
summarizers/digest helpers, strategy files and their tests, plus this spec.
The default summarizer's prompt and digest behavior remain unchanged.

The human partner's requested acceptance criteria authorize this bounded fix.
No strategy is retired. There is no usage evidence that would justify retirement,
and all seven implementations remain selectable in `agent/session_init.go`.

## Inventory and evidence

| Strategy | Summary/checkpoint path | Existing authority gap | Change |
| --- | --- | --- | --- |
| `compact`, also the `recall` compatibility alias | `Manager.MaybeCompact`, deterministic `checkpoint`, `summarizeFold`/`summarizeWithLLMSteered`, and `condensePartnerMessages` | The #4173 fixes already share `partnerAuthorityRule`, keep partner messages and earlier summaries, and have whole-prompt goldens | Preserve behavior and goldens unchanged; use as the compatibility reference |
| `obs-mask` | Aggressive tool-output masking, then the same deterministic `checkpoint` | No independent model summary or authority digest | Preserve its behavior; test that checkpointing and a repeated fold retain a quoted hold |
| `checkpoint-pred` | `predictiveCheckpoint`, then the existing summary fallback | Custom digest truncates user text to 300 bytes, ignores steering and previous checkpoints/summaries, and stops at 30,000 bytes; prompt lacks the rule | Shared faithful strategy digest and rule, including fallback requests; keep earlier quoted context and the existing verbatim partner carry across repeated checkpoints and deterministic fallback |
| `memory-crystals` | `crystallize` every third eligible history length, recent six turns; default compaction followed by crystal-bank injection | Custom digest omits user/steering/previous compaction turns; prompt asks only for tiny factual fragments | Shared faithful strategy digest and rule; quotations take precedence over the size/style preference |
| `recursive-distill` | `microSummarize` over the recent ten turns, `macroSummarize` over five micro-summaries; default compaction followed by memory injection | Micro digest omits user/steering/previous compactions; neither micro nor macro prompt has the rule | Shared faithful micro digest; rule on both stages, with macro inputs explicitly treated as earlier summaries that may supply quotations only |
| `session-log` | `forkSummarize` over recent ten turns; custom session-log checkpoint; existing summary fallback | Fork digest truncates user messages to 500 bytes, calls partner steering `System`, and ignores previous compactions; checkpoint retains only a 500-byte original prompt and model log entries | Shared faithful fork and fallback digest and rule without changing the JSON schema; checkpoint adds attributed carry and earlier context, ahead of fenced model-owned log/detail text |
| `ooda` | Embeds `SessionLogStrategy`; also injects its session log for orientation | Inherits both session-log gaps | Inherits the fixes; test its real `AfterAction` and `ManageContext` route separately |

The original #4173 rule and prompt goldens landed in `bac2c2418e` (#4239).
Earlier-summary preservation followed in #4240, and model-written compaction
turns' verbatim partner carry followed in #4241. The current implementation also
bounds default partner carry and condenses older messages under the same rule.
These existing default policies are not changed here.

`ElicitNote` is a pre-compaction exact-detail elicitor, not a replacement summary
or an opt-in strategy. Its prompts, skill selection and tail-first tool-detail
renderer stay unchanged, like the default summary and partner-batch prompts.
No other production model-summary calls exist in contextmgr beyond the paths
listed above and that existing default elicitor.

## Shared contract

### Strategy history digest

The independent opt-in summarizers use one history digest helper. It receives
only the history slice its caller already selects, so cadence, preserved-tail
selection and failure/retry policy do not change.

The helper:

1. Writes user input and user-sourced steering whole as `User:` records. It does
   not classify natural-language authority or decide which user sentences matter.
2. Keeps parent-agent steering whole with its parent attribution, never promoting
   it or daemon steering to the human partner's `User:` authority.
3. Writes typed `TurnCheckpoint` and `TurnSummary` whole as previous compactions.
   Legacy compaction text in a user-input turn remains distinguishable from a
   fresh human instruction.
4. Keeps chronological order. A soft budget may omit assistant/tool detail but
   must continue scanning for user messages, attributed steering and earlier
   compactions beyond it. No per-message prefix cap may cut those source records.
5. Retains assistant text, tool calls and tool results with bounded ordinary
   detail, at least as much as the old opt-in renderers normally retained.
   Communicate messages and tool-error provenance remain distinguishable from
   user authority. It never creates a permission/hold record or an inferred
   approval from those lower-authority sources.

The default renderer is deliberately not migrated to the new helper: its
existing byte caps, batch-condensation prefix and exact prompt are compatibility
requirements. Opt-in strategy summary fallbacks use the shared strategy digest
with the existing summary prompt and output handling, so they cannot reintroduce
the default renderer's earlier-context prefix cap. Default callers remain on
their existing renderer. Sharing avoids independent lossy opt-in loops without
changing the established default behavior.

New opt-in digests include user messages, steering and previous compactions that
old digests dropped; checkpoint-pred also receives source text beyond 300 bytes
and beyond its old total cutoff. Ordinary detail uses consistent limits rather
than the old 100/200/300/500-byte variants. This is intentional: the new requests
are more faithful and may be larger. The authority/source records are not silently
cut to satisfy the soft detail budget. Existing cheap-call failure branches and
checkpoint-pred's deterministic fallback handle provider refusal. The fallback
must retain earlier quoted context whole rather than apply the default
checkpoint's earlier-summary size cap.

### Prompts and no invention

Every independently model-written strategy summary uses the existing
`partnerAuthorityRule` constant verbatim. It must not acquire a weaker local
variant. The rule requires exact quotation, source attribution, no added or
dropped standing authority, and `None.` when no authority is present.

Style preferences (under 100 tokens, under 500 words, 2-4 sentences, 3-5
sentences) yield to the quoting rule. Fork summaries still return the same JSON
object and put any required quotations in its summary field. Macro-distillation
may carry only quotations actually present in its input summaries, preserving
attribution; a micro-summary's own caution or request is not a new human hold.
Questions, suggestions, assistant caution, tool text and daemon nudges must not
be relabeled as the partner's authorization. Every independent strategy summary
rejects known output-limit responses using the existing `checkNotCutOff` check
before publishing output or clearing its source bank. This narrowly extends
existing error admission, not the compaction threshold or retry policy.

Code does not attempt to parse natural language to certify model obedience.
Deterministic tests prove faithful inputs, shared rule delivery and unchanged
storage/wiring with a scripted provider. They do not claim that an arbitrary
live model cannot hallucinate. The existing deterministic partner-message carry
remains the independent record beside model-written checkpoints.

### Checkpoint preservation and recovery

Predictive and session-log checkpoints keep partner messages using the existing
conversation carry and its fitting/recovery semantics. They also keep earlier
summaries/quoted checkpoint context in parseable `Earlier Summaries` sections,
so a later deterministic or predictive checkpoint can recover it without
relabeling model text as fresh human input. A legacy opaque checkpoint that
cannot be parsed into those sections is retained as earlier context rather than
discarded. Already structured checkpoints are unpacked rather than repeatedly
nested, avoiding recursive duplication of their conversation carry. Earlier
context precedes unbounded model/log detail. Model-owned text and free-text
metadata are fenced with `renderCompactionModelText` or equivalent. Only
deliberately rendered, canonical conversation sections supply partner carry;
headings inside a log, format example or legacy opaque checkpoint do not.

The existing recent tail remains untouched. The existing transcript pointer
continues to own recovery of budget-trimmed long messages. Auxiliary call errors,
including known truncated completions, remain warnings with existing
cadence/retry behavior; they do not replace history or publish invented
successful results. No new compaction threshold is introduced.

## Non-goals

- No strategy removal, strategy-selection/configuration change or usage claim.
- No default summary, caller-instruction, partner-batch or elicitation prompt edit.
- No new context-pressure, preserved-tail, cadence or retry policy. Only opt-in
  source preservation bypasses lossy earlier-summary caps; ordinary detail and
  the default path retain their existing budgets.
- No natural-language authority classifier, live-provider dependency in default
  tests, model-output rewriting or claim of deterministic hallucination detection.
- No unrelated prompt changes, product-policy changes, new labels or changes
  outside the allowed paths. Unrelated defects found during delivery get their
  own GitHub issues rather than being folded into this patch.

## Verification

### Red-first regressions

Before implementation, run new deterministic tests against the old source and
record actual failures. Cover each independent strategy summary request with a
scripted provider and authority supplied by the test itself:

- `checkpoint-pred`: a permission after byte 300, partner steering, and a prior
  `TurnSummary`/`TurnCheckpoint` containing a quoted hold. Test a source record
  beyond the old 30,000-byte cutoff and a second compaction after the first
  predictor reply omits authority. Force predictor failure with earlier context
  beyond the default checkpoint's cap and assert the sole quote survives.
- `memory-crystals`: a user permission/hold in the six selected turns that the
  old digest omits, plus prior compaction and partner-steering cases.
- `recursive-distill`: the same omitted source kinds in micro input and exact
  authority/source text in macro input. Exercise the real micro-to-macro path,
  including a length-limited macro that must not clear the five source micros.
- `session-log` and `ooda`: a permission past byte 500 and user steering in the
  fork request, previous compactions, and exact partner/earlier-context recovery
  after their custom checkpoint and a repeated fold. Include misleading Markdown
  carry headings inside log text and original-prompt metadata. Test their actual
  summary fallback with a long cumulative log and a hold present only in an
  earlier summary, beyond 60,000 bytes. Legacy raw log examples remain model
  context, never new human messages.
- `compact`/`recall` and `obs-mask`: compatibility tests showing an existing
  quoted hold survives their shared default/deterministic route. Those already
  correct cases need not fail before the opt-in fix.

Use opaque test data to assert exact source transport and typed parsed
conversation entries to assert carries. Pin instruction prose with complete
request goldens, never substring assertions against production prompt copy.
For each independent prompt, include a no-authority golden: an ordinary question
and assistant caution retain their roles, the complete no-invention rule is
present, and no synthetic permission/hold data is added. Macro coverage includes
both quoted authority and authority-free summaries. Each independent model call
also gets a scripted output-limit reply and must take its existing failure path
without publishing a partial summary or consuming its source state.

Spec PAR found four distinct P2 gaps: fallback caps on earlier quoted context,
unfenced model text parsed as user carry, output-limit admission, and log/earlier
context ordering. Both independent reviewers reproduced their findings with
scratch-only Go overlays. All four are accepted and addressed by this revision.

The existing `TestSummaryPromptGolden` fixtures must not change. Add a complete
assembled default-request compatibility fixture before extracting or changing
shared plumbing if such plumbing is touched. Test legitimate review findings
red-first, then rerun affected cases.

### Delivery gates

Run focused `go test ./agent/internal/contextmgr` checks covering every strategy
and the package's deterministic non-live tests. Run `make build` after inspecting
the Makefile and its invoked scripts. Do not run the full suite; required CI is
the source of truth for whole-tree tests, race, fuzz, lint and client gates.

Freeze the spec and run PAR with two independent competing reviewers. Resolve
legitimate findings, then implement using TDD. Freeze the implementation diff
and run PAR again. Run `simplify-code`, rerun affected checks, commit the allowed
files, then run `roborev-review-branch` (or the documented `roborev review --branch
--wait` equivalent if invocation is denied). Re-fetch and merge `origin/main`
before the final push if it moved. Open a PR against main and run `shepherd-pr`.
Admin squash merge only on a current reviewed head with every required check
green and every RoboRev finding low or conclusively refuted. Verify GitHub's
merged state and merge SHA before cleanup and final reporting.
