# Native project browsing

Use existing revisioned evener/navigation/read resources rather than invent aggregate thread/list pagination. Jesse has delegated routine product choices and authorized full native workflow coverage.

Add Projects from Sessions. A project catalog includes active and archived project modes. A selected project shows current, recent and archived session tiers, with explicit Load more and pull-to-refresh. Navigation uses server project keys and session refs scoped to the selected hub. Retain the parent screen state when opening a session.

A page owner keeps raw offsets separate from deduplicated visible rows, serializes continuation, cancels stale completions on leaving a screen, and rejects generation/revision changes between pages with an explicit refresh action. Read failures retain already loaded rows. Unknown/malformed responses become actionable errors. Use the server remaining count; never infer completion from page length alone.

Verify with network-boundary tests for offsets, duplicates, stale bindings, changed revisions and errors. Build/install both native releases and manually exercise real isolated-hub project/tier navigation; use a scripted WebSocket fixture for multi-page UI if the real dataset is too small. Record boundaries and remaining work honestly. Persisting project routes after process death and navigation invalidation notifications remain separate acceptance items.
