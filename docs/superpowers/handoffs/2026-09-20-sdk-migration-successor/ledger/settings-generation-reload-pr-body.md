A failed local save can reload the same shortcut draft and erase its ready-generation stamp. After a reconnect reuses the same revision, that could make an old draft appear saveable again. This change preserves the stamp when the classified stored checkpoint has the same identity, while treating replacement records as newly restored drafts.

This is the immediate recovery successor to merged #2014 and the final small replacement for #1792. It addresses local RoboRev #2644 and remote member #22318.

Validation: 125 focused tests, production reversal checks, TypeScript build, package qualification, import lint, and scoped Biome passed. Independent correctness/simplification review and local RoboRev #2648 passed. Restacking onto merged #2014 preserves the complete binary patch exactly (SHA-256 c92cca66ae7a9b632403272c93fdede974ed77b0cc612a288d9c7b4ccd93d52c). Current-head CI and raw remote review are required before merge.
