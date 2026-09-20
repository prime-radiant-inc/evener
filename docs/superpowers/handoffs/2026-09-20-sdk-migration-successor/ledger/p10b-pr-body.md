Add a shared transcript-display defaults store that fences reads and notifications by the active hub generation. A reconnect can accept the restarted hub revision sequence, while responses from retired generations cannot overwrite current state. Support changes and notifications missed during initial loading trigger the required refresh.

This slice provides the read-side SDK lifecycle and package exports. PATCH/preview reconciliation and web adoption remain separate, retained successor slices.

Validation: focused store and generation-fence tests, package build and qualification, frontend typecheck, Biome, and package-import checks. Full-branch RoboRev qualification is recorded before publication.
