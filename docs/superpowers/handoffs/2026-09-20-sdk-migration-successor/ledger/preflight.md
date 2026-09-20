# Queue preflight, 2026-09-18 pickup
Current handoff rulings override earlier undecided/older plan entries. Spec: docs/design/2026-09-12-sdk-migration-inventory.md; active authorization and required behavior: HANDOFF.md plus linked lane briefs/state notes.

| Task | Internal consistency | Shared interface/dependency handling |
| --- | --- | --- |
| #1931 fixture support boundary | Source follow-up list named .testFixture.ts but the gate only permits *TestUtils.*; use accepted public boundary convention and retain checker. | Existing test imports; no dependency on other lanes. Reducer Low overlaps later C/E/D only if fix touches warnings: report exact changed area. |
| #1921 deterministic retirement claim | Claimed determinism contradicts scheduler yielding; diagnose before patch, preserve real helper. | #1906 also in agent module but distinct production vs test files; integrate current main before gates. |
| #1890/#1897 marketplace | Memo clean label contradicts raw Medium. Five-round cap requires decomposition before more implementation. | Both share store and hub wire outcome; one owner, serial decomposition/restacks. |
| #1916/#1917 outbox | WIP regression preserved separately; port and oracle define contract, not review guess. | Same storage file: implement predecessor only, successor held until reviewed. |
| #1919/#1920 history/usage | Rehydrate and loadOlder must share fresh-wins fallback contract. | #1920 consumes the repaired turns; merge-order dependency. No literal stack. |
| #1902/#1904/#1792/#1841/#1844/#1845 offline settings | Recovery seam belongs to successor; own provider discard bug stays in predecessor. | Same keybindings/native draft files: one owner, serial. D2 reconnect files are separate for current p6b-only scope. |
| #1915/#1922 reconnect | A render-time readiness boolean cannot guard a deferred confirmation. Validate current hook/caller contract. | Same connectionDisplay/screens: one owner, serial. No current p6b provider overlap. |
| #1737/#1738/#1740 projections | Canonical answer values must survive bounded display; no signature migration per ruling. | Shares conversation.ts/reducer.ts with #1919/#1931: hold implementation until those changes are available. |
| #1936 timer diagnosis | Actual CI failure differs from memo speculation; no root cause yet. | Separate hub timer domain; diagnose read-only, a separate lane if code needed. |

All other pairs are file/domain-disjoint at the currently scoped fixes. No new product decisions; TestFlight and A2 remain parked. Each implementation receives a fresh narrow brief, independent review, then CI/raw-panel merge gates.
