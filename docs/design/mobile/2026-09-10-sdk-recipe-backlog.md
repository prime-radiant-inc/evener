# SDK recipe backlog (preservation draft)

Status: incomplete recovery snapshot. This branch preserves the SDK discovery recipe source from `codex/mobile-sdk-discovery-examples` at `32ecd0b5760edf337308840e5efa0401ddb9cb7e`. It is a candidate for later stacking on the reviewed #1108 SDK changes; it is not a buildable or release qualified deliverable.

## Preserved recipe surface

The branch contains the existing example recipes under `cmd/evener-hub/frontend/src/protocol/examples/`, including the discovery client, CLI wrapper, discovery validation logic, WebSocket connection helper, inspection example, and private output helper. No source recipe was overwritten during preservation; the worktree was created directly from the exact source commit.

## Follow-up batches

1. Rebase or stack on the final #1108 SDK contract changes and confirm generated protocol types match the recipe imports.
2. Run the installed-package qualification against a real external WebSocket boundary. Keep the server at the scripted WebSocket boundary and exercise all seven discovery actions, malformed responses, wire errors, and invalid input with no RPC for rejected parameters.
3. Verify private-output lifecycle outcomes with fixture-owned paths: mode `0600`, exclusive creation, replacement-path preservation after failure, cleanup of only owned temporary artifacts, and no deletion of a user-selected pathname on failure.
4. Run the frontend Biome scope and package qualification from the pinned toolchain, then record exact package/source hashes and output ownership. These checks are pending for this preservation snapshot.

No live provider, hub, invitation, Apple, TestFlight, or external mutation was executed while preserving this draft.
