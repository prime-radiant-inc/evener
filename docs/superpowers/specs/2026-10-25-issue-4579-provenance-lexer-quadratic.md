# Provenance lexer remainder proof: per frame plus per token

## Intent and scope

Resolve issue #4579. `documentSourceMap.ts` proved on **every pinned-tokenizer
dispatch** that the `src` string Marked handed over was the current frame's
tail, then proved it again in `token()`. Both were full-length content
comparisons. A large single-frame container (one list item, one blockquote,
one table cell block) probes every block rule against the remaining frame per
token, so the pass was quadratic in frame size.

The proof must stay meaningful: the file throws on any mismatch with pinned
Marked 18.0.6. This change moves **where** the content proof happens; it does
not weaken or delete it.

The implementation lives on the annotations branch
(`wip/markdown-document-annotations`, PR #4395), not on `origin/main`; this is a
stacked change against that branch.

## Evidence at the start

Measured in this worktree with the existing long-list shape (one list item,
lazy continuation lines, inline markup on anchor lines only):

| lines | bytes | render |
| --- | --- | --- |
| 2,000 | 76,928 | ~0.30 s |
| 4,000 | 154,928 | ~1.29 s |
| 8,000 | 310,928 | 6.8 s and 12.6 s on two runs (issue: ~11.7 s) |
| 16,000 | 628,928 | not measured before the fix |

Instrumentation on the 8,000-line shape (the long-list shape with inline
anchors on its first and middle lines) counted 104,104 tokenizer dispatches
and **32.5 billion characters compared** by `inputFor` plus `token()`
(16.2 B in each). Roughly 13 block rules are probed per continuation line.

## Decision: where the proof moves

A block or inline frame is proven **once at entry** (`blockTokens` /
`inlineTokens`), which already compares the frame's full text against the
trace. Inside the frame the lexer carries a verified cursor, `offset`, in
UTF-16 units:

- A dispatch's tail is located by `slice(frame.input, frame.offset)` and is
  accepted only when `src.length` equals the frame's remaining length. The
  length check catches a dispatch that does not line up; it is not the content
  proof.
- Each successful token then proves its own content at its consumed raw size:
  its `raw` is compared against the slice of the trace at the cursor (already
  did this), and the cursor advances by that raw length. A container token's
  raw is the whole container, but there is one such token per container, not
  per line; the per-line `text` tokens are compared at line size.
- At frame exit, the cursor must equal the frame text length. Every byte the
  frame consumed was therefore verified at token size, and no byte was
  skipped.
- Recursive tokenizer calls assembled from provenance (`withTokenSource`, used
  by blockquote/list continuations) are not frame dispatches. They keep their
  one full compare per recursive call, and their own nested frame entry proves
  the text again.

This satisfies the issue's second option: the token's `raw.text !== token.raw`
compare is the content proof inside a frame; the frame-level proof moved from
every dispatch to entry. The live content checks are the frame entry compare
and the token raw compare. The dispatch length check (`Unverified Marked
remainder`) and the exit check (`Unverified Marked consumption`) are guards
against a Marked change: pinned 18.0.6 always consumes `token.raw.length`, so
they do not fire in normal operation, and the suite can only exercise them
through an injected mismatch.

One pinned-Marked detail needs clamping: its blockquote tokenizer appends a
synthetic newline for a trailing empty continuation group, so `token.raw` can
be one unit longer than the frame. Marked consumes with
`src.substring(token.raw.length)`, which clamps, so the cursor advances by
`min(token.raw.length, remaining)` to track what Marked actually consumed.
With pinned 18.0.6 only the blockquote token can overhang, and its own trace
is still compared against the dispatch input, so the clamp cannot mask a wrong
content mapping; the blockquote's synthetic unit is the known overhang.

## Second quadratic found behind the proof

After the proof moved, wall time was still quadratic: 4.96 billion characters
were joined by `concatAll` during the 8,000-line parse. Four append sites per
continuation line (list raw accumulation, the space-token newline merge, the
text-token raw merge and text merge) rebuilt the accumulated string with
`pieces.join("")`, copying the whole prefix each time.

`Trace` now carries an O(1) `length` and an optional two-operand deferred
concatenation. A shared-storage concatenation keeps its operands and joins
once on first `text` read; non-shared concatenations materialize as before.
The list loop accumulates raw pieces and its trailing newline instead of
slicing and concatenating the accumulated raw trace per line; the text merge
uses the rightmost leaf to find the trailing newline without flattening the
prefix. This is span/text bookkeeping only; it does not change Marked
recognition or token order.

Proof units after the change for the 8,000-line shape: 1,235,700 full-frame
characters (4.0x the source, because every inline frame's derived text is also
proven) and 932,761 token characters (3.0x). Both grow linearly with the
source. `inputFor` does no content comparison, so re-adding one there would
not change these counters; the ratchet guards the shape the helpers express,
and the same-length corruption tests below guard that the helpers stay live.

## Correctness

- Exact coordinates stay unchanged for long list continuations (lazy and
  indented), a long quoted paragraph, table cells, ordinary multi-block
  documents, CRLF normalization, entities and transformed spellings. The
  existing suite plus new long blockquote and table cases cover them.
- A deliberately corrupted frame remainder still throws, at both a different
  length and the same length: `lexAgainstSourceForTests("same", "same and
  more")` and `lexAgainstSourceForTests("same", "sAme")`.
- A same-length corrupted token raw still throws: the suite patches Marked's
  own `Tokenizer.prototype.text` to alter a returned raw and asserts
  `parseDocument` refuses it (the message is not asserted; error copy is not a
  test oracle).
- A blockquote whose token raw carries Marked's synthetic newline renders and
  maps `b` in `> - a\nb` to source 6..7; the reviewer-found class (`> - a\n b`,
  `> 1. a\nb`, `> * a\nb`, `> - a\nb\nc`, and others) renders without a
  consumption mismatch. A list item ending at input without a trailing newline
  maps its final word to the exact source range.
- `slice` clamps a requested end to the trace, so `length` and the span view
  never exceed the sliced text; the deferred accounting depends on that
  invariant.
- A counting regression (`proves a frame once and each token at token size`)
  fails on the old shape and passes with the cursor proof. The one-off red run
  on the 4,000-line shape measured 8,110,097,548 frame units; the committed
  500-line fixture already exceeds its 16x bound by 412x on the old shape and
  costs ~40 ms, so the ratchet stays cheap. It is deterministic; no wall clock
  is asserted in the default suite.
- A wall-clock measurement is kept as one-off evidence, not a gate.

## Review outcome

Two independent adversarial reviews ran before the commit. Both verified the
cursor induction and the deferred `Trace` against every consumer.

- **Critical: synthetic blockquote newline.** Marked's blockquote token raw
  can exceed the frame by one synthetic newline; advancing by the raw length
  made `parseDocument("> - a\nb")` throw `Unverified Marked consumption` on a
  document the old code rendered. Fixed by clamping the advance to the frame's
  remaining length; the class is covered by a coordinate test and a table of
  the reviewer's other shapes. `slice` now clamps its end the same way, so a
  requested end past the trace cannot leave a phantom length or span.
- **Major: the ratchet did not count the removed compare.** Counters now
  increment inside `verifiedFrameText` / `verifiedTokenRaw` (a passing check
  records its units; call sites do not add separately). Same-length
  corruption tests at the frame entry and at Marked's `text` tokenizer refuse
  a mismatch. Limits, stated honestly: a bare
  uninstrumented `!==` re-added inside `inputFor` would not change the
  counters, and a mutant that keeps a counter while deleting only its
  equality test cannot be distinguished by counting. The corruption tests
  fail if both content proofs are removed; the deterministic ratchet guards
  the proof shape, and the issue's wall-clock evidence stays a one-off.
- **Minor: guards described as live proof.** The spec now distinguishes the
  frame/token content checks (live) from the dispatch-length and frame-exit
  checks (guards against a Marked change, unreachable with pinned 18.0.6).
- **Minor: "at token size" overstated.** Corrected: container tokens compare
  at container size, one per container; per-line tokens compare at line size;
  the total stays linear.
- **Minor: `toContain` could hide over-wide spans.** The long-list mid-run
  assertion now locates an interior line's first source unit and requires its
  exact rendered run and end. The quoted-paragraph run does the same; its
  source range spans the dropped `> ` prefixes between rendered lines, so a
  whole-run width check was the wrong invariant.

The diff review added one more honesty correction. Deleting only the
frame-entry content compare survives the suite (the token/inline transformation
checks still refuse the corruption), so the frame compare is defense-in-depth,
not an independently observable proof; the token raw compare is the one whose
removal fails the patched-tokenizer test. The counter increments were also
moved inside the helpers after review showed that `x += verifiedFrameText(...)`
reads `x` before the call and overwrote the helper's own increment; the call
sites no longer add the return value.

The reuse angle found one bare full-text compare left in `inlineInput` (the
derived inline text against `src`); it now routes through `verifiedFrameText`,
which is why the full-frame ratio is 4.0x rather than 3.0x at 8,000 lines.
The four-angle simplify review also sized the new fixtures down: the ratchet
uses 500 lines (still 412x over its bound on the old shape) and the table uses
200 rows, cutting about 900 ms from the default suite.

## Residual, out of scope

The 8,000-line shape is still superlinear after this change (~3.1-3.6 s;
16,000 lines measured 12.4-21.5 s) because pinned Marked's `lheading` rule
scans the entire remaining frame once per line: the rule's lazy multi-line
capture has to reach the end to prove there is no setext underline. Two
independent reviews measured 1.25 billion characters scanned by `lheading`
across 8,000 probes and 2.24 s of the 2.85 s plain-Marked block lex (~78%).
Bypassing it would mean reimplementing recognition, which the file
deliberately leaves to Marked. This is a separate finding and gets its own
issue; the 8,000-line repro is not gated in the suite.

A blockquote whose quoted lines alternate with lazy continuations also stays
superlinear in wall time (26 / 76 / 256 / 1,451 ms at 250 / 500 / 1,000 /
2,000 groups) from Marked's own continuation re-lexing; the current and
previous implementations measure the same, and this proof's units stay flat
(~2.7x the source). Both residuals are pinned-Marked costs, not the
provenance proof.

## Implementation boundary

Change only `cmd/evener-hub/frontend/src/widgets/markdown/documentSourceMap.ts`
and its test file. Do not change pinned Marked, the shared renderer, the
document pane, or the annotation protocol. Keep the `Unverified ...` throw
discipline and the existing token/span transformations.

## Verification

1. `npx vitest run src/widgets/markdown` (includes the source-map suite).
2. `npm run typecheck` from `cmd/evener-hub/frontend`.
3. `npx biome ci` on the touched files.
4. `make build`.
5. Re-measure the 8,000-line shape and a scaling series as one-off evidence.
6. CI is the source of truth for the full browser suite.
