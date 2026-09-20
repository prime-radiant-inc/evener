## Result

Marketplace removal now saves unregistration before deleting clone files. A failed save leaves registration and clone intact. If cleanup fails after the save, the server returns a typed applied-with-litter outcome with the updated marketplace list, or an explicit unavailable-list marker when reconciliation cannot complete. The CLI reports the applied removal and remaining cleanup accurately. Lookup misses return before deriving or touching a caller-controlled filesystem path.

The change preserves the #1796 source-protection integration at `c74f82c77a06e555df631218f37a3e242c3c2689`. Sweep protection is computed from the complete pre-removal registry, including the removed record, so protected and absent paths are skipped.

## Scope and stack

Own non-test scope is 130 touched lines at current head `2989ba6916e04b1fa3eeebe2714b7dc4a33522c5`, based on main `3ca68fad7ab4dec468ab2d51d1f33bcca2009b3e`. This is the server successor for superseded #1890. SDK reconciliation is #1954; web, native, and TUI consumers must surface the cleanup warning and prevent retrying an already-applied removal before this stack can merge.

## Validation and status

Focused plain/tagged plugin, CLI, and hub tests; normal/tagged/Windows vet; touched-module lint; pinned gofmt; and diff checks pass. Independent specification, quality, and simplification integration review passed. Local RoboRev2567 found only the separately tracked SDK/UI consumer dependency.

The coordinator refresh verified the own patch remains byte-identical at current head (SHA-256 `182dee360092e773c3f16c44e9161380c8c57cdff095a28b5cd44d18c84bd765`). The earlier CLI-isolation Medium is refuted by the existing `TestMain` and sentinel-environment probe; the exact server head remains unchanged. Fresh exact-head CI and raw panel qualification are required before merge. Consumer qualification remains held on #1954, web #1960, TUI-A #1966 with its ordering-B work in progress, and the native consumer review. Deferred coverage and diagnostics are tracked in #1944 and #1951; typed-row validation is #1953 and lifecycle coverage is #1959.
