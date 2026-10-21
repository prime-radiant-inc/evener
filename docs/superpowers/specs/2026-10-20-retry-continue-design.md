# Failed-turn Retry sends a continuation

## Intent and scope

Jesse requires failed-turn Retry to send `continue` instead of replaying the
previous prompt. This applies to the browser and native app. The existing
conversation is the context for the continuation.

This change covers browser `TurnFailureEndCap` and the native conversation's
failed-turn Retry action. The Retry labels and layout stay unchanged.
Queued-message, delivery-uncertain, and canceled-message retries keep their
existing semantics. Provider retry timing, quota handling, Resume, Sign in,
and automatic goal continuation are outside this change.

## Behavior

- A Retry press submits one new ordinary text message, `continue`, to the same
  session through the client's existing send path. Native keeps its existing
  send-versus-queue selection when another turn is active.
- Do not reconstruct, resend, or attach the earlier user prompt, images, skills,
  commands, or the current composer's content.
- Retain the full transcript and completed work. The original input stays where
  it was; the continuation appears as a new message through ordinary projection.
- Keep the composer's text, selections, and staged attachments unchanged, on
  both acceptance and failure. Retry does not consume the draft.
- Retry no longer depends on finding a previous user-input item or recovering
  its image bytes. An image-only failure and a failure whose earlier input is
  not loaded can still be continued. Remove the Retry-specific reattach warning.
- Keep existing session eligibility and error-action precedence: no browser
  submission without a session reference, and native still offers Retry only
  for the latest turn's own failure when sending is allowed. Native Resume and
  Sign in retain priority. Do not add new actions to unrelated error rows.
- Keep existing refusal and delivery recovery owners. Browser errors remain
  visible through its error reporting; native pending/refused submissions remain
  visible through its existing mutation recovery. A lost reply is reconciled by
  the existing outbox identity, never by replaying the old prompt.
- Once the underlying failure clears, Retry can produce a completed turn in the
  same conversation without removing the original failure or duplicating input.

## Implementation boundaries

Use existing widgets and submit paths, with no wire-protocol or server behavior
change. Share the continuation message definition between clients so their
meaning stays aligned. Remove obsolete originating-input reconstruction and its
Retry-only adapters/exports/tests if repository-wide usage confirms there are no
remaining consumers; retain any still-used helpers. Replace obsolete replay
expectations with continuation coverage rather than weakening tests.

Update `docs/daily-work.md` and the relevant browser/native rows of
`docs/product/subsystems.md` with current behavior. Historical design narratives
remain historical.

## Verification

Use TDD: first demonstrate that the existing Retry sends the old input or hides
the action, then change the implementation and show the regression passes.

1. Browser: click the real failed-turn button with the real thread store and
   mutation dispatcher. Check the submitted input is one text item with no
   image, skill, or command items; the earlier input occurs only once in history;
   and the current draft, selections, and attachments remain intact.
2. Native: press the real conversation error-row action through its document,
   conversation store, and mutation path. Check the same payload and preservation,
   including normal send-versus-queue selection.
3. Exercise text, inline-image, image-only with unavailable bytes, and unloaded
   earlier-input cases. Preserve native latest-turn/action-precedence gates.
4. Exercise a failed submission followed by successful recovery and a lost-reply
   reconciliation without creating a second continuation. Check retained drafts
   and the resulting completed turn, not just a mocked send callback.
5. Verify the full recovery journey against the real hub/session path with a
   scripted provider at the external LLM boundary. Observe the original failed
   turn, Retry, provider input with retained prior context plus the continuation,
   and completed output on both affected client paths. Do not require live quota
   failures or credentials.
6. Assert state, structured payloads, mutation identities and transcript records.
   Use whole-output golden snapshots where verifying model-facing message prose;
   do not make copy-fragment assertions or import the production message as the
   expected test value.
7. Run targeted client/shared tests, typechecks and touched-file formatting.
   Let CI run full gates; do not launch competing heavy local gates.

PAR reviews the frozen spec and implementation. Simplify the finished change,
shepherd CI and review, squash-merge under repository policy, verify GitHub's
merged content, then clean up only owned disposable artifacts. Deployment is
not authorized by this change.
