# Job Control Contract Cleanup — post-implementation punch list

Status: pending. Execute **after** the phase-6 cutover lands and `make test`/`make lint` are green on the merged result. Do not edit `docs/job-control.md` while the autonomous phase executor is mid-run — the phase plans' spec-compliance reviews consult it.

Source: adversarial contract review of `docs/job-control.md` (as of `3cbafe1f`), 2026-06-09 session. The review found the core contract (lifecycle, statuses, notification durability, restart, identity, the six tools + shell) coherent and implementable; the items below are the complete list of internal inconsistencies, underspecified corners, and nits it caught. The observer/sidecar/alias corner holds most of the weight.

## How to execute

- The contract is `docs/job-control.md` (evergreen). The implementation design is `docs/superpowers/specs/2026-06-08-job-control-design.md` (ephemeral). Cleanup edits go to the **contract**; the design spec only gets touched if a decision here contradicts it.
- The in-flight implementation has been answering several of these undefined questions implicitly (e.g. commits `af4dc502` negative-timeout validation, `15b6702e` foreground resume). For each item tagged **reconcile**: first discover what the shipped code actually does, then decide whether that behavior is the right contract answer. Edit the contract to state it, or file a code fix if the behavior is wrong — never silently bless a behavior just because it shipped.
- **mechanical** = pure spec-text edit, no decision needed. **decision (Jesse)** = needs a product call before editing.
- After all edits: run `make lint` (docscheck rides in it) and give the revised contract one fresh adversarial read for newly introduced contradictions.

## A. Internal contradictions

- [ ] **A1. Watch-send delivery rules collide — resolved jointly with B2.**
  Where: `### job_watch` Rules ("coalescing must not turn a matched condition into silence") vs `## Observer and sidecar composition` safety rules ("Observer failures should not fail the watched session; they produce diagnostics or warnings").
  Problem: for a `send` watch whose sidecar can't accept delivery (mid-turn — the *common* case for an `events` watch), the two rules give opposite answers: no-silence forbids dropping, diagnostics-only permits it.
  Cleanup: one decision covering this and B2; edit both passages together so the precedence is explicit. Tag: **decision (Jesse) + reconcile**.

- [ ] **A2. "Wake the owning session" contradicts the turn-based delegate model.**
  Where: `## Notifications` Rules ("Notifications wake the owning/visible session if idle") vs design principle 10 and the `### delegate` turn-based paragraph (only `job_send_message` resumes a delegate).
  Problem: for a nested job owned by a child session whose delegate turn already ended, "wake the owner" means an unprompted child resume — a resume by another name, with token spend and (presumably) a job record nobody asked for.
  Cleanup: state the child-side rule explicitly. Recommended: owner-session notifications for a session with no live run queue durably and deliver at next resume; only the visible/parent session is woken. Verify what the nested-jobs implementation (Phase 5) actually does. Tag: **decision (Jesse) + reconcile**.

- [ ] **A3. Status table says `running` gets "progress/match only" notifications, but promotion is a third kind.**
  Where: `## Job status and reason model` table (running row, Notification type column) vs the promotion notification example (`event="running"`, reason `foreground_timeout`) in the shell section.
  Cleanup: amend the running row to admit the promotion announcement, ideally as part of enumerating the notification `event` vocabulary (B7-d). Tag: **mechanical**.

- [ ] **A4. Vocabulary conflates Parent/Owner/Visible.**
  Where: `## Vocabulary` ("Parent session: The Serf session that owns or can see the job") vs the Owner/Visible rows below it; `## Durable job records` never distinguishes `parent_session_id` from `visible_to_session_id` for the nested case.
  Cleanup: redefine "Parent session" crisply and state what each of the three record fields holds for (a) a top-level job and (b) a nested forwarded job. Tag: **mechanical** (small wording decision inside).

- [ ] **A5. `limit_bytes` is half-in, half-out of the canonical shape.**
  Where: `### job_read_output` canonical-behavior bullets (normative default/max/clamp for `limit_bytes`) vs `#### Advanced output paging` (which excludes that machinery from the agent-facing contract).
  Cleanup: pick one — move the `limit_bytes` bounds into the advanced section (recommended; canonical shape stays `tail_bytes`/`grep`/`block`) or add `limit_bytes` to the canonical target shape. Tag: **mechanical**.

- [ ] **A6. `target_not_delegate` is declared and never used.**
  Where: `## Job status and reason model` synchronous-error list.
  Cleanup: grep the implementation for emit sites. If none, delete it from the canonical list; if some exist, document which calls return it (vs `target_not_messageable`). Tag: **reconcile → mechanical**.

## B. Underspecified contract points

- [ ] **B1. Alias semantics (`caller`/`main`/`watched`) are hand-waved.**
  Where: `### job_send_message` ("Advanced/contextual aliases"), `## Model-facing guidance requirements`, `## Observer and sidecar composition` — resolution is only ever "according to caller context and permissions."
  Problems: what `watched` resolves to when the originating watch target was `*` (not one session); what `caller` means from the root session (self? error?); `caller` and `main` are indistinguishable in every v1 topology, so v1 defines three aliases of which two are synonyms.
  Cleanup: either define each alias's resolution per caller context, or trim v1 to `caller` + `watched` and reserve `main` for nested-delegate futures. Check what the implementation resolves today. Tag: **decision (Jesse) + reconcile**.

- [ ] **B2. Watch→send delivery semantics — the steady state of the observer feature.**
  Where: `### job_watch` Delivery modes + `### job_send_message` semantics.
  Problems: (a) a frame arriving at a terminal-but-resumable sidecar *resumes it* via the `on_finished=resume` default — this implicit auto-resume is the load-bearing mechanism of the observer pattern and is never stated; (b) whether watch-sends pass `on_finished`, and which value, is unspecified; (c) busy-sidecar handling (queue / coalesce / drop) is unspecified, and is the A1 contradiction.
  Cleanup: state the resume behavior explicitly; define busy handling (recommended: coalesce frames per watch key and deliver when the sidecar is next idle — satisfies both A1 rules; drop only on hard failure, with a caller-visible diagnostic); define behavior when the sidecar is terminal and *not* resumable (clear the watch? notify the caller?). Reconcile with Phase 4/5 behavior. Tag: **decision (Jesse) + reconcile**.

- [ ] **B3. `result_schema` across resume + the validation-failure surface.**
  Where: `### delegate` defaults, `### job_send_message` (no `result_schema` param), `### job_read_output` delegate paragraph.
  Problems: whether a resumed job inherits the original delegate call's schema is unspecified, so the structured-result contract for any follow-up turn is undefined. And the failure surface is soft throughout ("should… when possible / when available") — the contract never commits to what the parent sees when no valid structured result exists (`structured_result` absent vs `structured_result_valid=false`).
  Note: the design spec pins the mechanism (communicate-boundary schema enforcement → valid-by-construction), which makes the *inheritance* question the live one. Cleanup: specify inheritance (recommended: a resumed job inherits the delegate session's schema) and specify the guaranteed shape when validation/capture fails. Tag: **decision (Jesse) + reconcile**.

- [ ] **B4. "Private" sidecar jobs have no defined marker.**
  Where: `## Notifications` ("Internal observer/sidecar jobs may run … with terminal notifications hidden"), `## Observer and sidecar composition` step 1 ("a private or normal delegate job depending on configuration"), `## Capacity and discovery requirements` (requires bounding "observer/sidecar jobs" as a class).
  Problem: no parameter, config surface, or named mechanism defines what makes a job a sidecar — so the notification-arming divergence is unreachable and the capacity-class bound is unenforceable as written.
  Cleanup: name the mechanism (delegate param? linkage minted when a watch's `send.to` targets the job? implementation config?) or explicitly delegate it to implementations *and* restate the capacity requirement in enforceable terms. Check what Phase 4 built. Tag: **decision (Jesse) + reconcile**.

- [ ] **B5. The v1 tool-availability matrix is half-stated.**
  Where: `## Nested jobs` ("Subagents must be able to start shell jobs"), design principle 9 (sidecars shouldn't delegate) — and nothing else.
  Problem: having stated part of the matrix, silence on the rest (subagent access to `job_watch`, `job_stop`, `job_list`, `job_read_output`, and legal `job_send_message` targets) reads as omission, not latitude. Tool availability is model-facing behavior and belongs in the contract.
  Note: the design spec already decided this (root-only `{delegate, job_watch}`; `job_send_message` present for subagents but target-gated — aliases ok, concrete delegate `job_id` root-only). Cleanup: verify against shipped behavior, then promote the matrix into the contract as a normative v1 table. Tag: **reconcile + mechanical**.

- [ ] **B6. `background=true` + `block_timeout_ms` interplay.**
  Where: shell Defaults bullets, `### delegate` defaults, `### job_send_message` target shape (the example shows both fields together).
  Problem: when both are supplied, is `block_timeout_ms` ignored, an error, or a brief startup wait? Unstated for all three surfaces. Also unstated though implied: foreground-resume wait semantics for `job_send_message(background=false)` (commit `15b6702e` says the implementation has an answer).
  Cleanup: state the rule per surface, fix the `job_send_message` example if it demonstrates a no-op combination, and add one sentence for foreground resume. Tag: **reconcile + mechanical**.

- [ ] **B7. Small-gap pile** (one or two sentences each in the contract):
  - [ ] a. `job_watch` on an *already-terminal* concrete job: error (`target_not_watchable`?) or no-op — currently only expiry-on-reaching-terminal is defined.
  - [ ] b. What ends a `*`/session-level watch's "configured scope" (`### job_watch` Rules) — session end, TTL, something else.
  - [ ] c. Whether `stopped/runtime_lost` delegates are ordinarily resumable — the natural post-restart recovery path is left entirely to the per-record flag with no stated expectation (recommended: resumable whenever the child transcript is intact).
  - [ ] d. Enumerate the notification `event` attribute vocabulary (`## Notifications` defines the attribute but never the value set: terminal kinds + promotion + progress/match…). Pairs with A3.
  - [ ] e. `supervision_lost` vs `runtime_lost`: distinguished only by example today; give each a one-line definition.
  - [ ] f. Delegate record `description` provenance: `job_list` shows one, `delegate` has no such param — state the derivation (truncated `task`?) or add the param.
  - [ ] g. `max_runtime_ms` is shell-only while `run_timeout` is a generic `stopped` reason. If delegates deliberately have no runtime bound in v1, say so.
  - [ ] h. `## Durable job records` vs `## Durable reconstruction invariants`: the invariants require reconstructing "internal/UI retained start offset and output availability," but the record JSON carries fields for neither. Add them to the record example or mark them storage-level.

## C. Cosmetic / confirm intent

- [ ] **C1. Promotion announces twice.** The tool return already carries `timed_out`/`job_id`/`running_in_background`; the promotion system notification then repeats it with usage guidance. If deliberate (notification carries the prose nudge for models that ignore return fields), add one sentence of rationale; otherwise drop the notification. Tag: **decision (Jesse)**.
- [ ] **C2. Header status line.** "Status: Evergreen reference design … a future system" — once the cutover lands, refresh to describe the shipped system while keeping the evergreen contract framing. Tag: **mechanical, post-cutover**.

## Done criteria

- Every item above checked, with the contract edited (or an explicit "won't fix — latitude is deliberate" note added to this file beside the item).
- No item resolved by silently blessing implementation behavior: each **reconcile** item records what the implementation did and whether the contract adopted or overrode it.
- `make lint` green; one fresh adversarial read of the revised `docs/job-control.md` finds no new contradictions.
