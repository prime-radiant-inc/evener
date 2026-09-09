# Task 3 — Workspace editorial system

Status: verified; ready for parent review. Base: a1adbcbf3. Isolated npm ci completed (147 packages, zero vulnerabilities); baseline `make test-web` exit 0: PASS web-typecheck, web-test, web-lint.

## Surface coverage matrix

| Surface | Treatment | Preserved contracts / checks |
|---|---|---|
| [x] AppShell, DockHost, StackHost, single-pane | Inherited flat PaneScaffold and warm page; inspect existing viewport ownership | fixed phone shell, keyboard inset, safe area, 899px switch; shellguard |
| [x] Dockview tabs | Direct: page-ground focused tab; fine separators | selected/focused group distinction, close/overflow and drag; shellguard |
| [x] Rail, rail rows, mobile drawer | Direct quiet index labels; inherited palette and shared controls | tree roving focus, selection, recency, resize, drawer focus; shellguard |
| [x] Palette, cheatsheet, session menu, hold hints, rail dialogs, MobilePanel | Inherited overlay boundary/radius/shadow and sans controls | Escape, focus trapping/restoration, protected force stop; unit suite and shellguard |
| [x] Welcome | Direct serif orientation; inherited serif EmptyState heading | resume/new-session controls, long titles and phone floors |
| [x] Spawn + advanced options | Direct serif page heading; inherited fields and notice ground | model/effort, directory confirmation, attachments, validation, container queries; spawnguard |
| [x] Settings navigation + field rows | Direct flat selection geometry and field rhythm; inherited section Cards | every section, filter, mobile drill-in, label/control association |
| [x] Provider credentials + sheets/dialogs | Direct group hierarchy and diagnostics; inherited real overlay/fields | editor, secrets, validation, connect/delete confirmation unchanged |
| [x] Remaining settings sections (general, display, theme, transcript, launch, agents, MCP, plugins, paths, notifications, storage, about, keybindings) | Inherited flat Card/InspectorCard and aligned SettingsField, shared fields | preferences/persistence unchanged; unit suite, settings overflow harness |
| [x] Documents | Inherited PaneScaffold/Markdown/CodeBlock; raw file text remains mono | wrapping, copyable code, images/lightbox, loading/error states |
| [x] Read-only transcript | Inherited PaneScaffold/shared transcript and Markdown; no live-transcript lane edits | retained raw log mono and scroll ownership; overflowguard |
| [x] Session panel pane wrappers | Inherited PaneScaffold and directly revised bodies below | no changed routing/props |
| [x] Session chrome / model, goal, status | Inherited warm controls and tabular sans readouts | every menu and selection, responsive containment; overflowguard |
| [x] Details | Direct sentence-case grouping, tabular figures | exact identifiers remain mono, unknown/accounting preferences unchanged |
| [x] Activity | Direct fine-rule detail evidence and tabular metadata | truthful states, independent open/disclosure targets, no invented data |
| [x] Tasks | Direct sentence-case groups and tabular figures | disclosure grouping and unknown/loading/error states retained |
| [x] Composer + attachments + CurrentWork | Inherited PromptCard/fields, direct quiet attachment ground | production inline-size containment untouched, focus/draft/send/steer and attachment controls retained |
| [x] Queue | Direct ruled rows instead of nested cards | reorder/cancel/reason/pending behavior retained |
| [x] AskDock | Direct single attention boundary, unboxed inner question, serif question prose | native options, labels, keyboard, send and editable font floors retained |
| [x] Composer surface gallery | Direct dedicated definite-width wrapper and focused regression | resting/drafted/pending in both themes, real Session comparison |

## Verification log

- `npm ci`: exit 0, zero vulnerabilities.
- Baseline root `make test-web`: exit 0, three PASS lines.
- Physical queue/AskDock ownership is under `src/panes/session/composer/{queue,askDock}/`; no separate session-level directories exist.
- Initial broad file-inspection commands had unmatched glob/missing-path errors; corrected inventory above. No production change was made from those errors.

## Implementation and preserved behavior

19 owned frontend files changed/added. No protocol, store, routing, persistence, global style, shared widget, dependency, or live-transcript-lane edits. Production Composer keeps its existing inline-size query container. AppShell's fixed phone viewport, 899px breakpoint, keyboard/safe-area ownership, model/goal/status menus, editable font thresholds and existing touch floors are unchanged.

The dedicated `composer-surface.module.css` gives each gallery fixture a definite 100% width before mount. The new focused regression was added first and failed on the old generic `row` wrapper. Its draft assertion then exposed a separate real fixture defect: the parent effect wrote the draft after the child Composer's `useState(readDraft)` had already run. Gallery-only seeded readiness now mounts the children after fixtures are populated. Both themed drafts, focus while typing, Send label and native question selection pass.

Details accounting is now tabular sans; paths, branch and InspectorCard identifiers remain mono. The only production JSX addition is the existing path class around branch text; no controls, handlers, labels or public props changed.

## Red → green evidence

| Command / check | Outcome |
|---|---|
| `npm ci` in this worktree | exit 0, 147 packages, zero vulnerabilities |
| Baseline root `make test-web` | exit 0: web-typecheck / web-test / web-lint PASS |
| New `npx vitest run src/dev/surface-sections/composer.test.tsx` before fixture edit | exit 1, old `_row_…` failed required paneFixture wrapper |
| Same regression after wrapper only | exit 1, actual draft empty; seed-before-mount fixture repair resolves it |
| Final gallery regression | exit 0, 1 test passed (both themes, editing/focus/native selection) |
| `DetailsPanel.test.tsx` + gallery focused run | exit 0, 34 tests passed |
| First full frontend rerun | failed historical AskQuestionCard bare-accent text assertion; updated to exact `--accent-ink` per canonical guide §2 (semantic text AA companion); suffix/label pairing unchanged |
| AskQuestionCard + unchanged token-contract focused run | exit 0, 1607 tests passed |
| Independent real-browser recommendation contrast | RED 3.752:1 with transparent question ground on amber; neutral page ground restored, GREEN dark 6.357:1 and light 5.538:1; floor stays 4.5 |
| Initial touched-src Biome | wrong root-prefixed paths; tool printed IO diagnostics despite exit 0; not counted as success |
| Corrected final `npx biome check --write` over named touched frontend-relative files | exit 0, checked 19 files, no fixes |
| Typecheck/build attempt | TS2769: unsupported Testing Library `exact` option; replaced by anchored `/^Send$/` accessible-name matcher with identical matching strength |
| Focused typecheck + gallery regression + production `npm run build` | exit 0; 586 modules transformed; Vite build passed |
| **Final root `make test-web`** | **exit 0: PASS web-typecheck, PASS web-test, PASS web-lint** |
| **Final `npm run shellguard`** | **exit 0**, shell viewport/tree/menu/selection contracts unchanged |
| **Final `npm run spawnguard`** | **exit 0**, 320/390/899/900/1440; directory workflows, accessibility, model/effort/Start containment, eight staged attachments |
| **Final `npm run overflowguard`** | **exit 0**, 320/390/700/899/900/1024/1400; native disclosures, Session/settings overflow, paging, focus, short-phone menu, 332px collapsed desktop pane and Verbosity targets |
| Focused private Chrome gallery/Session sweep | exit 0, both themes at 360/390/700/1440 with M and XL text; visible buttons contained, phone height floors >=44px, textarea fonts 16px / 20px |
| Independent reviewed-base Session comparison | exit 0; separate archived a1adbcbf3 frontend + own npm ci; all 16 measured cases exactly equal to revised Session |
| `git diff --check` | exit 0 |

The initial custom Session probe omitted harness `?w=` and measured its default 1400px pane despite a small viewport. This evidence was rejected; the final probe uses `?w=${width}` and asserts Composer fits that width. Initial standalone spawn/shell screenshots used mobile emulation without those harnesses having viewport meta; corrected to the canonical guards' CSS viewport configuration and asserted realized innerWidth. Only corrected final captures are retained.

## Visual evidence and actual geometry

Evidence is retained in `task-3-evidence/` beside this report (ignored local artifacts), including the reproducible private-CDP `gallery-check.mjs`, `geometry.json`, independently built `baseline-session-geometry.json`, and screenshots:

- `composer-{light,dark}-{390,1440}.png`: real resting/drafted Composer, pending AskDock, editable note and send controls. Reviewed visually: normal-scale phone fits, question prose is serif, input remains sans, action and attention remain distinct.
- `spawn-{light,dark}-{390,1440}.png`: real start flow; serif heading and full-width phone setting rows, retained Start/model/effort controls. Spawnguard exercises the actual directory controls and eight attachments independently of screenshots.
- `settings-{light,dark}-{390,1440}.png`: actual transcript-preference section and preview. This harness intentionally has unavailable hub support, so its hub-default fields are disabled rather than pretending writes succeeded.
- `session-{light,dark}-{390,1440}.png`: real Session footer/current work/queue/action controls and long machine evidence. This lane does not claim redesign of the live transcript itself.
- `shell-{light,dark}-{390,1440}.png`: real shell and welcome; dedicated shellguard covers drawer/tree interactions.

| Viewport | Gallery textarea M | Actual Session Composer / textarea (before = after) | Phone editable M / XL |
|---|---|---|---|
| 360 | 244px | 328 / 310px | 16 / 20px |
| 390 | 274px | 358 / 340px | 16 / 20px |
| 700 | 461.938px | 668 / 650px | 16 / 20px |
| 1440 | 461.938px | 704 / 686px | desktop 15 / 18.75px |

The gallery naturally produces ~480px desktop specimen panes, so its wide viewport also tests a narrow desktop Composer container. The real overflowguard independently tests a 332px main pane in a desktop viewport. No geometry assertion, mobile breakpoint, contrast floor or behavior test was loosened.

## Preview safety and limits

Every browser used a new private Chrome profile through existing browserGuardProcess/CDP helpers, never shared browser state. Vite was loopback-only and explicitly targeted non-live `EVENER_HUB_ADDR=http://127.0.0.1:1`; fixture clients supply data. Every guard's cleanup completed; no preview remains running. No live services or user state were accessed. The implementation gate is this lane's requested three browser guards plus focused gallery sweeps; the parent owns the full integration `make test-web-browser`, cross-lane review and live usability panel.

No remaining blocking lane concern. Inherited overlays/provider editors/notifications are covered by shared foundation and existing behavior tests, not claimed to have been exercised against live credentials or services. The amber envelope's proven 24%/55% treatment is deliberately preserved; removing its nested rounded border does not justify reducing the attention cue or changing its existing test.

## Commits and retained command evidence

- Implementation: `a48f26b44` — `feat(web): carry the editorial system across the workspace` (19 named owned frontend paths).
- This report is committed separately; no push or merge performed.
- Final root gate: `job:job_034Lj9CQX6ZjgO8dKF8tNb_DiiZvS5q4zA1` (exit 0).
- Final three browser guards: `job:job_034Lj9CQX6ZjgO8dKF8tNb_EOomVBwx4Exu` (each guard exit 0; subsequent build in that job failed the unsupported test option, then the focused fix/build below passed).
- Corrected focused typecheck/regression/build: `job:job_034Lj9CQX6ZjgO8dKF8tNb_tPvp47b5NIfi` (exit 0).
- Final focused gallery/Session/contrast/screenshots: `job:job_034Lj9CQX6ZjgO8dKF8tNb_pJwDBs9OoShW` (exit 0). Preview `http://127.0.0.1:51343`, Vite PID 81634, private Chrome PID 81635; both stopped and absent from subsequent process check.
- Reviewed-base Session sweep: `job:job_034Lj9CQX6ZjgO8dKF8tNb_IjJ4nKixDuVP` (exit 0). Preview `http://127.0.0.1:55781`, Vite PID 99847, private Chrome PID 99848; both stopped and absent from subsequent process check. Temporary independent baseline archive/install removed after exact JSON comparison.
