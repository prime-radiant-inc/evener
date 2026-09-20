The independent review of the native offline-draft storage replacement at 700b111a861b006b3d59c7535fc8a7f696b8384f found one Low test-coverage gap. Valid replaceIf and insertIfAbsent cases use the in-memory fake; directly exercise these contracts through rawStringDraftBackend with a fake string-storage port.

Cover matching replacement, stale-marker rejection, absent insert, existing-value refusal, malformed/null storage, and canonical object identity without relying on property order. Assert persisted values and return behavior. Keep the existing provider recovery coverage in its own successor. This is a test-only follow-up after the storage replacement, not a reason to expand the storage PR.


Additional Low coverage from the storage/provider reviews: compare decodeKeybindingDraftFields with the live keybindings-store restore projection using shared valid, absent/null, invalid-object, malformed-byte and stored-null fixtures; the existing test named matches restoreDraft only calls the decoder. Also assert that a successful recovery clears a previously published storageUnavailable state. Keep these behavioral tests in a follow-up, with no wording/implementation-string assertions.
