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

No native source changed in this increment. The separately reproduced Dynamic
Type reader failure remains open, as do the full iOS release matrix and final
canonical merge gate. Android source is preserved and qualification is deferred
for iOS-only v1. Neither recipe was published, and no branch was pushed or merged.
