# Activity sidebar automatic paging and consistent lists

## Intent

The Overview sidebar's Agents, Jobs, Watches, and Tasks tabs should feel like the same kind of list. Collections load in bounded pages without requiring a Load more click, pending content uses quiet placeholders, and totals come from authoritative summaries rather than the rows downloaded so far.

## Goals

- Agents, Jobs, and Watches automatically request continuation pages at the visible page boundary.
- Pending pages show static Skeleton placeholders, not a Load more button or a simulated busy animation.
- Inactive-agent and completed-job folds show direct-session summary totals as soon as those totals are known.
- The four list tabs share the same outer padding, row gap, and history-disclosure treatment.
- Tasks continues to load its complete list and uses its authoritative task total for its initial placeholders.

## Non-goals

- No change to cursor-based APIs, page sizes, collection ordering, scroll restoration, reconnect behavior, or error retry ownership.
- No automatic expansion of inactive/completed history.
- No new widget or widget prop.
- No redesign of the sidebar's typography, palette, tabs, or row actions.

## Constraints

Use the existing `Disclosure`, `ListRow`, and `Skeleton` widgets and spacing tokens. Summary counts remain independent of loaded rows; unknown counts must not be presented as zero. The Agents tab's all-descendants badge count is not a direct-session history total. Preserve visible-demand paging and existing refresh/reconnect behavior.

## Approaches

1. **One shared automatic page boundary plus existing row/history components (chosen).** Remove the boundary's button, retain its intersection-driven demand, and render a Skeleton while more rows exist. Remove Agents' closed-fold gate. Use each tab's direct-session summary for inactive/completed totals. This preserves the current store and viewport ownership with the smallest change.
2. **Eagerly drain every collection in the shared store.** This would simplify views but load invisible work, bypass visible demand, and mix network policy into the store. Rejected.
3. **Add a virtualized, uniformly paginated list abstraction for all four tabs.** Tasks has no paged collection API and the other lists have different history grouping, so this would be a disproportionate rewrite. Rejected.

## Design

`ActivityPageBoundary` remains the sole visibility-to-page-demand adapter. A visible boundary requests one page through the existing store; loading, errors, permanent failures, and re-observed row identities retain their current guards. Its visual output is a static Skeleton. Restoration-only demand keeps its existing paced path.

`AgentsTab` no longer disables page demand when its inactive fold is closed. Agents and Jobs use the same `Disclosure` treatment for history. Their history totals are `total - active` from the direct-session summary when known; Tasks uses `scope.counts.tasksTotal` for its initial Skeleton. Loaded-row counts never substitute for an unknown summary total.

The sidebar body retains one shared padding rule. Activity stacks, task lists, and history folds use the same `--space-1` row rhythm, with no Agents-only top inset.

## Verification

Focused Vitest coverage proves automatic continuation with closed history, no Load more control, summary-backed initial/history totals, and unchanged paging/refresh/reconnect behavior after multiple pages. Run the frontend typecheck, Biome on touched files, `make test-web`, and real-browser Overview layout checks where supported. Review both themes and constrained widths.
