# Native hub upgrade workflow

Jesse authorized implementation through the native delivery plan, now scoped to
iOS-only v1. Use Luna medium implementers and independent review; Bot owns
integration, devices and commits.

## Contract and design

`evener/upgrade` with `{ requested: "" }` invokes the existing server updater.
Its successful response describes files installed and includes `restartMessage`.
`evener/settings/overview` describes the currently running binary. These are
separate facts: a readback, changed version or cached overview cannot establish
that a particular installation is now running. Never automatically repeat an
upgrade whose outcome is unknown.

The controller belongs to one immutable hub/profile and client. Save a pending
checkpoint before dispatch; storage failure prevents dispatch. Save a returned
installation response to that same hub even if its view has been disposed.
Late responses cannot publish to another hub's UI. Reconstruction restores
pending checkpoints as uncertain and known installation responses as installed.
Explicit refresh reads overview only and retains the original outcome.

Use `idle`, `running`, `installed`, `uncertain` and `storageUnavailable` states.
Only idle may dispatch. Display installation/restart instructions separately
from running version/commit. Do not silently erase a checkpoint or turn an
informational readback into verified success. A future explicit new-upgrade
transition needs deliberate user intent and a source-backed release target;
this initial controller does not invent that verification contract.

## Implementation and evidence

- [x] Add `hubUpgrade.ts` with injected storage and immutable hub ownership.
- [x] Add `hubUpgradeRepository.ts` with injected synchronous storage, per-hub
  keys and malformed-record rejection; `nativeHubUpgrade.ts` binds Expo SQLite.
  Avoid platform-shadow filenames that cause Metro to resolve an import to itself.
- [x] Exercise request parameters, checkpoint-before-dispatch, storage failure,
  lost reply, read-only reconciliation, reconstruction, and a deferred A reply
  after switching to B. Coordinator observed eight focused tests passing.
- [ ] Independent final review and integrated native typecheck/test gate.
- [ ] Add a purposeful Hub Settings upgrade surface, scoped to the active hub;
  preserve restart instructions and unknown outcomes across navigation/restart.
- [ ] Verify the existing deterministic Go updater seam; add failure coverage
  only where missing. Never download/install a real release in default tests.
- [ ] Qualify the iOS workflow using a disposable installation and independently
  read its running identity after a controlled restart. Record exact artifact,
  source, actual installer result and unresolved evidence separately.

No production upgrade, credential output, external messages, or forwarding/fault
proxy is authorized for qualification. Controller tests do not certify the
native UI, actual installation, restart, signing or release acceptance.
