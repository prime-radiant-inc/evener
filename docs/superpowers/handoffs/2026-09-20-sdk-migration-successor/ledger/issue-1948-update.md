The independent review of the native offline-draft storage replacement at 700b111a861b006b3d59c7535fc8a7f696b8384f found one Low test-coverage gap. Valid replaceIf and insertIfAbsent cases use the in-memory fake; directly exercise these contracts through rawStringDraftBackend with a fake string-storage port.

Cover matching replacement, stale-marker rejection, absent insert, existing-value refusal, malformed/null storage, and canonical object identity without relying on property order. Assert persisted values and return behavior. Keep the existing provider recovery coverage in its own successor. This is a test-only follow-up after the storage replacement, not a reason to expand the storage PR.


Additional Low coverage from the storage/provider reviews: compare decodeKeybindingDraftFields with the live keybindings-store restore projection using shared valid, absent/null, invalid-object, malformed-byte and stored-null fixtures; the existing test named matches restoreDraft only calls the decoder. Also assert that a successful recovery clears a previously published storageUnavailable state. Keep these behavioral tests in a follow-up, with no wording/implementation-string assertions.



Additional Low from retained-snapshot round4 independent review at dd60a43: add a provider-level regression carrying loadError through a failed offline probe and subsequent storage recovery. Direct native projection/source-preservation cases already establish the contract; this closes the end-to-end coverage gap without changing behavior.


Measured storage-test-double Low from the #1950 raw review at 3f6ba28ad: the new unique-symbol StoredNullRecord and UnparseableDraftBytes brands are invisible to canonicalJson and structuredClone. The fake draft backends can therefore reduce a stored-null marker to an ordinary empty object or an unparseable marker to its raw-only shape, losing marker identity; no current test routes these sentinels through those doubles, so no immediate product break is observed. Add marker-aware identity/round-trip coverage or route the doubles through the shared marker-aware identity helper. Keep this as test-double conformance work under #1948, separate from #1950's Lows-only scope.
