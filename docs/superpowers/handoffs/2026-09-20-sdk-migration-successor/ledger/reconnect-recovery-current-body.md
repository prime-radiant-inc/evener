Retained native settings screens now refresh after reconnecting, adopt a replacement client only when ready, and distinguish an offline refusal from an already-running mutation. Fatal connection failures keep the wall in place until the replacement is ready. Provider and marketplace form modals expose the reconnect action without discarding unsaved input.

Stacked on #1955, which follows #1952. All PRs target main; the aggregate diff includes those parents until they land. This PR preserves the original #1922 recovery work and its tests. The separate WIP test-deletion branch remains untouched. Current head:57bfcbf156fa958725974caf148ebfa540da2e9d, based on mainc74f82c77a06e555df631218f37a3e242c3c2689.

Validation:64 focused tests across eight files, native typecheck, package-import lint and diff checks pass. Tests cover passive flaps, fatal manual retry, connecting replacement clients, deferred confirmations, and modal draft retention. Independent spec/quality/simplify review is accepted; local RoboRev2569 reports only a Low plugin-details modal follow-up tracked in #1942. This is recovery review round4/5; current-head CI and raw panel qualification remain required.

Low cleanup and presentation follow-ups are tracked in #1942. Resolves the plugin reconnect staleness described in #1914 once the stack lands.
