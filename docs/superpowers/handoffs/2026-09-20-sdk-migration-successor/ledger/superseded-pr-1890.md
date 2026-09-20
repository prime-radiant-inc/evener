Superseded by #1940 (server applied-with-litter outcome) and #1954 (revision- and generation-fenced SDK reconciliation). The original branch remains preserved. Web/native/TUI presentation and retry handling are required consumer follow-ups before that stack merges; #1897 remains the separate server migration successor.

---

Decomposed from #1810 (closes part of #1700), split 2 of 3. Stacked on #1889; diff against main includes it until it merges.

Ruling 2026-09-18: keep the RemoveMarketplace behaviour change (save metadata before deleting the clone; a clone-cleanup failure after a successful save is returned to the caller, never swallowed as a warning with a success reply), with its rewritten tests.

`RemoveMarketplace` now saves the metadata first — a save failure is a plain refusal that leaves the marketplace registered and its clone untouched — and only then removes the clone. A clone-removal failure is reported via the new `cloneRemovalFailed` helper (named for the domain outcome per #1810's round-5 Low, not the mechanism) instead of swallowed as a warning while returning success. Rewrites `coverage_marketplaces_fuzz_test.go`'s clone-removal-failure case to expect the error, and adds `TestRemoveMarketplaceSaveFailureNamesNoPath` / `TestRemoveMarketplaceCloneRemovalFailureNamesNoPath`.