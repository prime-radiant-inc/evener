The accepted simplify reviews of #1902/#1904 identified duplicated raw-string storage test fixtures, parallel draft classifications/read helpers, repeated reads during discard, and a swallowed rejection from the live-model nudge.

The replacement stack is still evolving. Recheck each item against the landed code before editing: readable-replacement recovery now uses the value-bearing read, so that helper must not be removed as supposedly unused. Preserve all CAS/identity checks and any rereads needed to prove replacement safety.

In a small follow-up, consolidate only remaining duplicate fixtures/state, explain or remove redundant reads, and make nudge rejection observable through the existing provider error path. Preserve offline recovery and readable replacements with meaningful behavior tests. This is separate from #1918's draft-port findings.
