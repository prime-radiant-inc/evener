The server replacement #1940 tests MarketplaceUnregisteredCloneRemainsData as a Go error value. Add a small follow-up that exercises the actual AppWire JSON-RPC serialization boundary for both outcomes: an available authoritative marketplace array (including an empty array), and AppliedUnavailable with no authoritative list.

Verify the client fixture accepts a real array and never treats unavailable/null data as an authoritative empty list. Preserve the existing discriminator and Go omitempty wire shapes. Tests should inspect decoded behavior, not match a rendered JSON string.

The client publication fence remains active migration work; orphan clone cleanup is #1923 and the server read-side path is #1896. This issue tracks only the accepted wire-level coverage Low.
