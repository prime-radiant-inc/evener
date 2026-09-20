## Result

Native preferences now use the shared raw draft backend and can discard a corrupt local draft without a connected hub. Compare-and-swap discard preserves a newer readable replacement, and a matching live model reclassifies through the existing recovery seam instead of mutating a snapshot copy.

## Scope and stack

This is the provider successor for the offline draft recovery decomposition formerly tracked by #1902, stacked on the storage slice #1950. Retained same-hub snapshot recovery is #1964, and the cold-offline recovery screen is #1904. The old #1902 branch is superseded and preserved separately; this PR does not include retained-snapshot or screen behavior.

Own non-test scope is 195 changed lines. The previous round-five provider implementation remains preserved byte-for-byte in the lane history.

## Validation and status

The six provider tests, 41 native draft tests, and six focused shared discard tests pass. Native typecheck, package-import lint, and diff checks pass, with failing-first provider regressions preserved. Independent review and simplification found no own must-fix. Local RoboRev2559 identified only the planned retained-snapshot dependency.

The coordinator's `offline-refresh-6cf.json` receipt records the current target `main` at `d25d5afaa7fee061801258c6b64fee558d958bb3`, this head at `b023817024734603805b469c4328529c4052741f`, and the reviewed own-patch proof SHA-256 `aedeb4a4f92473bff7a43b8fe401607007991bfa67c18976948ab7e4c42f4dc8`, with #1950 as the preceding offline-chain patch. Current CI and the raw review panel are still pending; Fresh exact-head CI and complete raw-panel qualification are required before merge. Optional provider `loadError` coverage remains #1948, while broader recovery simplification and the measured recovery-delay Low remain tracked in #1941.

Current main includes #1975. This merge-only refresh changes only the inherited retirement test; the entire owned patch is byte-identical. Current-head CI and complete substantive raw member reviews remain required.
