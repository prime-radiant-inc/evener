# Preserve transcript position when focusing a Dock group

The workspace keeps a selected pane in each Dock group. Bringing an already selected pane into global focus must preserve its mounted transcript and reading position; choosing a different tab must still select that panel.

The actual Dock/Session browser journey reproduces a root viewport changing from 16,277px to zero with identical viewport height and scroll extent. Dockview panel activation opens the already selected panel, removes and reattaches its content, and resets native nested scroll positions without reconciling the retained virtual range. The existing public group activation path changes global focus without reopening content.

Use group activation only when the target is already selected in its group. Keep panel activation for a different selected tab. Do not change transcript, virtualizer, API, or recovery ownership.

Verify actual browser root-away content and both scroll positions through root/observer/grandchild navigation and root focus, then select the retained observer tab and confirm the root stays visible at the same position. Verify real Dock workspace focus identities and selected tabs in component tests. Run affected unit tests, typecheck, Biome, and the existing transcript browser guard, with normal commit hooks. Live runtime qualification remains separate.
