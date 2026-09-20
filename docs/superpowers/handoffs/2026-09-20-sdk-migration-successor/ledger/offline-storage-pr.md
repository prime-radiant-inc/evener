Adds a shared keybinding-draft decoder and a raw string-storage backend that classifies absent, readable, and corrupt records without losing the identity needed for compare-and-swap recovery. Symbol markers keep a stored JSON null distinct from absence and canonical decoded identity avoids object-key-order mismatches.

This is the storage-only replacement for part of #1902. The provider-wiring successor will use this backend in production; merge is held until that PR is concrete. The remaining retained-snapshot recovery fix stays in its own following PR. The original round-five branch is preserved as codex/offline-draft-round-five.

Validation: 39 native storage tests and 3 targeted decoder tests, native/frontend typecheck, package qualification and root-export falsification, package-import lint, AppWire Biome, and diff checks pass. Independent review passed. Local RoboRev2552 identified only the explicit provider-wiring dependency. Refresh to main 9cb596336f6b33fa61606f950b99dbf3829a9222 preserves the reviewed product patch byte-for-byte (SHA-256 d94e012184796e2c24373c78435f5eab08705756a2b142fe09c5d91df57c496f).

Own non-test scope: 142 changed lines. Raw-backend CAS test coverage is tracked in #1948; provider/screen code is outside this PR.
