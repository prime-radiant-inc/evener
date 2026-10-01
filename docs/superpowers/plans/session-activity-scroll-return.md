# Desktop Activity reload anchor return

Preserve the inspected semantic row and its viewport offset across reload using the existing per-session/category view intent and collection page boundary. Do not persist domain rows, add a fetch/retry owner, or require the full list when the requested position is already reachable.

Observed fixture: History sample01 output and completed-history expansion restore, but sidebar scrollTop drops from2656.5 to2178.5; sample01 lies below the viewport after idle. Hypothesis to prove before production edits: the saved first-visible row exists in an incomplete page, but its desired offset needs additional trailing rows. The browser clamps scrollTop to that short page's maximum. ActivityViewport currently clears pending restoration before checking whether the requested position was applied, so later page growth cannot reconcile it.

1. Add a real sidebar/store reload regression with browser-boundary geometry: first cold page contains the saved anchor but cannot supply its requested offset; admit remaining rows through existing page visibility and assert final anchor offset. Observe RED before edits.
2. If proved, retain pending intent until the requested position is applied or the collection is authoritatively complete. Preserve user cancellation, current-view fences and existing page demand; do not edit ActivityPageBoundary owned by the mobile lane.
3. Cover authoritative completion with insufficient trailing extent and avoid additional page demand when restoration is already possible.
4. Update owning session-activity guide. Run focused viewport/retention/activity tests, full web gate and appropriate browser guards; commit with normal hooks and freeze for parent integration/live retest.
