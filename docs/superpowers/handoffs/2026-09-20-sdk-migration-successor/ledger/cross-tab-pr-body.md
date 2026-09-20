Move transcript display BroadcastChannel and storage-event handling into a browser synchronization module. The existing store keeps state and effective-setting transition ownership through callbacks. This prepares the web adapter for shared SDK adoption while preserving cross-tab updates, source filtering, storage fallback, and listener cleanup.

Validation: all 30 existing transcript display tests and the full make test-web gate passed. Independent source review and exact-base RoboRev2678 found no issues. Two files changed; the extracted module is 149 lines.
