# Manage queued input on a phone

The native composer currently shows only queue depth. Add an inspectable queue surface using canonical `QueueState` and the existing shared conversation service. Each row shows its text, order and actions. Prefer a native sheet with comfortable reading width and compact separators, following the approved mobile guide; avoid one large card per message.

Preserve server identity: cancellation/promotion use the observed index plus expected entry ID and thread instance ID. Draining into the current turn uses expected queue revision. Stale state must refresh and explain the change, never silently target a new item at the old index. Disable unavailable actions according to current capabilities and running-turn state. Keep uncertain mutation outcomes explicit and do not retry automatically.

Expose Inspect queue from the composer whenever entries exist. Support cancel, use as steering input, and drain into steering where allowed. Present full text when supplied, otherwise clearly bounded previews; images need a count/attachment surface when the protocol supplies them. Preserve hub/session isolation, close the sheet when leaving the destination, and test both native back behaviors.

Before implementation, inspect the server handler contracts and web implementation for exact capabilities, ordering, notification and error semantics. Extend shared service wrappers only where needed; do not duplicate transport policy in screen components.

- [ ] Confirm server contracts and current native/shared state ownership.
- [ ] Behavioral tests through real shared state and scripted protocol boundary.
- [ ] Native queue surface and guarded operations.
- [ ] Both-platform manual E2E including stale queue, cancellation, steering, reconnect and no replay.

## Shared projection prerequisite

The prior MobileQueue projection dropped revision, entry IDs, client mutation IDs and full texts. It now preserves those fields through initial thread projection and `thread/queueChanged`, cloning arrays so source mutation cannot rewrite published state. Wire revision is required in the view model; fixture queues now declare it. Initial and notification paths use the same projection function.

Behavioral tests first failed on the missing identity/revision fields, then passed with the implementation. Shared mobile Vitest: 2,230 tests across 90 files passed. `mobile` typecheck and Biome gate passed. Native TypeScript and 53 tests passed. This is a data-contract prerequisite, not an implemented queue-management screen or manual native acceptance of queue actions.
