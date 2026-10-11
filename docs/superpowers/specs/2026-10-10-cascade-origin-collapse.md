# Agent cascade does not repeat its origin conversation

Status: approved by Jesse on 2026-10-10 ("Don't repeat the origin").

## Authority and scope

Jesse reported that opening a subagent from the Overview sidebar showed "a
panel that had the top level repeated and the second level", and ruled the
duplication broken. The secondary-pane amendment (2026-10-04) kept both the
mounted center conversation and the nested two-column cascade, so opening a
direct subagent of the session being viewed rendered that session twice: once
as the origin pane and once as the cascade's immediate-parent column. This
amendment removes the duplication.

It changes only which scopes the cascade renders readable. Placement,
ancestry, pop, peek, history, activity, input-delivery, Return and mobile
contracts otherwise remain in force, including the previous-layout policy for
saved cascades without inspection intent. The evergreen authority after
implementation is
[Session activity](../../product/session-activity.md#automatic-agent-cascade).

## Rule

When a separated cascade has a live origin pane association and the immediate
parent scope is that origin's conversation, the immediate parent renders as a
52px compact spine instead of a readable column; only the selected leaf stays
readable. Deeper drills keep two readable columns (the origin is then an
ancestor above the immediate parent). A cascade with no live origin keeps the
readable parent column, and closing the origin pane retires the association
and restores it. Popping to the origin ref keeps its existing behavior: the
popped scope is the leaf and renders readable.

## Proof

- jsdom: the origin-visible collapse, the deeper-drill two-column return, the
  origin-absent column pair, and the origin-closed recovery, in
  `Zoom.test.tsx`; the DockHost restore journeys and the AppShell nested
  drill pin the restored and live-origin shapes.
- The real cascadeguard's first drill asserts the spine-and-leaf shape beside
  the mounted center; depth six keeps two readable columns and five spines.

Verification commands from the repository root:

```sh
make test-web
make build-web
go test -tags browserguard ./cmd/evener-hub -run '^TestAgentCascadeBrowser$' -count=1 -v
```
