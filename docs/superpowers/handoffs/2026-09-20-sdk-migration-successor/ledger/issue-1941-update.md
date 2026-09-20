The accepted simplify reviews of #1902/#1904 identified duplicated raw-string storage test fixtures, parallel draft classifications/read helpers, repeated reads during discard, and a swallowed rejection from the live-model nudge.

The replacement stack is still evolving. Recheck each item against the landed code before editing: readable-replacement recovery now uses the value-bearing read, so that helper must not be removed as supposedly unused. Preserve all CAS/identity checks and any rereads needed to prove replacement safety.

In a small follow-up, consolidate only remaining duplicate fixtures/state, explain or remove redundant reads, and make nudge rejection observable through the existing provider error path. Preserve offline recovery and readable replacements with meaningful behavior tests. This is separate from #1918's draft-port findings.



Measured Low from local RoboRev2579/2580: an initial genuine storage read failure leaves the live model storageUnavailable=true and draftUnreadable=false. If storage later exposes an unreadable record, successful store-free discard removes it but the model's eligibility check correctly refuses the diagnostic-only nudge. The stale unavailable snapshot survives until the existing Check current shortcuts recovery action. That action is enabled after reconnect by #1904, and model.refresh retries restoreDraft before loading hub settings (existing keybindingRecovery.test.ts:137–153 covers storage failure then refresh recovery). Improve this transition in a separate small follow-up without weakening discard eligibility, deleting readable replacements, or bypassing writeUncertain protection. Pin same-client recovery with a provider-level behavioral test. This is a recoverable diagnostic/editing delay, not data loss or a permanent lockout.


Measured mechanical Low from the #1964 raw review at 4d9a13016: NativePreferencesProvider.tsx re-declares DRAFT_RESTORE_FAILED_MESSAGE while keybindingsStore.ts already owns the same restore-error literal. Share the constant (or helper) and assert the shared value in provider coverage so retained/offline and live restore messages cannot drift. This is cleanup only and does not change recovery behavior.
