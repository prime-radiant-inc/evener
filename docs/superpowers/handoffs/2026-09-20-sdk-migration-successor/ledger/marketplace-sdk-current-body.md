Marketplace removal can apply successfully while clone cleanup fails. Consume the typed error's usable applied snapshot through the original mutation revision and generation, settle list/loading/error/cache state together, then preserve the cleanup error for callers. Stale failures cannot overwrite newer successes; unavailable or malformed payloads cannot erase the list.

Accepted whole-list snapshots also retire cached catalogs for absent marketplaces. This covers an older applied cleanup failure held behind a newer unrelated successful removal: the older writer may be superseded, but its removed marketplace cannot retain a stale catalog. Retirement also fences in-flight browse replies.

This is the SDK successor to merged #1940. Publication #1973 and web #1960 remain separate presentation/retry-handling successors.

Validation: 154 deterministic extension tests, frontend typecheck and package-test collection, AppWire build and installed-package qualification, Biome, package-import checks, and diff checks passed. The cache-retirement regression failed before the correction. Independent review and RoboRev2684 passed the correction on top of the reviewed parent; current-head CI and remote review are required before merge.

Tracked Low follow-ups remain separate: #1953 row validation, #1944 round-trip coverage, #1951 logging, and #1959 late outcome coverage.
