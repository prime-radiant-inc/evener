Loaded native Providers, Plugins, and Hub Settings screens retain their content through recoverable connection interruptions and show a reconnect banner. Initial and fatal connection failures retain the blocking connection view; changing hubs resets retained client ownership.

This replaces the screen/banner portion of #1915. The required successors are #1955 for invocation-time mutation readiness and #1922 for automatic read recovery and retained form retries. Merge the qualified stack together.

Refreshed to main `d25d5afaa7fee061801258c6b64fee558d958bb3` at head `77a2e2f748b8c22a54ee7cfbcf0ad6dfbd8b7d00`. The complete own patch is byte-identical to its reviewed predecessor, SHA-256 `70aae595fe38c09c68183305c2387fe33bad15a4667ca1f02bb5ae281adeb3b3`. Independent review/simplify and local branch review are qualified with the documented successor dependencies. Post-refresh validation: 64 focused native tests, native typecheck, package import lint, and diff checks passed. New exact-head CI is required. Separate Lows remain in #1942.

The current base includes merged #1975, which fixes the independently reproduced retirement replay-test race. This refresh changes no owned production or test behavior. Complete raw reviewer-member qualification remains required; a queued synthesis alone is not a gate.
