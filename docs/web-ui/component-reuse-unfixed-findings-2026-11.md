# Unfixed findings from the web UI reuse audit

Base: `347036b94b`. Source: the 21 lane reports and the reuse plan at `docs/web-ui/component-reuse-plan-2026-11.md`.

The audit recorded 300 change findings (37 High, 113 Medium, 150 Low). Two hundred twenty-three shipped across 137 reuse PRs merged 2026-11-22 through 2026-11-25; two (`Y1-F08`, `I1-F01`) were disproved; thirty-nine more are settled outside the tables (30 no change by design, 2 ruled out by Jesse, 1 kept as is on his call, 5 already fixed on main, 1 measured no-change, recorded below); **36 remain, all listed below**: no High, two Medium (`P5-F06`, `Y1-F01`), and 34 Low. Eleven rows were verified fixed on `cfecf86d97` and removed on 2026-11-24: `I1-F03`, `I1-F04`, `I1-F05`, `G2-F01`, `G2-F02`, `G2-F14`, `G3-F14`, `P3-F03`, `P7-F03`, `W3-F14`, `Y1-F03`; three more (`P6-R05`, `S1-F10`, `S2-F09`) were already fixed by #7617 when the low sweep reached them. The partials closed on 2026-11-25: `D1-F01` (51 sections in #7598, the 13 variants in #7633, the six surface sections in #7710), `D1-F08` (the mockup's capability and status vocabulary comes from the production helpers after #7713; `StaticSheet` stays because the real `Sheet` is modal and the screenshot tooling cannot rasterize it), `G1-F02` (five sections keep their own gate, each with its reason written beside it after #7736), `G1-F05` (no change needed: `about.tsx` already pre-mounts a polite `InlineNotice`, and `SectionStatus` would break `about.test.tsx:97`), `G3-F07` (`marketplacesPlugins/index.tsx` renders through the shared notice after #7729), `G3-F08` (fixed by #7580), `P2-F08` (the barrel exports the code-block and disclosure pieces and every consumer imports them after #7740), `P4-F03` (`ImageThumbnail` and `MediaLightbox` in #7718), and `W1-F02` (Menu takes `useRovingRadio`'s menu mode, #7727). `P5-F06` and `Y1-F01` stay partial and stay in the tables. The plan's Phase 3 records the widget-API extensions Jesse approved on 2026-11-22 and 2026-11-23.

## Shipped

| PR | Change |
|---|---|
| #7487 | refactor(web): delete dead pending message stack |
| #7488 | refactor(web): share chunk failure boundary |
| #7489 | refactor(web): share ToolRow row shells |
| #7490 | refactor(web): share rail optimistic mutations |
| #7491 | refactor(web): adopt shared session notice and status dot |
| #7492 | refactor(web): share pending row rendering |
| #7494 | refactor(web): delete the dead oauth dialog error class |
| #7495 | refactor(web): parameterize the system notice line |
| #7496 | refactor(web): delete dead job-log CSS classes |
| #7497 | refactor(web): use the shared Eyebrow for the subagent job label |
| #7498 | refactor(web): compose the button recipe for menu triggers |
| #7499 | refactor(web): share the session overlay dock recipe |
| #7506 | refactor(web): add the quiet Select variant |
| #7507 | refactor(web): add ModelSwitchTrigger fill and shrink |
| #7513 | refactor(web): add Chip mono variant and migrate the available-tools pill |
| #7516 | refactor(web): add ZoomableMedia radius and migrate the touch tile |
| #7518 | refactor(web): add Textarea fill and migrate NotesPanel |
| #7519 | refactor(web): add Card padding none and migrate tool bodies |
| #7522 | refactor(web): share the clipboard fallback and add a CopyButton text variant |
| #7525 | refactor(web): parameterize the close glyph |
| #7526 | refactor(web): compose mobile setting rows from ListRow and RadioGroup |
| #7527 | refactor(web): add Button block and migrate both call sites |
| #7528 | refactor(web): expose Menu open state |
| #7529 | refactor(web): add FocusScope className and asChild |
| #7531 | refactor(web): let Disclosure lead with a glyph and Chevron own open rotation |
| #7532 | refactor(web): add Input fill and ref |
| #7533 | refactor(web): share absolute time formatting |
| #7534 | refactor(web): add HoverCard block |
| #7538 | refactor(web): extend PropertyList row API and migrate call sites |
| #7539 | refactor(web): add PathField prominent variant |
| #7540 | refactor(web): render keybindings warnings through Diagnostics |
| #7541 | refactor(web): extend ConfirmDialog and Dialog for caller-owned forms and panels |
| #7545 | refactor(web): share the long-press gesture |
| #7546 | refactor(web): let SegmentedControl wrap and enter the selected option |
| #7547 | refactor(web): render key sequences through ShortcutList |
| #7548 | refactor(web): add CountButton widget for activity counters |
| #7550 | refactor(web): share code block markup and add DiffBlock copy control |
| #7553 | refactor(web): share the lightbox frame and image CSS |
| #7555 | refactor(web): share the rail's row, section, and mutation helpers |
| #7556 | refactor(web): share the async-confirm rule and param comparison |
| #7557 | refactor(web): share transcript disclosure and secondary-text recipes |
| #7560 | refactor(web): share the Send label-collapse recipe |
| #7561 | refactor(web): share credential-header validation and the host retry notice |
| #7562 | refactor(web): drive the dev routes from one table |
| #7565 | refactor(web): use refParam at the lenient session-ref readers |
| #7569 | refactor(web): share composer truncation, attachment refusal, emptiness, and selection equality |
| #7570 | refactor(web): share AskDock roving navigation and card primary action |
| #7571 | refactor(web): share the transcript pending row and speaker header |
| #7572 | refactor(web): share the delegate status vocabulary and tool meta |
| #7573 | refactor(web): share the status family map |
| #7576 | refactor(web): share the settings poll, launch form paths, and launch error treatment |
| #7578 | refactor(web): delete dead rail exports (S1-F14) |
| #7579 | refactor(web): share the activity collection gate and label the phone Overview as a dialog |
| #7580 | refactor(web): share marketplace failure text, match predicates, and detail escape |
| #7581 | refactor(web): render job watch rows with Disclosure |
| #7583 | refactor(web): share the reasoning-effort control and drop the duplicate meter |
| #7584 | refactor(web): share the session panels' section frame |
| #7588 | refactor(web): export one status label table |
| #7590 | refactor(web): use the dock recommended marker in the transcript card |
| #7591 | refactor(web): export isWebHref from the widgets barrel |
| #7593 | refactor(web): share the repeated vertical stack rule |
| #7594 | refactor(web): share the picker trigger and listbox engine |
| #7597 | refactor(web): put the command palette input on the shared Input combobox API (S3-F02, S3-F17) |
| #7598 | refactor(dev): extract the shared gallery section scaffold (D1-F01) |
| #7608 | refactor(web): use IconButton for the command palette back control |
| #7610 | fix(ui): use --danger-ink for FormRow error text (W1-F11) |
| #7614 | refactor(web): adopt the shared Disclosure in the session notes panel (P7-F06, P7-F07) |
| #7616 | refactor(web): unify the plugin preview Retry treatment (I1-F02) |
| #7615 | refactor(web): share the form-control field chrome (W1-F01) |
| #7612 | refactor(web): share the floating-layer surface recipe (W2-F01) |
| #7617 | refactor(web): compose the shared truncation recipe (Y1-F01 partial) |
| #7613 | refactor(web): share the caption and help text recipe (G1-F01, G3-F02) |
| #7627 | refactor(web): share the settings section load and refresh gate (G1-F02 partial) |
| #7632 | refactor(web): share the picker panel recipe (W3-F03) |
| #7634 | fix(web): share the centered reading-measure column (P4-F04, P8-F01) |
| #7635 | refactor(web): reuse the truncation recipe and InlineNotice in the doc and plugin panes (M1-F03, M1-F02) |
| #7636 | refactor(web): share the tool-row disclosure reset and overlay (P1-F02) |
| #7637 | refactor(web): gate the remaining settings sections with SectionGate (G1-F02) |
| #7638 | refactor(web): sweep low-severity rail and ask dock reuse findings (slice 1) |
| #7639 | refactor(web): share the phone tap floor recipe (W1-F03, W1-F10) |
| #7640 | refactor(web): render KeyHint's hidden words through VisuallyHidden (W1-F07, Y1-F02) |
| #7641 | refactor(web): share the slash listbox engine and prompt-card recipes (P5-F06 partial, P5-F04) |
| #7642 | refactor(web): share the eyebrow type recipe (W1-F08, Y1-F04) |
| #7643 | refactor(web): share the force-stop recovery sequence (P8-F04) |
| #7644 | refactor(dev): share the gallery section discovery (D1-F02) |
| #7645 | refactor(web): reuse ConfirmDialog and InlineNotice in the session menu and palette (S3-F04, S3-F05) |
| #7646 | refactor(web): render transcript detail notices through InlineNotice (P1-F05) |
| #7647 | refactor(web): render watch rows with ListRow (S2-F03) |
| #7648 | refactor(dev): share the harness mount scaffold (D1-F03) |
| #7649 | refactor(web): share the left-rail body recipe (P2-F05) |
| #7650 | refactor(web): share the fold-header recipe and one chevron cue (W1-F09) |
| #7651 | refactor(dev): share the gallery demo frame recipe (D1-F04) |
| #7652 | refactor(dev): compose the gallery row label recipe (D1-F05) |
| #7653 | refactor(dev): share the surface thread fixture (D1-F07) |
| #7654 | fix(transcript): render empty and unavailable tool states through shared widgets (P3-F02) |
| #7655 | refactor(dev): use Button for the model catalog reopen (D1-F09) |
| #7656 | fix(sandbox): render the escalation failure through InlineNotice (P3-F07) |
| #7657 | refactor(web): render the dialog close control as the shared IconButton (W2-F05) |
| #7658 | refactor(transcript): render compact_context raw arguments through MCPToolArguments (P3-F06) |
| #7659 | refactor(settings): share the section heading recipe (G1-F04) |
| #7660 | refactor(web): share the tone-family token triple (W2-F09) |
| #7661 | refactor(web): share shell recipes and settle shell chrome (slice 2 lows) |
| #7663 | refactor(settings): share the quiet status line (G1-F05 partial) |
| #7664 | refactor(web): render the picker's rows through ListRow's plain variant (W3-F07) |
| #7665 | refactor(web): draw the chooser's navigation glyphs with Chevron and Disclosure (W3-F08) |
| #7666 | refactor(settings): share the column-stack recipes (G1-F07, G3-F03) |
| #7667 | refactor(web): render the field errors through InlineNotice's danger tone (W3-F09) |
| #7668 | refactor(web): render the list placeholders through EmptyState and Loader (W3-F10) |
| #7669 | refactor(settings): render the credential JSON field through Textarea (G2-F05) |
| #7670 | refactor(web): render the diagram viewer through Dialog's large size (W3-F11) |
| #7671 | refactor(web): draw the path field's folder and file glyphs with ToolIcon (W3-F12) |
| #7672 | refactor(settings): share the marketplace source field group (G3-F04) |
| #7673 | refactor(settings): align the pairing action with the controls row (G3-F09) |
| #7710 | refactor(dev): migrate the remaining surface gallery sections (D1-F01) |
| #7713 | refactor(dev): reuse the production plugin vocabulary in the mockup (D1-F08) |
| #7716 | web-ui: PickerTrigger slot box, one task-row time, barrel hook export, difftable gallery-only |
| #7717 | refactor(web): share the row reset, hover wash and press (S3-F07) |
| #7718 | refactor(web): share the image thumbnail and lightbox (P4-F03) |
| #7726 | refactor(web): add Chip removeLabel and accent tone, migrate the palette pill (S3-F03) |
| #7727 | refactor(web): move Menu onto the shared roving navigation (W1-F02) |
| #7728 | refactor(web): share the hidden image input (I1-F06, P5-F03) |
| #7729 | refactor(settings): share the marketplace load-failure notice (G3-F07) |
| #7730 | refactor(settings): share the PathField paths wiring (G3-F06) |
| #7732 | refactor(web): compose the truncation recipe in four remaining file groups (Y1-F01 partial) |
| #7733 | refactor(web): share the responsive modal and overlay close helpers (W2-F04, W2-F07, W2-F12, W2-F13) |
| #7735 | web: sweep the remaining component-reuse Lows (20 rows) |
| #7736 | docs(settings): record why the repository section keeps its own gate (G1-F02) |
| #7737 | refactor(web): share the code-point clip helper (P1-F10) |
| #7738 | refactor(settings): give MobileSection its own layout module (G1-F08) |
| #7739 | refactor(transcript): render job_list rows with ListRow (P3-F10) |
| #7740 | refactor(web): import transcript widget pieces from the barrel (P2-F08) |
| #7741 | refactor(web): share the conversation deleted and unavailable faces (P8-F05) |
| #7742 | refactor(web): share the entity card shell and JSON code block (P1-F06, P1-F08, P1-F09) |
| #7743 | refactor(rail): share the pin section name form (S1-F06) |
| #7744 | refactor(rail): build the four rail row kinds on one shell (S1-F02) |
| #7745 | refactor(web): share the directory listing state machine (W3-F05) |
| #7750 | refactor(web): finish web-UI reuse leftovers (P4-F06) |

## Measured or settled outside the list

- Measured net-positive, then built on Jesse's 2026-11-25 override: `P1-F06` (+31 as measured, shipped in #7742), `P1-F08`/`P1-F09` (+32/−21, #7742), `P1-F10` (#7737), `P3-F10` (#7739; the description keeps its own wrapping line), `P5-F03` (#7728, +18 measured against the doc's +3), `P8-F05` (#7741, +26; the loading face stays per-seat because the pane's carries force-stop recovery), `S1-F02` (#7744, +51; `RailRowShell` owns the row box and the four kinds keep their activation element and test ids), `S1-F06` (#7743, +27), `S3-F07` (#7717, +4), `W2-F04`/`W2-F07`/`W2-F12`/`W2-F13` (#7733, +64), `W3-F05` (#7745, +130), and `G1-F08` (#7738, +42).
- Decided on 2026-11-25 and shipped: `P4-F03` (#7718), `S3-F03` (#7726), `P7-F08` (#7716), `P7-F04` (#7716), `W3-F17` (#7716), `G1-F14` (#7716), and `I1-F06` (#7728).
- Kept as is on Jesse's call: `W1-F18`, the Loader's elapsed readout.
- Ruled out by Jesse, not to be rebuilt: `P6-R01` AnswerOptionRow (+65) and `P6-R04` appwire-client answer counts (+31).
- Already fixed on main when the 2026-11-25 lanes reached them: `G3-F08` (#7580) and `P4-F09` (the JobLog byte formatter).
- Test-pinned or behavior-changing: `P2-F07` (Jesse's ruling), `S2-F05`.
- Disproved: `Y1-F08`. `--tooltip-muted` is a required palette token (the token contract fails without it, 51 of 52 roles); the `--ask-tab-font` half is covered by `P6-R11`.
- Disproved: `I1-F01`. The plugin preview's three renderings are not copies of one treatment: `StatusDot` carries health, `Chip` carries flags, and `InlineNotice` carries the error with its Retry action. #7616 unified the one genuinely duplicated piece (the Retry button, `I1-F02`) and left the three states distinct on purpose.

### Settled on 2026-11-24, removed from the work tables

Fifty-seven rows left the per-lane tables on 2026-11-24, leaving 69 open findings; 33 of those left the tables on 2026-11-25 (the decisions section below records the calls, and the tables hold the 36 that remain). Their reasons:

- No change by design (30 rows). Each row's own conclusion is that the pattern is intentional: `G1-F15` (one table, until a second exists), `G1-F16`, `G1-F17` (sanctioned `requireClass` pattern), `I1-F13`, `M1-F09`, `M1-F10`, `P2-F04` (`railseat.contract.test.ts:135-160` deliberately pins that ThinkBlock `.icon` and SteeringItem `.railIcon` compose nothing from the rail seat), `P2-F07`, `P2-F15`, `P2-F16`, `P3-F15`, `P3-F16`, `P3-F17`, `P3-F18`, `P5-F14`, `P5-F15`, `P7-F13`, `S2-F05` (three one-for-one glyph swaps where the visible result is meant to change), `S2-F14`, `S2-F15`, `S2-F16`, `S3-F11` (the plan's keep-Loader case), `S3-F15`, `S3-F16`, `W1-F19`, `W3-F18`, `W3-F19`, `W3-F20`, `W3-F21`, and `W3-F22`.
- Deferred on measurement (16 rows; all sixteen were built on Jesse's 2026-11-25 override): `P1-F08`, `P1-F09`, `P1-F10`, `P1-F06`, `P3-F10` (ListRow adoption alone +2; readable descriptions plus a whole-row tap target cost +27 more, net +25; reverted from #7654), `P5-F03` (a shared ImagePickerInput measures ~15 lines against 12 removed, net positive), `P8-F05` (+5 production), `S1-F02`, `S1-F06`, `S3-F07` (~18 recipe lines against ~20 shed, and the palette's `aria-selected`/`--hover-2` semantics differ from ListRow's `aria-current`), `W2-F04`, `W2-F07`, `W2-F12`, `W2-F13`, `W3-F05`, and `G1-F08` (net-positive as a standalone layout move).
- Needs a decision, so not started (8 rows; six were decided and shipped on 2026-11-25, and `P6-R01`/`P6-R04` stay ruled out): `W3-F17` DiffTable ownership, `W1-F18` suspected-dead Loader readout, `G1-F14` public hooks export, `P6-R01`/`P6-R04` (ruled out by Jesse), `P7-F04` (needs the product split changed, or a class-B Timestamp/ActivityTime API; `TasksPanel.test.tsx:441-444` pins the split), `P7-F08` (needs PickerTrigger to render its wrapper on both breakpoints, which changes ModelCatalog/PathField DOM and the classes `ModelSwitchTrigger.test.tsx:105-112` pins; class C), and `S3-F03` (Chip needs a remove-label prop and an accent tone, both unapproved widget API).
- Already fixed on main when the sweep reached them (3 rows): `P6-R05` (fixed before slice 1), `S1-F10` and `S2-F09` (fixed by #7617).

### Decisions taken on 2026-11-25

Jesse answered the nine decision cards and approved the fourteen-item override in one go:

1. `P4-F03` — build `ImageThumbnail` plus an optional `MediaLightbox` and migrate both call sites: #7718.
2. `S3-F03` — approve Chip `removeLabel` and an accent tone, migrate the palette pill, delete `.pill` and `.pillBack`: #7726.
3. `P7-F08` — `PickerTrigger` renders the trigger wrapper on both breakpoints; `.triggerWrapper` deleted: #7716.
4. `P7-F04` — one task-row time on `ActivityTime`; the standalone body adopts it: #7716.
5. `W3-F17` — keep `DiffTable` and mark it gallery-only: #7716.
6. `W1-F18` — keep the Loader elapsed readout as is: no change.
7. `G1-F14` — export `useDismissOnOutsidePress` from the widgets barrel and drop the deep import: #7716.
8. `SCOPE` — do both the Low sweep and a second High/Medium campaign: the sweep is #7735 (20 of 56 shipped, 36 measured away or left to a product call) and the campaign is the nine lanes behind the PRs in the ledger.
9. `DOCS` — keep this file and the plan uncommitted until the decisions land, then commit once: this file and `component-reuse-plan-2026-11.md` land together.
10. `OVERRIDE` — build the findings measured net-positive earlier: `P1-F06`, `P1-F08`, `P1-F09` (#7742), `P1-F10` (#7737), `P3-F10` (#7739), `P5-F03` (#7728), `P8-F05` (#7741), `S1-F02` (#7744), `S1-F06` (#7743), `W2-F04`, `W2-F07`, `W2-F12`, `W2-F13` (#7733), `W3-F05` (#7745), and `G1-F08` (#7738).

The second campaign reached four Highs — `W1-F02` (#7727), `G1-F02` (#7736), `D1-F01` (#7710), and `Y1-F01` (#7732, still partial) — and every Medium except `P5-F06` and the `Y1-F01` remainder.

### Newly recorded after the 2026-11-23 lanes

- `P7-F06` and `P7-F07` shipped in #7614: `Disclosure` gained `summaryAriaLabel`, TopNotesPanel adopted the shared disclosure, and the fourth `PanelSection` site went with it. Details, Notes, Tasks, and TopNotesPanel all render the shared frame.
- Remaining raw controls on session chrome and spawn are all class B/C, measured on `1c78ce245e`; no PR was opened and nothing changed:
  - `RailRow.tsx:692-707` session-title button: `Button variant="link"` cannot keep `.sessionTitleButton`/`.label` (no `className`), would change ink and add underline, and would break the HoverCard `aria-describedby` wiring. `S1-F02` shipped in #7744 (+51) as `RailRowShell`, with the four kinds keeping their own activation element and test ids.
  - `AskDock.tsx:304-334` question tab: `Button` cannot carry `.tab`'s pill radius, padding, selected accent, and fixed height/nowrap would change the visible result.
  - `AskQuestionCard.tsx:145-158`/`:164-181` option checkbox/radio inputs: `Input` excludes those types and `RadioGroup` renders `role="radio"` buttons, not native inputs. `P6-R01` was ruled out.
  - `AskQuestionCard.tsx:222-235` free-text input: needs a `className` prop to keep `.textInput`; the `aria-labelledby`/`data-*` passthrough it also needed now exists via #7597.
  - `Composer.tsx:1619` and `Spawn.tsx:2945` hidden file inputs: native picker plus ref handler; `I1-F06`/`P5-F03` were the handler duplication around it, now one `ImagePickerInput` (#7728) across both panes.
  - `panes/zoom/CascadeSpine.tsx:15-22` 52px vertical-text button: `M1-F09` KEEP-BESPOKE.
  - `ToolRow.tsx:513` `RowTrigger`: bespoke row grammar with caller `.trigger` and rest props; #7581 shipped the shared row shell.
- `W2-F01` follow-up: four pane/shell surfaces repeat the floating-surface tokens but are not floating widgets, so they were left local: `panes/session/composer/slashcompletionmenu.module.css` and `panes/session/transcript/selectionquote.module.css` (`--surface-2` + `--radius-pane` + `--shadow-overlay`), `shell/holdhints` (`--surface-2` + `--shadow-overlay` + `--radius-control`), and `widgets/banner` (`--shadow-overlay` only, a status strip).
- Two more bare-`--danger` text failures, found while fixing `W1-F11` and left unfixed: `widgets/collectioneditor/collectioneditor.module.css:86` `.error` (light theme 3.98:1 on white, 3.72:1 on `--surface-0`) and `widgets/button/button.module.css:151` `.dangerQuiet` (3.98:1 on a transparent ground, worse on its `--danger-bg` hover tint). Both fail AA normal text; `--danger-ink` is the passing companion.
- `P7-F01` follow-up: `panes/spawn/spawnSlashMenu.ts` rebuilt the reasoning-effort option list (default label, ladder, trailing none); #7750 moved that derivation beside the shared control and both call it.
- `Y1-F01` follow-up: 39 of the 60 truncation declarations compose `.truncate` after #7617, and #7732 adds four more whole-file groups. Left local: `widgets/internal/quietTrigger.module.css` `.quietValue` (its three declarations stay because migrating it would nest compositions through ModelSwitch and Select and force recursive expansion into the layoutguard resolver), the sites other lanes own (`shell/palette/commandpalette.module.css`, `panes/settings/sections/**`, `panes/plugins/**`, `widgets/internal/fieldTrigger.module.css`), and ten compound-selector rules whose selector shapes cannot compose.
- `G1-F01`/`G3-F02` follow-up: the shared caption recipe is `styles/help.module.css` `.helpCaption`, with `.helpMuted` (same face and size, `--ink-low`) and `.helpUi` (UI text, `--ink-mid`, `margin: 0`). Six settings copies were not migrated because those lanes own them or they carry extra declarations: `mcp` `.sectionHelp`, `inrepo` `.note`, `hosts` `.planNotice`, `hubUpdates` `.status`, `credentials/oauthDialogs` `.status`, and `hubResidents` `.meta`. `devicePairing` `.help` stays local on purpose (only `color` and `font-size`, no `margin: 0`).
- `G1-F02` follow-up: five settings sections keep their own load/refresh gate because each differs from the shared shape. `inrepo.tsx` uses `live="off"`, a differently labelled retry, and no `data-reason`; `agents.tsx` and `agentsDoc.tsx` have no `data-reason`, live mode, or retry and render beside the heading instead of replacing the section; `dirListSetting.tsx` has no title or retry and checks error before loading; `mcp.tsx` has no title or retry and renders its skeleton only when there is no error. `SectionGate` is an early return without children so the sections that pass it keep their own body. #7736 writes `inrepo.tsx`'s reason in place; the other four keep their gate as recorded here.
- `P6-R08` shipped one shared marker through `AskOptionCopy` (#7590), but two static layoutguard harnesses still hardcode the old `Recommended` copy and now drift from component output: `scripts/layoutguard/cases/askuser-history-long-label/harness.html:35` and `scripts/layoutguard/cases/askdock-mobile-tall-crush/harness.html:97`. The design spec at `docs/superpowers/specs/2026-11-23-question-display-design.md:38` also still says `Recommended`.
- Task #50's four remaining CSS row groups were measured on `477f3a2ac5` and deferred: all still matched exactly (hover-card body 5 declarations at `entityref.module.css` `.card` and `RailRow.module.css` `.contextCard`; chip row with margin 5 at `pluginDetail.module.css` `.chips` and `marketplacesPlugins.module.css` `.chipRow`, the latter dev-only; chip row without margin 4 at `hosts.module.css` `.chipRow` and `InstanceSheet.module.css` `.effortGroup`; wrap row 3 at `delegateStatus.module.css` `.toolsChips` and `askdock.module.css` `.tabs`). The candidate production diff was +48/−34, net **+14**; stripping every comment and blank line still leaves +3, and `chipRowSpaced` cannot compose `chipRow` because layoutguard inlines only one `composes` level. No PR was opened.
- Task #44 module-class audit (35/35 classified, 21 LOCAL, 7 UNCLEAR):
  - Duplicate groups: the four two-site groups above are deferred on measurement; the vertical-stack group shipped in #7593.
  - Unclear, needs a product decision: `slashcompletionmenu.module.css:9-24` `.menu` (Menu inline variant or composer-owned listbox); `jobWatch.module.css:16-21` `.card` (flush Card variant or keep); `MobileSettingRows.module.css:11-25` `.rowButton` (ListRow ownership); `statusbar.module.css:36-51` `.chip` (interactive Chip variant); `zoom.module.css:182-198` `.activityChip` (shared ActivityCounter with the status-bar chip); `commandpalette.module.css` `.pillBack` shipped as #7608.
  - Count note: `ds-audit` and the plan agree on 35; the 2026-11-06 UX audit recorded 38 at commit 55635d1 and its per-class list was not preserved.

### Phase 2 (the 2026-11-24 lanes)

- `W1-F07`/`Y1-F02` shipped in #7640: KeyHint's compact form renders the `VisuallyHidden` widget and `keyhint.module.css` no longer defines `.srOnly`, so the last hand-rolled copy is gone.
- `G1-F05` shipped its status-line half in #7663: a settings-local `SectionStatus` owns the caption status paragraph that `keybindings`, `transcript`, and `TranscriptDisplayCard` each declared, with `.settingsSectionStatus` in `surface.module.css`. The row then settled on 2026-11-25: `about.tsx` already pre-mounts a polite `InlineNotice`, and `SectionStatus` there would break `about.test.tsx:97`, so no change was needed.
- `P5-F06` shipped its engine half in #7641: the composer and spawn slash menus take their active key, active id, scroll-into-view, option ids, and row rendering from `useListbox`/`ListboxOption`; the container `<ul>` stays local because `Listbox` exposes no test-id prop and its max-height/padding would fight the pinned 220px scroll box.
- Slice 2 of the low sweep set three lows aside for file overlap; #7735 shipped all three: `S2-F11` (activitybar), `S3-F08` (commandpalette), and `S3-F10` (SessionMenu).
- A modelCatalog keyboard test flake blocked several merges during this phase. It is a real race: the option rows commit one render before `firstMatchQueryRef`'s effect sets the first row active, so the test now asserts the row count and the active option text together. It reproduced 9 times in 30 before the fix and 0 in 50 after, and landed as #7678.

### The Low sweep of 2026-11-25

PR #7735 shipped 20 of the 56 Low rows, one commit each, over 34 production files at +183/−207 (net −24). The other 36 were measured and set aside; #7750 then shipped `P4-F06` and `P4-F09` was already fixed, so 34 stay in the tables below:

- Premise stale on the sweep's base (7): `D1-F06`, `G2-F07`, `G2-F08`, `P2-F10`, `P3-F12`, `P7-F10`, `P8-F09`.
- Net-positive once the helper or export exists (11): `G1-F09` (+2), `G1-F12` (+5), `G1-F13` (+8), `I1-F10` (+3), `M1-F06` (+7), `P3-F11`, `P4-F06` (+1 or +2, shipped anyway in #7750), `P7-F12` (+1), `W1-F04`, `W1-F06` (+1), `P8-F10` (+4).
- Net-zero, no win (5): `G2-F03`, `M1-F07`, `P4-F08`, `P4-F10`, `W1-F17`.
- A check forbids it or the premise is unsafe (3): `P7-F09` and `W1-F05` (CSS Modules cannot compose inside a `:focus-visible` rule), `Y1-F07` (`groupedRows` would also border the expanded detail sections).
- Needs new machinery, a widget API, or a product call (8): `G2-F12`, `I1-F09`, `M1-F08`, `P5-F10`, `W1-F14`, `W2-F08`, `W2-F10`, `W3-F15`.
- Already fixed in the audit (1): `P4-F09`.
- A visible change left for product review (1): `G2-F09`.

Two review rounds fixed findings red-first: the moved `border-radius: 0` broke `floatingSurface.contract.test.ts`, and the dialog-title and pill cross-sheet face overrides got contract tests that failed before their fix.

## Unfixed by lane

Severity is the audit's judgement. Titles are the lane reports' own.

### D1 (1)

| Finding | Severity | Title |
|---|---|---|
| `D1-F06` | Low | `.hint` caption paragraph re-declared in six section CSS modules |

### G1 (3)

| Finding | Severity | Title |
|---|---|---|
| `G1-F09` | Low | Inline `<code>` bypasses the section's own `Code` helper |
| `G1-F12` | Low | The remove-failure toast is copy-pasted in two collection editors |
| `G1-F13` | Low | Chord KeyHint rendering is written three times |

### G2 (5)

| Finding | Severity | Title |
|---|---|---|
| `G2-F03` | Low | Dialog action rows repeat the same Button pair and `.actions` CSS |
| `G2-F07` | Low | Secondary-copy recipe copied across four modules |
| `G2-F08` | Low | Dialog body stack copied across three modules |
| `G2-F09` | Low | Status strips are hand-built instead of `InlineNotice` |
| `G2-F12` | Low | Text glyphs stand in for shared icons |

### I1 (2)

| Finding | Severity | Title |
|---|---|---|
| `I1-F09` | Low | `.sheetBody` is passed as the Sheet footer's wrapper, overriding the footer's layout |
| `I1-F10` | Low | The sheet-body safe-area recipe is copied across modules |

### M1 (3)

| Finding | Severity | Title |
|---|---|---|
| `M1-F06` | Low | `registerPane` + `lazy()` boilerplate repeated per pane — COMPONENT-DUP |
| `M1-F07` | Low | DocPane repeats its scaffold/state frame for each pre-content state — COMPONENT-DUP |
| `M1-F08` | Low | `.stateLabel` composes a widget's private stylesheet and overrides its recipe — MISUSE |

### P2 (1)

| Finding | Severity | Title |
|---|---|---|
| `P2-F10` | Low | Text glyphs stand in for action icons |

### P3 (2)

| Finding | Severity | Title |
|---|---|---|
| `P3-F11` | Low | No shared caption/mono meta recipe; declarations repeat across six modules |
| `P3-F12` | Low | Interactive row reset and hover wash are duplicated outside the shared row recipes |

### P4 (2)

| Finding | Severity | Title |
|---|---|---|
| `P4-F08` | Low | The bounded flex-column height chain is copied from the session pane into the JobLog stylesheet |
| `P4-F10` | Low | Two panes repeat the PaneScaffold+Loader loading shell |

### P5 (2)

| Finding | Severity | Title |
|---|---|---|
| `P5-F06` | Medium | `SlashCompletionMenu` was a fourth hand-built activedescendant listbox (partial: the active key, active id, scroll-into-view, option ids, and row rendering come from `useListbox`/`ListboxOption` after #7641; the container `<ul>` stays local) |
| `P5-F10` | Low | The storage-stall notice is a bespoke `role=status` div beside `InlineNotice` |

### P7 (3)

| Finding | Severity | Title |
|---|---|---|
| `P7-F09` | Low | Inset focus ring recipe copied four times inside the lane |
| `P7-F10` | Low | NotesPanel reaches into Textarea's DOM to set width |
| `P7-F12` | Low | Tabular-figure declaration repeated in every chrome CSS module |

### P8 (2)

| Finding | Severity | Title |
|---|---|---|
| `P8-F09` | Low | Two hand-built polite live regions in the lane |
| `P8-F10` | Low | The version-mismatch remediation instruction is maintained twice in ConversationBody |

### W1 (5)

| Finding | Severity | Title |
|---|---|---|
| `W1-F04` | Low | Hover wash and its transition are restated in eight widgets |
| `W1-F05` | Low | The focus-ring pair is restated in fourteen stylesheets |
| `W1-F06` | Low | The disabled/refusal dim is restated in nine modules |
| `W1-F14` | Low | IconButton re-declares Button's size ladder and imports Button's private classes |
| `W1-F17` | Low | EmptyState and InlineNotice duplicate their action-slot layout |

### W2 (2)

| Finding | Severity | Title |
|---|---|---|
| `W2-F08` | Low | Modal overlays do not lock background scroll; floating ones dismiss on scroll |
| `W2-F10` | Low | Floating layers disagree about which surface role they sit on |

### W3 (1)

| Finding | Severity | Title |
|---|---|---|
| `W3-F15` | Low | PromptCard's stylesheet contains a composer-only `:global` and `:has` rule |

### Y1 (2)

| Finding | Severity | Title |
|---|---|---|
| `Y1-F01` | High | The truncation trio is copied into 60 declarations and has no shared recipe (partial: 39 declarations across 26 stylesheets compose `.truncate` after #7617, and #7732 adds four more whole-file groups; `.quietValue`, the sites other lanes own, and ten compound-selector rules stay local) |
| `Y1-F07` | Low | Row-divider lookalikes bypass `groupedRows` |

## P6's five unrecorded findings

P6 counted 17 findings. Twelve were recovered from its analysis and are included in the 300 above, under P6. The other five (one Medium, four Low) were claimed but never drafted, so they have no IDs or titles. They are an evidence gap outside the 300, not refuted findings.
