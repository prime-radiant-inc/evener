// The cheatsheet overlay (Phase 4a, the p4 plan's Design decision 3): a
// modal listing every keybinding action with its EFFECTIVE chord, grouped
// into Sessions / Transcript / Composer / General. The action list and the
// chords are sourced live - ACTION_DISPLAY_ROWS derives from the default
// map, and each row's chord comes from the registry at render via
// keybindings/display.ts, the same source the Settings keybindings section
// reads - so hub-synced overrides and unbound actions render truthfully and
// no hand-maintained copy can go stale (the survey's stale-HELP_ROWS
// lesson).
//
// Triggers live in the default map (keybindings/defaults.ts): ⌘/ (the $mod
// chord, allowInEditable + allowInModal so it also toggles the overlay
// closed while open) and the pref-conditional "?" (cheatsheetController.ts's
// reconcile). Escape closes through the OverlayPanel's own handler, with the
// scope-gated cheatsheet.close binding as the window-level backstop - the
// Settings pane's SETTINGS_SCOPE precedent.
//
// Desktop-only: AppShell mounts this component only off mobile, so no action
// is ever registered on a touch viewport and the trigger chords stay inert
// there (RailHost's rail.toggle no-registration pattern).

import { ACTION_DISPLAY_ROWS, ACTIONS, CHEATSHEET_SCOPE, displayBindingsFor } from "@evener/appwire-client";
import { useEffect } from "react";
import { useStore } from "zustand";
import { keybindingsRegistry } from "../../keybindings/appRegistry";
import { Dialog, Eyebrow, KeyHint, ShortcutList } from "../../widgets";
import { requireClass } from "../../widgets/internal/requireClass";
import { installKeybindings } from "../installKeybindings";
import styles from "./cheatsheet.module.css";
import { closeCheatsheet, toggleCheatsheet, useCheatsheetStore } from "./cheatsheetController";

const CLASS = {
  groups: requireClass(styles.groups, "cheatsheet.module.css", "groups"),
  group: requireClass(styles.group, "cheatsheet.module.css", "group"),
  footerHint: requireClass(styles.footerHint, "cheatsheet.module.css", "footerHint"),
};

type GroupId = "sessions" | "transcript" | "composer" | "general";

const GROUPS: readonly { id: GroupId; title: string }[] = [
  { id: "sessions", title: "Sessions" },
  { id: "transcript", title: "Transcript" },
  { id: "composer", title: "Composer" },
  { id: "general", title: "General" },
];

// Presentation-only grouping (Design decision 3). Anything not named here -
// including any action a future phase adds to the default map - lands in
// General, so a new action can never silently vanish from the overlay.
const GROUP_BY_ACTION: Readonly<Record<string, GroupId>> = {
  [ACTIONS.sessionNext]: "sessions",
  [ACTIONS.sessionPrevious]: "sessions",
  [ACTIONS.nextNeedsYou]: "sessions",
  [ACTIONS.transcriptLineUp]: "transcript",
  [ACTIONS.transcriptLineDown]: "transcript",
  [ACTIONS.transcriptPageUp]: "transcript",
  [ACTIONS.transcriptPageDown]: "transcript",
  [ACTIONS.transcriptScrollTop]: "transcript",
  [ACTIONS.transcriptScrollBottom]: "transcript",
  [ACTIONS.transcriptFind]: "transcript",
  [ACTIONS.composerFocus]: "composer",
  [ACTIONS.selectionQuote]: "composer",
};

function groupOf(actionId: string): GroupId {
  return GROUP_BY_ACTION[actionId] ?? "general";
}

export function CheatsheetOverlay() {
  const open = useCheatsheetStore((s) => s.open);
  const bindings = useStore(keybindingsRegistry, (s) => s.bindings);

  // Registers the toggle action (the chord→handler half; the chords
  // themselves are the default map's, registered by installKeybindings).
  // Desktop-only by virtue of the mount site, per this file's header.
  useEffect(() => {
    installKeybindings();
    const unregister = keybindingsRegistry
      .getState()
      .registerAction(ACTIONS.cheatsheetToggle, () => toggleCheatsheet());
    return () => {
      unregister();
      // Unmount is the mobile/desktop breakpoint swap (AppShell gates this
      // component on !isMobile), and the store is a singleton: an overlay
      // left open on the way to mobile would otherwise REAPPEAR already open
      // on the way back to desktop (roborev PR #884 round 4).
      closeCheatsheet();
    };
  }, []);

  // While open, push the cheatsheet scope and register the close action -
  // the scope-gated Escape backstop (the OverlayPanel's own Escape handler
  // claims the key first whenever focus is inside the dialog; this covers a
  // keydown whose target sits outside it, and keeps the chord remappable).
  useEffect(() => {
    if (!open) return undefined;
    const registry = keybindingsRegistry.getState();
    const popScope = registry.pushScope(CHEATSHEET_SCOPE);
    const unregister = registry.registerAction(ACTIONS.cheatsheetClose, () => closeCheatsheet());
    return () => {
      unregister();
      popScope();
    };
  }, [open]);

  return (
    <Dialog
      open={open}
      onClose={closeCheatsheet}
      title="Keyboard shortcuts"
      footer={
        // The hold-hints feature itself is Phase 4a's next task; this line
        // is its discoverability hook (Design decision 4). "Mod" renders ⌘
        // on Apple platforms and Ctrl elsewhere (widgets/keyhint).
        <span className={CLASS.footerHint}>
          Hold <KeyHint keys={["Mod"]} /> to see hints
        </span>
      }
    >
      <div className={CLASS.groups}>
        {GROUPS.map((group) => (
          <section className={CLASS.group} key={group.id}>
            <Eyebrow as="h3">{group.title}</Eyebrow>
            <ShortcutList
              items={ACTION_DISPLAY_ROWS.filter((row) => groupOf(row.actionId) === group.id).map((row) => ({
                label: row.title,
                sequences: displayBindingsFor(bindings, row.actionId).map((binding) => binding.chord),
                unbound: "Unbound",
              }))}
            />
          </section>
        ))}
      </div>
    </Dialog>
  );
}
