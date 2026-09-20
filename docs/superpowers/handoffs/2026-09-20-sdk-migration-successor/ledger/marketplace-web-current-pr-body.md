The web marketplace UI preserves cleanup warnings when removal applied but clone deletion failed. A client-scoped guard prevents repeating that removal until the SDK accepts a newer authoritative list, including when another client re-adds the same name. Failed or held snapshots cannot release the guard; late outcomes from a replaced client cannot affect its replacement.

This adopts the merged SDK cache and publication contracts (#1954, #1973). The change adds the shared removal-outcome classifier, uses it in the web marketplace page and sheet, preserves guards across sheet remounts, and keeps accepted-list cache retirement intact. The separate no-litter outcome introduced by #2050 remains a focused successor.

Validation at `a6e134f2125166b654fbabe669c09dc00bd8cc2f`, based on `5dfd06d299a60f3919e17b4d546589c5b2c92860`:
- 94 focused marketplace and SDK tests passed.
- Full `make test-web` passed (typecheck, tests, Biome).
- RoboRev exact-base branch review 2706: no issues found.
- Root review confirmed accepted snapshot publication, cache retirement, and loading-state semantics are preserved.

Retained #1959 includes additional lifecycle coverage and native obligations; this PR does not claim that whole issue is resolved.
