# A settled needs-you page owns completion

Issue: [#4912](https://github.com/prime-radiant-inc/evener/issues/4912). Fast
follow from PR #4854, where this surfaced as a low-severity review finding that
could not be driven to a failing test in that branch's window.

## Problem

The shell's one needs-you controller decides whether it may request another
page from four signals (`cmd/evener-hub/frontend/src/shell/needsYouActions.ts`,
`read()`):

```ts
const canLoadMore = retryingPopulatedPage || !hasManifest || failed || remaining > 0 || count > uniqueRows.length;
```

`count` is the navigation manifest's own total, and the manifest refreshes on
its own schedule. A section page that has settled at `remaining: 0` is
therefore overridden whenever the manifest still counts more sessions than the
rows on hand. After queue shrinkage, or after overlapping pages were
deduplicated, Next and Load more stay available over a finished queue; pressing
them requests a page that has nothing in it.

## Scope

One change in `needsYouActions.ts`: a settled `needs_you` page owns completion,
and the manifest count decides only before any page has settled.

```ts
const settledPage = [...state.resources.values()].some(
  (resource) =>
    resource.key.kind === "section" &&
    resource.key.section === "needs_you" &&
    !resource.loading &&
    !resource.error &&
    !resource.stale,
);
const canLoadMore =
  retryingPopulatedPage || failed || remaining > 0 || (!settledPage && (!hasManifest || count > uniqueRows.length));
```

The issue's proposed snippet leaves out `retryingPopulatedPage`, the condition a
populated page's in-flight retry added to keep the control mounted (and its
visibility effect from cancelling the retry). This spec keeps it: a populated
page that is retrying is not settled, and it is the only case where a loading
page still needs demand.

`failed` also stays outside the settled gate. A settled page elsewhere must not
hide a failed boundary the reader can still retry.

## Non-goals

- No change to how many pages a press requests, to retry offsets, or to the
  Next/select ordering.
- No native-client change: the native app reads the same navigation sections
  but does not share this controller.
- No manifest-refresh change or new completion signal from the hub.
- No change to the control's visibility rules for IME, pending questions, the
  palette or modals.

## Behavior

1. Cold shell, no page settled yet: the manifest count still justifies initial
   demand, as today.
2. Any settled `needs_you` page with `remaining: 0` and no failure: the section
   is complete. `canLoadMore` is false, so Next has no target beyond the loaded
   rows and Load more is not offered.
3. A populated page retrying: `canLoadMore` stays true while the retry is in
   flight, exactly as the existing test requires.
4. A failed page: `canLoadMore` stays true so the failure can be retried.
5. Two settled pages that overlap (`remaining: 0`, fewer unique rows than the
   manifest count): completed. This is the shrinkage/dedup case the issue
   names.

## Preservation and recovery

Nothing here removes a reader, a page or a retry. The change narrows one
predicate; every existing recovery path (failed-boundary retry, populated-page
retry, reconnect refresh) keeps its own conditions and tests. Existing pages
stay loaded and selectable.

## Test plan (whole journey, not just the predicate)

Controller tests in `src/shell/needsYouActions.test.ts` against the real
navigation store and a scripted client:

1. **Settled page, larger manifest count** — manifest count 3, one settled page
   with two rows and `remaining: 0`: `canLoadMore` is false, `nextRef` still
   names the first row when focus is elsewhere, and pressing Next navigates to
   it without requesting another page (assert the read count).
2. **Cold shell before any page settles** — no settled page, manifest count 3:
   `canLoadMore` is true, preserving today's initial demand.
3. **Populated retry in flight** — the existing "a populated page's retry keeps
   demand available while it is in flight" must keep passing unchanged.
4. **Failed boundary** — the existing failed-page tests must keep passing;
   `canLoadMore` stays true while the failure is retryable.
5. **Load more after completion** — with the settled page, `loadMore()` does
   not request a page and the snapshot stays stable.

Browser guard: not needed. The control's geometry and pointer behavior are
untouched, and the cascade guard already covers the complete-queue journey it
shares with Next.

## Documentation

Update the owning guide, `docs/web-ui/README.md` § "Floating Next-needs-you
control", with the completion rule: a settled page's `remaining` decides that
the queue is finished, and the manifest total cannot keep demand alive after
that. Update the S02 row in `docs/product/subsystems.md` beside the existing
needs-you owner sentence. No friction case is open for this.
