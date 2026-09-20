The #1906 review identified a Low difference between live and restored steering classification: live classification passes empty text evidence, while restore can recognize the human-note prefix for a kindless, provenance-less existing journal record. Current SetHumanNote writes explicit provenance, so this is not evidence of a new-write mismatch.

After #1906/#1907 or their replacements land, add a meaningful live/restore oracle for the existing journal shape in session_notes_provenance_test.go, then make the smallest correction if the mismatch is reachable. Preserve the no-askPending-fallback ruling and the existing counter-clearing contract. Do not introduce a new compatibility layer or broaden which inputs answer a pending question.

The accepted simplify notes also identify repeated scans/helpers/test fixtures around this same boundary; measure and consolidate remaining duplication only after the active correctness fixes settle. Keep this separate from their must-fix terminal-state work.



Additional optional Low from #1958 raw-panel audit: add one combined restoration fixture with a resolving user steer followed by a same-round daemon task reminder. The current outer restore scan reaches the user entry and clears older asks even when the inner entry predicate first sees the non-resolving reminder; no production defect was found. A combined fixture would pin this interaction directly alongside the existing individual oracles.


Independent review of the poison-refusal fixture at a55e6f1d3a6d0c319e62df24fda30eff5794f4cc records a P3 test-quality follow-up: assert askPendingCount()==1 after both the live and restored awaiting states, and register deferred cleanup immediately after creating each session so a fatal assertion cannot leave it open. This does not change the qualified failure-boundary production fix.


Measured optional Low from the #1907 carrier panic test at 14303886: the test proves a real append panic propagates and the deferred claim marker clears, but it does not itself seed or assert a pending ask or durable ownership. Independent review confirms A/B/C and startup recovery already cover those broader semantics. Add this as focused coverage only if useful; it is not a blocker and does not justify expanding #1907.
