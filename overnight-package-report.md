# Overnight package report

Branch: `codex/mobile-sdk-package`

The package substrate is implemented in `cmd/evener-hub/frontend/src/protocol`.
It exports the strict AppWire client, generated protocol catalog, transport
contract, wire errors, and the existing pure question answer formatter. The
package metadata builds a CommonJS distribution with ESM and CommonJS export
conditions. The package includes only the two read-only connection and
inspection examples needed for an initial consumer.

The branch is stacked on strict-client commit `79ccc0471ac750aeaaf9ff50409ed825c6e28ac2`,
which cherry-picked as local commit `8ab261301` because the package requires
its handshake decoder boundary. Portable activity and job helpers are owned by
the companion worker and remain a dependency for later export/example batches;
this branch does not duplicate or edit those files.

Qualification is installed-consumer based. `npm run build` passed, and
`npm run qualification` passed after packing the tarball into a fresh temporary
consumer, installing offline, compiling ESM and CommonJS TypeScript imports,
executing both runtime import forms, and checking the tarball contents. Focused
client and error behavior also passed: 84 tests via Vitest.

This does not claim the SDK outcome matrix is complete. Read-only discovery,
portable projections, notification bounds, and deliberate mutation recipes
remain subsequent capability batches. No hub endpoint, credential, or native
device was used.
