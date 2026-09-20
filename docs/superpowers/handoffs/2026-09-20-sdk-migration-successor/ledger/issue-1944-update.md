The server replacement #1940 tests MarketplaceUnregisteredCloneRemainsData as a Go error value. Add a small follow-up that exercises the actual AppWire JSON-RPC serialization boundary for both outcomes: an available authoritative marketplace array (including an empty array), and AppliedUnavailable with no authoritative list.

Verify the client fixture accepts a real array and never treats unavailable/null data as an authoritative empty list. Preserve the existing discriminator and Go omitempty wire shapes. Tests should inspect decoded behavior, not match a rendered JSON string.

The client publication fence remains active migration work; orphan clone cleanup is #1923 and the server read-side path is #1896. This issue tracks only the accepted wire-level coverage Low.



Additional Low findings from the raw #1940 panel at 16e017439:
- Use effective UID in the two permission-based hub tests so their skip condition matches permission enforcement.
- The CLI applied-with-litter test should assert the clone still exists as a directory, not only the warning/error result.

Measured consumer Lows from the refreshed marketplace stack remain separate from the wire-boundary coverage:
- Native head f50f58e2: when a marketplace refresh returns an error while a retained list is nonempty, the visible path can fall through to ListEmptyComponent text saying no marketplaces. This is a false-empty presentation issue with no data loss and no mutation-guard bypass.
- Web head 07b00b45: a late typed outcome from a replaced client can trigger a redundant list read against the current client. The read remains correctly fenced; no wrong-hub mutation or stale overwrite was observed.

Keep both as follow-ups under #1944's measured consumer coverage; neither is a new server wire-shape defect or a merge-readiness claim.
