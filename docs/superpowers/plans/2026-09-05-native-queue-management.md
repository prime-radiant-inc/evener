# Manage queued input on a phone

The native composer currently shows only queue depth. Add an inspectable queue surface using canonical `QueueState` and the existing shared conversation service. Each row shows its text, order and actions. Prefer a native sheet with comfortable reading width and compact separators, following the approved mobile guide; avoid one large card per message.

Preserve server identity: cancellation/promotion use the observed index plus expected entry ID and thread instance ID. Draining into the current turn uses expected queue revision. Stale state must refresh and explain the change, never silently target a new item at the old index. Disable unavailable actions according to current capabilities and running-turn state. Keep uncertain mutation outcomes explicit and do not retry automatically.

Expose Inspect queue from the composer whenever entries exist. Support cancel, use as steering input, and drain into steering where allowed. Present full text when supplied, otherwise clearly bounded previews; images need a count/attachment surface when the protocol supplies them. Preserve hub/session isolation, close the sheet when leaving the destination, and test both native back behaviors.

Before implementation, inspect the server handler contracts and web implementation for exact capabilities, ordering, notification and error semantics. Extend shared service wrappers only where needed; do not duplicate transport policy in screen components.

- [ ] Confirm server contracts and current native/shared state ownership.
- [ ] Behavioral tests through real shared state and scripted protocol boundary.
- [ ] Native queue surface and guarded operations.
- [ ] Both-platform manual E2E including stale queue, cancellation, steering, reconnect and no replay.
