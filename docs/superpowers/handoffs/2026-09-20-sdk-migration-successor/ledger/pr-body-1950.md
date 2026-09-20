## Result

Native preference drafts now use a shared decoder and raw string-storage backend that distinguish absent, readable, and corrupt records while preserving the identity required for compare-and-swap recovery. Symbol markers keep stored JSON `null` distinct from absence, and canonical decoded identity avoids object-key-order mismatches.

## Scope and stack

This is the storage-only successor for the offline draft recovery decomposition formerly tracked by #1902. Provider wiring is #1956; retained same-hub snapshot recovery is #1964; the cold-offline recovery screen is #1904. The old #1902 branch is superseded and preserved separately. Provider and screen behavior remain outside this PR.

Own non-test scope is 142 changed lines. The original round-five branch remains preserved as `codex/offline-draft-round-five`.

## Validation and status

The 39 native storage tests, three targeted decoder tests, native/frontend typechecks, package qualification and root-export falsification, package-import lint, AppWire Biome, and diff checks pass. Independent review passed. Local RoboRev2552 identified only the explicit provider-wiring dependency.

The coordinator's `offline-refresh-6cf.json` receipt records the current target `main` at `d25d5afaa7fee061801258c6b64fee558d958bb3`, this head at `7db6c56bb77eb238700b07d40895ac305d864e57`, and the reviewed own-patch proof SHA-256 `78df920453b8e6059a9218f7961a284797c691fdc95100da598fb587354b9846`. Current CI and the raw review panel are still pending; Fresh exact-head CI and complete raw-panel qualification are required before merge. Raw-backend CAS coverage remains tracked in #1948, while broader native recovery simplification and the measured recovery-delay Low remain tracked in #1941.

Current main includes #1975. This merge-only refresh changes only the inherited retirement test; the entire owned patch is byte-identical. Current-head CI and complete substantive raw member reviews remain required.
