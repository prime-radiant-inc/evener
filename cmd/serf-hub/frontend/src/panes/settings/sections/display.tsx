import { prefsStore, usePrefsStore } from "../../../stores/prefs";
import { Switch, useToasts } from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import styles from "./display.module.css";

const CLASS = {
  root: requireClass(styles.root, "display.module.css", "root"),
  intro: requireClass(styles.intro, "display.module.css", "intro"),
  row: requireClass(styles.row, "display.module.css", "row"),
  help: requireClass(styles.help, "display.module.css", "help"),
};

/**
 * Settings -> Display (parity-m7-settings.md §5): 2 composer/cost-display
 * toggles on prefs.ts's SHARED keys (serf.prefs.enterToSend/showCost - the
 * exact names a parallel wave's interim hook already reads, per the wave-7
 * plan's own binding constraint). Deliberately does NOT reach into the
 * composer/session pane to rewrite its kbd-hint glyphs
 * (applyComposerKeybindHints in the legacy) - panes/session/** and the
 * Textarea widget are W5-owned this cycle; the composer reads these same
 * prefs itself for that, on its own schedule.
 */
export function DisplaySection() {
  const enterToSend = usePrefsStore((s) => s.enterToSend);
  const showCost = usePrefsStore((s) => s.showCost);
  const { push } = useToasts();

  return (
    <div className={CLASS.root}>
      <p className={CLASS.intro}>Composer and cost-display preferences. Saved per-browser.</p>

      <div className={CLASS.row}>
        <Switch
          label="Enter sends"
          checked={enterToSend}
          onChange={(value) => {
            prefsStore.getState().setEnterToSend(value);
            push("success", "Settings saved");
          }}
        />
        <p className={CLASS.help}>
          Default off: ⌘/Ctrl-Enter sends, Enter inserts a newline. On: Enter sends, Shift-Enter inserts a newline (the
          steer keyboard shortcut is unavailable in this mode — the steer button still works).
        </p>
      </div>

      <div className={CLASS.row}>
        <Switch
          label="Show estimated cost"
          checked={showCost}
          onChange={(value) => {
            prefsStore.getState().setShowCost(value);
            push("success", "Settings saved");
          }}
        />
        <p className={CLASS.help}>
          Default off. Shows each round's estimated cost under the round, from catalog pricing — an estimate, not a
          billing-exact figure. The session's total cost always shows in the footer.
        </p>
      </div>
    </div>
  );
}
