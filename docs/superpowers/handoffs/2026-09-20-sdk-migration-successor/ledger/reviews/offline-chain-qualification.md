# Offline recovery chain remote qualification

Read-only qualification against current `main` `6cf3f0263887914e87dbc70d558979ecea143abb`. REST PR metadata, exact-head check-runs/statuses, local `git merge-base`, and `rawreviews.sh` were used. Completed raw panel bodies are mirrored beside this report in `reviews/raw/*-remote.md`. No refresh, code/ref, PR, or external comment action was performed.

## Qualification status

**NOT QUALIFIED.** Every exact-head RoboRev panel still has at least one running member, and every PR has a pending `roborev` status. PRs 1956 and 1904 also still have an in-progress GitHub `Build snapshot artifacts` check.

All four REST PR records are open, `mergeable: true`, and `mergeable_state: behind`.

| PR | exact head | GitHub base | computed merge-base with current main | GitHub checks | raw panel |
|---|---|---|---|---|---|
| 1950 | `c9173db55aa64dedbfa5420ffca0fd233fe58d73` | `3ca68fad7ab4dec468ab2d51d1f33bcca2009b3e` | `3ca68fad7ab4dec468ab2d51d1f33bcca2009b3e` | 15/15 success; `roborev` pending | 3 done, job 20747 running |
| 1956 | `a69867528466fb0db7d9967d92a38b5ca3af424d` | `3ca68fad7ab4dec468ab2d51d1f33bcca2009b3e` | `3ca68fad7ab4dec468ab2d51d1f33bcca2009b3e` | 14 success, Build snapshot in progress; `roborev` pending | 3 done, job 20732 running |
| 1964 | `a13be3c214ab9cb0da38fd187f2c4281f3a3e05f` | `6cf3f0263887914e87dbc70d558979ecea143abb` | `3ca68fad7ab4dec468ab2d51d1f33bcca2009b3e` | 15/15 success; `roborev` pending | 3 done, job 20727 running |
| 1904 | `8181740971c1f4f214cad6a4de0f62dd3ce27e78` | `6cf3f0263887914e87dbc70d558979ecea143abb` | `3ca68fad7ab4dec468ab2d51d1f33bcca2009b3e` | 14 success, Build snapshot in progress; `roborev` pending | jobs 20794 and 20797 running; 2 done |

The GitHub `base.sha` for 1964 and 1904 is the target tip `6cf`, not the branch ancestor; the computed merge-base is the older `3ca` commit.

## Completed raw-body disposition

- **1950:** members 0 and 2 independently report Medium for the production provider not using the new raw backend. The PR body explicitly makes provider wiring #1956 and screen behavior downstream scope, so this is a stacked seam finding, not an own-scope defect. Member 1 reports no issue.
- **1956:** member 0 reports the screen consumer and retained-snapshot behavior; member 2 reports the decoder caller, documentation field, and screen consumer. Those are the declared #1964/#1904 downstream seams, not provider-only own scope. Member 1 reports no issue.
- **1964:** member 0 reports the screen consumer as the declared #1904 seam and separately flags retained-projection unreadable-state freshness. Members 1 and 2 report no issue. The retained-projection concern is a provider seam inherited by the final screen integration; it is not a screen-owned finding.
- **1904:** member 1 repeats the retained-projection concern: `reconcileRetainedDraftProjection` does not update `draftUnreadable` and leaves `draftError` unchanged on an unreadable outcome. Member 2 reports no issue. This is in inherited provider code from #1964, not in the byte-identical screen/recovery files; it should be tracked as the parent-chain seam. The known Low #1941 recovery delay is distinct and is recoverable through “Check current shortcuts,” as recorded in the screen review.

Exact seam measurement: parent a13be3c's nativePreferences.ts has draftError, hubError, and loadError in KeybindingsPreferenceState, but no draftUnreadable field in PreferenceState or keybindingsDomain. Final 818 adds draftUnreadable during the merge, while NativePreferencesProvider.tsx remains byte-identical to a13 and its retained projection still does not update that newly present field. This makes the raw finding a real inherited provider-projection seam in the final tree, but it is not owned by the screen PR's changed files and is not evidence of a new screen interaction failure.

No completed raw member reported an `error` state with an output that could be treated as pass. Running jobs remain non-pass and block qualification.
