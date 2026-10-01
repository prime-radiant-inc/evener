# Nested activity transcript focus

Activity delegate rows must use the existing transcript opener. Its canonicalization preserves one pane per child while retaining the enclosing session and other secondary tabs. A restored child transcript without `parentRef` can otherwise coexist with a second variant, causing the retained parent-context walk to select the incomplete variant and AppShell to refocus the root route when a grandchild opens.

Verify through real AppShell and Dockview with ordinary and retained ref-only child panes, a retained job log, an open inactive disclosure, and root → child → grandchild → root navigation. Pin root transcript preservation with realistic virtual viewport geometry. Keep proven API scope context and existing placement/recovery ownership. The live blank-root observation needs separate browser evidence; passing this test does not establish its cause.
