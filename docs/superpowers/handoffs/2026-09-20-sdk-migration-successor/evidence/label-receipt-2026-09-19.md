# SDK migration PR label receipt

Recorded 2026-09-19. GitHub API verification after labeling. Existing labels were preserved.

| PR | Title | Labels | State |
|---:|---|---|---|
| 1844 | D6 piece 9: checkpointed draft editor primitives (discard/persist) | sdk-refactor, sdk, native | open |
| 1845 | D6 piece 10: transcriptDisplayStore hub defaults + direct write | sdk-refactor, sdk, native | open |
| 1897 | plugins: close the store-path class (migration recovery, join paths) | sdk-refactor, sdk, server | open |
| 1919 | fix(mobile): the conversation keeps its turns and wire cursor coherent across paging and rehydrate | sdk-refactor, sdk, native | open |
| 1920 | feat(mobile): session usage totals from the package store (D18 B3) | sdk-refactor, sdk, web, native | open |
| 1922 | feat(mobile): retained screens recover on reconnect (D28 D2a) | sdk-refactor, native | open |
| 1934 | docs(handoff): mobile landing + SDK migration queue state, 2026-09-18 | sdk-refactor, docs | open |
| 1952 | mobile: retain loaded screens behind reconnect banners | sdk-refactor, native | open |
| 1954 | sdk: fence reconciliation of applied marketplace removals | sdk-refactor, sdk, server | open |
| 1955 | mobile: recheck live connection ownership before deferred mutations | sdk-refactor, native | open |
| 1960 | Preserve applied marketplace removals across web reconciliation | sdk-refactor, web, sdk, server | open |
| 1966 | Handle applied marketplace removals in the TUI | sdk-refactor, server, tui | open |
| 1972 | Surface applied marketplace removals in native | sdk-refactor, web, native, sdk, server | open |
| 1973 | Expose accepted marketplace snapshot publication | sdk-refactor, sdk, server | open |
| 1976 | Preserve marketplace outcomes while ordering TUI reconciliation | sdk-refactor, server, tui | open |
| 1978 | Retain marketplace outcomes across native browser remounts | sdk-refactor, web, native, sdk, server | open |
| 1981 | Add durable native mutation runtime with hub-scoped dispatch | sdk-refactor, native | open |
| 1983 | Recover native marketplace removal guards after accepted updates | sdk-refactor, web, native, sdk, server | open |
| 2005 | agent: persist interrupt boundaries before resolving pending asks | sdk-refactor, server | open |
| 2035 | appwire: expose history merge coverage and overlap | sdk-refactor, sdk | open |
| 2041 | refactor(web): extract transcript display local persistence | sdk-refactor, web | open |
| 1841 | refactor(sdk): share settings generation lifecycle | sdk-refactor, sdk | closed/merged |
| 1904 | Recover unreadable keybinding drafts while offline | sdk-refactor, native | closed/merged |
| 1907 | agent: steering-carrier claim id is released on every exit | sdk-refactor, server | closed/merged |
| 1916 | feat(native): MutationOutboxStorage write path over expo-sqlite (D25d-1a) | sdk-refactor, native | closed/merged |
| 1917 | feat(native): MutationOutboxStorage read path over expo-sqlite (D25d-1b) | sdk-refactor, native | closed/merged |
| 1940 | fix(plugins): distinguish applied marketplace removal from cleanup failure | sdk-refactor, server | closed/merged |
| 1949 | test: loosen warning scan ordering and clarify payload fields | sdk-refactor, sdk, native | closed/merged |
| 1950 | native: classify persisted preference drafts through a shared storage backend | sdk-refactor, sdk, native | closed/merged |
| 1956 | native: recover preference drafts without a connected hub | sdk-refactor, native | closed/merged |
| 1958 | fix(agent): restore pending asks using durable turn boundaries | sdk-refactor, server | closed/merged |
| 1961 | Coalesce transitively overlapping history fragments | sdk-refactor, sdk | closed/merged |
| 1962 | Keep live ask state aligned through failure and yield boundaries | sdk-refactor, server | closed/merged |
| 1964 | Preserve diagnostic sources during offline draft recovery | sdk-refactor, native | closed/merged |
| 1965 | Normalize native outbox records and strengthen conformance coverage | sdk-refactor, native | closed/merged |
| 1968 | Preserve retained turn order in paginated history | sdk-refactor, sdk | closed/merged |
| 1975 | Wait for session namer before replay retirement assertion | sdk-refactor, server | closed/merged |
| 1977 | Preserve pending questions until a reply is durably admitted | sdk-refactor, server | closed/merged |
| 1982 | appwire: preserve tool-result source precedence across history fragments | sdk-refactor, sdk | closed/merged |
| 1988 | native: reconcile draft snapshots when the same client disconnects | sdk-refactor, native | closed/merged |
| 2009 | fix(appwire): retain ready-generation stamps on shortcut drafts | sdk-refactor, sdk | closed/merged |
| 2014 | fix(appwire): require rebase for drafts from an earlier ready generation | sdk-refactor, sdk | closed/merged |
| 2024 | fix(sdk): retain draft generation through storage recovery | sdk-refactor, sdk | closed/merged |
| 2036 | test(sdk): verify settings generation teardown order | sdk-refactor, sdk | closed/merged |

## Excluded

- #1480 parked transcript fold: explicitly out of scope.
- #1580 native projection oracle/reference: reference only.
- #1947 retirement timer cleanup: explicitly unrelated/out of scope.
- #2037 mobile warning projection: author-owned but absent from the coordinator's managed SDK queue; left untouched rather than guessing.
- Other `obra` PRs and research/cleanup PRs: no coordinator ownership evidence.
