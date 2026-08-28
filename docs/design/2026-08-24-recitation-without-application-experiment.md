# Recitation Without Application: Mechanism and Pre-flight Experiment Plan

> **Status:** design note — no runtime change is proposed by this document
>
> **Date:** 2026-08-24
>
> **Owner:** issue [#367](https://github.com/prime-radiant-inc/evener/issues/367)

## Decision to test

The evidence in #367 is not evidence that the prompt is unreadable. It is evidence
that a declarative rule can be understood and still fail to control the next
operation. The intervention therefore has three distinct layers:

1. **Prompt recitation** is a statement of a principle in the model-visible
   instructions. It can explain a good decision after the fact, but it does not
   itself prove that the decision was made from the principle.
2. **An operational pre-flight** is an observable act required immediately before
   a risky operation. It records the concrete counterexample, input, artifact, or
   decision that makes the next check meaningful. It is still model-executed and
   can be skipped, faked, or misunderstood; it is a testable steering boundary,
   not a security boundary.
3. **An enforceable mechanism** is a tool, runtime, filesystem, or lifecycle
   invariant that refuses an unsafe operation or makes the unsafe state
   unrepresentable. It must hold even when the model recites the right rule and
   then does the wrong thing.

This note proposes an experiment that measures those layers separately and a
promotion rule that does not use aggregate task scores as proof. The first
implementation candidates are mechanism work already identified by [#366](https://github.com/prime-radiant-inc/evener/issues/366),
[#368](https://github.com/prime-radiant-inc/evener/issues/368), and
[#394](https://github.com/prime-radiant-inc/evener/issues/394); this PR does not
implement them.

## Evidence and documented failure classes

The primary record is #367. Its initial report documents six trials across three
work shapes in which the agent later quoted, verbatim and unprompted, the rule
that would have prevented its failure. The follow-up comment adds three more
classes, bringing the reported set to nine trials. The later chronics comment
adds two sharper examples: a disconfirming probe was run and its result ignored,
and correct competing evidence was gathered and then destroyed. These are trace
observations, not claims that a prompt word count caused a particular outcome.

| Class | Trace shape | What the rule failed to control |
| --- | --- | --- |
| Example-derived constant | `extract-elf` searched for the example's constants, found no match, and still rebased from the example; a sibling computed both candidate tables and chose the wrong one even though extra keys were accepted | Treating an illustrative example as authoritative; failing to preserve a safe hedge when the checker permits one |
| Self-check with the same premise | The agent recited that a self-test must discriminate, then checked a construction that shared the same premise | Calling a re-derivation or same-premise assertion independent evidence |
| Named path versus scratch/install path | `sqlite-with-gcov` recited that a named path is the working location while building in scratch and using the named path as an install prefix | Resolving two individually plausible instructions without an explicit collision decision |
| Buffered output | `make-mips-interpreter` recited total-program and consumed-interface verification rules but buffered incremental output without a signal path | Applying a verification rule while violating the operational property needed for the verification to observe progress |
| Unverified delegate hypothesis | The ML batch accepted a delegate's claim about a fact the delegate could not observe, including a wrong gradient convention | Treating a report as primary evidence without independently checking its premise |
| No held-out/margin check | Constants were tuned to the one visible instance; the interview acknowledged that a small held-out case would have exposed the error | Optimizing the visible case instead of testing the requirement's admissible range |
| Structure-only perceptual check | A perceptual deliverable received a structure-only verification | Treating a syntactic artifact check as evidence of perceptual correctness |
| Destroyed pending disambiguation | Two `count-dataset` trials gathered the right competing evidence, selected the wrong answer, then stopped the job or delegate holding the correction | Turning uncertainty into irreversible action and discarding the path to correction |
| Residue/destructive cleanup and shared-state races | The related #366 report describes a read-only delegate deleting the root deliverable and reporting after the run ended; #368 describes scratch confinement driving blind shell edits and residue | Relying on an advisory scope or cleanup instruction where ownership and write boundaries are required |

The last row is a related mechanism class rather than one of the nine #367
recitation trials. It is included because it gives a clean test of the thesis:
if the unsafe write is refused by the runtime, recitation is irrelevant. The
same applies to #394's unsupported-host sandbox configuration: a truthful,
capability-aware contract should prevent a predictable two-step repair dance.

The [#367 ML-batch comment](https://github.com/prime-radiant-inc/evener/issues/367#issuecomment-5384860703)
and [#367 chronics comment](https://github.com/prime-radiant-inc/evener/issues/367#issuecomment-5390644705)
are the source for the added trace shapes. [#395](https://github.com/prime-radiant-inc/evener/issues/395)
sets the evidence bar: prefer a mechanism/tool fix, otherwise use pooled runs
that clear the measured same-binary noise floor, or show that omitting a rule
licenses destructive or wrong behavior.

## Hypotheses

Each hypothesis has a falsifier. We should not promote a sentence because an
agent can repeat it, or because one noisy run moved in the desired direction.

* **H1 — action beats recitation for decision-local failures.** For a named
  failure class, requiring a pre-flight artifact immediately before the risky
  check reduces the class's *wrong-action rate* relative to prompt-only
  guidance, while not increasing destructive cleanup or premature termination.
  **Falsifier:** the pre-flight arm has no reproducible reduction after pooled,
  same-binary repetitions, or its collateral rate is higher.
* **H2 — enforcement dominates both prompt arms for boundary violations.** A
  write-block, ownership check, or capability-aware tool contract prevents its
  target violation even when the model explicitly attempts it after reciting the
  correct rule. **Falsifier:** an adversarial deterministic test can complete the
  forbidden operation, or the mechanism reports a state it did not enforce.
* **H3 — pre-flight and mechanism coverage are complementary.** A pre-flight can
  catch semantic errors (same-premise checks, missing margin, unverified delegate
  claims); a mechanism can catch authority errors (out-of-scope writes, cleanup
  races, unsupported sandbox settings). Combined use must not be credited with
  preventing an error outside its boundary. **Falsifier:** the combined arm's
  trace shows that only a prompt reminder, rather than the claimed intervention,
  changed the outcome.
* **H4 — clearer contracts reduce retry loops only when the host state is
  truthful.** On hosts without a sandbox backend, one refusal that names all
  required parameter changes should eliminate the known second rejection without
  pretending to enforce a mode. **Falsifier:** the two-step sequence persists,
  or the message claims enforcement that the host does not provide.

## Intervention boundaries

### Pre-flight candidates

These are model-facing actions, not safety guarantees:

* **Independent-check card:** before running a checker, name one specific wrong
  implementation it would reject; identify the premise it does *not* share with
  the construction; then run it against the primary input. “It would fail if it
  crashed” is not a qualifying answer.
* **Ambiguity/hedge card:** list the admissible interpretations or candidate
  tables, the evidence distinguishing them, and the lowest-risk output when the
  consumer accepts a superset. Do not kill an outstanding evidence-gathering
  job until its result has a recorded disposition.
* **Input/path collision card:** name the authoritative input and working path,
  state whether a copy is a shield or a new workspace, and verify the consumed
  interface rather than an install location.
* **Delegate-report card:** label every delegate assertion as observed, derived,
  or unverified; independently check any assertion outside the delegate's
  observable surface.
* **Perceptual/content card:** for a perceptual deliverable, record the content
  inspection that was performed, not only dimensions, file type, or structure.

These cards should be short, typed where possible, and emitted into the run
trace immediately before the corresponding operation. They are not a license to
add another paragraph of doctrine to the base prompt. They should be trialled as
an operational form with a required field and an explicit “not applicable” path,
so omission is measurable rather than silently treated as compliance.

### Mechanism candidates

These belong below the model boundary:

* **Read-only delegate:** make the declared read-only scope write-blocked, at
  least for writes and deletion outside the delegate's owned scratch area; add
  ownership checks to cleanup. This is the direct safety surface in [#366](https://github.com/prime-radiant-inc/evener/issues/366).
* **Structured scratch writes:** permit `write_file`/`edit_file` in the session's
  blessed scratch/sandbox area while retaining confinement elsewhere. This
  removes the documented incentive to write into the deliverable tree or use a
  blind shell replacement, as proposed in [#368](https://github.com/prime-radiant-inc/evener/issues/368).
* **Sandbox capability contract:** resolve host capability before advertising a
  mode; refuse unsupported enforcement truthfully. For the `sandbox=off` plus
  `sandbox_net` case, either make the field inert with a warning or reject it
  with all required changes named, as described in [#394](https://github.com/prime-radiant-inc/evener/issues/394).
* **Lifecycle and report ordering:** keep the root's final verification and
  delegate teardown ordered by runtime state, not by an advisory instruction.
  A delegate that can still mutate shared state must not outlive the state it can
  change. This is the race described in #366 and should be handled in the
  lifecycle mechanism, not by another “remember to wait” sentence.

A mechanism may expose a diagnostic pre-flight (for example, a host capability
probe), but the diagnostic does not replace enforcement. Conversely, an
independent-check card must not be described as independent merely because it
has a new heading.

## Controlled comparison

### Arms

Use one immutable build and one pinned task corpus. Randomize task order and
interleave arms within the same host class. At minimum run:

* **P:** current prompt guidance only (the control);
* **F:** the same guidance plus the operational pre-flight card, with no runtime
  behavior change;
* **M:** the mechanism/tool intervention, with no new pre-flight card;
* **F+M:** both interventions.

For semantic classes, include a small no-card pilot only if it is needed to
separate the current prompt from a card that merely repeats it; do not turn a
pilot into an argument from one run. For authority classes, use deterministic
adversarial tool tests in addition to model trials. The mechanism tests are the
primary proof of a write or capability invariant; model behavior is only evidence
of how often the boundary is reached.

Keep the provider/model configuration, tool schemas, host capability snapshot,
task inputs, and time budget fixed within a block. Stratify results by host
sandbox capability and task failure class. Repeat each arm across multiple
independent runs and size the run count from the calibration distribution. The
same-binary calibration reported in [PR #380's evidence comment](https://github.com/prime-radiant-inc/evener/pull/380#issuecomment-5390044101)
showed that single-run deltas smaller than the observed noise floor are not
resolvable; that is a reason to collect more paired traces, not a reason to
call a prompt change effective.

### Primary measurements

Record counts and event-level outcomes, not a single aggregate quality number:

* class-specific wrong-action rate and prevention rate;
* whether the pre-flight was present, complete, and truthful before the action;
* independent-check properties: wrong implementation named, premise difference,
  primary input consumed, and result inspected;
* candidate/hedge properties: alternatives recorded, evidence retained, and
  correction path preserved;
* mechanism invariant outcomes: forbidden write/delete attempted, refused, or
  completed; sandbox mode actually enforced; cleanup owner matched;
* retries caused by a contract refusal, including whether the first refusal named
  every required parameter change;
* collateral events: deliverable deletion, residue, blind shell patching,
  premature job cancellation, or final-state mutation after root completion.

Trace records must contain only stable class names, tool/result types, boolean
outcomes, and redacted identifiers. Do not record provider credentials, prompt
secrets, raw deliverable contents, or machine-specific absolute paths. Preserve
an immutable per-trial event sequence so an interview answer cannot be mistaken
for decision-time evidence.

### Analysis and decision rule

Pre-register the primary contrast for each class (P versus F for semantic
classes, P versus M for authority classes) and report paired absolute-rate
changes with uncertainty intervals. Pool the independent runs before making a
claim. A pre-flight is useful only if the class-specific reduction is replicated
above the #395 calibration bar and there is no increase in destructive or
collateral events. A mechanism is accepted when its deterministic invariant
suite fails closed for every adversarial case and its capability/reporting
surface is truthful; model-trial frequency is secondary.

A result that does not clear the bar is “inconclusive,” not “no effect.” A result
that improves one class but causes a destructive collision fails the intervention
as shipped. Do not use benchmark or aggregate task scores as evidence for any
of these decisions.

## Success and failure criteria

### Success

* The pre-flight is emitted at the correct boundary, names a falsifiable
  counterexample or evidence gap, and the paired traces show a replicated,
  class-specific reduction without collateral increase.
* The mechanism refuses every forbidden write/delete or unsupported capability
  in deterministic tests, including an explicit model attempt after reciting the
  relevant rule.
* Tool errors and environment/status lines describe the capability actually
  available, including all parameters that must change for recovery.
* Existing valid workflows retain their artifacts, use structured edits in
  scratch, and do not require blind shell patching.

### Failure

* The agent can satisfy the card with a tautology, the card appears after rather
  than before the risky operation, or the same failure remains at the same
  reproducible rate after pooled runs.
* A declared read-only actor can mutate an unowned path, cleanup can delete a
  root-owned artifact, or a sandbox status line claims enforcement on an
  unsupported host.
* The intervention increases residue, premature stopping, retries, or other
  collateral events, even if a semantic class improves.
* A conclusion depends on a single run, a post-hoc interview, or an aggregate
  score rather than immutable decision-time traces.

## Confounds and controls

* **Same-binary nondeterminism:** use calibration blocks and paired task order;
  do not interpret a delta inside the measured noise floor.
* **Model/provider drift:** pin model identifier, provider settings, tool schema,
  and build; record them as non-secret fingerprints.
* **Host capability:** stratify by sandbox backend and OS; never pool an enforced
  host with a host where the mode is unavailable.
* **Task mix and class prevalence:** pre-label tasks by failure class and keep
  class counts balanced; report unobserved classes separately.
* **Prompt collision and dilution:** compare the exact prompt delta, measure
  assembled prompt size, and inspect whether the card duplicates or contradicts
  an existing rule (the #395/#380 evidence shows that collisions can cause harm).
* **Observer and instrumentation effects:** emit compact structured events at the
  tool boundary; compare traces with instrumentation enabled in every arm.
* **Extra time and effort:** record rounds and wall time as secondary diagnostics,
  never as correctness proof; cap the card's size and do not reward verbosity.
* **Survivorship/reporting bias:** classify every trial from the event log before
  reading post-hoc interviews; retain failures and aborted runs.

## Rollback and promotion

Ship semantic cards behind a named experiment flag with no change to the
current default until the pooled decision is made. A card rollback removes the
injection and leaves the trace schema readable. Mechanism changes should be
introduced with deterministic invariant tests and a capability probe; if a
mechanism has a safety regression, fail closed for the unsafe operation and
revert the feature flag or commit without weakening the existing boundary.
Never roll back a write or ownership fence in order to recover throughput.

Promote only the smallest intervention that passes its class-specific criterion:
semantic pre-flights remain pre-flights, while authority and capability behavior
becomes runtime mechanism. Do not convert a successful card into permanent
prompt doctrine merely because the card worked once. Reopen the experiment if a
later pooled replication falls below the #395 bar or if a new collision appears.

## Implementation order

1. Add trace-only event types and a redacted sequence recorder; verify that the
   recorder cannot contain credentials, raw prompt text, or absolute machine
   paths.
2. Add deterministic mechanism tests for #366, #368, and #394 surfaces before
   changing their production behavior. The tests should exercise real tool
   validation and filesystem/runtime boundaries, not hand-fed states that the
   production fence would reject.
3. Run the P/F semantic pilot with the five pre-flight cards above, classifying
   outcomes from traces. Do not tune the cards against one visible task; hold out
   class-matched cases.
4. Run the M/F+M authority comparison and capability matrix on each supported
   host class. Use the mechanism invariants as the acceptance gate.
5. Review the pooled result against #395, publish the trace summary without
   secrets, and either promote, revise, or close as inconclusive.

## References

* [#367 — doctrine recitation without application](https://github.com/prime-radiant-inc/evener/issues/367)
* [#395 — doctrine backlog and evidence bar](https://github.com/prime-radiant-inc/evener/issues/395)
* [#366 — write-blocked read-only delegates](https://github.com/prime-radiant-inc/evener/issues/366)
* [#368 — structured scratch writes](https://github.com/prime-radiant-inc/evener/issues/368)
* [#394 — truthful sandbox rejection/capability contract](https://github.com/prime-radiant-inc/evener/issues/394)
* [PR #380 — prior prompt consolidation and controlled-comparison evidence](https://github.com/prime-radiant-inc/evener/pull/380)
