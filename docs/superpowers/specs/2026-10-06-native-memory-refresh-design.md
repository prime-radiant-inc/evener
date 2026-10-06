# Native memory refresh notifications

Status: proposed native amendment, awaiting Jesse's approval.

## Scope

Extend the [memory refresh design](2026-10-06-memory-refresh-notification-design.md)
to the native iPhone transcript. Jesse requested this follow-up after the web
delivery. This amendment replaces that design's native-presentation exclusion;
its web behavior and other preservation requirements remain in force.

This is an extension of the existing native notice renderer, Markdown component
and disclosure state. It adds no backend protocol, memory store, recall timing,
model-context change or new disclosure owner. Memory tools, CLI/TUI, deployment
and the deferred Android/iPad work remain outside scope.

## Reader experience

- Keep automatic refreshes as standalone rows in their recorded position.
- Use **Refreshed my memory** with the existing native disclosure affordance.
  Start closed at every verbosity level, including Full, with System events
  either on or off. General expansion defaults must not open these rows.
- Tapping opens scope, state and formatted index content through the existing
  native Markdown renderer. Reuse its current link and content policies.
- Keep unavailable and revoked states visible while closed. Show truncation
  honestly and distinguish empty, missing, revoked and unavailable observations.
- Include a separately folded **Source** inside an expanded refresh. It exposes
  the complete original recorded text, including syntax or content omitted from
  the formatted view. An invalid structured payload opens as complete original
  text rather than guessed content.
- Reuse native disclosure persistence, scoped by hub, session and item. Explicit
  open and closed choices survive row remounts and verbosity changes without
  affecting another session or hub. Preserve existing accessibility roles and
  expansion state; retain readable wrapping at narrow phone widths.
- Live and reloaded history use the same presentation. A malformed observation
  must not break adjacent rows or prevent a later valid observation rendering.

## Existing owners and smallest change

The shared model already retains `raw.memoryContext`. Native's
`projectedRows.ts` currently keeps the observation as an ordinary lifecycle
notice. Extend that path and the notice branch in `TimelineItem.tsx`, reusing
`SystemEvent`, `MarkdownResponse`, `nativeDisclosure` and disclosure keys.

Move the existing pure web payload validator and event discriminator into the
shared AppWire TypeScript package, following its exports convention. Both clients
consume the same validation rather than maintaining two decoders. Preserve the
web validator's accepted and rejected shapes and all existing web behavior.
Keep recorded source text available independently of decoded content.

Update `docs/product/memory.md`, the relevant rows in
`docs/product/subsystems.md` and `mobile-native/README.md` with verified behavior.

## Verification and limits

Use the real producer corpus in `agent/testdata/memorycontextwire/events.json`
through shared hydration/projection and the native renderer. Add failing tests
before implementation. Replace only the native assertions that deliberately pin
the old generic presentation; retain source-preservation and visibility checks.

Cover all five verbosity levels and both System events settings, real explicit
toggle callbacks, open/close through remount and verbosity changes, hub/session
isolation, formatted content, literal Source, state/truncation, malformed fallback
and later valid recovery. Keep ordinary notices, shared notes, steering and
memory-tool rendering unchanged. Exercise both live reduction and history
hydration. Shared extraction must retain the web parser and renderer regressions.

Run focused tests and `make test-native`, which includes the real iOS Metro
bundle, tests, TypeScript, script-import checks and Biome. Run affected shared/web
checks, then the repository's current-head CI and review gates.

Linux can establish source behavior and bundle resolution. It cannot establish
installed-iPhone rendering, touch interaction or geometry. A current-source iOS
simulator/device smoke check must verify tapping open/closed, formatted content,
Source access and narrow-layout wrapping. Until that separate check runs, report
device qualification as outstanding rather than treating JavaScript tests as
device proof. No simulator result or screenshot is claimed by this proposal.
