# Decomposition audit — 2026-09-19

This is a read-only decomposition audit of the current offline draft recovery
and TUI marketplace work. No source files, branches, refs, or tests were
changed. The only write is this ignored coordinator ledger.

## TUI marketplace: split before round five

The current TUI head is `cc239432845c501ccd9d68e723151f259a723449` in
`/Users/jesse/.codex/worktrees/marketplace-tui-outcome/evener`, qualified
against server parent `16e0174391e1207f88f7eefe3a3e9d63ebb59317`. Its net
production tree delta is approximately `+173/-15`; the direct commit additions
are larger because several fixes replace earlier lines. The change contains
two independent production contracts and should be decomposed now rather than
carried into another review round.

The current round-4 Medium is real. `hub_update_config.go` currently treats a
successful mutation snapshot as authoritative while a typed unavailable
removal is waiting on a fresh list. A mutation command was started before the
removal, but Bubble Tea v1.3.10 delivers `tea.Cmd` results from independent
goroutines. Server/AppWire serial dispatch does not establish UI message
delivery order. Therefore an old successful mutation result can restore the
removed row and clear the pending reconciliation fence before the fresh list
arrives.

### TUI-A — typed marketplace outcome and removal reconciliation

This slice owns the domain contract that a marketplace mutation can report a
typed outcome: the clone is gone, the clone remains but the mutation was
applied, or the mutation outcome is unavailable and requires reconciliation.

Production boundary:

- `cmd/evener-tui/marketplace_outcome.go`: the classifier and decoder in full,
  including the marker-first handling and the malformed/partial applied-data
  hardening from `99e64ac4d` and `cc2394328`.
- The typed error branches of
  `cmd/evener-tui/hub_update_config.go` that classify applied versus unavailable
  outcomes, preserve the warning, apply an explicitly supplied applied
  snapshot, or start a reconciliation.
- The removal/reconciliation state fields in `cmd/evener-tui/hub_model.go` and
  the mutation action/name data needed to classify the result in
  `internal/launchconfig/plugins_client.go`.
- The associated typed-outcome tests in `hub_marketplace_outcome_test.go`.

The direct production contribution is about `+139/-16` when grouped by the
`ac60228d7` applied-removal commit (`+113/-10`), the classifier portion of
`99e64ac4d` (`+20/-5`), and the malformed partial-data portion of `cc2394328`
(`+6/-1`). The exact net is less useful than the file/function boundary; this
is inside the requested 80–150 production-line range without padding.

Dependencies are the already-qualified server/AppWire typed error contract;
TUI-A does not need list-generation ordering to classify a mutation. Its test
oracle is:

1. Typed and JSON forms classify applied, unavailable, and ordinary errors.
2. Nil, missing, malformed, or wrong-shaped applied payloads remain
   unavailable rather than being treated as a partial authoritative snapshot.
3. An applied-but-clone-remains result shows the warning and applies the
   complete supplied snapshot.
4. An unavailable result starts one generation-bearing reconciliation, keeps
   duplicate removals fenced, and preserves the warning until reconciliation
   succeeds.
5. Ordinary mutation errors retain their existing behavior.

This is a production-useful PR because it establishes the domain outcome and
the safety rule that prevents an uncertain removal from being silently
reported as successful. It can be reviewed without reasoning about stale list
responses or Tea command scheduling.

### TUI-B — ordered list reads and asynchronous settlement

This slice owns the passive-read ordering contract and the interaction between
list results and an in-flight mutation reconciliation. It stacks on TUI-A.

Production boundary:

- `MarketplaceListResultMsg.ListGeneration`,
  `CmdMarketplaceListWithGeneration`, and the shared list-command plumbing in
  `internal/launchconfig/plugins_client.go`.
- Generation allocation at the `/plugins` command and notification-refresh
  call sites in `hub_command_registry.go` and `hub_notifications.go`.
- The list-generation field and stale-list guard/floor in `hub_model.go` and
  `hub_update_config.go`.
- The list-ordering tests from `3f474ed71` and the mutation/reconciliation
  settlement tests from `cc2394328`, updated to exercise asynchronous result
  delivery rather than assuming server response order.

The existing boundary is about `+25/-8` from the generation-only part of
`99e64ac4d` plus `+36/-21` from `3f474ed71`, with the current settlement branch
at roughly `+4/-1`. The safe correction adds approximately 9–15 production
lines, putting the complete slice at roughly 74–80 additions. It is slightly
under the nominal range at the low end, but it is the smallest complete
read-ordering contract; adding abstractions or unrelated UI behavior solely to
hit a line count would make the split worse.

The safe settlement behavior is:

- If a successful mutation arrives while reconciliation is pending, do not
  apply its list or clear the warning/fence.
- Advance/invalidate the relevant list generation and issue a fresh
  `CmdMarketplaceListWithGeneration`.
- Only that fresh list result may settle the reconciliation and become the
  authoritative snapshot. If it fails, the fence remains; a later refresh can
  recover it.
- A successful mutation outside reconciliation remains authoritative under the
  existing rules.

This avoids adding mutation-provenance epochs while closing the actual Tea
delivery gap. TUI-B depends on TUI-A's pending state and typed unavailable
outcome, but it does not need to alter the outcome classifier.

Its test oracle is:

1. Start a removal, deliver typed unavailable, and verify the pending fence
   and warning.
2. Deliver a successful mutation result carrying the pre-removal row while
   reconciliation is pending; verify that the stale snapshot is ignored, the
   fence/warning remain, and a fresh generation-bearing list is requested.
3. Deliver the delayed old list and verify it is ignored.
4. Deliver the fresh list and verify it alone settles the fence and updates the
   panel; the next removal is then accepted.
5. Preserve the existing stale-list, applied-snapshot, failed-reconcile, and
   duplicate-remove coverage, including the rule that a successful mutation
   outside pending reconciliation may still update the panel.

This is a real split rather than a tiny follow-up fix: TUI-A defines what the
mutation result means, while TUI-B defines which asynchronous read is allowed
to change state after that result. Decompose the TUI work into these two PRs
before another review round.

## Native marketplace: split at the screen-store ownership seam

The native head is `67b5b90a3` in
`/Users/jesse/.codex/worktrees/marketplace-native-outcome/evener`, based on
`42e9ca`. Its production delta is 193 changed lines (`MarketplaceBrowser.tsx`
89 additions/9 deletions and `PluginsScreen.tsx` 94 additions/1 deletion).
The typed outcome, warning, and client-fenced guard form a meaningful first
slice through `54c661759`; the current-browser epoch added by `67b5b90a3` is a
second slice that is not the right lifetime boundary. It still leaves a valid
applied snapshot in a disposed Browser A store when the user remounts Browser
B, and the parent callback carries only the name and client, so B cannot
recover that snapshot if its first read fails.

### Native-A — typed outcome and host warning/guard

Keep the existing boundary through `54c661759`:

- `marketplaceRemovalOutcome` and its malformed/unavailable classification in
  the AppWire marketplace extension.
- The Browser mutation branches that distinguish an applied list from an
  unavailable outcome and report the business warning.
- The Plugins host's client-fenced warning and applied-removal guard, plus
  authoritative-list pruning.

This is approximately 148 changed production lines on top of `42e9ca` and
has an independent oracle: recognized applied-with-litter errors show the
warning and use the complete applied list; unavailable errors keep the
name fenced and request reconciliation; ordinary errors retain existing
behavior; a result from an old client cannot mutate the current host.

### Native-B — screen-owned marketplace model and remount reconciliation

Make `Plugins` own one `createMarketplacesStore(client)` instance alongside
its existing `createPluginsStore` model. Pass that model into
`MarketplaceBrowser`; move model disposal to the `Plugins` lifetime and keep
the SDK model's existing `connectionChanged` and `start` semantics. The
parent should own all three lifecycle calls: bind the connection and call
`start` in a layout effect (the credential-store hook uses this same ordering),
then dispose the model from a separate lifetime cleanup. That makes the model
ready before a Browser passive effect can fetch. The Browser mount effect
then keeps the wanted fetch-on-mount behavior while dropping its own
`connectionChanged`/`start` calls; its cleanup advances only the local UI
revision and must not dispose the shared screen model. Passive connection
transitions are handled by the parent even while Browser is unmounted.

Replace the epoch/ref plumbing from `67b5b90a3` with a reconciliation callback
that fetches through the parent-owned model. This is roughly 65–90 changed
production lines before the guard repair, likely around 75–105 including it.
It is a complete ownership and list-fence contract even at the low end of a
nominal 80–150-line target; padding it would make the split less coherent.

Do not treat an `AddMarketplace` success callback as the complete guard repair.
It can clear a guard for this screen's own add, but a second client can remove
and re-add the same name while this screen is offline; the later accepted list
then still leaves the name-only guard stuck. The warning remains truthful and
must not be suppressed. Native-B therefore depends on the shared SDK's
accepted-publication signal (the same prerequisite needed by the web consumer)
before it changes guard lifetime. Its regression must cover typed-unavailable,
failed reconciliation, another-client remove/re-add, and a notification or
reconnect whose accepted list contains the new same-name instance. Do not
clear the guard merely because any list contains the name, and do not use a
promise-completion callback as a substitute for accepted publication.

The test oracle is:

1. Remove in Browser A with a valid applied list, switch to Installed so A
   unmounts, then remount Browser B with its first list read failing. B still
   renders the SDK-owned applied list and the warning/guard remains correct.
2. Remove with an unavailable outcome while A is unmounted; the parent-owned
   model fetches through the same list revision stream, and B observes the
   settled result without snapshot injection or a second store.
3. A passive connection transition and a client replacement fence old replies;
   a late old-client outcome cannot mutate the current screen.
4. The accepted-publication seam clears a stale same-name guard after a
   cross-client remove/re-add; an unrelated list or promise completion does
   not.
5. Existing browse catalog retirement and list-revision tests remain valid.

Screen ownership is safer than direct `setState` snapshot injection: the latter
would bypass the SDK's `listRevision`, generation, and browse-catalog fences.
It also fixes the actual disposed-store loss for both valid and unavailable
outcomes without a new shared API.

One revision semantic remains a separate review point. The SDK deliberately
rethrows an older applied-with-litter failure even when a newer list revision
superseded its snapshot; the host warning reflects the cleanup business outcome
and must not be suppressed merely because that snapshot was not accepted. The
accepted-publication seam is specifically for guard lifetime, not warning
truth. Do not solve either concern by injecting a list or by keeping the
current-browser epoch machinery.

Native-A and Native-B are therefore real reviewable slices: A defines the
outcome and warning contract; B defines store lifetime and preserves the SDK's
authoritative ordering across Browser remounts. Do not carry the 193-line head
as one undivided PR.

## Offline draft recovery: keep the retained-snapshot change cohesive

The offline chain is already divided at useful product boundaries:

- Storage #1950 (`1f5cb2e61`): shared raw-string draft backend, about 142 own
  non-test production lines. This is the storage contract.
- Provider #1956 (`f7e6a6045`): probe, store-free discard, readable replacement
  CAS guard, and live-model reclassification, about 195 changed own non-test
  lines. These operations form one provider recovery contract; splitting probe
  state from its consumers would expose an unconsumed intermediate API.
- Retained snapshot (`dd60a43b90546e3cf7ab3e73571132fe7c71f6a7`): about
  132–140 own non-test production lines in `NativePreferencesProvider.tsx` and
  `nativePreferences.ts`.
- Screen #1904 (`aa48b44fd`): an independent screen seam, about 124 changed
  production lines across the screen/recovery helper and preference state,
  with its own cold-offline malformed-data and connected-diagnostic tests.

Do not split the retained-snapshot PR further. Its snapshot projection and its
explicit diagnostic-source fields enforce one invariant: same-hub retained
snapshot reclassification clears only draft-owned diagnostics while preserving
unrelated hub/load errors. The intermediate snapshot-only form contains the
known ownership bug where inferred `storageUnavailable` can erase an unrelated
hub error. A source-fields-only PR would expose fields without their consumer
behavior and would be helper-only/dead until the later projection change. That
would be a shuffle of inseparable invariants, not a production-useful slice.

## D6 draft/settings/transcript chain: #1792 -> #1841 -> #1844 -> #1845

This audit uses the immutable stacked ranges in the handoff, rather than
historical panel labels:

| Piece | Immutable range | Own production diff | Actual scope and dependency |
| --- | --- | ---: | --- |
| #1792 p7 | `3c6436d6f..41fc2f2e8` | `96+/12-` | Stamps a draft with its ready-generation identity, updates `keybindingsStore`, and exports `KeybindingsDraft`; it depends on the recovered p6c store and generation fence. |
| #1841 p8 | `41fc2f2e8..c2afb3836` | `151+/54-` | Extracts settings-hub retirement, lost-write settlement, and ready-generation notification wiring, then adopts all three in `keybindingsStore`; it depends on p7's fence and is the reusable core for transcript display. |
| #1844 p9 | `c2afb3836..a50ac1a95` | `166+/56-` | Extracts the checkpointed draft gate/persist/discard primitives, adopts them in `keybindingsStore`, and strips the generation field at the native preference projection; it depends on p8's shared generation core. |
| #1845 p10 | `a50ac1a95..737b7cebd` | `608+/19-` | Adds the transcript-display defaults store, per-layout direct PATCH state machine, decoder/fence tolerance, and package exports; it uses p8 but has no semantic dependency on p9's checkpointed editor. |

### Pieces that should remain whole

P7 is a smooth 108-line contract. Its generation stamp is earned by the first
authoritative payload, preserved through edits, and checked by the same store
fence. Splitting the interface/export from the store would leave an unconsumed
type; splitting restore from edit/save would separate one staleness invariant.
It can be prepared against the immutable p6c successor, but qualification still
has to rebase it onto the concrete recovery chain (#1950/#1956/#1964/#1904)
before merge. No history/export consumer blocks p7.

P9 is over the nominal target, but its three extracted operations are one
checkpointed-editor contract: the discard gate, durable save/refusal handling,
and classified-record discard/refusal handling all use the same repository
ownership and restore projection. The native seven-line projection is required
by p7's new generation field; peeling it out would be a tiny consumer-only PR.
Do not split p9 into helper-only or projection-only pieces. Its oracle is the
existing `checkpointedDraftEditor.test.ts` plus the unchanged keybindings and
native projection tests, with the intentional fake-backend test cleanup
preserved.

P8 is just above the nominal target, but its current 205 touched lines can be
split cleanly if review pressure demands it:

- **P8-A, retirement/settlement core:**
  `retireSettingsHubPayload` and `settleUnsettleableWrite`, their tests, and
  the two `keybindingsStore` call-site adoptions. This is roughly 95–125
  production lines and has a complete oracle for fencing, write uncertainty,
  superseded writes, and same-publish extras.
- **P8-B, ready-generation wiring:**
  `createSettingsHubGeneration`, its tests, and the keybindings
  begin/end/wire replacement. This is roughly 70–95 production lines and
  depends only on P8-A plus p7. It owns notification subscription ordering,
  unwiring, and retirement invocation.

This is a real split because the first slice is already consumed by
`keybindingsStore`; neither PR ships an unreachable helper. If the coordinator
chooses not to spend a round on p8, the original p8 can remain cohesive; do
not split the generic helper from its only consumer.

The immutable p8 review record shows no own instability comparable to the TUI
or p10 findings. Therefore this is an optional queueing split, not a required
repair: keep p8 cohesive unless the review queue needs the two contracts
separately, and spend the decomposition pressure on p10 first.

### Required decomposition: p10 read contracts, read lifecycle, and direct write

P10 is not smooth at `608` production additions. Its tests expose a further
boundary inside the read side: `transcriptDisplayStore.test.ts` has independent
hub-default/read-generation cases and direct-write cases, while the package
already has two production consumers of the lower-level decoder changes. Do not
make a 350–390-line P10-read PR merely because it is under the hard ceiling.
Make the first read PR the smallest useful wire-contract slice, then put the
fence/publication state machine in a second read PR, and retain direct PATCH as
the third slice. All three start from p8 or a named predecessor; p10 has no
semantic dependency on p9.

- **P10a — `D6 p10a: stabilize transcript-display wire contracts`:**
  Move only the p10 additions in `errors.ts` (`wireRejectionPayload`), the
  forward-compatible `fromWireDefault`/`fromWireDefaults` behavior in
  `transcriptDisplayConfig.ts`, the `keybindingsStore.ts` adoption, and the
  corresponding decoder/error tests. The rejection helper has a live
  production consumer in `keybindingsStore`; the default decoders are already
  consumed by the existing web `transcriptDisplay` store. This is about
  `36` production additions (`18 + 9 + 8 + 1` for the public error export),
  plus focused tests. It is intentionally smaller than 80–150; padding it with
  read-store types would create an orphaned API or pull lifecycle state across
  the boundary. Its oracle is malformed/foreign rejection payloads, default
  records with future top-level keys, and the existing keybindings conflict and
  post-apply paths. The exact starting point is immutable p8
  `c2afb38362d562370e81de5f559d162ae5ef6d4e`; it does not need p9.

- **P10b — `D6 p10b: add the transcript-display read generation store`:**
  Stack on P10a. Add the read-only half of `transcriptDisplayStore.ts`:
  `TranscriptDisplaySupport`, layout/default/change contracts,
  `fromWireChange`, store state with support/loading/error/confirmed defaults,
  `setSupport`, `beginReadyGeneration`/`endReadyGeneration`, `detachHub`,
  `reset`/`dispose`, notification and relayed-change handling,
  `applyHubDefault`, and `refreshHubDefaults`. Include the
  `readyGenerationFence` first-authoritative-payload state, its tests, the
  `createSettingsHubGeneration` wiring, package exports, and the complete
  read-side test group. Keep `saving` and `writeUncertain` only as the idle
  fields required by the already-shipped p8 retirement primitive; do not add a
  PATCH action or preview state here. The package store itself is the complete
  public consumer of these APIs, so this is not helper-only even though the
  later web adapter is the first host integration.

  This is approximately `280–330` production lines after removing the direct
  write helpers from the current source, plus the 19-line fence extension and
  exports. It is a real lifecycle/publication boundary: first-payload
  authority, notification-before-read recovery, revision filtering, and
  generation retirement must remain together, but they do not require PATCH
  decoding or preview ownership. Its oracle is the existing read cases through
  the “hub defaults” describe: support transitions, independent stores,
  malformed notifications, stale/equal revisions, lower first-generation
  revisions, missed notifications, transient disconnect/identity reset,
  disposal, and late-reply fencing. P10b depends on P10a for the decoder
  contract and on p8 for shared generation retirement; it can be prepared
  before p9 merges.

- **P10c — `D6 p10c: add transcript-display direct PATCH reconciliation`:**
  Stack on P10b and add `hubErrors`, `drafts`, `patchTokens`, preview bases,
  `layoutError`/preview retirement, PATCH response/conflict/post-apply
  decoders, `patchHubDefault`, the remaining `applyHubDefault` preview
  contradiction behavior, and the direct-write tests. Estimate
  `160–220` production lines. Preserve every existing write permutation:
  fenced replies, superseded writes, lower/equal new-generation revisions,
  malformed replies, conflicts, preview contradiction, and post-apply durable
  failures. This is the only slice that owns the PATCH action; there is no
  placeholder mutation API in P10b.

The smallest independently testable starting slice is therefore P10a, not an
arbitrary subset of the read store. Its production delta is small because the
two changed contracts already have live consumers, and the test oracle is
independent. P10b then owns the complete read state machine rather than
publishing a fence or decoder with no consumer. P10c grows the same public
store once, avoiding a temporary duplicate store. The later checkpointed
transcript editor remains the separate p12 design from the handoff.

The exact next bounded task is therefore **P10a from the immutable p8 tip**,
followed by P10b's read-generation store and then P10c's direct write. P7
waits on the concrete recovery qualification; P8 follows p7; P10a is otherwise
independent of p9, and P10b can be prepared before p9 merges. The current
history/export work does not justify starting a mobile consumer: `index.ts` and
`tsconfig.build.json` are package surface changes owned by these pieces, while
the web adapter and native projection remain later consumers. Preserve every
current WIP/test deletion; do not reconstruct the chain from older panels or
old backup refs.

The inherited live-model reclassification issue measured as recoverable through
the existing “Check current shortcuts” refresh path and is Low #1941, not a
decomposition blocker. The source-provenance/loadError coverage item is the
separate Low #1948 follow-up. The screen head has an independent PASS and
should remain separate from provider/storage work.

## Disposition

Decompose the TUI head into TUI-A (typed outcome/reconciliation) and TUI-B
(generation-ordered reads and asynchronous settlement) before round five.
Decompose the native head at `54c661759`: keep Native-A's typed outcome and
host warning/guard contract, then make Native-B a screen-owned marketplace
store with remount-safe reconciliation. The same-name guard remains dependent
on the shared accepted-publication seam, not an own-add callback. Do not use
direct snapshot injection; keep the SDK list-revision and catalog fences
authoritative.
Keep the offline retained-snapshot change as one cohesive PR; retain the
existing storage, provider, and screen boundaries. No further offline split is
meaningful without either shipping an unconsumed helper or reintroducing the
diagnostic-ownership bug.
