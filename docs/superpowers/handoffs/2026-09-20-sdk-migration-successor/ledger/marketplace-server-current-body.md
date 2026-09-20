Marketplace removal now saves unregistration before deleting the clone. A failed save leaves registration and clone intact; failed cleanup returns a typed applied-with-litter outcome with the updated marketplace list, or an explicit unavailable-list marker. The CLI reports the completed removal and remaining cleanup accurately. Lookup misses never derive or touch a caller-controlled filesystem path.

Integrated main #1796 source protections at c74f82c77a06e555df631218f37a3e242c3c2689. Sweep protection is computed from the complete pre-removal registry, including the removed record; protected or absent paths are skipped. Current head:16e0174391e1207f88f7eefe3a3e9d63ebb59317. Own non-test delta:130 touched lines.

Validation: focused plain/tagged plugin, CLI and hub tests; normal/tagged/Windows vet; touched-module lint; pinned gofmt and diff checks. Independent spec/quality/simplify integration review passed. Local RoboRev2567 reports only the separately tracked SDK/UI consumer dependency.

Held for the consumer stack: #1954 reconciles the SDK from the typed outcome; web, native and TUI consumers must show the cleanup warning and prevent retrying an already-applied remove. #1890 is superseded by this decomposition. Current-head CI and raw panel qualification remain required before merge. Deferred coverage and cleanup are tracked in #1944 and #1951.
