Loaded native Providers, Plugins, and Hub Settings screens retain their content through recoverable connection interruptions and show a reconnect banner. Initial and fatal connection failures retain the blocking connection view; changing hubs resets retained client ownership.

This replaces the screen/banner portion of #1915. Required successors are #1955 for invocation-time mutation readiness and #1922 for automatic read recovery and retained form retries. Those corrections are locally implemented and independently reviewed; merge order remains #1952 -> #1955 -> #1922.

Refreshed onto main `323f28c30536c3a07f21538128cdad1e3afbb607` at head `6dacb1999d8d658803fb96e8b1bd4d4f5d310114`. The complete owned binary patch is identical to reviewed local `51bb119c`, SHA-256 `3266cc0e29cf50fcf0dcef63bcdf38c87c27b452bec34ca3eba005676ee3d76a`. Main's intervening mobile changes are file-disjoint. Declaration/conflict-marker and diff checks passed.

The integrated reconnect stack passed 104 focused tests, native typecheck, import lint, independent correctness/simplify review, and local RoboRev2621. Provider stale-save and AddMarketplace not-ready tests were falsified against their production guards. This refresh changes no owned behavior. Fresh current-head CI and raw reviewer qualification are pending. Separate Lows remain in #1942.
