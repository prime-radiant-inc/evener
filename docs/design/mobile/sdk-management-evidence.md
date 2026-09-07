# SDK management recipe evidence

## Scope and artifact

The provider-instance and plugin-management examples extend the packaged
`@evener/appwire-client` without changing its transport or generated contracts.
The continuation starts from `93e817cd3`. Luna medium agents supplied source
proposals and reviewed the integrated algorithms; their filesystem views were
unreliable, so root integrated the files and executed all checks locally in the
authoritative worktree.

The tarball installed in a separate consumer for the observations below has
SHA-256 `cf291967ba53461145a5d594632860a6f4a42ba267e6698f9c63290337195dc2`.
It was installed with scripts disabled and offline package resolution. The
package qualification gate separately packed and installed an artifact outside
the repository and ran its declaration, runtime-import and example contracts.

## Executed checks

- `make test-api-package`: passed, including ESM/CommonJS declarations and
  runtime imports, packed contents and installed example contract tests.
- All five `examples/*.contract.mjs` files: 52 tests passed. The 28 new checks
  exercise all six plugin and four instance mutation actions, exact targets,
  omitted/empty/false values, preflight refusal, malformed responses, lost
  replies, matching readback without an acknowledgment, and ordered error
  preservation. These use a scripted SDK boundary and issue no network calls.
- `make test-web`: typecheck, tests and Biome passed. Touched source files were
  formatted with Biome; `git diff --check` passed.
- Both installed CLI examples ran in their default read-only mode against the
  direct owned v4 hub on port 54211. Plugin count was zero; instance count was
  two and registry writes were allowed. Output contained counts/status only.
- The installed provider logic created `sdk-management-fixture-20260907` from
  the `ollama` base, edited its endpoint to a deliberately unused loopback
  address, set it as default, restored the original `fake` default, cleared the
  endpoint override, and removed the fixture. Each step received an
  acknowledgment and passed an independent list assertion. The final complete
  instance list equaled the initial list. No inference request was issued.

The catalog report lists 10 recipes containing 36 of 91 request names and three
notification names. This measures recipe presence, not live execution of every
listed request, branch/outcome coverage or release completion.

## Limits and next work

Actual plugin download/installation/upgrade and provider connectivity were not
exercised. The test hub had no installed plugin, so plugin mutation acceptance
still needs a disposable marketplace fixture. Credential flows, marketplace
management, decisions, hub upgrades and additional recovery recipes remain open.

The examples issue one mutation and one immediate readback attempt. A socket
loss can also prevent that read; they do not wait through a reconnect loop or
automatically repeat mutations. After an incomplete operation the caller must
read current state and prepare a deliberate next action. Readback does not
prove which writer made a change. These contracts have no mutation ID or
revision precondition; preflight checks are not atomic with writes.

No native source changed in this increment. Reader diagnosis and qualification
are recorded separately in [reader evidence](reader-continuity-evidence.md).
The full iOS release matrix and final canonical merge gate remain open.
Android source is preserved and qualification is deferred
for iOS-only v1. Neither recipe was published, and no branch was pushed or merged.

## Marketplace and sandbox approval recipes

The continuation from `92bfcbf5d` adds marketplace list/browse/add/refresh/remove
and sandbox approval read/resolve examples. Both use the existing one-mutation,
one-readback recovery helper and separate owned-endpoint confirmation. Approval
resolution captures the reviewed card before connecting, checks its full current
contents and instance identity, preserves explicit denial, and rejects a replaced
instance on readback. It always reports execution as unverified. The server
resolve contract lacks an expected-instance or mutation-ID precondition; the
client check cannot close the race between preflight and dispatch.

The final tarball used below has SHA-256
`707c7f1ff6798aff1ce3f1f1fcf901737499a620fef96d1c30f60fa51c92bca5`.
It was packed from the authoritative worktree and installed outside the checkout
with offline resolution and install scripts disabled. The final package gate
also installed and ran the artifact independently. Luna medium agents supplied
proposals and reviewed the algorithms; root corrected and integrated them and
ran the checks because the agents could not access the current filesystem.

- All seven example contract files pass: 81 tests, including 29 new cases.
  These exercise every action, source kinds, omitted/empty/false values, full
  approval-card equality, caller changes while connection awaits, stale bindings,
  malformed replies, one dispatch without replay, and both-error preservation
  including thrown `undefined` and `null`.
- The initial checks failed while the implementation modules were absent.
  Live alias browsing then exposed a wrong assumption in the first implementation:
  Browse returns the manifest catalog name, which can differ from its registered
  alias. The added alias regression failed before the correction and passed
  afterward. The request still uses the exact registered alias; the response
  preserves the manifest name. This matches `hubPluginsController.Browse`.
- Final `make test-api-package` and `make test-web` passed. The latter reports
  typecheck, tests and Biome passing; touched source formatting and diff checks
  pass. No generated contract or native source changed.
- The installed final CLI recipes read the direct owned v4 hub on port 54211:
  marketplace count two after cleanup, and zero pending sandbox approvals on
  the retained reader fixture. Output contained only counts/outcomes.
- The final installed marketplace helper read the alias left by the first live
  attempt, successfully browsed it, deliberately removed it, and added it again.
  `sdk-marketplace-acceptance` returned catalog name `native-git-acceptance` and
  one `native-tools` entry. Refresh fetched a changed local Git commit and its
  v4 content sentinel. Both owned marketplace registrations were then removed;
  the complete original two-entry marketplace list was restored exactly.

The earlier plugin-management tarball also installed the owned Git plugin,
upgraded it to a new revision, disabled/enabled it, toggled automatic upgrades
on/off and removed it. Its direct native notification and Git-failure recovery
acceptance is recorded in [plugin evidence](plugins-evidence.md#direct-v4-git-upgrade-and-recovery).
The local Git fixtures contain manifests and sentinel text only. Seeded
marketplaces were not browsed or refreshed and no production runtime was used.

The recipe catalog now names 12 examples containing 42 of 91 request names and
three notification names. These are presence counts, not executed-path or
release coverage. Actual sandbox approval/denial with tool execution still needs
an owned scripted-provider acceptance fixture; injected notifications are not
proof of resumption. Questions, credentials, continuous reconnect handling,
remaining SDK recipes and the full iOS release matrix remain open.
