# Native hub upgrade evidence

The iOS-only v1 Hub Settings route now exposes a collapsible Hub update section.
Its controller is owned by the immutable selected hub/client and disposed when
the connection or screen changes. Installation/restart details are separate
from the running version/commit. Unknown outcomes offer read-only refresh;
storage failures do not falsely claim that no installation occurred.

## Observed 6 September 2026, Pacific time

On the iPhone 17 Pro iOS 26.5 simulator, the coordinator built and installed the
Release app from the authoritative integration worktree with these UI changes.
JavaScript bundle SHA-256:
`b786cdbe7d203332ba31a74a3a30385ef568e06a055e90f99902a3b5fc1bc01a`.

Against the direct owned v4 fixture hub on port 54211:

1. Open Sessions → Hub settings → Hub update.
2. Observe selected profile **v4 acceptance**, running version **0.1.0** and
   truthful unavailable commit (this fixture binary has no commit metadata).
3. Tap Upgrade hub. The native confirmation names the selected hub and explains
   that the installed update may need a restart.
4. Cancel. The section returns to idle with Upgrade hub available. No installer
   was intentionally dispatched in this journey.

![Native confirmation](assets/hub-upgrade/confirmation.jpg)

`make test-native` passed 395 tests across 51 files plus typecheck. The existing
`TestHubRPCUpgradeRunsSelfUpdater` and `go test ./internal/selfupdate -count=1`
also passed. Controller tests separately cover durable intent before dispatch,
late A results after switching to B, unknown replies and failed storage.

## Remaining acceptance

This is confirmation/cancellation evidence, not an actual-install or restart
qualification. Still required: disposable-prefix installation, reconnect and
independent running-identity verification, failure states on device, pending
status while disconnected, VoiceOver/large text, process death and an explicit
source-backed flow for initiating a later update after a previous installation.
An overview change alone cannot prove which installation is running. Production
installations and credentials were not used for this test.
