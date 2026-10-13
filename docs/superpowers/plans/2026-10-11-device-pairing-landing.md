# Shared device pairing: landing and iOS distribution

## Goal and approved scope

Land the existing generic invitation/approval protocol on current main, qualify
the web and native clients, deploy the merged Hub to magic-kingdom, and publish a
GitHub-built iOS TestFlight build. Jesse performs the physical iPhone check.
The protocol reference is `docs/device-pairing.md`. The standalone Playdate
client remains in prime-radiant-inc/evener-client-playdate.

## Global constraints

- Work in the isolated `codex/device-pairing` branch. Extract shared changes
  from `codex/playdate-client`; omit hardware experiments and unrelated history.
- Hub/web behavior is device agnostic. Sound and QR carry the same invitation.
  Pairing works from plain HTTP on the tailnet and preserves explicit approval.
- Preserve existing manual and direct-link onboarding, review before saving,
  client-owned keys, cancellation, retry, expiry and background recovery.
- Use targeted local checks and end-to-end qualification; full CI is authoritative.
- Use the existing GitHub iOS workflow and its Apple processing receipt. Never
  claim physical iOS validation from unit tests, bundling or an upload alone.

### Task 1: Extract and qualify the shared implementation

Produces: typed AppWire methods, Hub invitation/claim lifecycle, shared TypeScript
codec, settings UI, native Add Hub flow and evergreen docs.

1. Apply only the shared patch onto current main and inspect its scope.
   Expected: no hardware source, private credentials or experimental relay.
2. Reconcile generated files and dependency ownership. Run focused Go tests,
   pairing codec and Add Hub tests, web typecheck and formatting, native bundle
   gate and package qualification.
   Expected: contracts and current client consumers pass.
3. Run the real local Hub/browser approval check and acoustic interoperability
   using the separate Playdate checkout.
   Expected: approved credentials authenticate; rejected/cancelled/expired claims
   cannot replace a reviewed Hub; sound decodes the same invitation as QR.
4. Commit the implementation with provenance and validation evidence.

### Task 2: Review and land

Consumes: Task 1's shared implementation and qualification evidence.
Produces: merged main commit with successful CI and reviewed exact head.

1. Run simplify-code's four read-only review angles and a whole-branch correctness
   review. Resolve findings with targeted regression coverage where needed.
   Expected: scoped maintainable implementation with no unresolved important bugs.
2. Open and attach the PR, run CI/RoboRev, read the review body and per-commit
   reviews, reconcile current main before final approval, then squash merge.
   Expected: all checks pass on the reviewed final head and merge is confirmed.

### Task 3: Deploy and distribute

Consumes: Task 2's merged source revision.
Produces: live Hub pairing and a processed internal TestFlight build.

1. Deploy the merged Hub using its established service workflow. Verify the
   running version and generic pairing endpoint without disrupting saved work.
   Expected: magic-kingdom serves the merged feature and authenticates normally.
2. Dispatch the existing GitHub iOS TestFlight workflow on main with an unused
   Apple build number. Follow its archive, upload, processing and group receipt.
   Expected: the receipt identifies the merged feature's source and available build.
3. Update the standalone client's canonical protocol reference and give Jesse
   concise TestFlight steps: scan, compare code, approve, review/save, use Hub.
   Expected: documented handoff distinguishes delivered build from physical testing.

## Review focus

Check HTTP origin handling, invitation and credential separation, key ownership,
expiry and retry after dropped responses, conflicting claims, cancellation and
background/foreground lifecycle, plain-HTTP tailnet use, Metro resolution of the
shared crypto dependency, and generation/registration consistency.
