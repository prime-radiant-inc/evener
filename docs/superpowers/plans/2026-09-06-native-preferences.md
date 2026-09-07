# Native iOS preferences

Jesse authorized complete native feature coverage and selected iOS-only v1.
Use Luna medium workers for bounded packages; Bot owns integration and devices.

## Current contracts

`evener/settings/keybindings/get` and `/patch` use version-1 rule arrays and a
safe nonnegative revision. Preserve unknown action rules; a `loadError` means
the hub returned fallback rules and edits must be blocked. Patch carries the
confirmed `expectedRevision` and complete rule array.

`evener/settings/transcriptDisplay/get` returns desktop/mobile defaults. Patch
carries `layout: "mobile"`, `expectedRevision` and the complete wire config.
Use the framework-independent canonical `fromWireConfig`/`toWireConfig` from
`frontend/src/transcriptDisplay/config.ts`; custom content is nested on the
wire and flattened in the normalized model. Do not duplicate a partial decoder.

Each model belongs to an immutable client and explicit capability snapshot.
Dispose/recreate on connection replacement. A stale GET must not overwrite a
newer acknowledged write; refresh during a pending PATCH must not discard the
pending draft. Notifications retain pending drafts. A confirmed PATCH may clear
its own pending state; an older response must not replace a newer notification.
Block duplicate, uncertain, disposed and load-error writes. Unknown outcomes
require explicit read-only reconciliation, never automatic mutation replay.

## Delivery steps

- [x] Implement the capability-gated controller and canonical transcript decode.
- [x] Coordinator observed four failing deferred/state regressions, then fifteen
  passing tests across both domains after correction. Coverage includes stale
  GET after PATCH, GET during PATCH, duplicate/uncertain/disposed writes, late A
  events after opening B, nested custom config and load-error write rejection.
- [x] Preserve transcript proposals and uncertain checkpoints in hub-scoped local
  storage before exposing the editor. Restore synchronously and fence late
  acknowledgements by operation identity. Simulator restart evidence is recorded
  below; the complete hub-switch/death matrix remains in qualification.
- [ ] Add native keybinding editing with meaningful action labels, complete rule
  preservation, explicit save and conflict/recovery UI. Verify hardware keyboard
  behavior separately from editing the hub's stored configuration.
- [x] Wire confirmed transcript display settings into the native presentation
  and disclosure path. Preserve the unfiltered store and source identity; apply
  content/advanced settings while retaining critical context and attachments.
- [x] Add the mobile transcript editor only with matching renderer behavior,
  including custom settings and metadata availability. Preserve unrelated fields.
- [ ] Qualify both workflows on the isolated v4 hub and current iOS artifact:
  explicit save/readback, external changes, hub switches, keyboard/VoiceOver,
  large text, process restart and uncertain outcome recovery.
- [x] Add corresponding executable SDK recipes and document supported behavior
  (625c59d4e, including outside-checkout qualification).

No production settings changes, credential output, external messages or fault
proxies are part of qualification. Deterministic client tests exercise ordering
faults; native journeys exercise the real isolated hub and actual UI. Passing
controller tests does not establish editor, renderer or release acceptance.

The transcript editor and owned-hub read/write/conflict/restart journey now have
[scoped evidence](../../design/mobile/transcript-preferences-evidence.md). Native
keybinding UI and the complete preference acceptance matrix remain open.
